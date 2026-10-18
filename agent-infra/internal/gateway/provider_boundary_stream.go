package gateway

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"teamie/agent-infra/internal/api"
	"teamie/agent-protocol/modeltransport"
)

// A synchronous response-body guard adds no producer goroutine. The official
// SDK reads only complete, validated SSE frames and owns all model conversion.
type guardedStreamBody struct {
	source         io.ReadCloser
	scanner        *bufio.Scanner
	ctx            context.Context
	guard          *credentialStreamGuard
	pending        bytes.Buffer
	data           strings.Builder
	total          int
	finished, done bool
}

func guardStreamingResponse(response *http.Response, key string, ctx context.Context) (*http.Response, error) {
	if !strings.HasPrefix(strings.ToLower(response.Header.Get("Content-Type")), "text/event-stream") {
		body, _ := io.ReadAll(io.LimitReader(response.Body, 16385))
		response.Body.Close()
		var result struct {
			Error json.RawMessage `json:"error"`
		}
		if json.Unmarshal(body, &result) == nil && len(result.Error) > 0 && string(result.Error) != "null" {
			if contextErr := providerContextError(body); contextErr != nil {
				return nil, contextErr
			}
			return nil, api.Err(502, "upstream_in_stream_error", "Provider reported an error in its response")
		}
		return nil, api.Err(502, "invalid_upstream_response", "Provider did not return an event stream")
	}
	scanner := bufio.NewScanner(io.LimitReader(response.Body, modeltransport.MaxProviderStreamBytes+1))
	scanner.Buffer(make([]byte, 4096), MaxResponseBytes)
	response.Body = &guardedStreamBody{source: response.Body, scanner: scanner, ctx: ctx, guard: newCredentialStreamGuard(key)}
	response.ContentLength = -1
	return response, nil
}

func (b *guardedStreamBody) Close() error { return b.source.Close() }
func (b *guardedStreamBody) emit(raw string) error {
	b.pending.WriteString("data: ")
	b.pending.WriteString(raw)
	b.pending.WriteString("\n\n")
	return nil
}

func (b *guardedStreamBody) frame() error {
	if b.data.Len() == 0 {
		return nil
	}
	raw := strings.TrimSuffix(b.data.String(), "\n")
	b.data.Reset()
	if raw == "[DONE]" {
		if !b.finished {
			return api.Err(502, "upstream_interrupted", "Provider stream ended before a finish reason")
		}
		if err := b.guard.finish(b.emit); err != nil {
			return err
		}
		b.done = true
		return b.emit("[DONE]")
	}
	var chunk struct {
		Object  string          `json:"object"`
		Error   json.RawMessage `json:"error"`
		Choices []struct {
			Index        int             `json:"index"`
			Delta        json.RawMessage `json:"delta"`
			FinishReason *string         `json:"finish_reason"`
		} `json:"choices"`
		Usage *struct {
			Prompt     int64 `json:"prompt_tokens"`
			Completion int64 `json:"completion_tokens"`
		} `json:"usage"`
	}
	if json.Unmarshal([]byte(raw), &chunk) != nil {
		return api.Err(502, "invalid_upstream_response", "Provider sent invalid stream JSON")
	}
	if len(chunk.Error) > 0 && string(chunk.Error) != "null" {
		if contextErr := providerContextError([]byte(raw)); contextErr != nil {
			return contextErr
		}
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
			b.finished = true
		}
	}
	if chunk.Usage != nil && (chunk.Usage.Prompt < 0 || chunk.Usage.Completion < 0) {
		return api.Err(502, "invalid_upstream_response", "Provider sent invalid usage")
	}
	return b.guard.push(raw, b.emit)
}

func (b *guardedStreamBody) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	for b.pending.Len() == 0 {
		if b.done {
			return 0, io.EOF
		}
		if !b.scanner.Scan() {
			if b.scanner.Err() != nil {
				return 0, streamError(b.ctx, "upstream_interrupted")
			}
			if b.data.Len() > 0 {
				if err := b.frame(); err != nil {
					return 0, err
				}
				if b.done {
					break
				}
			}
			return 0, streamError(b.ctx, "upstream_interrupted")
		}
		line := b.scanner.Text()
		b.total += len(line) + 1
		if b.total > modeltransport.MaxProviderStreamBytes {
			return 0, api.Err(502, "invalid_upstream_response", "Provider response exceeded limits")
		}
		if line == "" {
			if err := b.frame(); err != nil {
				return 0, err
			}
			continue
		}
		if strings.HasPrefix(line, "data:") {
			b.data.WriteString(strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
			b.data.WriteByte('\n')
		}
	}
	return b.pending.Read(p)
}
