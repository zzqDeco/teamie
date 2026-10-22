package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"go.uber.org/zap"
	"teamie/agent-infra/internal/api"
	"teamie/agent-protocol/modeltransport"
	"teamie/backend/pkg/observability"
)

const defaultTavilyBaseURL = "https://api.tavily.com"
const maxExtractContentBytes = 64 << 10
const maxSearchSnippetBytes = 1500
const maxSearchTitleBytes = 512

func utf8Prefix(text string, limit int) string {
	if len(text) <= limit {
		return text
	}
	for limit > 0 && !utf8.RuneStart(text[limit]) {
		limit--
	}
	return text[:limit]
}

func validDomainName(raw string) (string, bool) {
	d := strings.ToLower(strings.TrimSpace(raw))
	if d == "" || len(d) > 253 || strings.HasPrefix(d, ".") || strings.HasSuffix(d, ".") || !strings.Contains(d, ".") {
		return "", false
	}
	if d == "localhost" || strings.HasSuffix(d, ".localhost") || strings.HasSuffix(d, ".local") || strings.HasSuffix(d, ".internal") {
		return "", false
	}
	if net.ParseIP(d) != nil {
		return "", false
	}
	labels := strings.Split(d, ".")
	for _, label := range labels {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return "", false
		}
		for i := 0; i < len(label); i++ {
			ch := label[i]
			if !((ch >= 'a' && ch <= 'z') || (ch >= '0' && ch <= '9') || ch == '-') {
				return "", false
			}
		}
	}
	return d, true
}

func normalizeDomains(items []string) ([]string, error) {
	if len(items) > 10 {
		return nil, api.Err(400, "invalid_request", "At most 10 domain filters are allowed")
	}
	if len(items) == 0 {
		return nil, nil
	}
	out := make([]string, 0, len(items))
	seen := make(map[string]bool, len(items))
	for _, raw := range items {
		d, ok := validDomainName(raw)
		if !ok {
			return nil, api.Err(400, "invalid_request", "Invalid domain filter")
		}
		if !seen[d] {
			seen[d] = true
			out = append(out, d)
		}
	}
	return out, nil
}

func validateExtractURLs(rawURLs []string) ([]string, error) {
	if len(rawURLs) == 0 || len(rawURLs) > 5 {
		return nil, api.Err(400, "invalid_request", "Between 1 and 5 URLs are required")
	}
	out := make([]string, 0, len(rawURLs))
	seen := make(map[string]bool, len(rawURLs))
	for _, raw := range rawURLs {
		trimmed := strings.TrimSpace(raw)
		if trimmed == "" || len(trimmed) > 2048 || strings.ContainsAny(trimmed, "\r\n\t\x00") {
			return nil, api.Err(400, "invalid_request", "Invalid target URL")
		}
		u, err := url.Parse(trimmed)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || u.Fragment != "" {
			return nil, api.Err(400, "invalid_request", "Use public HTTP or HTTPS URLs without credentials or fragments")
		}
		if p := u.Port(); p != "" {
			port, err := strconv.Atoi(p)
			if err != nil || port < 1 || port > 65535 {
				return nil, api.Err(400, "invalid_request", "Invalid URL port")
			}
		}
		host := strings.ToLower(strings.TrimSuffix(u.Hostname(), "."))
		if host == "localhost" || strings.HasSuffix(host, ".localhost") || strings.HasSuffix(host, ".local") || strings.HasSuffix(host, ".internal") || !strings.Contains(host, ".") {
			return nil, api.Err(400, "invalid_request", "Private or internal hostnames are not permitted")
		}
		if ip := net.ParseIP(host); ip != nil && !PublicIP(ip) {
			return nil, api.Err(400, "invalid_request", "Private or reserved IP addresses are not permitted")
		}
		normalized := u.String()
		if !seen[normalized] {
			seen[normalized] = true
			out = append(out, normalized)
		}
	}
	return out, nil
}

func (s *Service) authorizeBearer(w http.ResponseWriter, r *http.Request) bool {
	if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") || !api.EqualSecret(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "), s.Token) {
		api.Fail(w, api.Err(401, "unauthorized", "Invalid service credential"))
		return false
	}
	return true
}

func (s *Service) registerWebCancel(ctx context.Context, agentID, runID string) (context.Context, func()) {
	ctx, cancel := context.WithCancel(ctx)
	index := agentID + "/" + runID
	s.mu.Lock()
	s.webSeq++
	webKey := index + "/web/" + strconv.FormatUint(s.webSeq, 10)
	s.active[webKey] = cancel
	if at, ok := s.canceled[index]; ok && time.Since(at) < 2*time.Minute {
		cancel()
	}
	s.mu.Unlock()
	return ctx, func() {
		cancel()
		s.mu.Lock()
		delete(s.active, webKey)
		s.mu.Unlock()
	}
}

func (s *Service) tavilyBaseURL() string {
	base := strings.TrimRight(strings.TrimSpace(s.WebSearchURL), "/")
	if base == "" {
		return defaultTavilyBaseURL
	}
	return base
}

func classifyWebError(ctx context.Context, status int, err error) error {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return &api.Error{Status: 504, Code: "upstream_timeout", Message: "Web search request timed out", Cause: err}
	}
	if errors.Is(err, context.Canceled) || ctx.Err() != nil {
		return &api.Error{Status: 499, Code: "canceled", Message: "Web search request canceled", Cause: err}
	}
	if err != nil {
		return &api.Error{Status: 502, Code: "provider_failed", Message: "Web search provider request failed", Cause: err}
	}
	switch status {
	case 400, 422:
		return api.Err(400, "invalid_request", "Web search provider rejected the request parameters")
	case 401, 403:
		return api.Err(502, "provider_auth_failed", "Web search provider rejected the saved credential")
	case 429:
		return api.Err(429, "upstream_rate_limited", "Web search provider rate limit or quota reached")
	default:
		return api.Err(502, "provider_failed", "Web search provider returned an unexpected status")
	}
}

func (s *Service) doTavilyJSON(ctx context.Context, path, apiKey string, payload any, out any) (int, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return 0, api.Err(400, "invalid_request", "Invalid web search payload")
	}
	callCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(callCtx, http.MethodPost, s.tavilyBaseURL()+path, bytes.NewReader(body))
	if err != nil {
		return 0, api.Err(500, "gateway_unavailable", "Web search request could not be constructed")
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	client := s.Provider.HTTP
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
		return len(raw), api.Err(502, "response_too_large", "Web search provider response exceeded size limit")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return len(raw), classifyWebError(callCtx, resp.StatusCode, nil)
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return len(raw), &api.Error{Status: 502, Code: "provider_invalid_response", Message: "Web search provider returned invalid JSON", Cause: err}
	}
	return len(raw), nil
}

func writeWebSearchError(w http.ResponseWriter, err error) *api.Error {
	var a *api.Error
	if !errors.As(err, &a) {
		a = &api.Error{Status: 500, Code: "gateway_unavailable", Message: "Web search gateway is temporarily unavailable", Cause: err}
	}
	api.Write(w, 200, modeltransport.WebSearchResponse{
		OK:           false,
		ErrorCode:    a.Code,
		ErrorMessage: a.Message,
		StatusCode:   a.Status,
	})
	return a
}

func writeWebExtractError(w http.ResponseWriter, err error, failed []modeltransport.WebExtractFailure) *api.Error {
	var a *api.Error
	if !errors.As(err, &a) {
		a = &api.Error{Status: 500, Code: "gateway_unavailable", Message: "Web extract gateway is temporarily unavailable", Cause: err}
	}
	api.Write(w, 200, modeltransport.WebExtractResponse{
		OK:            false,
		FailedResults: failed,
		ErrorCode:     a.Code,
		ErrorMessage:  a.Message,
		StatusCode:    a.Status,
	})
	return a
}

func (s *Service) webSearchHTTP(w http.ResponseWriter, r *http.Request) {
	if !s.authorizeBearer(w, r) {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 32<<10)
	var in modeltransport.WebSearchRequest
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if dec.Decode(&in) != nil || dec.Decode(new(any)) != io.EOF || !validID(in.OwnerID) || !validID(in.AgentID) || !validID(in.SessionID) || !validID(in.RunID) {
		api.Fail(w, api.Err(400, "invalid_request", "Invalid web search request"))
		return
	}
	run, attempt, _ := strings.Cut(in.RunID, ":")
	ctx, op := observability.Begin(r.Context(), "web-search-call",
		zap.String("custom.agent.id", observability.ID(in.AgentID)),
		zap.String("custom.runtime.session_id", observability.ID(in.SessionID)),
		zap.String("custom.runtime.run_id", observability.ID(run)),
		zap.String("custom.runtime.attempt_id", observability.ID(attempt)),
	)
	ctx = observability.WithHTTPAttempts(ctx)
	var callFailure error
	phase := "admission"
	resultCount, respBytes := 0, 0
	defer func() {
		op.End(callFailure,
			zap.String("custom.model.failure_stage", phase),
			zap.Int("custom.web.result_count", resultCount),
			zap.Int("custom.web.response_bytes", respBytes),
			zap.Int("custom.retry.attempts", observability.HTTPAttempts(ctx)),
		)
	}()
	fail := func(err error) {
		callFailure = writeWebSearchError(w, err)
	}
	query := strings.TrimSpace(in.Query)
	if qLen := len([]rune(query)); qLen < 1 || qLen > 400 || strings.ContainsAny(query, "\x00") {
		fail(api.Err(400, "invalid_request", "Search query must be between 1 and 400 characters"))
		return
	}
	maxResults := in.MaxResults
	if maxResults == 0 {
		maxResults = 5
	}
	if maxResults < 1 || maxResults > 10 {
		fail(api.Err(400, "invalid_request", "maxResults must be between 1 and 10"))
		return
	}
	timeRange := strings.TrimSpace(in.TimeRange)
	switch timeRange {
	case "", "day", "week", "month", "year":
	default:
		fail(api.Err(400, "invalid_request", "Unsupported timeRange filter"))
		return
	}
	includeDomains, err := normalizeDomains(in.IncludeDomains)
	if err != nil {
		fail(err)
		return
	}
	excludeDomains, err := normalizeDomains(in.ExcludeDomains)
	if err != nil {
		fail(err)
		return
	}
	c, err := s.Store.Load(ctx)
	if err != nil {
		fail(err)
		return
	}
	if !c.WebSearchConfigured() {
		fail(api.Err(409, "web_search_unconfigured", "An administrator must configure the web search provider"))
		return
	}
	if !c.WebSearchEnabled {
		fail(api.Err(409, "web_search_disabled", "Web search is disabled"))
		return
	}
	select {
	case s.webSlots <- struct{}{}:
		defer func() { <-s.webSlots }()
	default:
		fail(api.Err(429, "gateway_busy", "Web search gateway is busy"))
		return
	}
	apiKey, err := s.Cipher.Decrypt(c.WebSearchCipherKey)
	if err != nil {
		fail(&api.Error{Status: 503, Code: "credential_unavailable", Message: "Saved web search credential could not be decrypted", Cause: err})
		return
	}
	callCtx, cleanup := s.registerWebCancel(ctx, in.AgentID, in.RunID)
	defer cleanup()

	phase = "provider-search"
	payload := map[string]any{
		"query":               query,
		"search_depth":        "basic",
		"max_results":         maxResults,
		"include_answer":      false,
		"include_raw_content": false,
		"include_images":      false,
	}
	if timeRange != "" {
		payload["time_range"] = timeRange
	}
	if len(includeDomains) > 0 {
		payload["include_domains"] = includeDomains
	}
	if len(excludeDomains) > 0 {
		payload["exclude_domains"] = excludeDomains
	}
	var upstream struct {
		Results []struct {
			Title         string  `json:"title"`
			URL           string  `json:"url"`
			Content       string  `json:"content"`
			PublishedDate string  `json:"published_date"`
			Score         float64 `json:"score"`
		} `json:"results"`
	}
	respBytes, err = s.doTavilyJSON(callCtx, "/search", apiKey, payload, &upstream)
	if err != nil {
		fail(err)
		return
	}
	results := make([]modeltransport.WebSearchResult, 0, min(len(upstream.Results), maxResults))
	for _, item := range upstream.Results {
		if len(results) >= maxResults {
			break
		}
		u := utf8Prefix(strings.TrimSpace(item.URL), 2048)
		if u == "" {
			continue
		}
		results = append(results, modeltransport.WebSearchResult{
			Title:         utf8Prefix(strings.TrimSpace(item.Title), maxSearchTitleBytes),
			URL:           u,
			Snippet:       utf8Prefix(strings.TrimSpace(item.Content), maxSearchSnippetBytes),
			PublishedDate: utf8Prefix(strings.TrimSpace(item.PublishedDate), 64),
			Score:         item.Score,
		})
	}
	resultCount = len(results)
	api.Write(w, 200, modeltransport.WebSearchResponse{
		OK:         true,
		Results:    results,
		StatusCode: 200,
	})
}

func (s *Service) webExtractHTTP(w http.ResponseWriter, r *http.Request) {
	if !s.authorizeBearer(w, r) {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 32<<10)
	var in modeltransport.WebExtractRequest
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if dec.Decode(&in) != nil || dec.Decode(new(any)) != io.EOF || !validID(in.OwnerID) || !validID(in.AgentID) || !validID(in.SessionID) || !validID(in.RunID) {
		api.Fail(w, api.Err(400, "invalid_request", "Invalid web extract request"))
		return
	}
	run, attempt, _ := strings.Cut(in.RunID, ":")
	ctx, op := observability.Begin(r.Context(), "web-extract-call",
		zap.String("custom.agent.id", observability.ID(in.AgentID)),
		zap.String("custom.runtime.session_id", observability.ID(in.SessionID)),
		zap.String("custom.runtime.run_id", observability.ID(run)),
		zap.String("custom.runtime.attempt_id", observability.ID(attempt)),
	)
	ctx = observability.WithHTTPAttempts(ctx)
	var callFailure error
	phase := "admission"
	urlCount, resultCount, failedCount, respBytes := len(in.URLs), 0, 0, 0
	defer func() {
		op.End(callFailure,
			zap.String("custom.model.failure_stage", phase),
			zap.Int("custom.web.url_count", urlCount),
			zap.Int("custom.web.result_count", resultCount),
			zap.Int("custom.web.failed_url_count", failedCount),
			zap.Int("custom.web.response_bytes", respBytes),
			zap.Int("custom.retry.attempts", observability.HTTPAttempts(ctx)),
		)
	}()
	fail := func(err error, failed []modeltransport.WebExtractFailure) {
		callFailure = writeWebExtractError(w, err, failed)
	}
	urls, err := validateExtractURLs(in.URLs)
	if err != nil {
		fail(err, nil)
		return
	}
	urlCount = len(urls)
	c, err := s.Store.Load(ctx)
	if err != nil {
		fail(err, nil)
		return
	}
	if !c.WebSearchConfigured() {
		fail(api.Err(409, "web_search_unconfigured", "An administrator must configure the web search provider"), nil)
		return
	}
	if !c.WebSearchEnabled {
		fail(api.Err(409, "web_search_disabled", "Web search is disabled"), nil)
		return
	}
	select {
	case s.webSlots <- struct{}{}:
		defer func() { <-s.webSlots }()
	default:
		fail(api.Err(429, "gateway_busy", "Web search gateway is busy"), nil)
		return
	}
	apiKey, err := s.Cipher.Decrypt(c.WebSearchCipherKey)
	if err != nil {
		fail(&api.Error{Status: 503, Code: "credential_unavailable", Message: "Saved web search credential could not be decrypted", Cause: err}, nil)
		return
	}
	callCtx, cleanup := s.registerWebCancel(ctx, in.AgentID, in.RunID)
	defer cleanup()

	phase = "provider-extract"
	payload := map[string]any{
		"urls":           urls,
		"extract_depth":  "basic",
		"format":         "markdown",
		"include_images": false,
	}
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
	respBytes, err = s.doTavilyJSON(callCtx, "/extract", apiKey, payload, &upstream)
	if err != nil {
		fail(err, nil)
		return
	}
	results := make([]modeltransport.WebExtractResult, 0, len(upstream.Results))
	for _, item := range upstream.Results {
		u := utf8Prefix(strings.TrimSpace(item.URL), 2048)
		content := utf8Prefix(strings.TrimSpace(item.RawContent), maxExtractContentBytes)
		if u == "" || content == "" {
			continue
		}
		results = append(results, modeltransport.WebExtractResult{
			URL:     u,
			Content: content,
		})
	}
	failedResults := make([]modeltransport.WebExtractFailure, 0, len(upstream.FailedResults))
	for _, item := range upstream.FailedResults {
		u := utf8Prefix(strings.TrimSpace(item.URL), 2048)
		if u == "" {
			continue
		}
		failedResults = append(failedResults, modeltransport.WebExtractFailure{
			URL:   u,
			Error: "extract_failed",
		})
	}
	resultCount = len(results)
	failedCount = len(failedResults)
	if len(results) == 0 {
		fail(api.Err(422, "web_extract_failed", "Target URLs could not be extracted"), failedResults)
		return
	}
	api.Write(w, 200, modeltransport.WebExtractResponse{
		OK:            true,
		Results:       results,
		FailedResults: failedResults,
		StatusCode:    200,
	})
}
