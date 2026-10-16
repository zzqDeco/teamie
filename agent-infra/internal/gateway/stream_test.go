package gateway

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/cloudwego/hertz/pkg/common/adaptor"

	"teamie/agent-infra/internal/api"
	"teamie/agent-protocol/kitex_gen/agent"
)

const delta = `{"id":"fixture","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant","content":"one"},"finish_reason":null}]}`
const finalDelta = `{"id":"fixture","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`

func streamFixtureService(t *testing.T, handler http.HandlerFunc) (*Service, *httptest.Server) {
	t.Helper()
	fixture := httptest.NewTLSServer(handler)
	t.Cleanup(fixture.Close)
	crypto := testCipher(t)
	key := "fixture-provider-stream-secret"
	encrypted, e := crypto.Encrypt(key)
	if e != nil {
		t.Fatal(e)
	}
	store := &memoryStore{c: Config{BaseURL: fixture.URL + "/v1", Model: "chosen-model", CipherKey: encrypted, Enabled: true}}
	s := New(store, crypto, testToken)
	s.Provider = Provider{fixture.Client()}
	return s, httptest.NewServer(s.Routes(api.AdminAuth{}))
}
func streamRequest(t *testing.T, ctx context.Context, url, run string) *http.Request {
	t.Helper()
	raw, _ := json.Marshal(map[string]any{"ownerId": "o", "agentId": "a", "sessionId": "s", "runId": run, "requestJson": `{"model":"bypass","messages":[{"role":"user","content":"test"}],"stream":true}`})
	r, e := http.NewRequestWithContext(ctx, http.MethodPost, url+"/internal/v1/chat/stream", bytes.NewReader(raw))
	if e != nil {
		t.Fatal(e)
	}
	r.Header.Set("Authorization", "Bearer "+testToken)
	return r
}

func TestGatewayActualIncrementalStreamingToolsUsageAndTerminal(t *testing.T) {
	release := make(chan struct{})
	providerDone := make(chan struct{})
	s, server := streamFixtureService(t, func(w http.ResponseWriter, r *http.Request) {
		defer close(providerDone)
		var in map[string]json.RawMessage
		_ = json.NewDecoder(r.Body).Decode(&in)
		if string(in["stream"]) != "true" || string(in["model"]) != `"chosen-model"` || string(in["stream_options"]) != `{"include_usage":true}` {
			t.Error("stream policy missing", in)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "data: %s\n\n", delta)
		w.(http.Flusher).Flush()
		<-release
		fmt.Fprint(w, "data: {\"id\":\"fixture\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"tool-id\",\"type\":\"function\",\"function\":{\"name\":\"shell\",\"arguments\":\"{\\\"command\\\":\\\"pwd\\\"}\"}}]},\"finish_reason\":\"tool_calls\"}]}\n\n")
		fmt.Fprint(w, "data: {\"object\":\"chat.completion.chunk\",\"choices\":[],\"usage\":{\"prompt_tokens\":12,\"completion_tokens\":7}}\n\ndata: [DONE]\n\n")
	})
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	res, e := http.DefaultClient.Do(streamRequest(t, ctx, server.URL, "incremental"))
	if e != nil {
		t.Fatal(e)
	}
	defer res.Body.Close()
	scan := bufio.NewScanner(res.Body)
	if !scan.Scan() {
		t.Fatal("no first chunk", scan.Err())
	}
	var first StreamEvent
	if json.Unmarshal(scan.Bytes(), &first) != nil || first.Type != "chunk" || !strings.Contains(first.ResponseJSON, "one") {
		t.Fatal(first)
	}
	select {
	case <-providerDone:
		t.Fatal("stream was buffered until provider completion")
	default:
	}
	close(release)
	var events []StreamEvent
	for scan.Scan() {
		var event StreamEvent
		if e = json.Unmarshal(scan.Bytes(), &event); e != nil {
			t.Fatal(e)
		}
		events = append(events, event)
	}
	if scan.Err() != nil || len(events) != 3 || events[2].Type != "done" || !strings.Contains(events[0].ResponseJSON, "tool_calls") {
		t.Fatal(events, scan.Err())
	}
	store := s.Store.(*memoryStore)
	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.usage) != 1 || store.usage[0].Status != "success" || store.usage[0].PromptTokens != 12 || store.usage[0].CompletionTokens != 7 {
		t.Fatal(store.usage)
	}
}

func TestGatewayStreamFailureClassesAndRedaction(t *testing.T) {
	for _, test := range []struct {
		name       string
		status     int
		body, code string
	}{{"in-stream failure", 200, "data: " + delta + "\n\ndata: {\"error\":{\"message\":\"fixture-provider-stream-secret leaked raw\"}}\n\n", "upstream_in_stream_error"}, {"partial unknown interruption", 200, "data: " + delta + "\n\n", "upstream_interrupted"}, {"http rejection", 503, "raw fixture-provider-stream-secret", "upstream_http_error"}, {"bad chunk", 200, "data: bad-json\n\n", "invalid_upstream_response"}, {"done without finish", 200, "data: [DONE]\n\n", "upstream_interrupted"}} {
		t.Run(test.name, func(t *testing.T) {
			s, server := streamFixtureService(t, func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				w.Header().Set("Content-Type", "text/event-stream")
				w.WriteHeader(test.status)
				fmt.Fprint(w, test.body)
			})
			defer server.Close()
			res, e := http.DefaultClient.Do(streamRequest(t, context.Background(), server.URL, test.name))
			if e != nil {
				t.Fatal(e)
			}
			defer res.Body.Close()
			body, e := io.ReadAll(res.Body)
			if e != nil {
				t.Fatal(e)
			}
			if !strings.Contains(string(body), `"errorCode":"`+test.code+`"`) || strings.Contains(string(body), `"type":"done"`) || strings.Contains(string(body), "fixture-provider-stream-secret") {
				t.Fatal(string(body))
			}
			store := s.Store.(*memoryStore)
			store.mu.Lock()
			defer store.mu.Unlock()
			if len(store.usage) != 1 || store.usage[0].Status != test.code {
				t.Fatal(store.usage)
			}
		})
	}
}

func TestGatewayStreamConsumerAndExplicitCancellation(t *testing.T) {
	for _, explicit := range []bool{false, true} {
		t.Run(fmt.Sprint(explicit), func(t *testing.T) {
			upstreamCanceled := make(chan struct{})
			s, server := streamFixtureService(t, func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprintf(w, "data: %s\n\n", delta)
				w.(http.Flusher).Flush()
				<-r.Context().Done()
				close(upstreamCanceled)
			})
			defer server.Close()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			res, e := http.DefaultClient.Do(streamRequest(t, ctx, server.URL, "cancel-me"))
			if e != nil {
				t.Fatal(e)
			}
			scan := bufio.NewScanner(res.Body)
			if !scan.Scan() {
				t.Fatal("first chunk missing")
			}
			if explicit {
				ack, _ := s.Cancel(context.Background(), &agent.CancelRequest{ServiceToken: testToken, AgentId: "a", RunId: "cancel-me"})
				if !ack.Ok {
					t.Fatal(ack)
				}
				if !scan.Scan() || !strings.Contains(scan.Text(), `"errorCode":"canceled"`) {
					t.Fatal(scan.Text(), scan.Err())
				}
			} else {
				cancel()
				res.Body.Close()
			}
			select {
			case <-upstreamCanceled:
			case <-time.After(2 * time.Second):
				t.Fatal("provider continued after cancellation")
			}
			res.Body.Close()
		})
	}
}

func TestHertzStreamAdapterFlushAndDisconnectCancellation(t *testing.T) {
	upstreamCanceled := make(chan struct{})
	s, dummy := streamFixtureService(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "data: %s\n\n", delta)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
		close(upstreamCanceled)
	})
	dummy.Close()
	listener, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	h := server.New(server.WithListener(listener), server.WithSenseClientDisconnection(true), server.WithWriteTimeout(10*time.Second))
	h.Any("/*path", adaptor.HertzHandler(s.Routes(api.AdminAuth{})))
	stopped := make(chan struct{})
	go func() { defer close(stopped); _ = h.Run() }()
	defer func() {
		s.CancelRun("a", "hertz-disconnect")
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = h.Shutdown(ctx)
		listener.Close()
		select {
		case <-stopped:
		case <-ctx.Done():
		}
	}()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	res, e := http.DefaultClient.Do(streamRequest(t, ctx, "http://"+listener.Addr().String(), "hertz-disconnect"))
	if e != nil {
		t.Fatal(e)
	}
	defer res.Body.Close()
	scan := bufio.NewScanner(res.Body)
	if !scan.Scan() || !strings.Contains(scan.Text(), `"type":"chunk"`) {
		t.Fatal("Hertz did not flush incrementally", scan.Text(), scan.Err())
	}
	cancel()
	res.Body.Close()
	select {
	case <-upstreamCanceled:
	case <-time.After(2 * time.Second):
		t.Fatal("Hertz disconnect did not cancel provider context")
	}
}
