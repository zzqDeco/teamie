package app

import (
	"context"
	"strings"

	"teamie/services/llm-gateway/internal/domain"
)

// WebSearchOutcome records the result and diagnostic telemetry of a web search call.
type WebSearchOutcome struct {
	Results       []domain.WebSearchItem
	ResponseBytes int
	Phase         string
	Failure       *domain.Error
}

// WebExtractOutcome records the result and diagnostic telemetry of a web extract call.
type WebExtractOutcome struct {
	Results       []domain.WebExtractItem
	FailedResults []domain.WebExtractFailure
	URLCount      int
	ResponseBytes int
	Phase         string
	Failure       *domain.Error
}

func (s *Service) tavilyBaseURL() string {
	base := strings.TrimRight(strings.TrimSpace(s.WebSearchURL), "/")
	if base == "" {
		return domain.DefaultTavilyBaseURL
	}
	return base
}

// ExecuteWebSearch validates and executes a Tavily web search call.
func (s *Service) ExecuteWebSearch(ctx context.Context, in domain.WebSearchInput) WebSearchOutcome {
	s.ensureInit()
	outcome := WebSearchOutcome{Phase: "admission"}
	fail := func(err error) WebSearchOutcome {
		de := domain.AsError(err)
		if de.Code == "gateway_unavailable" && de.Message == "Model gateway is temporarily unavailable" {
			de = domain.ErrWithCause(500, "gateway_unavailable", "Web search gateway is temporarily unavailable", err)
		}
		outcome.Failure = de
		return outcome
	}
	query := strings.TrimSpace(in.Query)
	if qLen := len([]rune(query)); qLen < 1 || qLen > 400 || strings.ContainsAny(query, "\x00") {
		return fail(domain.Err(400, "invalid_request", "Search query must be between 1 and 400 characters"))
	}
	maxResults := in.MaxResults
	if maxResults == 0 {
		maxResults = 5
	}
	if maxResults < 1 || maxResults > 10 {
		return fail(domain.Err(400, "invalid_request", "maxResults must be between 1 and 10"))
	}
	timeRange := strings.TrimSpace(in.TimeRange)
	switch timeRange {
	case "", "day", "week", "month", "year":
	default:
		return fail(domain.Err(400, "invalid_request", "Unsupported timeRange filter"))
	}
	includeDomains, err := domain.NormalizeDomains(in.IncludeDomains)
	if err != nil {
		return fail(err)
	}
	excludeDomains, err := domain.NormalizeDomains(in.ExcludeDomains)
	if err != nil {
		return fail(err)
	}
	c, err := s.Store.Load(ctx)
	if err != nil {
		return fail(err)
	}
	if !c.WebSearchConfigured() {
		return fail(domain.Err(409, "web_search_unconfigured", "An administrator must configure the web search provider"))
	}
	if !c.WebSearchEnabled {
		return fail(domain.Err(409, "web_search_disabled", "Web search is disabled"))
	}
	release, ok := s.AcquireWebSlot()
	if !ok {
		return fail(domain.Err(429, "gateway_busy", "Web search gateway is busy"))
	}
	defer release()
	apiKey, err := s.Cipher.Decrypt(c.WebSearchCipherKey)
	if err != nil {
		return fail(domain.ErrWithCause(503, "credential_unavailable", "Saved web search credential could not be decrypted", err))
	}
	callCtx, cleanup := s.RegisterWebCancel(ctx, in.AgentID, in.RunID)
	defer cleanup()

	outcome.Phase = "provider-search"
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
	results, respBytes, err := s.WebSearch.Search(callCtx, s.tavilyBaseURL(), apiKey, payload, maxResults)
	outcome.ResponseBytes = respBytes
	if err != nil {
		return fail(err)
	}
	outcome.Results = results
	return outcome
}

// ExecuteWebExtract validates and executes a Tavily web extract call.
func (s *Service) ExecuteWebExtract(ctx context.Context, in domain.WebExtractInput) WebExtractOutcome {
	s.ensureInit()
	outcome := WebExtractOutcome{Phase: "admission", URLCount: len(in.URLs)}
	fail := func(err error, failed []domain.WebExtractFailure) WebExtractOutcome {
		de := domain.AsError(err)
		if de.Code == "gateway_unavailable" && de.Message == "Model gateway is temporarily unavailable" {
			de = domain.ErrWithCause(500, "gateway_unavailable", "Web extract gateway is temporarily unavailable", err)
		}
		outcome.Failure = de
		outcome.FailedResults = failed
		return outcome
	}
	urls, err := domain.ValidateExtractURLs(in.URLs)
	if err != nil {
		return fail(err, nil)
	}
	outcome.URLCount = len(urls)
	c, err := s.Store.Load(ctx)
	if err != nil {
		return fail(err, nil)
	}
	if !c.WebSearchConfigured() {
		return fail(domain.Err(409, "web_search_unconfigured", "An administrator must configure the web search provider"), nil)
	}
	if !c.WebSearchEnabled {
		return fail(domain.Err(409, "web_search_disabled", "Web search is disabled"), nil)
	}
	release, ok := s.AcquireWebSlot()
	if !ok {
		return fail(domain.Err(429, "gateway_busy", "Web search gateway is busy"), nil)
	}
	defer release()
	apiKey, err := s.Cipher.Decrypt(c.WebSearchCipherKey)
	if err != nil {
		return fail(domain.ErrWithCause(503, "credential_unavailable", "Saved web search credential could not be decrypted", err), nil)
	}
	callCtx, cleanup := s.RegisterWebCancel(ctx, in.AgentID, in.RunID)
	defer cleanup()

	outcome.Phase = "provider-extract"
	payload := map[string]any{
		"urls":           urls,
		"extract_depth":  "basic",
		"format":         "markdown",
		"include_images": false,
	}
	results, failedResults, respBytes, err := s.WebSearch.Extract(callCtx, s.tavilyBaseURL(), apiKey, payload)
	outcome.ResponseBytes = respBytes
	if err != nil {
		return fail(err, nil)
	}
	outcome.Results = results
	outcome.FailedResults = failedResults
	if len(results) == 0 {
		return fail(domain.Err(422, "web_extract_failed", "Target URLs could not be extracted"), failedResults)
	}
	return outcome
}
