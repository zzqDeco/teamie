package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"teamie/agent-infra/internal/api"
)

// StreamEvent is the private NDJSON transport. A response has exactly one terminal
// done/error event; an EOF without a terminal event must never be treated as success.
type StreamEvent struct {
	Type         string `json:"type"`
	ResponseJSON string `json:"responseJson,omitempty"`
	ErrorCode    string `json:"errorCode,omitempty"`
	ErrorMessage string `json:"errorMessage,omitempty"`
	StatusCode   int    `json:"statusCode,omitempty"`
}

func streamError(ctx context.Context, fallback string) error {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return api.Err(504, "upstream_timeout", "Model request exceeded its deadline")
	}
	if ctx.Err() != nil {
		return api.Err(499, "canceled", "Model request canceled")
	}
	return api.Err(502, fallback, "Provider stream ended without a complete response")
}

func (s *Service) streamHTTP(w http.ResponseWriter, r *http.Request) {
	in, ok := s.decodeHTTPCall(w, r)
	if !ok {
		return
	}
	w.Header().Set("Content-Type", "application/x-ndjson")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	enc := json.NewEncoder(w)
	emit := func(event StreamEvent) error {
		if e := enc.Encode(event); e != nil {
			return e
		}
		return http.NewResponseController(w).Flush()
	}
	terminal := func(e error) {
		if e == nil {
			_ = emit(StreamEvent{Type: "done"})
			return
		}
		failed := failed(e)
		_ = emit(StreamEvent{Type: "error", ErrorCode: failed.GetErrorCode(), ErrorMessage: failed.GetErrorMessage(), StatusCode: int(failed.GetStatusCode())})
	}
	c, e := s.Store.Load(r.Context())
	if e != nil {
		terminal(e)
		return
	}
	if !c.Configured() {
		terminal(api.Err(409, "model_unconfigured", "An administrator must configure the model gateway"))
		return
	}
	if !c.Enabled {
		terminal(api.Err(409, "model_disabled", "The model gateway is disabled"))
		return
	}
	select {
	case s.slots <- struct{}{}:
		defer func() { <-s.slots }()
	default:
		terminal(api.Err(429, "gateway_busy", "The model gateway is busy"))
		return
	}
	key, e := s.Cipher.Decrypt(c.CipherKey)
	if e != nil {
		terminal(api.Err(503, "credential_unavailable", "Saved model credential could not be decrypted"))
		return
	}
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	index := in.AgentID + "/" + in.RunID
	s.mu.Lock()
	s.active[index] = cancel
	if at, ok := s.canceled[index]; ok && time.Since(at) < 2*time.Minute {
		cancel()
	}
	s.mu.Unlock()
	defer func() { s.mu.Lock(); delete(s.active, index); s.mu.Unlock() }()
	start := time.Now()
	prompt, completion, e := s.Provider.Stream(ctx, c, key, in.RequestJSON, func(raw string) error { return emit(StreamEvent{Type: "chunk", ResponseJSON: raw}) })
	status := "success"
	if e != nil {
		status = failed(e).GetErrorCode()
	}
	audit, auditCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer auditCancel()
	if s.Store.Record(audit, Usage{OwnerID: in.OwnerID, AgentID: in.AgentID, SessionID: in.SessionID, RunID: in.RunID, Status: status, PromptTokens: prompt, CompletionTokens: completion, LatencyMS: time.Since(start).Milliseconds()}) != nil {
		terminal(api.Err(503, "usage_unavailable", "Model usage could not be recorded"))
		return
	}
	terminal(e)
}
