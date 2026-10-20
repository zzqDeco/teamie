package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"go.uber.org/zap"
	"net/http"
	"teamie/backend/pkg/observability"
	"time"

	"teamie/agent-infra/internal/api"
	"teamie/agent-protocol/modeltransport"
)

// StreamEvent is the private NDJSON transport. A response has exactly one terminal
// done/error event; an EOF without a terminal event must never be treated as success.
type StreamEvent struct {
	Type          string `json:"type"`
	ResponseJSON  string `json:"responseJson,omitempty"`
	ErrorCode     string `json:"errorCode,omitempty"`
	ErrorMessage  string `json:"errorMessage,omitempty"`
	StatusCode    int    `json:"statusCode,omitempty"`
	ModelConfigID string `json:"modelConfigId,omitempty"`
}

func streamError(ctx context.Context, fallback string) error {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return &api.Error{Status: 504, Code: "upstream_timeout", Message: "Model request exceeded its deadline", Cause: ctx.Err()}
	}
	if ctx.Err() != nil {
		return &api.Error{Status: 499, Code: "canceled", Message: "Model request canceled", Cause: ctx.Err()}
	}
	return api.Err(502, fallback, "Provider stream ended without a complete response")
}

func (s *Service) streamHTTP(w http.ResponseWriter, r *http.Request) {
	in, ok := s.decodeHTTPCall(w, r)
	if !ok {
		return
	}
	ctx, operation := gatewayOperation(r.Context(), in.AgentID, in.SessionID, in.RunID, true)
	r = r.WithContext(ctx)
	var streamFailure error
	var prompt, completion int64
	phase := "admission"
	chunks, bytes := 0, 0
	defer func() {
		operation.End(streamFailure, zap.String("custom.model.failure_stage", phase), zap.Int64("custom.model.prompt_tokens", prompt), zap.Int64("custom.model.completion_tokens", completion), zap.Int("custom.model.stream.chunk_count", chunks), zap.Int("custom.model.stream.transport_bytes", bytes), zap.String("custom.duration_scope", "admission-through-provider-stream-and-usage-record"), zap.Int("custom.retry.attempts", observability.HTTPAttempts(ctx)))
	}()
	w.Header().Set("Content-Type", "application/x-ndjson")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	enc := json.NewEncoder(w)
	emit := func(event StreamEvent) error {
		if event.Type == "chunk" {
			chunks++
			bytes += len(event.ResponseJSON)
		}
		if e := enc.Encode(event); e != nil {
			return e
		}
		return http.NewResponseController(w).Flush()
	}
	var configID string
	terminal := func(e error) {
		streamFailure = e
		if e == nil {
			if err := emit(StreamEvent{Type: "done", ModelConfigID: configID}); err != nil {
				streamFailure = observability.Diagnostic("downstream_terminal_write_failed", err)
			}
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
	configID = c.CountingConfigID()
	if configID != "" {
		w.Header().Set(modeltransport.ModelConfigIDHeader, configID)
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
		terminal(&api.Error{Status: 503, Code: "credential_unavailable", Message: "Saved model credential could not be decrypted", Cause: e})
		return
	}
	ctx, cancel := context.WithCancel(ctx)
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
	phase = "provider-stream"
	prompt, completion, e = s.Provider.Stream(ctx, c, key, in.RequestJSON, func(raw string) error { return emit(StreamEvent{Type: "chunk", ResponseJSON: raw}) }, in.InputMessageLimit())
	status := "success"
	if e != nil {
		status = failed(e).GetErrorCode()
	} else {
		phase = "usage-persist"
	}
	audit, auditCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer auditCancel()
	if auditErr := s.Store.Record(audit, Usage{OwnerID: in.OwnerID, AgentID: in.AgentID, SessionID: in.SessionID, RunID: in.RunID, Status: status, PromptTokens: prompt, CompletionTokens: completion, LatencyMS: time.Since(start).Milliseconds()}); auditErr != nil {
		phase = "usage-persist"
		terminal(&api.Error{Status: 503, Code: "usage_unavailable", Message: "Model usage could not be recorded", Cause: auditErr})
		return
	}
	if e == nil {
		phase = "terminal-write"
	}
	terminal(e)
}
