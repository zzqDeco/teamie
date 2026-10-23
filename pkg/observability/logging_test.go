package observability

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

type logBuffer struct {
	mu   sync.Mutex
	data bytes.Buffer
}

func (b *logBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.data.Write(p)
}
func (b *logBuffer) Bytes() []byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]byte{}, b.data.Bytes()...)
}
func (b *logBuffer) String() string { return string(b.Bytes()) }
func testLogging(t *testing.T) *logBuffer {
	t.Helper()
	var out logBuffer
	logger := New(Config{Service: "logging-test", Version: "test", SHA: "checked-sha", Node: "fixture-node", Output: &out, Level: zapcore.DebugLevel})
	undo := zap.ReplaceGlobals(logger)
	previous := otel.GetTracerProvider()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSampler(sdktrace.NeverSample()))
	otel.SetTracerProvider(provider)
	t.Cleanup(func() { undo(); otel.SetTracerProvider(previous); _ = provider.Shutdown(context.Background()) })
	return &out
}

func logLines(t *testing.T, out *logBuffer) []map[string]any {
	t.Helper()
	var events []map[string]any
	for _, line := range bytes.Split(bytes.TrimSpace(out.Bytes()), []byte("\n")) {
		if len(line) == 0 {
			continue
		}
		var event map[string]any
		if err := json.Unmarshal(line, &event); err != nil {
			t.Fatalf("non-JSON event: %s: %v", line, err)
		}
		if event["@timestamp"] == nil || event["ecs.version"] == nil || event["log.level"] == nil || event["message"] == nil {
			t.Fatalf("missing formatter fields: %v", event)
		}
		events = append(events, event)
	}
	return events
}

func TestNativeECSOutputErrorOwnershipAndRedaction(t *testing.T) {
	out := testLogging(t)
	ctx, op := Begin(context.Background(), "fixture-operation")
	err := Diagnostic("native_concat_failed", errors.New("secret-password private-prompt tool-arguments"))
	L(ctx).Info("Static event", zap.String("authorization", "Bearer secret-key"), zap.String("api_key", "secret-api-key"), zap.Int("custom.model.output_token_limit", 32768), zap.Any("config", map[string]any{"password": "private"}), zap.String("custom.model.prompt", "private-prompt"), zap.Int("custom.model.prompt_tokens", 7990), zap.String("custom.http.headers_duration_ns", "private-headers"), zap.String("custom.http.body_consumed_to_eof", "private-body"))
	op.End(err)
	op.End(err)
	if strings.Contains(out.String(), "secret-password") || strings.Contains(out.String(), "secret-key") || strings.Contains(out.String(), "private-prompt") || strings.Contains(out.String(), "tool-arguments") || strings.Contains(out.String(), "private-headers") || strings.Contains(out.String(), "private-body") {
		t.Fatal("private material reached logs")
	}
	events := logLines(t, out)
	if len(events) != 2 {
		t.Fatalf("duplicate owning error event: %d", len(events))
	}
	if events[0]["custom.model.output_token_limit"] != float64(32768) || events[0]["custom.model.prompt_tokens"] != float64(7990) {
		t.Fatal("numeric capacity metric was redacted")
	}
	e := events[1]
	if e["event.outcome"] != "failure" || e["error.type"] != "*observability.CodeError" {
		t.Fatal(e)
	}
	native, ok := e["error"].(map[string]any)
	if !ok || native["message"] != "native_concat_failed" || native["stack_trace"] == nil {
		t.Fatalf("official native error formatter was bypassed: %v", e)
	}
	if e["service.environment"] != "production" || e["trace.id"] == nil || e["span.id"] == nil {
		t.Fatal(e)
	}
}

func TestHTTPContextRelationshipsWithoutExportAndUnsampledSpans(t *testing.T) {
	out := testLogging(t)
	const traceID = "1234567890abcdef1234567890abcdef"
	const parentID = "0123456789abcdef"
	var entry, sent, downstream trace.SpanContext
	var gotState string
	mux := http.NewServeMux()
	mux.HandleFunc("GET /items/{id}", func(w http.ResponseWriter, r *http.Request) {
		downstream = trace.SpanContextFromContext(r.Context())
		gotState = r.Header.Get("tracestate")
		L(r.Context()).Info("Downstream handled")
		_, _ = io.WriteString(w, "ok")
	})
	leaf := httptest.NewServer(HTTP(mux))
	defer leaf.Close()
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		parent := Propagator().Extract(context.Background(), propagation.HeaderCarrier(r.Header))
		sent = trace.SpanContextFromContext(parent)
		return http.DefaultTransport.RoundTrip(r)
	})
	entryServer := httptest.NewServer(HTTP(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		entry = trace.SpanContextFromContext(r.Context())
		if entry.IsSampled() || trace.SpanFromContext(r.Context()).IsRecording() {
			t.Error("test unexpectedly samples/exports")
		}
		req, _ := http.NewRequestWithContext(r.Context(), "GET", leaf.URL+"/items/private-token?token=never-log-this", nil)
		response, err := Client(&http.Client{Transport: transport}, "leaf").Do(req)
		if err != nil {
			t.Error(err)
			return
		}
		_, _ = io.Copy(io.Discard, response.Body)
		_ = response.Body.Close()
		w.WriteHeader(204)
	})))
	defer entryServer.Close()
	req, _ := http.NewRequest("GET", entryServer.URL, nil)
	req.Header.Set("traceparent", "00-"+traceID+"-"+parentID+"-00")
	req.Header.Set("tracestate", "vendor=value")
	req.Header.Set("X-Teamie-Test-Run-ID", "logging-integration")
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	for _, sc := range []trace.SpanContext{entry, sent, downstream} {
		if !sc.IsValid() || sc.TraceID().String() != traceID {
			t.Fatal("trace was not continued", sc)
		}
	}
	if entry.SpanID().String() == parentID || entry.SpanID() == sent.SpanID() || sent.SpanID() == downstream.SpanID() {
		t.Fatal("operation IDs reused")
	}
	if gotState != "vendor=value" {
		t.Fatalf("vendor state lost: %q", gotState)
	}
	if strings.Contains(out.String(), "private-token") || strings.Contains(out.String(), "never-log-this") {
		t.Fatal("path/query secret logged")
	}
	for _, event := range logLines(t, out) {
		if event["labels.test_run_id"] != "logging-integration" {
			t.Fatal("allowlisted test label did not propagate", event)
		}
		if event["trace.id"] != traceID {
			t.Fatalf("uncorrelated event: %v", event)
		}
		if event["custom.http.route"] == "GET /items/{id}" && event["custom.trace.parent_span_id"] != sent.SpanID().String() {
			t.Fatal("HTTP receiver did not use outbound operation as parent", event)
		}
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestConcurrentAndInvalidParentsCreateIsolatedRoots(t *testing.T) {
	out := testLogging(t)
	const valid = "1234567890abcdef1234567890abcdef"
	parents := []string{"", "malformed", "00-00000000000000000000000000000000-0000000000000000-00", "00-" + valid + "-0123456789abcdef-00"}
	var mu sync.Mutex
	seen := map[string]string{}
	srv := httptest.NewServer(HTTP(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get("X-Fixture-ID")
		sc := trace.SpanContextFromContext(r.Context())
		if !sc.IsValid() {
			t.Error("invalid root")
		}
		mu.Lock()
		seen[id] = sc.TraceID().String()
		mu.Unlock()
		ctx, operation := Begin(r.Context(), "async-fixture")
		done := make(chan struct{})
		go func() {
			defer close(done)
			L(ctx).Info("Async fixture", zap.String("labels.test_run_id", "isolated-test"))
			operation.End(nil)
		}()
		<-done
		w.WriteHeader(204)
	})))
	defer srv.Close()
	var wg sync.WaitGroup
	for i, parent := range parents {
		wg.Add(1)
		go func(i int, parent string) {
			defer wg.Done()
			req, _ := http.NewRequest("GET", srv.URL, nil)
			req.Header.Set("X-Fixture-ID", fmt.Sprint(i))
			req.Header.Set("traceparent", parent)
			if i == 3 {
				req.Header.Set("tracestate", "invalid vendor state")
			}
			res, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Error(err)
				return
			}
			_ = res.Body.Close()
		}(i, parent)
	}
	wg.Wait()
	if seen["3"] != valid {
		t.Fatal("invalid tracestate invalidated valid traceparent")
	}
	unique := map[string]bool{}
	for _, id := range seen {
		if unique[id] {
			t.Fatal("independent roots merged")
		}
		unique[id] = true
	}
	L(context.Background()).Info("After request")
	events := logLines(t, out)
	last := events[len(events)-1]
	if last["trace.id"] != nil || last["span.id"] != nil {
		t.Fatal("context leaked outside scope")
	}
}

func TestStreamingDurationAndCancellationAreLoggedOnce(t *testing.T) {
	out := testLogging(t)
	entered, release := make(chan struct{}), make(chan struct{})
	srv := httptest.NewServer(HTTP(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "first")
		w.(http.Flusher).Flush()
		close(entered)
		<-release
		_, _ = io.WriteString(w, "last")
	})))
	defer srv.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL, nil)
	res, err := Client(nil, "delayed-fixture").Do(req)
	if err != nil {
		t.Fatal(err)
	}
	<-entered
	for _, e := range logLines(t, out) {
		if e["event.action"] == "http-dependency" {
			t.Fatal("logged full response at headers")
		}
	}
	time.Sleep(20 * time.Millisecond)
	cancel()
	_, _ = io.Copy(io.Discard, res.Body)
	_ = res.Body.Close()
	close(release)
	count := 0
	for _, e := range logLines(t, out) {
		if e["event.action"] == "http-dependency" {
			count++
			if e["event.outcome"] != "failure" || e["event.duration"].(float64) < float64(15*time.Millisecond) {
				t.Fatal(e)
			}
			if _, ok := e["custom.http.headers_duration_ns"].(float64); !ok {
				t.Fatal("safe response timing metric was redacted", e)
			}
			if eof, ok := e["custom.http.body_consumed_to_eof"].(bool); !ok || eof {
				t.Fatal("canceled body EOF metadata missing or incorrect", e)
			}
		}
	}
	if count != 1 {
		t.Fatalf("terminal dependency events: %d", count)
	}
}

func TestHTTPProbeFailuresAndWebSocketHijacking(t *testing.T) {
	out := testLogging(t)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(503) })
	mux.HandleFunc("GET /ws", func(w http.ResponseWriter, r *http.Request) {
		conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		_ = conn.Close()
	})
	mux.HandleFunc("GET /panic", func(w http.ResponseWriter, r *http.Request) { panic("private-panic-data") })
	wrapped := HTTP(mux)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if recover() != nil {
				w.WriteHeader(500)
			}
		}()
		wrapped.ServeHTTP(w, r)
	}))
	defer srv.Close()
	for _, path := range []string{"/healthz", "/readyz", "/panic"} {
		req, _ := http.NewRequest("GET", srv.URL+path, nil)
		req.Header.Set("User-Agent", "kube-probe/1.34")
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = res.Body.Close()
	}
	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http")+"/ws", nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = conn.Close()
	for _, e := range logLines(t, out) {
		if e["custom.http.route"] == "GET /healthz" {
			t.Fatal("healthy probe log not filtered")
		}
	}
	if strings.Contains(out.String(), "private-panic-data") {
		t.Fatal("panic body leaked")
	}
	if !strings.Contains(out.String(), "503") || !strings.Contains(out.String(), "http_handler_panic") {
		t.Fatal("failure was filtered")
	}
}

type rpcResultEnvelope struct {
	Success *rpcReadyReply
}

type rpcReadyReply struct {
	Ready bool
}

func (r *rpcReadyReply) GetReady() bool {
	return r != nil && r.Ready
}

func TestRPCMiddlewarePropagatesCurrentOperation(t *testing.T) {
	out := testLogging(t)
	observed := make(chan trace.SpanContext, 1)
	serverEndpoint := rpcMiddleware(true)(func(ctx context.Context, _, response any) error {
		observed <- trace.SpanContextFromContext(ctx)
		L(ctx).Info("RPC handled")
		if env, ok := response.(*rpcResultEnvelope); ok {
			env.Success = &rpcReadyReply{Ready: true}
		}
		return nil
	})
	clientEndpoint := rpcMiddleware(false)(serverEndpoint)
	parent, outer := Begin(context.Background(), "rpc-fixture")
	before := trace.SpanContextFromContext(parent)
	res := &rpcResultEnvelope{}
	if err := clientEndpoint(parent, struct{}{}, res); err != nil {
		t.Fatal(err)
	}
	if !res.Success.GetReady() {
		t.Fatal("fixture not ready")
	}
	actual := <-observed
	if !actual.IsValid() || actual.TraceID() != before.TraceID() || actual.SpanID() == before.SpanID() {
		t.Fatal("RPC correlation failed", actual, before)
	}
	outer.End(nil)
	var clientSpan, serverSpan, serverParent string
	for _, e := range logLines(t, out) {
		if e["event.action"] == "rpc-client" {
			clientSpan, _ = e["span.id"].(string)
		}
		if e["event.action"] == "rpc-server" {
			serverSpan, _ = e["span.id"].(string)
			serverParent, _ = e["custom.trace.parent_span_id"].(string)
		}
	}
	if clientSpan == "" || serverSpan == "" || clientSpan == serverSpan || serverParent != clientSpan {
		t.Fatal("RPC operation spans absent or reused")
	}
}

type duplexFixture struct {
	bytes.Buffer
	closed bool
}

func (b *duplexFixture) Close() error { b.closed = true; return nil }
func TestTransportPreservesDuplexUpgradeBody(t *testing.T) {
	out := testLogging(t)
	body := &duplexFixture{}
	base := roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 101, Header: http.Header{}, Body: body}, nil
	})
	req, _ := http.NewRequest("POST", "http://docker/attach", nil)
	res, err := (Transport{Base: base, Peer: "docker"}).RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	duplex, ok := res.Body.(io.ReadWriteCloser)
	if !ok {
		t.Fatal("instrumentation erased upgraded writer")
	}
	if _, err = duplex.Write([]byte("private-child-input")); err != nil {
		t.Fatal(err)
	}
	if err = duplex.Close(); err != nil || !body.closed {
		t.Fatal("underlying upgraded connection not closed", err)
	}
	if body.String() != "private-child-input" || strings.Contains(out.String(), "private-child-input") {
		t.Fatal("duplex data changed or was logged")
	}
}

func TestRetryAttemptsKeepTraceAndMeasureLogicalTotal(t *testing.T) {
	out := testLogging(t)
	requests := make(chan struct{}, 2)
	srv := httptest.NewServer(HTTP(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests <- struct{}{}
		if len(requests) == 1 {
			w.WriteHeader(503)
		} else {
			w.WriteHeader(200)
		}
		_, _ = io.WriteString(w, "private-retry-body")
	})))
	defer srv.Close()
	ctx, total := Begin(context.Background(), "logical-retry-fixture")
	ctx = WithHTTPAttempts(ctx)
	for i := 0; i < 2; i++ {
		if i > 0 {
			time.Sleep(10 * time.Millisecond)
		}
		req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL, nil)
		response, err := Client(nil, "retry-fixture").Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, response.Body)
		_ = response.Body.Close()
	}
	total.End(nil, zap.Int("custom.retry.attempts", HTTPAttempts(ctx)))
	if HTTPAttempts(ctx) != 2 {
		t.Fatal("actual dependency attempts not counted")
	}
	traceID := trace.SpanContextFromContext(ctx).TraceID().String()
	spans := map[string]bool{}
	attempts := map[float64]bool{}
	var longest, totalNS float64
	for _, event := range logLines(t, out) {
		if event["trace.id"] != traceID {
			t.Fatal("retry detached from originating trace", event)
		}
		if event["event.action"] == "http-dependency" {
			spans[event["span.id"].(string)] = true
			attempts[event["custom.retry.attempt"].(float64)] = true
			if ns := event["event.duration"].(float64); ns > longest {
				longest = ns
			}
		}
		if event["event.action"] == "logical-retry-fixture" {
			totalNS = event["event.duration"].(float64)
		}
	}
	if len(spans) != 2 || !attempts[1] || !attempts[2] || totalNS <= longest {
		t.Fatal("attempt/total scope or identities lost")
	}
	if strings.Contains(out.String(), "private-retry-body") {
		t.Fatal("dependency response body logged")
	}
}
