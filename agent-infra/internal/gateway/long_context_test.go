package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	"teamie/agent-infra/internal/api"
	"teamie/agent-protocol/kitex_gen/agent"
	"teamie/agent-protocol/modeltransport"
)

func TestGatewayPreservesFrozenOutputAfterWindowDecrease(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		for _, requested := range []int{4096, 131072, 262144} {
			t.Run(fmt.Sprintf("stream=%t/output=%d", streaming, requested), func(t *testing.T) {
				var observed atomic.Int64
				fixture := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					var request struct {
						Max int `json:"max_completion_tokens"`
					}
					if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
						t.Error(err)
					}
					observed.Store(int64(request.Max))
					if streaming {
						w.Header().Set("Content-Type", "text/event-stream")
						fmt.Fprint(w, "data: {\"id\":\"frozen-output\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"done\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
					} else {
						w.Header().Set("Content-Type", "application/json")
						fmt.Fprint(w, `{"id":"frozen-output","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"done"},"finish_reason":"stop"}]}`)
					}
				}))
				defer fixture.Close()
				provider := Provider{fixture.Client()}
				config := Config{BaseURL: fixture.URL + "/v1", Model: "chosen-model", ContextWindowTokens: 4096}
				raw := nativeRequest(t, []*schema.AgenticMessage{schema.UserAgenticMessage("frozen request")}, model.Options{MaxTokens: &requested}, streaming)
				var err error
				if streaming {
					_, _, err = provider.Stream(context.Background(), config, "synthetic-key", raw, func(string) error { return nil })
				} else {
					_, _, _, err = provider.Chat(context.Background(), config, "synthetic-key", raw)
				}
				if err != nil || observed.Load() != int64(min(requested, modeltransport.MaxOutputTokens)) {
					t.Fatal("current gateway window changed frozen output, or bypassed protocol ceiling", err, observed.Load())
				}
			})
		}
	}
}

func TestLongContextCrossesActualHertzAndOfficialProviderBoundaries(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		t.Run(fmt.Sprint(streaming), func(t *testing.T) {
			output := strings.Repeat("result ", 100000)
			var observed atomic.Bool
			s, unused := streamFixtureService(t, func(w http.ResponseWriter, r *http.Request) {
				var request struct {
					Model    string `json:"model"`
					Max      int    `json:"max_completion_tokens"`
					Messages []struct {
						Role    string
						Content json.RawMessage
					} `json:"messages"`
				}
				text := func(raw json.RawMessage) string {
					var plain string
					if json.Unmarshal(raw, &plain) == nil {
						return plain
					}
					var parts []struct {
						Text string `json:"text"`
					}
					if json.Unmarshal(raw, &parts) != nil {
						return ""
					}
					var out strings.Builder
					for _, part := range parts {
						out.WriteString(part.Text)
					}
					return out.String()
				}
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil || len(request.Messages) != 301 || request.Max != 131072 || request.Model != "chosen-model" || !strings.HasPrefix(text(request.Messages[0].Content), "start-marker") || text(request.Messages[300].Content) != "end-marker" {
					t.Error("long context, message count, output reserve or model policy was truncated", err, len(request.Messages), request.Max)
				}
				observed.Store(true)
				if streaming {
					w.Header().Set("Content-Type", "text/event-stream")
					frame, _ := json.Marshal(map[string]any{"id": "long-stream", "object": "chat.completion.chunk", "choices": []any{map[string]any{"index": 0, "delta": map[string]string{"role": "assistant", "content": output}, "finish_reason": "stop"}}})
					fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", frame)
				} else {
					_ = json.NewEncoder(w).Encode(map[string]any{"id": "long-unary", "object": "chat.completion", "choices": []any{map[string]any{"index": 0, "message": map[string]string{"role": "assistant", "content": output}, "finish_reason": "stop"}}, "usage": map[string]int{"prompt_tokens": 400000, "completion_tokens": 100000}})
				}
			})
			unused.Close()
			store := s.Store.(*memoryStore)
			store.c.ContextWindowTokens = 1_000_000
			base := serveUnaryHertz(t, s.Routes(api.AdminAuth{}))
			messages := make([]*schema.AgenticMessage, 0, 301)
			for i := 0; i < 300; i++ {
				messages = append(messages, schema.UserAgenticMessage(fmt.Sprintf("start-marker %d %s", i, strings.Repeat("history ", 600))))
			}
			messages = append(messages, schema.UserAgenticMessage("end-marker"))
			max := 131072
			raw := nativeRequest(t, messages, model.Options{MaxTokens: &max}, streaming)
			if len(raw) <= 528384 {
				t.Fatal("fixture does not exercise the former HTTP body ceiling")
			}
			body, _ := json.Marshal(modeltransport.CallRequest{OwnerID: "owner", AgentID: "agent", SessionID: "session", RunID: "long-run", RequestJSON: raw, MessageLimit: modeltransport.MaxInputMessages})
			path := modeltransport.GeneratePath
			if streaming {
				path = modeltransport.StreamPath
			}
			req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, base+path, bytes.NewReader(body))
			req.Header.Set("Authorization", "Bearer "+testToken)
			response, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			if response.StatusCode != 200 {
				t.Fatal("large request did not cross Hertz admission", response.StatusCode)
			}
			var result *schema.AgenticMessage
			if streaming {
				decoder := json.NewDecoder(response.Body)
				chunks := []*schema.AgenticMessage{}
				terminal := false
				for {
					var frame StreamEvent
					if err = decoder.Decode(&frame); err == io.EOF {
						break
					}
					if err != nil || frame.Type == "error" {
						t.Fatal("large stream failed", err, frame.ErrorCode)
					}
					if frame.Type == "done" {
						terminal = true
						break
					}
					var message schema.AgenticMessage
					if err = (&schema.HumanReadableSerializer{}).Unmarshal([]byte(frame.ResponseJSON), &message); err != nil {
						t.Fatal(err)
					}
					chunks = append(chunks, &message)
				}
				if !terminal {
					t.Fatal("large stream had no terminal receipt")
				}
				result, err = schema.ConcatAgenticMessages(chunks)
			} else {
				var reply agent.GatewayChatResponse
				if err = json.NewDecoder(response.Body).Decode(&reply); err != nil || !reply.Ok {
					t.Fatal("large unary failed", err, reply.GetErrorCode())
				}
				var message schema.AgenticMessage
				err = (&schema.HumanReadableSerializer{}).Unmarshal([]byte(reply.GetResponseJson()), &message)
				result = &message
			}
			if err != nil || !observed.Load() || result == nil || len(result.ContentBlocks) != 1 || result.ContentBlocks[0].AssistantGenText.Text != output {
				t.Fatal("large output was truncated", err)
			}
		})
	}
}

func TestFrozenMessageLimitsReachNativeAndProviderAdmission(t *testing.T) {
	messages := make([]*schema.AgenticMessage, 129)
	providerMessages := make([]map[string]string, len(messages))
	for i := range messages {
		messages[i] = schema.UserAgenticMessage("small historical record")
		providerMessages[i] = map[string]string{"role": "user", "content": "small historical record"}
	}
	providerRaw, _ := json.Marshal(map[string]any{"messages": providerMessages})
	for _, limit := range []int{0, modeltransport.MaxInputMessages} {
		_, err := normalizeModeWithOutputLimit(string(providerRaw), "model", false, modeltransport.MaxOutputTokens, limit)
		if (err == nil) != (limit > 0) {
			t.Fatal("provider boundary ignored the frozen count", limit, err)
		}
		for _, streaming := range []bool{false, true} {
			t.Run(fmt.Sprintf("limit=%d/stream=%t", limit, streaming), func(t *testing.T) {
				var calls atomic.Int64
				s, unused := streamFixtureService(t, func(w http.ResponseWriter, r *http.Request) {
					calls.Add(1)
					if streaming {
						w.Header().Set("Content-Type", "text/event-stream")
						fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", finalDelta)
					} else {
						w.Header().Set("Content-Type", "application/json")
						fmt.Fprint(w, `{"id":"count-fixture","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"done"},"finish_reason":"stop"}]}`)
					}
				})
				unused.Close()
				raw := nativeRequest(t, messages, model.Options{}, streaming)
				if len(raw) > 262144 {
					t.Fatal("historical message fixture exceeded the old byte limit")
				}
				body, _ := json.Marshal(modeltransport.CallRequest{OwnerID: "owner", AgentID: "agent", SessionID: "session", RunID: "count-run", RequestJSON: raw, MessageLimit: limit})
				path := modeltransport.GeneratePath
				if streaming {
					path = modeltransport.StreamPath
				}
				request := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
				request.Header.Set("Authorization", "Bearer "+testToken)
				recorder := httptest.NewRecorder()
				s.Routes(api.AdminAuth{}).ServeHTTP(recorder, request)
				if limit == 0 && (calls.Load() != 0 || !strings.Contains(recorder.Body.String(), "invalid_request")) {
					t.Fatal("historical count rejection changed into a provider call", recorder.Body.String())
				}
				if limit > 0 && (calls.Load() != 1 || strings.Contains(recorder.Body.String(), "errorCode")) {
					t.Fatal("new count admission did not reach the provider", recorder.Body.String())
				}
			})
		}
	}
}
