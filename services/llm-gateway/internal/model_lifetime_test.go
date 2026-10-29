package gateway_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	rpcx "teamie/services/llm-gateway/internal/infra/rpcclient"
)

type lifetimeTransport func(*http.Request) (*http.Response, error)

func (f lifetimeTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

const lifetimeCompletion = `{"id":"long-inference","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"complete"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}}`

func lifetimeService(t *testing.T, transport http.RoundTripper) *Service {
	t.Helper()
	cipher := testCipher(t)
	encrypted, err := cipher.Encrypt("synthetic-lifetime-key")
	if err != nil {
		t.Fatal(err)
	}
	s := New(&memoryStore{c: singleModel(
		ProviderProfile{BaseURL: "https://provider.example/v1", CipherKey: encrypted, Enabled: true},
		ModelProfile{Model: "fixture", Enabled: true},
	)}, cipher, testToken)
	// The adapter must use the owning context in either mode, even when it
	// receives a client originally configured for bounded ordinary HTTP work.
	s.Provider.HTTP = &http.Client{Transport: transport, Timeout: 50 * time.Second}
	return s
}

func TestGatewayUnaryLifetimeFollowsCaller(t *testing.T) {
	for _, ending := range []string{"complete", "cancel", "deadline"} {
		t.Run(ending, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				if ending == "deadline" {
					ctx, cancel = context.WithTimeout(ctx, 80*time.Second)
					defer cancel()
				} else if ending == "cancel" {
					go func() { time.Sleep(80 * time.Second); cancel() }()
				}
				s := lifetimeService(t, lifetimeTransport(func(r *http.Request) (*http.Response, error) {
					got, gotOK := r.Context().Deadline()
					want, wantOK := ctx.Deadline()
					if gotOK != wantOK || gotOK && !got.Equal(want) {
						t.Fatal("gateway/official SDK added an inference deadline")
					}
					select {
					case <-time.After(90 * time.Second):
						return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(lifetimeCompletion)), Request: r}, nil
					case <-r.Context().Done():
						return nil, r.Context().Err()
					}
				}))
				raw := nativeRequest(t, []*schema.AgenticMessage{schema.UserAgenticMessage("long inference")}, model.Options{}, false)
				start := time.Now()
				response := s.call(ctx, "owner", "agent", "session", "run", raw)
				want := "success"
				if ending == "complete" {
					if !response.Ok || time.Since(start) != 90*time.Second {
						t.Fatalf("long unary inference failed: %s after %s", response.GetErrorCode(), time.Since(start))
					}
				} else {
					want = "canceled"
					if ending == "deadline" {
						want = "upstream_timeout"
					}
					if response.Ok || response.GetErrorCode() != want || time.Since(start) != 80*time.Second {
						t.Fatalf("caller termination was ignored: %+v after %s", response, time.Since(start))
					}
				}
				usage := s.Store.(*memoryStore).usage
				if len(usage) != 1 || usage[0].Status != want {
					t.Fatalf("lifetime outcome was not audited: %+v", usage)
				}
			})
		})
	}
}

func TestGatewayActiveStreamExceedsFormerFiveMinuteBudget(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := lifetimeService(t, lifetimeTransport(func(r *http.Request) (*http.Response, error) {
			if _, ok := r.Context().Deadline(); ok {
				t.Fatal("gateway/official SDK added a streaming deadline")
			}
			reader, writer := io.Pipe()
			stop := context.AfterFunc(r.Context(), func() { _ = writer.CloseWithError(r.Context().Err()) })
			go func() {
				defer stop()
				defer writer.Close()
				for i := 0; i < 9; i++ {
					select {
					case <-time.After(time.Minute):
					case <-r.Context().Done():
						return
					}
					if _, err := fmt.Fprintf(writer, "data: %s\n\n", delta); err != nil {
						return
					}
				}
				_, _ = fmt.Fprintf(writer, "data: %s\n\ndata: [DONE]\n\n", finalDelta)
			}()
			return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: reader, Request: r}, nil
		}))
		response := httptest.NewRecorder()
		start := time.Now()
		s.Routes(rpcx.AdminAuth{}).ServeHTTP(response, streamRequest(t, context.Background(), "https://gateway.example", "long-stream"))
		if time.Since(start) != 9*time.Minute || !strings.Contains(response.Body.String(), `"type":"done"`) || strings.Contains(response.Body.String(), `"type":"error"`) {
			t.Fatalf("active stream was truncated after %s: %s", time.Since(start), response.Body.String())
		}
		var text strings.Builder
		dec := json.NewDecoder(response.Body)
		for dec.More() {
			var event StreamEvent
			if err := dec.Decode(&event); err != nil {
				t.Fatal(err)
			}
			if event.Type == "chunk" {
				var chunk schema.AgenticMessage
				if err := (&schema.HumanReadableSerializer{}).Unmarshal([]byte(event.ResponseJSON), &chunk); err != nil {
					t.Fatal(err)
				}
				for _, block := range chunk.ContentBlocks {
					if block.AssistantGenText != nil {
						text.WriteString(block.AssistantGenText.Text)
					}
				}
			}
		}
		if text.String() != strings.Repeat("one", 9) {
			t.Fatal("stream suffix was lost")
		}
		usage := s.Store.(*memoryStore).usage
		if len(usage) != 1 || usage[0].Status != "success" || usage[0].LatencyMS != (9*time.Minute).Milliseconds() {
			t.Fatalf("long stream was not audited: %+v", usage)
		}
	})
}

func TestOfficialProviderWaitsForDelayedTLSHeaders(t *testing.T) {
	if testing.Short() {
		t.Skip("real 51s TLS header-delay regression")
	}
	// Both modes run concurrently so the real-network regression adds one
	// 51s wait, exceeding the former 45s header and 50s unary limits.
	for _, streaming := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%t", streaming), func(t *testing.T) {
			t.Parallel()
			fixture := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				select {
				case <-time.After(51 * time.Second):
				case <-r.Context().Done():
					return
				}
				if streaming {
					w.Header().Set("Content-Type", "text/event-stream")
					fmt.Fprintf(w, "data: %s\n\ndata: %s\n\ndata: [DONE]\n\n", delta, finalDelta)
				} else {
					w.Header().Set("Content-Type", "application/json")
					fmt.Fprint(w, lifetimeCompletion)
				}
			}))
			defer fixture.Close()
			client := NewHTTPClient()
			transport := client.Transport.(*http.Transport)
			// Only the dial target/trust are replaced for this local TLS fixture;
			// all production timeout settings and the official codec remain real.
			transport.DialContext = (&net.Dialer{Timeout: time.Second}).DialContext
			transport.TLSClientConfig = fixture.Client().Transport.(*http.Transport).TLSClientConfig.Clone()
			defer client.CloseIdleConnections()
			provider := Provider{HTTP: client}
			request := nativeRequest(t, []*schema.AgenticMessage{schema.UserAgenticMessage("wait for inference")}, model.Options{}, streaming)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			start := time.Now()
			var err error
			if streaming {
				var chunks int
				_, _, err = provider.Stream(ctx, ResolvedModel{BaseURL: fixture.URL + "/v1", Model: "fixture"}, "synthetic-delayed-key", request, func(string) error { chunks++; return nil })
				if err == nil && chunks == 0 {
					t.Fatal("no delayed stream output")
				}
			} else {
				_, _, _, err = provider.Chat(ctx, ResolvedModel{BaseURL: fixture.URL + "/v1", Model: "fixture"}, "synthetic-delayed-key", request)
			}
			if err != nil || time.Since(start) < 51*time.Second {
				t.Fatalf("delayed TLS response failed after %s: %v", time.Since(start), err)
			}
		})
	}
}
