package gateway_test

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
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/cloudwego/hertz/pkg/common/adaptor"

	gatewaygen "teamie/contracts/gen/agent/hertz_gen/agent/gateway"
	"teamie/contracts/gen/agent/kitex_gen/agent"
	"teamie/pkg/httpx"
	httpAdapter "teamie/services/llm-gateway/internal/adapter/http"
	rpcx "teamie/services/llm-gateway/internal/infra/rpcclient"
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
	s.Provider = Provider{HTTP: fixture.Client()}
	return s, httptest.NewServer(s.Routes(rpcx.AdminAuth{}))
}
func streamRequest(t *testing.T, ctx context.Context, url, run string) *http.Request {
	t.Helper()
	raw, _ := json.Marshal(map[string]any{"ownerId": "o", "agentId": "a", "sessionId": "s", "runId": run, "requestJson": fixtureNativeRequest(`{"model":"bypass","messages":[{"role":"user","content":"test"}],"stream":true}`, true)})
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
	if scan.Err() != nil || len(events) != 3 || events[2].Type != "done" || !strings.Contains(events[0].ResponseJSON, "function_tool_call") {
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
	h.Any("/*path", adaptor.HertzHandler(s.Routes(rpcx.AdminAuth{})))
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

func TestHertzNativeSSEWriterStreamChat(t *testing.T) {
	s, dummy := streamFixtureService(t, func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Stream bool `json:"stream"`
		}
		_ = json.NewDecoder(r.Body).Decode(&request)
		switch r.URL.Path {
		case "/search":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"results":[{"title":"IANA Example Domains","url":"https://www.iana.org/help/example-domains","content":"Example domains reserved by IANA","score":0.99}]}`))
			return
		case "/extract":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"results":[{"url":"https://www.iana.org/help/example-domains","raw_content":"# IANA Example Domains\nexample.com"}],"failed_results":[]}`))
			return
		}
		if !request.Stream {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"fixture-unary","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"OK"},"finish_reason":"stop"}]}`))
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "data: %s\n\n", delta)
		fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", finalDelta)
	})
	dummy.Close()
	crypto := testCipher(t)
	wsCipher, err := crypto.Encrypt("tvly-hertz-test-key")
	if err != nil {
		t.Fatal(err)
	}
	store := s.Store.(*memoryStore)
	store.mu.Lock()
	store.c.WebSearchCipherKey = wsCipher
	store.c.WebSearchEnabled = true
	baseURL := store.c.BaseURL
	store.mu.Unlock()
	s.WebSearchURL = strings.TrimSuffix(baseURL, "/v1")
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	h := server.New(server.WithListener(listener))
	httpx.MountHertz(h, s.Routes(rpcx.AdminAuth{}))
	_ = httpAdapter.New(s.App)
	var _ gatewaygen.ModelGatewayHTTPService = (*httpAdapter.Adapter)(nil)
	stopped := make(chan struct{})
	go func() { defer close(stopped); _ = h.Run() }()
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = h.Shutdown(ctx)
		listener.Close()
		<-stopped
	}()
	req := streamRequest(t, context.Background(), "http://"+listener.Addr().String(), "sse-writer")
	req.Header.Set("Accept", "text/event-stream")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "event: chunk") || !strings.Contains(string(body), "event: done") {
		t.Fatalf("expected Hertz sse.NewWriter events, got %s", string(body))
	}
	if !strings.HasPrefix(res.Header.Get("Content-Type"), "text/event-stream") {
		t.Fatalf("expected text/event-stream Content-Type, got %q", res.Header.Get("Content-Type"))
	}
	if res.Header.Get("X-Teamie-Model-Config-ID") == "" {
		t.Fatalf("expected X-Teamie-Model-Config-ID header, got empty (headers=%v)", res.Header)
	}
	scriptPath, err := filepath.Abs("../../../ops/acceptance-real-model.py")
	if err != nil {
		t.Fatal(err)
	}
	py := exec.Command(
		"python3",
		"-c",
		`import importlib.util, sys
spec = importlib.util.spec_from_file_location("m", sys.argv[1])
m = importlib.util.module_from_spec(spec)
spec.loader.exec_module(m)
c = m.Client({"origin": sys.argv[2], "identity": sys.argv[2], "chat": sys.argv[2], "control": sys.argv[2], "gateway": sys.argv[2], "internal_gateway": sys.argv[2], "runtime_token": sys.argv[4]})
cid = c.verify_internal_unary(sys.argv[4], "owner-1")
assert cid == sys.argv[3], f"{cid} != {sys.argv[3]}"
c.verify_internal_sse_stream(sys.argv[4], "owner-1", cid)
c.verify_internal_web_search(sys.argv[4], "owner-1")
`,
		scriptPath,
		"http://"+listener.Addr().String(),
		res.Header.Get("X-Teamie-Model-Config-ID"),
		testToken,
	)
	if out, err := py.CombinedOutput(); err != nil {
		t.Fatalf("acceptance-real-model.py SSE/web-search client against real Hertz gateway failed: %v\n%s", err, string(out))
	}
	_ = app.RequestContext{}
}
