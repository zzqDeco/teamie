package httpx

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/route"
)

func reserveLocalAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen: %v", err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	return addr
}

func waitForHTTPStatus(t *testing.T, url string, want int) {
	t.Helper()
	tr := &http.Transport{DisableKeepAlives: true}
	defer tr.CloseIdleConnections()
	client := &http.Client{Timeout: 500 * time.Millisecond, Transport: tr}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := client.Get(url)
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == want {
				return
			}
		}
		time.Sleep(15 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s status %d", url, want)
}

func TestParseDrainGrace(t *testing.T) {
	t.Setenv("TEAMIE_DRAIN_GRACE", "")
	if got := ParseDrainGrace(); got != DefaultDrainGrace {
		t.Fatalf("unset TEAMIE_DRAIN_GRACE = %v, want %v", got, DefaultDrainGrace)
	}

	t.Setenv("TEAMIE_DRAIN_GRACE", "0")
	if got := ParseDrainGrace(); got != 0 {
		t.Fatalf("TEAMIE_DRAIN_GRACE=0 = %v, want 0", got)
	}

	t.Setenv("TEAMIE_DRAIN_GRACE", "75ms")
	if got := ParseDrainGrace(); got != 75*time.Millisecond {
		t.Fatalf("TEAMIE_DRAIN_GRACE=75ms = %v, want 75ms", got)
	}

	t.Setenv("TEAMIE_DRAIN_GRACE", "not-a-duration")
	if got := ParseDrainGrace(); got != DefaultDrainGrace {
		t.Fatalf("invalid TEAMIE_DRAIN_GRACE = %v, want %v", got, DefaultDrainGrace)
	}
}

func TestGracefulDrainSequenceAndInFlightCompletion(t *testing.T) {
	addr := reserveLocalAddr(t)
	slowStarted := make(chan struct{})
	releaseSlow := make(chan struct{})

	mux := http.NewServeMux()
	Operations(mux, func(*http.Request) error { return nil })
	handler := WrapHertzRouter(mux, func(r *route.RouterGroup) {
		r.GET("/api/v1/ping", func(_ context.Context, c *app.RequestContext) {
			c.JSON(http.StatusOK, map[string]string{"ok": "true"})
		})
		r.GET("/api/v1/slow", func(_ context.Context, c *app.RequestContext) {
			select {
			case <-slowStarted:
			default:
				close(slowStarted)
			}
			<-releaseSlow
			c.JSON(http.StatusOK, map[string]string{"completed": "true"})
		})
	})

	h := NewServer(addr, handler)
	ctrl := DrainControllerFor(h)

	var (
		mu     sync.Mutex
		events []string
	)
	record := func(ev string) {
		mu.Lock()
		events = append(events, ev)
		mu.Unlock()
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		RunServer(
			h,
			WithDrainGrace(120*time.Millisecond),
			WithShutdownTimeout(5*time.Second),
			OnDrainStart(func() { record("drain-start") }),
			OnPreShutdown(func(context.Context) { record("pre-shutdown-1") }),
			OnPreShutdown(func(context.Context) { record("pre-shutdown-2") }),
			OnShutdown(func(context.Context) { record("shutdown-hook") }),
		)
	}()

	baseURL := "http://" + addr
	waitForHTTPStatus(t, baseURL+"/readyz", http.StatusOK)

	// Trigger programmatic drain.
	if !ctrl.BeginDrain() {
		t.Fatal("expected BeginDrain to return true on first invocation")
	}
	if ctrl.BeginDrain() {
		t.Fatal("expected second BeginDrain call to return false")
	}

	client := &http.Client{Timeout: 3 * time.Second}

	// /readyz must immediately become 503 {"status":"draining"} while /healthz stays 200.
	readyResp, err := client.Get(baseURL + "/readyz")
	if err != nil {
		t.Fatalf("GET /readyz during drain: %v", err)
	}
	var readyPayload map[string]string
	_ = json.NewDecoder(readyResp.Body).Decode(&readyPayload)
	_ = readyResp.Body.Close()
	if readyResp.StatusCode != http.StatusServiceUnavailable || readyPayload["status"] != "draining" {
		t.Fatalf("GET /readyz = %d %v, want 503 status=draining", readyResp.StatusCode, readyPayload)
	}

	healthResp, err := client.Get(baseURL + "/healthz")
	if err != nil {
		t.Fatalf("GET /healthz during drain: %v", err)
	}
	_ = healthResp.Body.Close()
	if healthResp.StatusCode != http.StatusOK {
		t.Fatalf("GET /healthz during drain = %d, want 200", healthResp.StatusCode)
	}

	// Ordinary API calls arriving inside the drain grace window must still succeed.
	pingResp, err := client.Get(baseURL + "/api/v1/ping")
	if err != nil {
		t.Fatalf("GET /api/v1/ping during drain grace: %v", err)
	}
	_ = pingResp.Body.Close()
	if pingResp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/v1/ping during drain grace = %d, want 200", pingResp.StatusCode)
	}

	// Start a slow request inside the drain grace window and let it complete after grace expires.
	slowErrCh := make(chan error, 1)
	go func() {
		resp, reqErr := client.Get(baseURL + "/api/v1/slow")
		if reqErr != nil {
			slowErrCh <- reqErr
			return
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			slowErrCh <- fmt.Errorf("slow status = %d, want 200", resp.StatusCode)
			return
		}
		slowErrCh <- nil
	}()

	<-slowStarted
	// Wait past the 120ms drain grace so h.Shutdown is actively waiting on the in-flight slow request.
	time.Sleep(160 * time.Millisecond)
	select {
	case <-done:
		t.Fatal("RunServer exited before in-flight slow request finished")
	default:
	}

	close(releaseSlow)
	if slowErr := <-slowErrCh; slowErr != nil {
		t.Fatalf("in-flight slow request failed: %v", slowErr)
	}

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for RunServer to complete shutdown")
	}

	mu.Lock()
	gotEvents := append([]string{}, events...)
	mu.Unlock()
	if len(gotEvents) != 4 ||
		gotEvents[0] != "drain-start" ||
		gotEvents[1] != "pre-shutdown-1" ||
		gotEvents[2] != "pre-shutdown-2" ||
		gotEvents[3] != "shutdown-hook" {
		t.Fatalf("unexpected lifecycle hook order: %v", gotEvents)
	}
}

func TestSIGTERMTriggersGracefulDrain(t *testing.T) {
	addr := reserveLocalAddr(t)
	mux := http.NewServeMux()
	Operations(mux, func(*http.Request) error { return nil })

	h := NewServerWithOptions(addr, mux)
	ctrl := DrainControllerFor(h)

	done := make(chan struct{})
	go func() {
		defer close(done)
		RunServer(h, WithDrainGrace(40*time.Millisecond), WithShutdownTimeout(500*time.Millisecond))
	}()

	baseURL := "http://" + addr
	waitForHTTPStatus(t, baseURL+"/readyz", http.StatusOK)

	if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatalf("syscall.Kill SIGTERM: %v", err)
	}

	deadline := time.Now().Add(time.Second)
	for !ctrl.Draining() && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if !ctrl.Draining() {
		t.Fatal("expected SIGTERM to flip controller into draining state")
	}

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for SIGTERM graceful shutdown")
	}
}

func TestDualSlotDialContextFallsBackToCanaryOnConnectFailure(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen: %v", err)
	}
	defer ln.Close()
	go func() {
		for {
			c, acceptErr := ln.Accept()
			if acceptErr != nil {
				return
			}
			_ = c.Close()
		}
	}()

	var mu sync.Mutex
	var dialed []string
	dial := dualSlotDialContextWith(func(ctx context.Context, network, addr string) (net.Conn, error) {
		mu.Lock()
		dialed = append(dialed, addr)
		mu.Unlock()
		if addr == "chat-canary:8082" {
			return (&net.Dialer{}).DialContext(ctx, network, ln.Addr().String())
		}
		return nil, fmt.Errorf("dial %s: connection refused", addr)
	})

	conn, err := dial(context.Background(), "tcp", "chat:8082")
	if err != nil {
		t.Fatalf("expected fallback to chat-canary:8082 to succeed, got %v", err)
	}
	_ = conn.Close()

	mu.Lock()
	got := append([]string(nil), dialed...)
	dialed = nil
	mu.Unlock()
	if len(got) != 2 || got[0] != "chat:8082" || got[1] != "chat-canary:8082" {
		t.Fatalf("unexpected dial sequence for service host: %v", got)
	}

	for _, nonCanary := range []string{"127.0.0.1:8082", "localhost:8082", "chat-canary:8082", "api.example.com:443"} {
		if nonCanary == "chat-canary:8082" {
			continue
		}
		mu.Lock()
		dialed = nil
		mu.Unlock()
		if _, err := dial(context.Background(), "tcp", nonCanary); err == nil {
			t.Fatalf("expected dial error for %s", nonCanary)
		}
		mu.Lock()
		attempts := len(dialed)
		mu.Unlock()
		if attempts != 1 {
			t.Fatalf("expected single dial attempt for %s, got %d", nonCanary, attempts)
		}
	}

	canceledCtx, cancel := context.WithCancel(context.Background())
	cancel()
	mu.Lock()
	dialed = nil
	mu.Unlock()
	_, _ = dial(canceledCtx, "tcp", "chat:8082")
	mu.Lock()
	canceledAttempts := len(dialed)
	mu.Unlock()
	if canceledAttempts != 1 {
		t.Fatalf("expected canceled context not to fallback, got %d attempts", canceledAttempts)
	}
}
