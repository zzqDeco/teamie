package gateway_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func guardChunk(delta any) string {
	b, _ := json.Marshal(map[string]any{"object": "chat.completion.chunk", "choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": nil}}})
	return string(b)
}
func argumentsDelta(index int, arguments string) any {
	return map[string]any{"tool_calls": []any{map[string]any{"index": index, "function": map[string]any{"arguments": arguments}}}}
}
func unicodeJSONString(value string) json.RawMessage {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range value {
		fmt.Fprintf(&b, "\\u%04x", r)
	}
	b.WriteByte('"')
	return json.RawMessage(b.String())
}

func TestStreamCredentialGuardDecodedAndSplitFields(t *testing.T) {
	const key = "fixture-split-provider-secret"
	unicodeArguments := `{"token":` + string(unicodeJSONString(key)) + `}`
	for _, test := range []struct {
		name   string
		chunks []string
	}{
		{"split content", []string{guardChunk(map[string]any{"content": key[:13]}), guardChunk(map[string]any{"content": key[13:]})}},
		{"equivalent negative zero choice index", []string{guardChunk(map[string]any{"content": key[:13]}), strings.Replace(guardChunk(map[string]any{"content": key[13:]}), `"index":0`, `"index":-0`, 1)}},
		{"escaped unicode content", []string{guardChunk(map[string]any{"content": unicodeJSONString(key)})}},
		{"split tool arguments", []string{guardChunk(argumentsDelta(0, `{"token":"`+key[:13])), guardChunk(argumentsDelta(0, key[13:]+`"}`))}},
		{"tool index across array positions", []string{guardChunk(map[string]any{"tool_calls": []any{map[string]any{"index": 0, "function": map[string]any{"arguments": "{}"}}, map[string]any{"index": 1, "function": map[string]any{"arguments": key[:13]}}}}), guardChunk(argumentsDelta(1, key[13:]))}},
		{"unicode escaped tool JSON split inside escape", []string{guardChunk(argumentsDelta(0, unicodeArguments[:18])), guardChunk(argumentsDelta(0, unicodeArguments[18:]))}},
		{"escaped unicode metadata", []string{guardChunk(map[string]any{"content": "ok", "opaque": unicodeJSONString(key)})}},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprintf(w, "data: %s\n\n", guardChunk(map[string]any{"content": "plain greeting."}))
				for _, chunk := range test.chunks {
					fmt.Fprintf(w, "data: %s\n\n", chunk)
				}
				fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", finalDelta)
			}))
			defer fixture.Close()
			var received []string
			_, _, err := (Provider{HTTP: fixture.Client()}).Stream(context.Background(), ResolvedModel{BaseURL: fixture.URL + "/v1", Model: "fixture"}, key, fixtureNativeRequest(`{"messages":[{"role":"user","content":"hello"}]}`, true), func(raw string) error { received = append(received, raw); return nil })
			if guardErrorCode(err) != "invalid_upstream_response" {
				t.Fatal("credential response was not rejected with a safe code")
			}
			if len(received) != 1 {
				t.Fatal("a raw chunk containing a possible credential prefix reached the consumer")
			}
			for _, raw := range received {
				var decoded any
				if json.Unmarshal([]byte(raw), &decoded) != nil || decodedCredential(decoded, key) {
					t.Fatal("decoded credential reached the consumer")
				}
			}
			if strings.Contains(err.Error(), key) {
				t.Fatal("credential entered the error text")
			}
		})
	}
}

func TestStreamCredentialPrefixMismatchFlushesBeforeCompletion(t *testing.T) {
	const key = "fixture-prefix-matching-secret"
	release := make(chan struct{})
	fixture := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "data: %s\n\n", guardChunk(map[string]any{"content": "safe " + key[:12]}))
		w.(http.Flusher).Flush()
		fmt.Fprintf(w, "data: %s\n\n", guardChunk(map[string]any{"content": "! normal continuation."}))
		w.(http.Flusher).Flush()
		select {
		case <-release:
			fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", finalDelta)
		case <-r.Context().Done():
		}
	}))
	defer fixture.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	received := make(chan string, 3)
	done := make(chan error, 1)
	go func() {
		_, _, e := (Provider{HTTP: fixture.Client()}).Stream(ctx, ResolvedModel{BaseURL: fixture.URL + "/v1", Model: "fixture"}, key, fixtureNativeRequest(`{"messages":[{"role":"user","content":"hello"}]}`, true), func(raw string) error { received <- raw; return nil })
		done <- e
	}()
	for i := 0; i < 2; i++ {
		select {
		case <-received:
		case <-ctx.Done():
			t.Fatal("safe chunks were held until provider completion")
		}
	}
	select {
	case <-done:
		t.Fatal("provider completed before release")
	default:
	}
	close(release)
	if e := <-done; e != nil {
		t.Fatal("normal prefix mismatch stream failed")
	}
}

func TestStreamCredentialTrailingPrefixAndOverlap(t *testing.T) {
	for _, key := range []string{"aaaaab", "fixture-trailing-prefix"} {
		g := newCredentialStreamGuard(key)
		first := guardChunk(map[string]any{"content": key[:len(key)-1]})
		var received []string
		emit := func(raw string) error { received = append(received, raw); return nil }
		if e := g.push(first, emit); e != nil || len(received) != 0 {
			t.Fatal("possible prefix was not held")
		}
		second := guardChunk(map[string]any{"content": "!"})
		if e := g.push(second, emit); e != nil || len(received) != 2 {
			t.Fatal("mismatched prefix did not flush")
		}
		g = newCredentialStreamGuard(key)
		received = nil
		if e := g.push(first, emit); e != nil {
			t.Fatal("partial prefix rejected")
		}
		if e := g.finish(emit); e != nil || len(received) != 1 {
			t.Fatal("safe trailing prefix did not flush at terminal event")
		}
	}
}

func TestStreamCredentialAtomicMetadataDoesNotDelayContent(t *testing.T) {
	for _, key := range []string{"token-metadata-secret", "name-metadata-secret"} {
		g := newCredentialStreamGuard(key)
		raw := guardChunk(map[string]any{"role": "assistant", "content": "plain content.", "tool_calls": []any{map[string]any{"index": 0, "id": key[:1], "type": "function", "function": map[string]any{"name": key[:1], "arguments": "{}"}}}})
		received := 0
		if e := g.push(raw, func(string) error { received++; return nil }); e != nil || received != 1 {
			t.Fatal("atomic metadata prefix delayed ordinary content")
		}
	}
}

func TestUnaryCredentialGuardUnicodeEscapes(t *testing.T) {
	const key = "fixture-unary-encoded-credential"
	for _, message := range []map[string]any{
		{"role": "assistant", "content": unicodeJSONString(key)},
		{"role": "assistant", "content": nil, "tool_calls": []any{map[string]any{"id": "call-1", "type": "function", "function": map[string]any{"name": "tool", "arguments": `{"token":` + string(unicodeJSONString(key)) + `}`}}}},
	} {
		fixture := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.Copy(io.Discard, r.Body)
			_ = json.NewEncoder(w).Encode(map[string]any{"object": "chat.completion", "choices": []any{map[string]any{"message": message, "finish_reason": "stop"}}, "usage": map[string]int{"prompt_tokens": 1, "completion_tokens": 1}})
		}))
		body, _, _, err := (Provider{HTTP: fixture.Client()}).Chat(context.Background(), ResolvedModel{BaseURL: fixture.URL + "/v1", Model: "fixture"}, key, fixtureNativeRequest(`{"messages":[{"role":"user","content":"hello"}]}`, false))
		fixture.Close()
		if body != "" || guardErrorCode(err) != "invalid_upstream_response" {
			t.Fatal("encoded unary credential was forwarded")
		}
		if strings.Contains(err.Error(), key) {
			t.Fatal("credential entered the error text")
		}
	}
}
