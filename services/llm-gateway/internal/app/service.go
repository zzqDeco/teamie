// Package app implements the application use cases for the Model Gateway
// bounded context. It orchestrates domain entities, concurrency/slot admission,
// cancellation registries, and outbound ports without importing HTTP, RPC, Eino,
// or database drivers.
package app

import (
	"context"
	"strconv"
	"strings"
	"sync"
	"time"

	"teamie/services/llm-gateway/internal/domain"
)

type expectedConfigIDKey struct{}

// WithExpectedModelConfigID attaches an expected model config ID to ctx.
func WithExpectedModelConfigID(ctx context.Context, configID string) context.Context {
	return context.WithValue(ctx, expectedConfigIDKey{}, strings.TrimSpace(configID))
}

// ExpectedModelConfigID reads the expected model config ID from ctx.
func ExpectedModelConfigID(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	id, _ := ctx.Value(expectedConfigIDKey{}).(string)
	return id
}

// PingStatus holds the gateway readiness and configuration summary.
type PingStatus struct {
	Ready               bool
	Configured          *bool
	Enabled             *bool
	ContextWindowTokens *int64
	ModelConfigID       *string
}

// SaveConfigInput holds the administrator configuration update command.
type SaveConfigInput struct {
	BaseURL              string
	Model                string
	APIKey               *string
	Enabled              bool
	ClearAPIKey          bool
	ContextWindowTokens  *int64
	WebSearchEnabled     *bool
	WebSearchAPIKey      *string
	ClearWebSearchAPIKey bool
}

// CallOutcome records the result and diagnostic telemetry of a unary model call.
type CallOutcome struct {
	Result           domain.ChatCallResult
	Phase            string
	PromptTokens     int64
	CompletionTokens int64
	Failure          error
}

// StreamOutcome records the diagnostic telemetry of a streaming model call.
type StreamOutcome struct {
	Phase              string
	PromptTokens       int64
	CompletionTokens   int64
	ChunkCount         int
	TransportBytes     int
	Failure            error
	TerminalWriteError error
}

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

// Service coordinates Model Gateway use cases, slot admission, and run cancellation.
type Service struct {
	Store        domain.ConfigStore
	Cipher       domain.Cipher
	Model        domain.UpstreamModel
	WebSearch    domain.WebSearchProvider
	Token        string
	WebSearchURL string
	slots        chan struct{}
	webSlots     chan struct{}
	mu           sync.Mutex
	webSeq       uint64
	active       map[string]context.CancelFunc
	canceled     map[string]time.Time
}

// New constructs a new gateway application Service.
func New(store domain.ConfigStore, cipher domain.Cipher, model domain.UpstreamModel, webSearch domain.WebSearchProvider, token string) *Service {
	return &Service{
		Store:        store,
		Cipher:       cipher,
		Model:        model,
		WebSearch:    webSearch,
		Token:        token,
		WebSearchURL: domain.DefaultTavilyBaseURL,
		slots:        make(chan struct{}, 1),
		webSlots:     make(chan struct{}, 4),
		active:       make(map[string]context.CancelFunc),
		canceled:     make(map[string]time.Time),
	}
}

func (s *Service) ensureInit() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.slots == nil {
		s.slots = make(chan struct{}, 1)
	}
	if s.webSlots == nil {
		s.webSlots = make(chan struct{}, 4)
	}
	if s.active == nil {
		s.active = make(map[string]context.CancelFunc)
	}
	if s.canceled == nil {
		s.canceled = make(map[string]time.Time)
	}
}

// InFlightSlots returns the number of currently occupied foreground model slots.
func (s *Service) InFlightSlots() int {
	s.ensureInit()
	return len(s.slots)
}

// AuthorizeBearer checks a service Bearer Authorization header against s.Token.
func (s *Service) AuthorizeBearer(header string) error {
	if !strings.HasPrefix(header, "Bearer ") || !domain.EqualSecret(strings.TrimPrefix(header, "Bearer "), s.Token) {
		return domain.Err(401, "unauthorized", "Invalid service credential")
	}
	return nil
}

// Ping checks service token and returns current gateway configuration readiness.
func (s *Service) Ping(ctx context.Context, serviceToken string) PingStatus {
	if !domain.EqualSecret(serviceToken, s.Token) {
		return PingStatus{Ready: false}
	}
	c, err := s.Store.Load(ctx)
	configured, enabled := c.Configured(), c.Enabled
	cw := c.ContextWindow()
	var configID *string
	if id := c.CountingConfigID(); id != "" {
		configID = &id
	}
	return PingStatus{
		Ready:               err == nil,
		Configured:          &configured,
		Enabled:             &enabled,
		ContextWindowTokens: &cw,
		ModelConfigID:       configID,
	}
}

// ValidateUnaryChat validates unary RPC chat call credentials and attribution,
// returning protocol_unsupported because model inference requires private HTTP.
func (s *Service) ValidateUnaryChat(serviceToken, ownerID, agentID, sessionID, runID string) error {
	if !domain.EqualSecret(serviceToken, s.Token) {
		return domain.Err(401, "unauthorized", "Invalid service credential")
	}
	if !domain.ValidID(ownerID) || !domain.ValidID(agentID) || !domain.ValidID(sessionID) || !domain.ValidID(runID) {
		return domain.Err(400, "invalid_request", "Invalid attribution identifiers")
	}
	return domain.Err(400, "protocol_unsupported", "Model generation requires the private HTTP transport")
}

// LoadConfig loads the persisted gateway configuration.
func (s *Service) LoadConfig(ctx context.Context) (domain.Config, error) {
	return s.Store.Load(ctx)
}

// SaveConfig validates and persists updated administrator gateway configuration.
func (s *Service) SaveConfig(ctx context.Context, in SaveConfigInput) (domain.Config, error) {
	s.ensureInit()
	in.BaseURL = strings.TrimRight(strings.TrimSpace(in.BaseURL), "/")
	in.Model = strings.TrimSpace(in.Model)
	if len(in.Model) > 160 || strings.ContainsAny(in.Model, "\r\n\x00") ||
		in.APIKey != nil && (in.ClearAPIKey || len(*in.APIKey) == 0 || len(*in.APIKey) > 16384 || strings.ContainsAny(*in.APIKey, "\r\n\x00")) {
		return domain.Config{}, domain.Err(400, "invalid_request", "Invalid model configuration")
	}
	if in.WebSearchAPIKey != nil && (in.ClearWebSearchAPIKey || len(*in.WebSearchAPIKey) == 0 || len(*in.WebSearchAPIKey) > 16384 || strings.ContainsAny(*in.WebSearchAPIKey, "\r\n\x00")) {
		return domain.Config{}, domain.Err(400, "invalid_request", "Invalid web search configuration")
	}
	if in.ContextWindowTokens != nil && (*in.ContextWindowTokens < domain.MinContextWindow || *in.ContextWindowTokens > domain.MaxContextWindow) {
		return domain.Config{}, domain.Err(400, "invalid_request", "Context window must be between 4096 and 2000000 tokens")
	}
	validateCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := domain.ValidateEndpoint(validateCtx, in.BaseURL); err != nil {
		return domain.Config{}, err
	}
	c := domain.Config{
		BaseURL:           in.BaseURL,
		Model:             in.Model,
		Enabled:           in.Enabled,
		WebSearchAPIKey:   in.WebSearchAPIKey,
		ClearWebSearchKey: in.ClearWebSearchAPIKey,
	}
	if in.WebSearchEnabled != nil {
		c.WebSearchEnabled = *in.WebSearchEnabled
		c.WebSearchEnabledSet = true
	}
	if in.ContextWindowTokens != nil {
		c.ContextWindowTokens = *in.ContextWindowTokens
	}
	saved, err := s.Store.Save(validateCtx, c, in.APIKey, in.ClearAPIKey, s.Cipher)
	if err != nil {
		return domain.Config{}, err
	}
	s.CancelDisabledActiveRuns(saved)
	return saved, nil
}

// CancelDisabledActiveRuns cancels in-flight model or web-search calls when disabled or unconfigured.
func (s *Service) CancelDisabledActiveRuns(c domain.Config) {
	s.ensureInit()
	if !c.Enabled || !c.Configured() || !c.WebSearchEnabled || !c.WebSearchConfigured() {
		s.mu.Lock()
		for key, cancel := range s.active {
			isWeb := strings.Contains(key, "/web/")
			if (!isWeb && (!c.Enabled || !c.Configured())) || (isWeb && (!c.WebSearchEnabled || !c.WebSearchConfigured())) {
				cancel()
			}
		}
		s.mu.Unlock()
	}
}

// AcquireSlot attempts to acquire the single foreground model execution slot.
func (s *Service) AcquireSlot() (func(), bool) {
	s.ensureInit()
	select {
	case s.slots <- struct{}{}:
		return func() { <-s.slots }, true
	default:
		return nil, false
	}
}

// AcquireWebSlot attempts to acquire a web-search execution slot.
func (s *Service) AcquireWebSlot() (func(), bool) {
	s.ensureInit()
	select {
	case s.webSlots <- struct{}{}:
		return func() { <-s.webSlots }, true
	default:
		return nil, false
	}
}

// RegisterRunCancel registers a cancelable context for an agent run.
func (s *Service) RegisterRunCancel(ctx context.Context, agentID, runID string) (context.Context, func()) {
	s.ensureInit()
	ctx, cancel := context.WithCancel(ctx)
	index := agentID + "/" + runID
	s.mu.Lock()
	s.active[index] = cancel
	if at, ok := s.canceled[index]; ok && time.Since(at) < 2*time.Minute {
		cancel()
	}
	s.mu.Unlock()
	return ctx, func() {
		cancel()
		s.mu.Lock()
		delete(s.active, index)
		s.mu.Unlock()
	}
}

// RegisterWebCancel registers a cancelable context for an agent web-search/extract call.
func (s *Service) RegisterWebCancel(ctx context.Context, agentID, runID string) (context.Context, func()) {
	s.ensureInit()
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

// CancelRun cancels any active model or web-search call for the given agent and run.
func (s *Service) CancelRun(agentID, runID string) {
	s.ensureInit()
	index := agentID + "/" + runID
	s.mu.Lock()
	now := time.Now()
	for key, at := range s.canceled {
		if now.Sub(at) > 2*time.Minute {
			delete(s.canceled, key)
		}
	}
	if len(s.canceled) >= 256 {
		var oldestKey string
		var oldest time.Time
		for key, at := range s.canceled {
			if oldestKey == "" || at.Before(oldest) {
				oldestKey = key
				oldest = at
			}
		}
		delete(s.canceled, oldestKey)
	}
	s.canceled[index] = now
	var cancels []context.CancelFunc
	if cancel := s.active[index]; cancel != nil {
		cancels = append(cancels, cancel)
	}
	prefix := index + "/web/"
	for key, cancel := range s.active {
		if strings.HasPrefix(key, prefix) && cancel != nil {
			cancels = append(cancels, cancel)
		}
	}
	s.mu.Unlock()
	for _, cancel := range cancels {
		cancel()
	}
}

// Cancel validates the service token and attribution IDs and cancels the run.
func (s *Service) Cancel(serviceToken, agentID, runID string) bool {
	if !domain.EqualSecret(serviceToken, s.Token) || !domain.ValidID(agentID) || !domain.ValidID(runID) {
		return false
	}
	s.CancelRun(agentID, runID)
	return true
}

func failedChatResult(err error, configID string) domain.ChatCallResult {
	de := domain.AsError(err)
	return domain.ChatCallResult{
		OK:            false,
		ErrorCode:     de.Code,
		ErrorMessage:  de.Message,
		StatusCode:    de.Status,
		ModelConfigID: configID,
	}
}

// CallChat executes a unary model inference call with slot admission, cancellation tracking, and usage auditing.
func (s *Service) CallChat(ctx context.Context, owner, agentID, sessionID, runID, raw string, messageLimits ...int) CallOutcome {
	s.ensureInit()
	outcome := CallOutcome{Phase: "admission"}
	fail := func(err error, cfgID string) CallOutcome {
		outcome.Failure = err
		outcome.Result = failedChatResult(err, cfgID)
		return outcome
	}
	c, err := s.Store.Load(ctx)
	if err != nil {
		return fail(err, "")
	}
	if !c.Configured() {
		return fail(domain.Err(409, "model_unconfigured", "An administrator must configure the model gateway"), "")
	}
	if !c.Enabled {
		return fail(domain.Err(409, "model_disabled", "The model gateway is disabled"), "")
	}
	configID := c.CountingConfigID()
	if expected := ExpectedModelConfigID(ctx); expected != "" && expected != configID {
		return fail(domain.Err(409, "gateway_unavailable", "Model gateway configuration changed"), configID)
	}
	release, ok := s.AcquireSlot()
	if !ok {
		return fail(domain.Err(429, "gateway_busy", "The model gateway is busy"), configID)
	}
	defer release()
	key, err := s.Cipher.Decrypt(c.CipherKey)
	if err != nil {
		return fail(domain.ErrWithCause(503, "credential_unavailable", "Saved model credential could not be decrypted", err), configID)
	}
	callCtx, cleanup := s.RegisterRunCancel(ctx, agentID, runID)
	defer cleanup()

	start := time.Now()
	outcome.Phase = "provider-generate"
	body, prompt, completion, err := s.Model.Chat(callCtx, c, key, raw, messageLimits...)
	outcome.PromptTokens = prompt
	outcome.CompletionTokens = completion
	status := "success"
	if err != nil {
		outcome.Failure = err
		outcome.Result = failedChatResult(err, configID)
		status = outcome.Result.ErrorCode
	} else {
		outcome.Result = domain.ChatCallResult{
			OK:            true,
			ResponseJSON:  body,
			StatusCode:    200,
			ModelConfigID: configID,
		}
		outcome.Phase = "usage-persist"
	}
	auditCtx, auditCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer auditCancel()
	if auditErr := s.Store.Record(auditCtx, domain.Usage{
		OwnerID:          owner,
		AgentID:          agentID,
		SessionID:        sessionID,
		RunID:            runID,
		Status:           status,
		PromptTokens:     prompt,
		CompletionTokens: completion,
		LatencyMS:        time.Since(start).Milliseconds(),
	}); auditErr != nil {
		outcome.Phase = "usage-persist"
		return fail(domain.ErrWithCause(503, "usage_unavailable", "Model usage could not be recorded", auditErr), configID)
	}
	return outcome
}

// StreamChat executes an NDJSON streaming model inference call with slot admission, cancellation tracking, and usage auditing.
func (s *Service) StreamChat(ctx context.Context, owner, agentID, sessionID, runID, expectedConfigID, raw string, onConfigID func(string), emit func(domain.StreamEvent) error, messageLimits ...int) StreamOutcome {
	s.ensureInit()
	outcome := StreamOutcome{Phase: "admission"}
	var configID string
	writeEvent := func(ev domain.StreamEvent) error {
		if ev.Type == "chunk" {
			outcome.ChunkCount++
			outcome.TransportBytes += len(ev.ResponseJSON)
		}
		return emit(ev)
	}
	terminal := func(err error) StreamOutcome {
		outcome.Failure = err
		if err == nil {
			if wErr := writeEvent(domain.StreamEvent{Type: "done", ModelConfigID: configID}); wErr != nil {
				outcome.TerminalWriteError = wErr
			}
			return outcome
		}
		de := domain.AsError(err)
		_ = writeEvent(domain.StreamEvent{
			Type:          "error",
			ErrorCode:     de.Code,
			ErrorMessage:  de.Message,
			StatusCode:    de.Status,
			ModelConfigID: configID,
		})
		return outcome
	}
	c, err := s.Store.Load(ctx)
	if err != nil {
		return terminal(err)
	}
	if !c.Configured() {
		return terminal(domain.Err(409, "model_unconfigured", "An administrator must configure the model gateway"))
	}
	if !c.Enabled {
		return terminal(domain.Err(409, "model_disabled", "The model gateway is disabled"))
	}
	configID = c.CountingConfigID()
	if configID != "" && onConfigID != nil {
		onConfigID(configID)
	}
	if expected := strings.TrimSpace(expectedConfigID); expected != "" && expected != configID {
		return terminal(domain.Err(409, "gateway_unavailable", "Model gateway configuration changed"))
	}
	release, ok := s.AcquireSlot()
	if !ok {
		return terminal(domain.Err(429, "gateway_busy", "The model gateway is busy"))
	}
	defer release()
	key, err := s.Cipher.Decrypt(c.CipherKey)
	if err != nil {
		return terminal(domain.ErrWithCause(503, "credential_unavailable", "Saved model credential could not be decrypted", err))
	}
	callCtx, cleanup := s.RegisterRunCancel(ctx, agentID, runID)
	defer cleanup()

	start := time.Now()
	outcome.Phase = "provider-stream"
	prompt, completion, err := s.Model.Stream(callCtx, c, key, raw, func(chunk string) error {
		return writeEvent(domain.StreamEvent{Type: "chunk", ResponseJSON: chunk})
	}, messageLimits...)
	outcome.PromptTokens = prompt
	outcome.CompletionTokens = completion
	status := "success"
	if err != nil {
		status = domain.AsError(err).Code
	} else {
		outcome.Phase = "usage-persist"
	}
	auditCtx, auditCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer auditCancel()
	if auditErr := s.Store.Record(auditCtx, domain.Usage{
		OwnerID:          owner,
		AgentID:          agentID,
		SessionID:        sessionID,
		RunID:            runID,
		Status:           status,
		PromptTokens:     prompt,
		CompletionTokens: completion,
		LatencyMS:        time.Since(start).Milliseconds(),
	}); auditErr != nil {
		outcome.Phase = "usage-persist"
		return terminal(domain.ErrWithCause(503, "usage_unavailable", "Model usage could not be recorded", auditErr))
	}
	if err == nil {
		outcome.Phase = "terminal-write"
	}
	return terminal(err)
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
