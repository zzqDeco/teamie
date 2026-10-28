package ws

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"teamie/pkg/httpx"
	"teamie/services/realtime/internal/app"
	"teamie/services/realtime/internal/domain"
)

type stubValidator struct {
	mu       sync.Mutex
	sessions map[string]domain.User
	onValid  func(userID string)
}

func (v *stubValidator) Validate(_ context.Context, token string) (domain.Session, error) {
	v.mu.Lock()
	u, ok := v.sessions[token]
	onValid := v.onValid
	v.mu.Unlock()
	if !ok {
		return domain.Session{}, domain.Err(401, "unauthenticated", "Sign in to continue")
	}
	if onValid != nil {
		onValid(u.ID)
	}
	return domain.Session{User: u, CSRFToken: "csrf"}, nil
}

func (v *stubValidator) Ping(context.Context) error { return nil }

type stubAccess struct {
	accessCalls atomic.Int64
	members     []string
}

func (a *stubAccess) CheckAccess(_ context.Context, userID, conversationID string) (domain.AccessResult, error) {
	a.accessCalls.Add(1)
	if conversationID != "conv-1" {
		return domain.AccessResult{}, nil
	}
	for _, m := range a.members {
		if m == userID {
			return domain.AccessResult{
				Allowed:       true,
				MemberIDs:     append([]string(nil), a.members...),
				TypingEnabled: true,
			}, nil
		}
	}
	return domain.AccessResult{}, nil
}

func (a *stubAccess) CheckWorkspaceAccess(context.Context, string, string) (domain.WorkspaceAccessResult, error) {
	return domain.WorkspaceAccessResult{}, nil
}

func (a *stubAccess) SharedWorkspaceMembers(context.Context, string) ([]string, bool, error) {
	return nil, false, nil
}

func (a *stubAccess) Ping(context.Context) error { return nil }

func readInitialWSFrames(t *testing.T, c *websocket.Conn) {
	t.Helper()
	_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
	for _, want := range []string{"ready", "sync.required"} {
		var frame map[string]any
		if err := c.ReadJSON(&frame); err != nil {
			t.Fatalf("read initial %s frame: %v", want, err)
		}
		if frame["type"] != want {
			t.Fatalf("initial frame type = %v, want %s", frame["type"], want)
		}
	}
}

func TestConcurrentWebSocketHandshakesEnforcePerUserLimit(t *testing.T) {
	const burst = 24
	const origin = "https://teamie.example"
	token := strings.Repeat("a", 64)

	var arrived atomic.Int32
	release := make(chan struct{})
	var releaseOnce sync.Once

	validator := &stubValidator{
		sessions: map[string]domain.User{
			token: {ID: "alice", DisplayName: "Alice", Kind: "human"},
		},
		onValid: func(string) {
			if int(arrived.Add(1)) >= burst {
				releaseOnce.Do(func() { close(release) })
			}
			select {
			case <-release:
			case <-time.After(2 * time.Second):
			}
		},
	}

	var policy domain.TypingTracker
	streamApp := &app.StreamApp{
		Identity: validator,
		Chat:     &stubAccess{members: []string{"alice"}},
		Policy:   &policy,
	}
	adapter := &Adapter{
		App:    streamApp,
		Origin: origin,
	}
	streamApp.Clients = adapter.ActiveClients

	server := httptest.NewServer(adapter.Routes())
	defer server.Close()
	defer adapter.CloseAll()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/api/v1/ws"
	headers := http.Header{
		"Origin": {origin},
		"Cookie": {httpx.CookieName + "=" + token},
	}

	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		conns    []*websocket.Conn
		rejected int
	)
	for range burst {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c, resp, err := websocket.DefaultDialer.Dial(wsURL, headers)
			if err == nil {
				mu.Lock()
				conns = append(conns, c)
				mu.Unlock()
				return
			}
			if resp != nil {
				defer resp.Body.Close()
				if resp.StatusCode == http.StatusTooManyRequests {
					mu.Lock()
					rejected++
					mu.Unlock()
					return
				}
				t.Errorf("unexpected status = %d, err = %v", resp.StatusCode, err)
				return
			}
			t.Errorf("dial failed without HTTP response: %v", err)
		}()
	}
	wg.Wait()
	defer func() {
		for _, c := range conns {
			_ = c.Close()
		}
	}()

	if len(conns) != domain.MaxPeersPerUser {
		t.Fatalf("established connections = %d, want %d", len(conns), domain.MaxPeersPerUser)
	}
	if rejected != burst-domain.MaxPeersPerUser {
		t.Fatalf("rejected 429 connections = %d, want %d", rejected, burst-domain.MaxPeersPerUser)
	}
	for _, c := range conns {
		readInitialWSFrames(t, c)
	}
	if got := len(adapter.ActiveClients()); got != domain.MaxPeersPerUser {
		t.Fatalf("ActiveClients = %d, want %d", got, domain.MaxPeersPerUser)
	}

	// Disable barrier for subsequent sequential checks.
	validator.mu.Lock()
	validator.onValid = nil
	validator.mu.Unlock()

	// Closing two connections frees capacity for exactly two new connections.
	_ = conns[0].Close()
	_ = conns[1].Close()
	conns = conns[2:]

	deadline := time.Now().Add(2 * time.Second)
	for len(adapter.ActiveClients()) != domain.MaxPeersPerUser-2 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if got := len(adapter.ActiveClients()); got != domain.MaxPeersPerUser-2 {
		t.Fatalf("ActiveClients after close = %d, want %d", got, domain.MaxPeersPerUser-2)
	}

	for range 2 {
		c, _, err := websocket.DefaultDialer.Dial(wsURL, headers)
		if err != nil {
			t.Fatalf("expected connection after close to succeed: %v", err)
		}
		readInitialWSFrames(t, c)
		conns = append(conns, c)
	}
	if c, resp, err := websocket.DefaultDialer.Dial(wsURL, headers); err == nil {
		_ = c.Close()
		t.Fatal("connection beyond MaxPeersPerUser succeeded")
	} else if resp == nil || resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("expected 429 Too Many Requests, got resp=%v err=%v", resp, err)
	}
}

func TestConcurrentWebSocketHandshakesEnforceMaxTotalPeers(t *testing.T) {
	// 1. Stress test atomic reservation + registration across 10,400 concurrent attempts.
	t.Run("concurrent reservation and registration at MaxTotalPeers", func(t *testing.T) {
		adapter := &Adapter{}
		const totalAttempts = domain.MaxTotalPeers + 400
		start := make(chan struct{})
		var (
			wg        sync.WaitGroup
			admitted  atomic.Int32
			throttled atomic.Int32
		)
		for i := range totalAttempts {
			wg.Add(1)
			userID := fmt.Sprintf("user-%d", i/domain.MaxPeersPerUser)
			go func(uid string) {
				defer wg.Done()
				<-start
				if err := adapter.tryReservePeer(uid); err != nil {
					throttled.Add(1)
					return
				}
				adapter.registerPeer(uid, "tok", nil)
				admitted.Add(1)
			}(userID)
		}
		close(start)
		wg.Wait()

		if got := int(admitted.Load()); got != domain.MaxTotalPeers {
			t.Fatalf("admitted = %d, want %d", got, domain.MaxTotalPeers)
		}
		if got := int(throttled.Load()); got != totalAttempts-domain.MaxTotalPeers {
			t.Fatalf("throttled = %d, want %d", got, totalAttempts-domain.MaxTotalPeers)
		}
		if got := len(adapter.ActiveClients()); got != domain.MaxTotalPeers {
			t.Fatalf("ActiveClients = %d, want %d", got, domain.MaxTotalPeers)
		}
	})

	// 2. End-to-end concurrent WebSocket handshakes competing for the last 4 global slots.
	t.Run("concurrent HTTP WebSocket upgrades at MaxTotalPeers boundary", func(t *testing.T) {
		const remainingSlots = 4
		const contenders = 16
		const origin = "https://teamie.example"

		sessions := make(map[string]domain.User, contenders)
		for i := range contenders {
			tok := fmt.Sprintf("tok-%060d", i)
			sessions[tok] = domain.User{
				ID:          fmt.Sprintf("contender-%d", i),
				DisplayName: fmt.Sprintf("Contender %d", i),
				Kind:        "human",
			}
		}

		var arrived atomic.Int32
		release := make(chan struct{})
		var releaseOnce sync.Once

		validator := &stubValidator{
			sessions: sessions,
			onValid: func(string) {
				if int(arrived.Add(1)) >= contenders {
					releaseOnce.Do(func() { close(release) })
				}
				select {
				case <-release:
				case <-time.After(2 * time.Second):
				}
			},
		}

		var policy domain.TypingTracker
		streamApp := &app.StreamApp{
			Identity: validator,
			Chat:     &stubAccess{},
			Policy:   &policy,
		}
		adapter := &Adapter{
			App:    streamApp,
			Origin: origin,
		}
		streamApp.Clients = adapter.ActiveClients

		// Pre-populate MaxTotalPeers - remainingSlots active peers across distinct users.
		prefill := domain.MaxTotalPeers - remainingSlots
		for i := range prefill {
			uid := fmt.Sprintf("existing-%d", i/domain.MaxPeersPerUser)
			adapter.registerPeer(uid, "existing-token", nil)
		}

		server := httptest.NewServer(adapter.Routes())
		defer server.Close()
		defer adapter.CloseAll()

		wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/api/v1/ws"

		var (
			wg       sync.WaitGroup
			mu       sync.Mutex
			conns    []*websocket.Conn
			rejected int
		)
		for i := range contenders {
			wg.Add(1)
			tok := fmt.Sprintf("tok-%060d", i)
			go func(token string) {
				defer wg.Done()
				headers := http.Header{
					"Origin": {origin},
					"Cookie": {httpx.CookieName + "=" + token},
				}
				c, resp, err := websocket.DefaultDialer.Dial(wsURL, headers)
				if err == nil {
					mu.Lock()
					conns = append(conns, c)
					mu.Unlock()
					return
				}
				if resp != nil {
					defer resp.Body.Close()
					if resp.StatusCode == http.StatusTooManyRequests {
						mu.Lock()
						rejected++
						mu.Unlock()
						return
					}
					t.Errorf("unexpected status = %d, err = %v", resp.StatusCode, err)
					return
				}
				t.Errorf("dial failed without HTTP response: %v", err)
			}(tok)
		}
		wg.Wait()
		defer func() {
			for _, c := range conns {
				_ = c.Close()
			}
		}()

		if len(conns) != remainingSlots {
			t.Fatalf("upgraded connections = %d, want %d", len(conns), remainingSlots)
		}
		if rejected != contenders-remainingSlots {
			t.Fatalf("rejected 429 connections = %d, want %d", rejected, contenders-remainingSlots)
		}
		if got := len(adapter.ActiveClients()); got != domain.MaxTotalPeers {
			t.Fatalf("total ActiveClients = %d, want %d", got, domain.MaxTotalPeers)
		}
	})
}

func TestFailedAndAbortedUpgradesReleaseReservedCapacity(t *testing.T) {
	const origin = "https://teamie.example"
	token := strings.Repeat("a", 64)

	validator := &stubValidator{
		sessions: map[string]domain.User{
			token: {ID: "alice", DisplayName: "Alice", Kind: "human"},
		},
	}
	var policy domain.TypingTracker
	streamApp := &app.StreamApp{
		Identity: validator,
		Chat:     &stubAccess{members: []string{"alice"}},
		Policy:   &policy,
	}
	adapter := &Adapter{
		App:    streamApp,
		Origin: origin,
	}
	streamApp.Clients = adapter.ActiveClients

	server := httptest.NewServer(adapter.Routes())
	defer server.Close()
	defer adapter.CloseAll()

	// Issue non-WebSocket GET requests that pass origin, auth, and capacity reservation
	// but fail in upgrader.Upgrade.
	for range domain.MaxPeersPerUser * 2 {
		req, err := http.NewRequest(http.MethodGet, server.URL+"/api/v1/ws", nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Origin", origin)
		req.Header.Set("Cookie", httpx.CookieName+"="+token)
		resp, err := server.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode == http.StatusSwitchingProtocols {
			t.Fatal("plain HTTP GET unexpectedly upgraded to WebSocket")
		}
	}

	adapter.mu.Lock()
	reservedTotal := adapter.reservedTotal
	reservedUsers := len(adapter.reservedByUser)
	adapter.mu.Unlock()
	if reservedTotal != 0 || reservedUsers != 0 {
		t.Fatalf("leaked reservation after failed upgrades: reservedTotal=%d reservedUsers=%d", reservedTotal, reservedUsers)
	}

	// All MaxPeersPerUser valid WebSocket connections for Alice must now succeed.
	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/api/v1/ws"
	headers := http.Header{
		"Origin": {origin},
		"Cookie": {httpx.CookieName + "=" + token},
	}
	conns := make([]*websocket.Conn, 0, domain.MaxPeersPerUser)
	defer func() {
		for _, c := range conns {
			_ = c.Close()
		}
	}()
	for i := range domain.MaxPeersPerUser {
		c, _, err := websocket.DefaultDialer.Dial(wsURL, headers)
		if err != nil {
			t.Fatalf("connection %d after failed upgrades failed: %v", i, err)
		}
		readInitialWSFrames(t, c)
		conns = append(conns, c)
	}
}

func TestTypingFrameStrictValidationAndCoalescing(t *testing.T) {
	const origin = "https://teamie.example"
	aliceToken := strings.Repeat("a", 64)
	bobToken := strings.Repeat("b", 64)

	validator := &stubValidator{
		sessions: map[string]domain.User{
			aliceToken: {ID: "alice", DisplayName: "Alice", Kind: "human"},
			bobToken:   {ID: "bob", DisplayName: "Bob", Kind: "human"},
		},
	}
	access := &stubAccess{members: []string{"alice", "bob"}}
	var policy domain.TypingTracker
	streamApp := &app.StreamApp{
		Identity: validator,
		Chat:     access,
		Policy:   &policy,
	}
	adapter := &Adapter{
		App:    streamApp,
		Origin: origin,
	}
	streamApp.Clients = adapter.ActiveClients

	server := httptest.NewServer(adapter.Routes())
	defer server.Close()
	defer adapter.CloseAll()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/api/v1/ws"
	connect := func(token string) *websocket.Conn {
		t.Helper()
		headers := http.Header{
			"Origin": {origin},
			"Cookie": {httpx.CookieName + "=" + token},
		}
		c, _, err := websocket.DefaultDialer.Dial(wsURL, headers)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = c.Close() })
		readInitialWSFrames(t, c)
		return c
	}

	alice := connect(aliceToken)
	bob := connect(bobToken)

	invalidFrames := []string{
		`{"type":"typing","conversationId":"conv-1"}`,
		`{"type":"typing","conversationId":"conv-1","isTyping":null}`,
		`{"type":"typing","conversationId":"conv-1","isTyping":"true"}`,
		`{"type":"typing","conversationId":"conv-1","isTyping":true,"userId":"forged"}`,
		`{"type":"typing","conversationId":"conv-1","isTyping":true,"extra":123}`,
		`{"type":"typing","isTyping":true}`,
		`{"type":"typing","conversationId":"","isTyping":true}`,
		`{"type":"typing","conversationId":"   ","isTyping":true}`,
		`{"type":"typing","conversationId":"conv\n1","isTyping":true}`,
		fmt.Sprintf(`{"type":"typing","conversationId":"%s","isTyping":true}`, strings.Repeat("c", 161)),
	}
	for _, raw := range invalidFrames {
		if err := alice.WriteMessage(websocket.TextMessage, []byte(raw)); err != nil {
			t.Fatalf("write invalid frame %s: %v", raw, err)
		}
	}

	// Send a valid typing start frame right after all invalid frames.
	if err := alice.WriteMessage(websocket.TextMessage, []byte(`{"type":"typing","conversationId":"conv-1","isTyping":true}`)); err != nil {
		t.Fatal(err)
	}

	readTyping := func(wantActive bool) {
		t.Helper()
		_ = bob.SetReadDeadline(time.Now().Add(time.Second))
		var ev map[string]any
		if err := bob.ReadJSON(&ev); err != nil {
			t.Fatalf("read typing frame: %v", err)
		}
		if ev["type"] != "typing" || ev["conversationId"] != "conv-1" || ev["userId"] != "alice" || ev["displayName"] != "Alice" || ev["isTyping"] != wantActive {
			t.Fatalf("unexpected typing frame: %#v", ev)
		}
	}
	readTyping(true)

	// A single in-process PublishTyping + Deliver cycle invokes CheckAccess 3 times
	// (sender check in PublishTyping, sender recheck in Deliver, and recipient check for Bob).
	// None of the 10 preceding invalid frames on the same socket may invoke PublishTyping.
	if got := access.accessCalls.Load(); got != 3 {
		t.Fatalf("CheckAccess calls = %d, want 3 (invalid frames must not call PublishTyping)", got)
	}

	// While Alice's typing state is active, send a frame missing isTyping and a stop frame with an unknown field.
	// Neither may clear Alice's typing state or reset the positive-hint throttle.
	if err := alice.WriteMessage(websocket.TextMessage, []byte(`{"type":"typing","conversationId":"conv-1"}`)); err != nil {
		t.Fatal(err)
	}
	if err := alice.WriteMessage(websocket.TextMessage, []byte(`{"type":"typing","conversationId":"conv-1","isTyping":false,"extra":"ignored"}`)); err != nil {
		t.Fatal(err)
	}
	// A repeated positive hint within the 300ms throttle window must still be coalesced
	// because no valid stop frame was accepted.
	if err := alice.WriteMessage(websocket.TextMessage, []byte(`{"type":"typing","conversationId":"conv-1","isTyping":true}`)); err != nil {
		t.Fatal(err)
	}

	// Now send a valid typing stop frame; Bob must receive isTyping=false immediately.
	if err := alice.WriteMessage(websocket.TextMessage, []byte(`{"type":"typing","conversationId":"conv-1","isTyping":false}`)); err != nil {
		t.Fatal(err)
	}
	readTyping(false)

	// After a valid stop frame, an immediate valid start frame must be delivered without throttling.
	var validStart map[string]any
	_ = json.Unmarshal([]byte(`{"type":"typing","conversationId":"conv-1","isTyping":true}`), &validStart)
	if err := alice.WriteJSON(validStart); err != nil {
		t.Fatal(err)
	}
	readTyping(true)
}
