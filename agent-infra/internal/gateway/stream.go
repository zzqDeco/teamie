package gateway

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
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

// Stream forwards actual incremental provider chunks, including tool-call deltas.
// It does not synthesize a stream from the unary Chat result.
func (p Provider) Stream(ctx context.Context, c Config, key, raw string, emit func(string) error) (prompt, completion int64, err error) {
	if ctx.Err() != nil {
		return 0, 0, streamError(ctx, "")
	}
	payload, e := normalizeMode(raw, c.Model, true)
	if e != nil {
		return 0, 0, e
	}
	u, e := endpoint(c.BaseURL)
	if e != nil {
		return 0, 0, e
	}
	u.Path = strings.TrimRight(u.Path, "/") + "/chat/completions"
	req, e := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), bytes.NewReader(payload))
	if e != nil {
		return 0, 0, api.Err(502, "upstream_error", "Provider request could not be created")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Authorization", "Bearer "+key)
	client := *p.HTTP
	client.Timeout = 0 // Lifetime is governed by the stream context, not the unary timeout.
	res, e := client.Do(req)
	if e != nil {
		if ctx.Err() != nil {
			return 0, 0, streamError(ctx, "")
		}
		return 0, 0, api.Err(502, "upstream_unavailable", "Provider could not be reached")
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		if res.StatusCode == 429 {
			return 0, 0, api.Err(429, "upstream_rate_limited", "Provider rejected the request")
		}
		return 0, 0, api.Err(502, "upstream_http_error", "Provider rejected the request")
	}
	if !strings.HasPrefix(strings.ToLower(res.Header.Get("Content-Type")), "text/event-stream") {
		body, _ := io.ReadAll(io.LimitReader(res.Body, MaxRequestBytes))
		var obj struct {
			Error json.RawMessage `json:"error"`
		}
		if json.Unmarshal(body, &obj) == nil && len(obj.Error) > 0 && string(obj.Error) != "null" {
			return 0, 0, api.Err(502, "upstream_in_stream_error", "Provider reported an error in its response")
		}
		return 0, 0, api.Err(502, "invalid_upstream_response", "Provider did not return an event stream")
	}
	scan := bufio.NewScanner(io.LimitReader(res.Body, MaxResponseBytes+1))
	scan.Buffer(make([]byte, 4096), MaxRequestBytes)
	var data strings.Builder
	total := 0
	finished, done := false, false
	guard := newCredentialStreamGuard(key)
	emitSafe := func(value string) error {
		if e := emit(value); e != nil {
			return api.Err(499, "client_interrupted", "Stream consumer disconnected")
		}
		return nil
	}
	process := func() error {
		if data.Len() == 0 {
			return nil
		}
		value := strings.TrimSuffix(data.String(), "\n")
		data.Reset()
		if value == "[DONE]" {
			if !finished {
				return api.Err(502, "upstream_interrupted", "Provider stream ended before a finish reason")
			}
			if e := guard.finish(emitSafe); e != nil {
				return e
			}
			done = true
			return nil
		}
		if done {
			return api.Err(502, "invalid_upstream_response", "Provider sent data after its terminal event")
		}
		var chunk struct {
			Object  string          `json:"object"`
			Error   json.RawMessage `json:"error"`
			Choices []struct {
				Index        int             `json:"index"`
				FinishReason *string         `json:"finish_reason"`
				Delta        json.RawMessage `json:"delta"`
			} `json:"choices"`
			Usage *struct {
				Prompt     int64 `json:"prompt_tokens"`
				Completion int64 `json:"completion_tokens"`
			} `json:"usage"`
		}
		if json.Unmarshal([]byte(value), &chunk) != nil {
			return api.Err(502, "invalid_upstream_response", "Provider sent invalid stream JSON")
		}
		if len(chunk.Error) > 0 && string(chunk.Error) != "null" {
			return api.Err(502, "upstream_in_stream_error", "Provider reported an error inside its stream")
		}
		if chunk.Object != "chat.completion.chunk" || len(chunk.Choices) > 1 || len(chunk.Choices) == 0 && chunk.Usage == nil {
			return api.Err(502, "invalid_upstream_response", "Provider sent an invalid completion chunk")
		}
		for _, choice := range chunk.Choices {
			if choice.Index != 0 || len(choice.Delta) == 0 {
				return api.Err(502, "invalid_upstream_response", "Provider sent an invalid choice")
			}
			if choice.FinishReason != nil && *choice.FinishReason != "" {
				finished = true
			}
		}
		if chunk.Usage != nil {
			if chunk.Usage.Prompt < 0 || chunk.Usage.Completion < 0 {
				return api.Err(502, "invalid_upstream_response", "Provider sent invalid usage")
			}
			prompt, completion = chunk.Usage.Prompt, chunk.Usage.Completion
		}
		return guard.push(value, emitSafe)
	}
	for scan.Scan() {
		line := scan.Text()
		total += len(line) + 1
		if total > MaxResponseBytes {
			return prompt, completion, api.Err(502, "invalid_upstream_response", "Provider response exceeded limits")
		}
		if line == "" {
			if e = process(); e != nil {
				return prompt, completion, e
			}
			if done {
				return prompt, completion, nil
			}
			continue
		}
		if strings.HasPrefix(line, "data:") {
			value := strings.TrimPrefix(line, "data:")
			value = strings.TrimPrefix(value, " ")
			data.WriteString(value)
			data.WriteByte('\n')
		}
	}
	if scan.Err() != nil {
		return prompt, completion, streamError(ctx, "upstream_interrupted")
	}
	if data.Len() > 0 {
		if e = process(); e != nil {
			return prompt, completion, e
		}
	}
	if done {
		return prompt, completion, nil
	}
	return prompt, completion, streamError(ctx, "upstream_interrupted")
}

func (s *Service) streamHTTP(w http.ResponseWriter, r *http.Request) {
	if !api.EqualSecret(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "), s.Token) || !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
		api.Fail(w, api.Err(401, "unauthorized", "Invalid service credential"))
		return
	}
	var in struct {
		OwnerID     string `json:"ownerId"`
		AgentID     string `json:"agentId"`
		SessionID   string `json:"sessionId"`
		RunID       string `json:"runId"`
		RequestJSON string `json:"requestJson"`
	}
	// The OpenAI request is itself a JSON string and may double in size when encoded.
	r.Body = http.MaxBytesReader(w, r.Body, MaxRequestBytes*2+4096)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if dec.Decode(&in) != nil || dec.Decode(new(any)) != io.EOF || !validID(in.OwnerID) || !validID(in.AgentID) || !validID(in.SessionID) || !validID(in.RunID) {
		api.Fail(w, api.Err(400, "invalid_request", "Invalid stream request"))
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
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
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
