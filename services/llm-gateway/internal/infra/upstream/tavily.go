package upstream

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"teamie/contracts/gen/agent/hertz_gen/agent/modeltransport"
	"teamie/pkg/observability"
	"teamie/services/llm-gateway/internal/domain"
)

func classifyWebError(ctx context.Context, status int, err error) error {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return domain.ErrWithCause(504, "upstream_timeout", "Web search request timed out", err)
	}
	if errors.Is(err, context.Canceled) || ctx.Err() != nil {
		return domain.ErrWithCause(499, "canceled", "Web search request canceled", err)
	}
	if err != nil {
		return domain.ErrWithCause(502, "provider_failed", "Web search provider request failed", err)
	}
	switch status {
	case 400, 422:
		return domain.Err(400, "invalid_request", "Web search provider rejected the request parameters")
	case 401, 403:
		return domain.Err(502, "provider_auth_failed", "Web search provider rejected the saved credential")
	case 429:
		return domain.Err(429, "upstream_rate_limited", "Web search provider rate limit or quota reached")
	default:
		return domain.Err(502, "provider_failed", "Web search provider returned an unexpected status")
	}
}

func (p Provider) doTavilyJSON(ctx context.Context, baseURL, path, apiKey string, payload any, out any) (int, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return 0, domain.Err(400, "invalid_request", "Invalid web search payload")
	}
	callCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if base == "" {
		base = domain.DefaultTavilyBaseURL
	}
	req, err := http.NewRequestWithContext(callCtx, http.MethodPost, base+path, bytes.NewReader(body))
	if err != nil {
		return 0, domain.Err(500, "gateway_unavailable", "Web search request could not be constructed")
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	client := p.HTTP
	if client == nil {
		client = NewHTTPClient()
	}
	resp, err := observability.Client(client, "tavily.api").Do(req)
	if err != nil {
		return 0, classifyWebError(callCtx, 0, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, modeltransport.MaxWebResponseBytes+1))
	if err != nil {
		return 0, classifyWebError(callCtx, resp.StatusCode, err)
	}
	if len(raw) > modeltransport.MaxWebResponseBytes {
		return len(raw), domain.Err(502, "response_too_large", "Web search provider response exceeded size limit")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return len(raw), classifyWebError(callCtx, resp.StatusCode, nil)
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return len(raw), domain.ErrWithCause(502, "provider_invalid_response", "Web search provider returned invalid JSON", err)
	}
	return len(raw), nil
}

// Search executes a Tavily /search request and normalizes results on UTF-8 boundaries.
func (p Provider) Search(ctx context.Context, baseURL, apiKey string, payload map[string]any, maxResults int) ([]domain.WebSearchItem, int, error) {
	var upstream struct {
		Results []struct {
			Title         string  `json:"title"`
			URL           string  `json:"url"`
			Content       string  `json:"content"`
			PublishedDate string  `json:"published_date"`
			Score         float64 `json:"score"`
		} `json:"results"`
	}
	respBytes, err := p.doTavilyJSON(ctx, baseURL, "/search", apiKey, payload, &upstream)
	if err != nil {
		return nil, respBytes, err
	}
	results := make([]domain.WebSearchItem, 0, min(len(upstream.Results), maxResults))
	for _, item := range upstream.Results {
		if len(results) >= maxResults {
			break
		}
		u := domain.UTF8Prefix(strings.TrimSpace(item.URL), 2048)
		if u == "" {
			continue
		}
		results = append(results, domain.WebSearchItem{
			Title:         domain.UTF8Prefix(strings.TrimSpace(item.Title), domain.MaxSearchTitleBytes),
			URL:           u,
			Snippet:       domain.UTF8Prefix(strings.TrimSpace(item.Content), domain.MaxSearchSnippetBytes),
			PublishedDate: domain.UTF8Prefix(strings.TrimSpace(item.PublishedDate), 64),
			Score:         item.Score,
		})
	}
	return results, respBytes, nil
}

// Extract executes a Tavily /extract request and normalizes extracted markdown and failed URLs.
func (p Provider) Extract(ctx context.Context, baseURL, apiKey string, payload map[string]any) ([]domain.WebExtractItem, []domain.WebExtractFailure, int, error) {
	var upstream struct {
		Results []struct {
			URL        string `json:"url"`
			RawContent string `json:"raw_content"`
		} `json:"results"`
		FailedResults []struct {
			URL   string `json:"url"`
			Error string `json:"error"`
		} `json:"failed_results"`
	}
	respBytes, err := p.doTavilyJSON(ctx, baseURL, "/extract", apiKey, payload, &upstream)
	if err != nil {
		return nil, nil, respBytes, err
	}
	results := make([]domain.WebExtractItem, 0, len(upstream.Results))
	for _, item := range upstream.Results {
		u := domain.UTF8Prefix(strings.TrimSpace(item.URL), 2048)
		content := domain.UTF8Prefix(strings.TrimSpace(item.RawContent), domain.MaxExtractContentBytes)
		if u == "" || content == "" {
			continue
		}
		results = append(results, domain.WebExtractItem{
			URL:     u,
			Content: content,
		})
	}
	failedResults := make([]domain.WebExtractFailure, 0, len(upstream.FailedResults))
	for _, item := range upstream.FailedResults {
		u := domain.UTF8Prefix(strings.TrimSpace(item.URL), 2048)
		if u == "" {
			continue
		}
		failedResults = append(failedResults, domain.WebExtractFailure{
			URL:   u,
			Error: "extract_failed",
		})
	}
	return results, failedResults, respBytes, nil
}
