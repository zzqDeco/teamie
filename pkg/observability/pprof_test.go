package observability

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	rpprof "runtime/pprof"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestPprofRejectsNonLoopbackAndHonorsDisabledValues(t *testing.T) {
	out := testLogging(t)

	for _, disabledVal := range []string{"", "   ", "off", "OFF", "disabled", "false", "none", "0"} {
		addr, stop, err := StartPprof(disabledVal)
		if err != nil || addr != "" || stop != nil {
			t.Fatalf("expected %q to disable pprof cleanly, got addr=%q stopNonNil=%t err=%v", disabledVal, addr, stop != nil, err)
		}
	}
	if len(logLines(t, out)) != 0 {
		t.Fatalf("disabled pprof should not emit rejection errors: %s", out.String())
	}

	unsafeAddrs := []string{
		":6060",
		"0.0.0.0:6060",
		"[::]:6060",
		"192.168.1.25:6060",
		"10.0.0.8:6060",
		"invalid-addr",
		"127.0.0.1:70000",
	}
	for _, raw := range unsafeAddrs {
		addr, stop, err := StartPprof(raw)
		if err == nil || addr != "" || stop != nil {
			if stop != nil {
				_ = stop(context.Background())
			}
			t.Fatalf("expected unsafe/invalid pprof address %q to be rejected, got addr=%q err=%v", raw, addr, err)
		}
	}

	rejected := 0
	for _, event := range logLines(t, out) {
		if event["event.action"] == "pprof-rejected" && event["event.outcome"] == "failure" {
			rejected++
		}
	}
	if rejected != len(unsafeAddrs) {
		t.Fatalf("expected %d pprof-rejected events, got %d", len(unsafeAddrs), rejected)
	}
}

func TestPprofLoopbackEndpointsGuardrailsAndAuditRedaction(t *testing.T) {
	out := testLogging(t)

	addr, stop, err := StartPprof("localhost:0")
	if err != nil {
		t.Fatalf("StartPprof(localhost:0) failed: %v", err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := stop(ctx); err != nil {
			t.Errorf("stop pprof failed: %v", err)
		}
	}()
	if !strings.HasPrefix(addr, "127.0.0.1:") {
		t.Fatalf("expected localhost to normalize to 127.0.0.1, got %q", addr)
	}
	baseURL := "http://" + addr

	// Sanitize cmdline even if os.Args contains sensitive-looking flags.
	origArgs := append([]string(nil), os.Args...)
	os.Args = append(os.Args, "--secret-token=never-expose-cmdline-arg")
	defer func() { os.Args = origArgs }()

	res, err := http.Get(baseURL + "/debug/pprof/cmdline")
	if err != nil {
		t.Fatal(err)
	}
	cmdBody, _ := io.ReadAll(res.Body)
	_ = res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("cmdline status: %d", res.StatusCode)
	}
	if string(cmdBody) != filepath.Base(origArgs[0]) || strings.Contains(string(cmdBody), "never-expose") {
		t.Fatalf("cmdline leaked arguments: %q", string(cmdBody))
	}
	if res.Header.Get("Cache-Control") != "no-store" || res.Header.Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("missing security headers on pprof response: %v", res.Header)
	}

	// Verify gzipped protobuf snapshots (0x1f 0x8b magic) and query-secret non-logging.
	for _, path := range []string{
		"/debug/pprof/heap?gc=1",
		"/debug/pprof/allocs",
		"/debug/pprof/goroutine",
		"/debug/pprof/threadcreate",
	} {
		resp, err := http.Get(baseURL + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		data, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET %s status=%d body=%s", path, resp.StatusCode, string(data))
		}
		if len(data) < 2 || data[0] != 0x1f || data[1] != 0x8b {
			t.Fatalf("GET %s did not return gzipped pprof protobuf (len=%d)", path, len(data))
		}
	}

	// Readable aggregated goroutine stack (debug=1) works; debug=2 is blocked.
	resp, err := http.Get(baseURL + "/debug/pprof/goroutine?debug=1")
	if err != nil {
		t.Fatal(err)
	}
	stackText, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(stackText), "goroutine profile:") {
		t.Fatalf("expected goroutine debug=1 summary, status=%d", resp.StatusCode)
	}

	// Guardrail rejections.
	badRequests := []string{
		"/debug/pprof/goroutine?debug=2&secret=private-query-secret",
		"/debug/pprof/heap?debug=2",
		"/debug/pprof/profile?seconds=0",
		"/debug/pprof/profile?seconds=61",
		"/debug/pprof/profile?debug=1",
		"/debug/pprof/trace?seconds=0",
		"/debug/pprof/trace?seconds=11",
		"/debug/pprof/block?seconds=31",
		"/debug/pprof/goroutine?seconds=1&debug=1",
		"/debug/pprof/goroutine?gc=1",
	}
	for _, path := range badRequests {
		r, err := http.Get(baseURL + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		_, _ = io.Copy(io.Discard, r.Body)
		_ = r.Body.Close()
		if r.StatusCode != http.StatusBadRequest {
			t.Fatalf("expected 400 for %s, got %d", path, r.StatusCode)
		}
	}

	if strings.Contains(out.String(), "private-query-secret") || strings.Contains(out.String(), "never-expose-cmdline-arg") {
		t.Fatal("sensitive query or cmdline value leaked into ECS logs")
	}

	var sawListening, sawHeapCapture bool
	for _, ev := range logLines(t, out) {
		if ev["event.action"] == "pprof-listening" && ev["custom.pprof.addr"] == addr {
			sawListening = true
		}
		if ev["event.action"] == "pprof-capture" && ev["custom.pprof.profile"] == "heap" && ev["event.outcome"] == "success" {
			if b, ok := ev["http.response.body.bytes"].(float64); !ok || b <= 0 {
				t.Fatalf("expected numeric http.response.body.bytes metric, got %v", ev)
			}
			sawHeapCapture = true
		}
	}
	if !sawListening || !sawHeapCapture {
		t.Fatalf("missing expected pprof ECS events (listening=%v heap=%v)", sawListening, sawHeapCapture)
	}
}

func TestPprofSingleFlightConcurrencyAndDynamicMutexReset(t *testing.T) {
	_ = testLogging(t)
	runtime.SetMutexProfileFraction(0)
	defer runtime.SetMutexProfileFraction(0)

	addr, stop, err := StartPprof("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stop(context.Background()) }()
	baseURL := "http://" + addr

	// Generate real mutex contention while 1s mutex delta window runs.
	stopContention := make(chan struct{})
	var contendMu sync.Mutex
	var contendWG sync.WaitGroup
	for i := 0; i < 4; i++ {
		contendWG.Add(1)
		go func() {
			defer contendWG.Done()
			for {
				select {
				case <-stopContention:
					return
				default:
					contendMu.Lock()
					time.Sleep(200 * time.Microsecond)
					contendMu.Unlock()
				}
			}
		}()
	}

	type result struct {
		status int
		body   []byte
		err    error
	}
	firstDone := make(chan result, 1)
	go func() {
		resp, reqErr := http.Get(baseURL + "/debug/pprof/mutex?seconds=1")
		if reqErr != nil {
			firstDone <- result{err: reqErr}
			return
		}
		b, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		firstDone <- result{status: resp.StatusCode, body: b}
	}()

	// Wait until pprofSampling is acquired, then verify overlapping capture gets 409.
	deadline := time.Now().Add(800 * time.Millisecond)
	for !pprofSampling.Load() && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if !pprofSampling.Load() {
		close(stopContention)
		contendWG.Wait()
		t.Fatal("mutex window did not acquire single-flight sampling lock")
	}

	secondResp, err := http.Get(baseURL + "/debug/pprof/profile?seconds=1")
	if err != nil {
		close(stopContention)
		contendWG.Wait()
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, secondResp.Body)
	_ = secondResp.Body.Close()
	if secondResp.StatusCode != http.StatusConflict {
		close(stopContention)
		contendWG.Wait()
		t.Fatalf("expected 409 Conflict for concurrent sampling, got %d", secondResp.StatusCode)
	}

	first := <-firstDone
	close(stopContention)
	contendWG.Wait()
	if first.err != nil || first.status != http.StatusOK || len(first.body) < 2 || first.body[0] != 0x1f {
		t.Fatalf("mutex window capture failed: status=%d len=%d err=%v", first.status, len(first.body), first.err)
	}

	if gotFrac := runtime.SetMutexProfileFraction(-1); gotFrac != 0 {
		t.Fatalf("expected mutex profile fraction to reset to 0 after window, got %d", gotFrac)
	}

	// Verify lock was released by running a 1s execution trace.
	traceResp, err := http.Get(baseURL + "/debug/pprof/trace?seconds=1")
	if err != nil {
		t.Fatal(err)
	}
	traceBytes, _ := io.ReadAll(traceResp.Body)
	_ = traceResp.Body.Close()
	if traceResp.StatusCode != http.StatusOK || len(traceBytes) == 0 {
		t.Fatalf("expected 1s trace capture to succeed after lock release: status=%d len=%d", traceResp.StatusCode, len(traceBytes))
	}
}

func TestHTTPAndRPCLowCardinalityPprofLabels(t *testing.T) {
	_ = testLogging(t)

	var gotHTTPComponent, gotHTTPMethod string
	srv := httptest.NewServer(HTTP(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHTTPComponent, _ = rpprof.Label(r.Context(), "component")
		gotHTTPMethod, _ = rpprof.Label(r.Context(), "http.method")
		w.WriteHeader(http.StatusNoContent)
	})))
	defer srv.Close()

	req, err := http.NewRequest("CUSTOM_UNTRUSTED_METHOD", srv.URL+"/items/secret-path-id", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if gotHTTPComponent != "http-server" || gotHTTPMethod != "OTHER" {
		t.Fatalf("unexpected HTTP pprof labels: component=%q method=%q", gotHTTPComponent, gotHTTPMethod)
	}

	var gotRPCComponent, gotRPCService, gotRPCMethod string
	inbound := rpcMiddleware(true)(func(ctx context.Context, _, _ any) error {
		gotRPCComponent, _ = rpprof.Label(ctx, "component")
		gotRPCService, _ = rpprof.Label(ctx, "rpc.service")
		gotRPCMethod, _ = rpprof.Label(ctx, "rpc.method")
		return nil
	})
	if err := inbound(context.Background(), struct{}{}, &rpcResultEnvelope{Success: &rpcReadyReply{Ready: true}}); err != nil {
		t.Fatal(err)
	}
	if gotRPCComponent != "rpc-server" || gotRPCService != "unknown" || gotRPCMethod != "unknown" {
		t.Fatalf("unexpected RPC pprof labels: component=%q service=%q method=%q", gotRPCComponent, gotRPCService, gotRPCMethod)
	}
}

func TestCapturedProfileSymbolizesWithGoToolPprof(t *testing.T) {
	_ = testLogging(t)

	addr, stop, err := StartPprof("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stop(context.Background()) }()

	resp, err := http.Get("http://" + addr + "/debug/pprof/goroutine")
	if err != nil {
		t.Fatal(err)
	}
	payload, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("goroutine capture failed: status=%d err=%v", resp.StatusCode, err)
	}

	tmpFile := filepath.Join(t.TempDir(), "goroutine.pb.gz")
	if err := os.WriteFile(tmpFile, payload, 0o600); err != nil {
		t.Fatal(err)
	}

	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("go toolchain not in PATH")
	}
	cmd := exec.Command(goBin, "tool", "pprof", "-top", tmpFile)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("go tool pprof -top failed: %v\nstderr: %s", err, stderr.String())
	}
	if !strings.Contains(stdout.String(), "serveNamedProfile") && !strings.Contains(stdout.String(), "net/http") {
		t.Fatalf("expected symbolized Go function names in pprof output, got:\n%s", stdout.String())
	}
}

