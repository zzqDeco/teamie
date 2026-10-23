package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"go.uber.org/zap"
	"net/http"
	"strings"
	"sync"
	"teamie/backend/pkg/observability"
	"time"

	"teamie/agent-infra/internal/api"
	httpAdapter "teamie/agent-infra/internal/gateway/adapter/http"
	rpcAdapter "teamie/agent-infra/internal/gateway/adapter/rpc"
	"teamie/agent-infra/internal/gateway/app"
	"teamie/agent-infra/internal/gateway/domain"
	gatewaygen "teamie/agent-protocol/hertz_gen/agent/gateway"
	"teamie/agent-protocol/kitex_gen/agent"
	"teamie/agent-protocol/modeltransport"
)

var (
	_ gatewaygen.ModelGatewayHTTPService = (*httpAdapter.Adapter)(nil)
	_ agent.ModelGatewayService          = (*rpcAdapter.Server)(nil)
)

type Service struct {
	Store        Store
	Cipher       Cipher
	Provider     Provider
	Token        string
	WebSearchURL string
	slots        chan struct{}
	webSlots     chan struct{}
	mu           sync.Mutex
	webSeq       uint64
	active       map[string]context.CancelFunc
	canceled     map[string]time.Time
}

func New(store Store, cipher Cipher, token string) *Service {
	return &Service{Store: store, Cipher: cipher, Provider: Provider{NewHTTPClient()}, Token: token, WebSearchURL: defaultTavilyBaseURL, slots: make(chan struct{}, 1), webSlots: make(chan struct{}, 4), active: make(map[string]context.CancelFunc), canceled: make(map[string]time.Time)}
}
func (s *Service) Ping(ctx context.Context, r *agent.PingRequest) (*agent.PingResponse, error) {
	if r == nil || !api.EqualSecret(r.GetServiceToken(), s.Token) {
		return &agent.PingResponse{Ready: false}, nil
	}
	c, e := s.Store.Load(ctx)
	configured, enabled := c.Configured(), c.Enabled
	var configID *string
	if id := c.CountingConfigID(); id != "" {
		configID = &id
	}
	return &agent.PingResponse{Ready: e == nil, Configured: &configured, Enabled: &enabled, ContextWindowTokens: ptr(c.ContextWindow()), ModelConfigId: configID}, nil
}
func ptr[T any](v T) *T { return &v }
func failed(e error) *agent.GatewayChatResponse {
	var a *api.Error
	if !errors.As(e, &a) {
		a = &api.Error{Status: 500, Code: "gateway_unavailable", Message: "Model gateway is temporarily unavailable"}
	}
	return &agent.GatewayChatResponse{Ok: false, ErrorCode: &a.Code, ErrorMessage: &a.Message, StatusCode: ptr(int32(a.Status))}
}
func validID(v string) bool { return domain.ValidID(v) }
func (s *Service) Chat(ctx context.Context, r *agent.GatewayChatRequest) (*agent.GatewayChatResponse, error) {
	if r == nil || !api.EqualSecret(r.ServiceToken, s.Token) {
		return failed(api.Err(401, "unauthorized", "Invalid service credential")), nil
	}
	if !validID(r.OwnerId) || !validID(r.AgentId) || !validID(r.SessionId) || !validID(r.RunId) {
		return failed(api.Err(400, "invalid_request", "Invalid attribution identifiers")), nil
	}
	// Plain Thrift unary cancellation only releases the caller. Keeping a
	// provider call here could orphan the single model slot indefinitely.
	return failed(api.Err(400, "protocol_unsupported", "Model generation requires the private HTTP transport")), nil
}

func withExpectedModelConfigID(ctx context.Context, configID string) context.Context {
	return app.WithExpectedModelConfigID(ctx, configID)
}

func expectedModelConfigID(ctx context.Context) string {
	return app.ExpectedModelConfigID(ctx)
}

func (s *Service) call(ctx context.Context, owner, id, session, run, raw string, messageLimits ...int) (result *agent.GatewayChatResponse) {
	ctx, operation := gatewayOperation(ctx, id, session, run, false)
	var callFailure error
	var prompt, completion int64
	phase := "admission"
	defer func() {
		operation.End(callFailure, zap.String("custom.model.failure_stage", phase), zap.Int64("custom.model.prompt_tokens", prompt), zap.Int64("custom.model.completion_tokens", completion), zap.String("custom.duration_scope", "admission-through-provider-response-and-usage-record"), zap.Int("custom.retry.attempts", observability.HTTPAttempts(ctx)))
	}()
	fail := func(err error) *agent.GatewayChatResponse { callFailure = err; return failed(err) }
	c, e := s.Store.Load(ctx)
	if e != nil {
		return fail(e)
	}
	if !c.Configured() {
		return fail(api.Err(409, "model_unconfigured", "An administrator must configure the model gateway"))
	}
	if !c.Enabled {
		return fail(api.Err(409, "model_disabled", "The model gateway is disabled"))
	}
	var configID *string
	if id := c.CountingConfigID(); id != "" {
		configID = &id
	}
	failWithConfig := func(err error) *agent.GatewayChatResponse {
		res := fail(err)
		res.ModelConfigId = configID
		return res
	}
	if expected := expectedModelConfigID(ctx); expected != "" && expected != c.CountingConfigID() {
		return failWithConfig(api.Err(409, "gateway_unavailable", "Model gateway configuration changed"))
	}
	select {
	case s.slots <- struct{}{}:
		defer func() { <-s.slots }()
	default:
		return failWithConfig(api.Err(429, "gateway_busy", "The model gateway is busy"))
	}
	key, e := s.Cipher.Decrypt(c.CipherKey)
	if e != nil {
		return failWithConfig(&api.Error{Status: 503, Code: "credential_unavailable", Message: "Saved model credential could not be decrypted", Cause: e})
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	index := id + "/" + run
	s.mu.Lock()
	s.active[index] = cancel
	if at, ok := s.canceled[index]; ok && time.Since(at) < 2*time.Minute {
		cancel()
	}
	s.mu.Unlock()
	defer func() { s.mu.Lock(); delete(s.active, index); s.mu.Unlock() }()
	start := time.Now()
	phase = "provider-generate"
	var body string
	body, prompt, completion, e = s.Provider.Chat(ctx, c, key, raw, messageLimits...)
	status := "success"
	var response *agent.GatewayChatResponse
	if e != nil {
		callFailure = e
		response = failed(e)
		response.ModelConfigId = configID
		status = response.GetErrorCode()
	} else {
		response = &agent.GatewayChatResponse{Ok: true, ResponseJson: &body, StatusCode: ptr(int32(200)), ModelConfigId: configID}
		phase = "usage-persist"
	}
	auditCtx, auditCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer auditCancel()
	if auditErr := s.Store.Record(auditCtx, Usage{OwnerID: owner, AgentID: id, SessionID: session, RunID: run, Status: status, PromptTokens: prompt, CompletionTokens: completion, LatencyMS: time.Since(start).Milliseconds()}); auditErr != nil {
		phase = "usage-persist"
		return failWithConfig(&api.Error{Status: 503, Code: "usage_unavailable", Message: "Model usage could not be recorded", Cause: auditErr})
	}
	return response
}
func (s *Service) CancelRun(id, run string) {
	index := id + "/" + run
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
func (s *Service) Routes(auth api.AdminAuth) http.Handler {
	m := http.NewServeMux()
	api.Operations(m, s.Store.Ready)
	m.HandleFunc("POST "+modeltransport.GeneratePath, s.generateHTTP)
	m.HandleFunc("POST "+modeltransport.StreamPath, s.streamHTTP)
	m.HandleFunc("POST "+modeltransport.WebSearchPath, s.webSearchHTTP)
	m.HandleFunc("POST "+modeltransport.WebExtractPath, s.webExtractHTTP)
	wrap := func(fn func(http.ResponseWriter, *http.Request, string)) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			u, e := auth.Require(r)
			if e != nil {
				api.Fail(w, e)
				return
			}
			observability.AnnotateHTTP(w, zap.String("user.id", u.Id))
			fn(w, r, u.Id)
		}
	}
	m.HandleFunc("GET /api/v1/admin/model-gateway", wrap(func(w http.ResponseWriter, r *http.Request, _ string) {
		c, e := s.Store.Load(r.Context())
		if e != nil {
			api.Fail(w, e)
			return
		}
		api.Write(w, 200, c.View())
	}))
	m.HandleFunc("PUT /api/v1/admin/model-gateway", wrap(func(w http.ResponseWriter, r *http.Request, _ string) {
		var in struct {
			BaseURL              string  `json:"baseUrl"`
			Model                string  `json:"model"`
			APIKey               *string `json:"apiKey"`
			Enabled              bool    `json:"enabled"`
			Clear                bool    `json:"clearApiKey"`
			ContextWindowTokens  *int64  `json:"contextWindowTokens"`
			WebSearchEnabled     *bool   `json:"webSearchEnabled"`
			WebSearchAPIKey      *string `json:"webSearchApiKey"`
			ClearWebSearchAPIKey bool    `json:"clearWebSearchApiKey"`
		}
		if e := api.Decode(w, r, &in); e != nil {
			api.Fail(w, e)
			return
		}
		in.BaseURL = strings.TrimRight(strings.TrimSpace(in.BaseURL), "/")
		in.Model = strings.TrimSpace(in.Model)
		if len(in.Model) > 160 || strings.ContainsAny(in.Model, "\r\n\x00") || in.APIKey != nil && (in.Clear || len(*in.APIKey) == 0 || len(*in.APIKey) > 16384 || strings.ContainsAny(*in.APIKey, "\r\n\x00")) {
			api.Fail(w, api.Err(400, "invalid_request", "Invalid model configuration"))
			return
		}
		if in.WebSearchAPIKey != nil && (in.ClearWebSearchAPIKey || len(*in.WebSearchAPIKey) == 0 || len(*in.WebSearchAPIKey) > 16384 || strings.ContainsAny(*in.WebSearchAPIKey, "\r\n\x00")) {
			api.Fail(w, api.Err(400, "invalid_request", "Invalid web search configuration"))
			return
		}
		if in.ContextWindowTokens != nil && (*in.ContextWindowTokens < 4096 || *in.ContextWindowTokens > 2000000) {
			api.Fail(w, api.Err(400, "invalid_request", "Context window must be between 4096 and 2000000 tokens"))
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		if e := ValidateEndpoint(ctx, in.BaseURL); e != nil {
			api.Fail(w, e)
			return
		}
		c := Config{BaseURL: in.BaseURL, Model: in.Model, Enabled: in.Enabled, WebSearchAPIKey: in.WebSearchAPIKey, ClearWebSearchKey: in.ClearWebSearchAPIKey}
		if in.WebSearchEnabled != nil {
			c.WebSearchEnabled = *in.WebSearchEnabled
			c.WebSearchEnabledSet = true
		}
		if in.ContextWindowTokens != nil {
			c.ContextWindowTokens = *in.ContextWindowTokens
		}
		c, e := s.Store.Save(ctx, c, in.APIKey, in.Clear, s.Cipher)
		if e != nil {
			api.Fail(w, e)
			return
		}
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
		api.Write(w, 200, c.View())
	}))
	m.HandleFunc("POST /api/v1/admin/model-gateway/test", wrap(func(w http.ResponseWriter, r *http.Request, owner string) {
		c, e := s.Store.Load(r.Context())
		if e != nil {
			api.Fail(w, e)
			return
		}
		start := time.Now()
		raw, _ := json.Marshal(modeltransport.Request{Protocol: modeltransport.Version, Messages: json.RawMessage(`[{"role":"user","content_blocks":[{"type":"user_input_text","user_input_text":{"text":"Reply with OK."}}]}]`), Options: json.RawMessage(`{"MaxTokens":8,"AgenticToolChoice":{"Type":"forbidden"}}`)})
		res := s.call(r.Context(), owner, "admin-test", "admin-test", time.Now().UTC().Format("20060102T150405.000000000"), string(raw))
		if !res.Ok {
			api.Fail(w, api.Err(int(res.GetStatusCode()), res.GetErrorCode(), res.GetErrorMessage()))
			return
		}
		api.Write(w, 200, map[string]any{"ok": true, "model": c.Model, "latencyMs": time.Since(start).Milliseconds()})
	}))
	return m
}
func (s *Service) Cancel(ctx context.Context, r *agent.CancelRequest) (*agent.Ack, error) {
	if r == nil || !api.EqualSecret(r.ServiceToken, s.Token) || !validID(r.AgentId) || !validID(r.RunId) {
		return &agent.Ack{Ok: false}, nil
	}
	s.CancelRun(r.AgentId, r.RunId)
	return &agent.Ack{Ok: true}, nil
}
