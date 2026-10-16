package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"teamie/agent-infra/internal/api"
	"teamie/agent-protocol/kitex_gen/agent"
	"teamie/agent-protocol/modeltransport"
)

type Service struct {
	Store    Store
	Cipher   Cipher
	Provider Provider
	Token    string
	slots    chan struct{}
	mu       sync.Mutex
	active   map[string]context.CancelFunc
	canceled map[string]time.Time
}

func New(store Store, cipher Cipher, token string) *Service {
	return &Service{Store: store, Cipher: cipher, Provider: Provider{NewHTTPClient()}, Token: token, slots: make(chan struct{}, 1), active: make(map[string]context.CancelFunc), canceled: make(map[string]time.Time)}
}
func (s *Service) Ping(ctx context.Context, r *agent.PingRequest) (*agent.PingResponse, error) {
	if r == nil || !api.EqualSecret(r.GetServiceToken(), s.Token) {
		return &agent.PingResponse{Ready: false}, nil
	}
	c, e := s.Store.Load(ctx)
	configured, enabled := c.Configured(), c.Enabled
	return &agent.PingResponse{Ready: e == nil, Configured: &configured, Enabled: &enabled, ContextWindowTokens: ptr(c.ContextWindow())}, nil
}
func ptr[T any](v T) *T { return &v }
func failed(e error) *agent.GatewayChatResponse {
	var a *api.Error
	if !errors.As(e, &a) {
		a = &api.Error{Status: 500, Code: "gateway_unavailable", Message: "Model gateway is temporarily unavailable"}
	}
	return &agent.GatewayChatResponse{Ok: false, ErrorCode: &a.Code, ErrorMessage: &a.Message, StatusCode: ptr(int32(a.Status))}
}
func validID(v string) bool { return v != "" && len(v) <= 160 && !strings.ContainsAny(v, "\r\n\x00") }
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
func (s *Service) call(ctx context.Context, owner, id, session, run, raw string) *agent.GatewayChatResponse {
	c, e := s.Store.Load(ctx)
	if e != nil {
		return failed(e)
	}
	if !c.Configured() {
		return failed(api.Err(409, "model_unconfigured", "An administrator must configure the model gateway"))
	}
	if !c.Enabled {
		return failed(api.Err(409, "model_disabled", "The model gateway is disabled"))
	}
	select {
	case s.slots <- struct{}{}:
		defer func() { <-s.slots }()
	default:
		return failed(api.Err(429, "gateway_busy", "The model gateway is busy"))
	}
	key, e := s.Cipher.Decrypt(c.CipherKey)
	if e != nil {
		return failed(api.Err(503, "credential_unavailable", "Saved model credential could not be decrypted"))
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
	body, prompt, completion, e := s.Provider.Chat(ctx, c, key, raw)
	status := "success"
	var response *agent.GatewayChatResponse
	if e != nil {
		response = failed(e)
		status = response.GetErrorCode()
	} else {
		response = &agent.GatewayChatResponse{Ok: true, ResponseJson: &body, StatusCode: ptr(int32(200))}
	}
	auditCtx, auditCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer auditCancel()
	if s.Store.Record(auditCtx, Usage{OwnerID: owner, AgentID: id, SessionID: session, RunID: run, Status: status, PromptTokens: prompt, CompletionTokens: completion, LatencyMS: time.Since(start).Milliseconds()}) != nil {
		return failed(api.Err(503, "usage_unavailable", "Model usage could not be recorded"))
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
	cancel := s.active[index]
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}
func (s *Service) Routes(auth api.AdminAuth) http.Handler {
	m := http.NewServeMux()
	api.Operations(m, s.Store.Ready)
	m.HandleFunc("POST "+modeltransport.GeneratePath, s.generateHTTP)
	m.HandleFunc("POST "+modeltransport.StreamPath, s.streamHTTP)
	wrap := func(fn func(http.ResponseWriter, *http.Request, string)) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			u, e := auth.Require(r)
			if e != nil {
				api.Fail(w, e)
				return
			}
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
			BaseURL             string  `json:"baseUrl"`
			Model               string  `json:"model"`
			APIKey              *string `json:"apiKey"`
			Enabled             bool    `json:"enabled"`
			Clear               bool    `json:"clearApiKey"`
			ContextWindowTokens *int64  `json:"contextWindowTokens"`
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
		c := Config{BaseURL: in.BaseURL, Model: in.Model, Enabled: in.Enabled}
		if in.ContextWindowTokens != nil {
			c.ContextWindowTokens = *in.ContextWindowTokens
		}
		c, e := s.Store.Save(ctx, c, in.APIKey, in.Clear, s.Cipher)
		if e != nil {
			api.Fail(w, e)
			return
		}
		if !c.Enabled || !c.Configured() {
			s.mu.Lock()
			for _, cancel := range s.active {
				cancel()
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
