package realtime

import (
	"context"
	"github.com/gorilla/websocket"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"teamie/backend/internal/api"
	"teamie/backend/kitex_gen/im"
	"testing"
	"time"
)

type testIdentity struct {
	mu       sync.Mutex
	sessions map[string]api.User
}

func (i *testIdentity) Validate(ctx context.Context, token string) (api.Session, error) {
	i.mu.Lock()
	defer i.mu.Unlock()
	u, ok := i.sessions[token]
	if !ok {
		return api.Session{}, api.Err(401, "unauthenticated", "revoked")
	}
	return api.Session{User: u, CSRFToken: "csrf"}, nil
}
func (i *testIdentity) Users(context.Context, []string) ([]api.User, error) { return nil, nil }
func (i *testIdentity) Ping(context.Context) error                          { return nil }

type testAccess struct {
	mu      sync.Mutex
	allowed map[string]bool
}

func (a *testAccess) CheckAccess(ctx context.Context, user, conversation string) (*im.AccessResponse, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return &im.AccessResponse{Allowed: conversation == "conversation" && a.allowed[user], MemberIds: []string{"alice", "bob"}, TypingEnabled: true}, nil
}
func (a *testAccess) Ping(context.Context) error { return nil }
func TestWebSocketOriginTypingAuthorizationAndRevocation(t *testing.T) {
	aliceToken, bobToken := strings.Repeat("a", 64), strings.Repeat("b", 64)
	identity := &testIdentity{sessions: map[string]api.User{aliceToken: {ID: "alice", DisplayName: "Alice", Kind: "human"}, bobToken: {ID: "bob", DisplayName: "Bob", Kind: "human"}}}
	access := &testAccess{allowed: map[string]bool{"alice": true, "bob": true}}
	h := &Hub{Identity: identity, Chat: access, Origin: "https://teamie.example", RevalidationInterval: 100 * time.Millisecond}
	server := httptest.NewServer(h.Routes())
	defer server.Close()
	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/api/v1/ws"
	dial := func(origin, token string) (*websocket.Conn, *http.Response, error) {
		headers := http.Header{"Origin": {origin}, "Cookie": {api.CookieName + "=" + token}}
		return websocket.DefaultDialer.Dial(wsURL, headers)
	}
	if c, r, e := dial("https://evil.example", aliceToken); e == nil {
		c.Close()
		t.Fatal("foreign origin accepted")
	} else if r == nil || r.StatusCode != 403 {
		t.Fatal("unexpected origin failure", e)
	}
	if c, r, e := dial(h.Origin, ""); e == nil {
		c.Close()
		t.Fatal("anonymous WS accepted")
	} else if r == nil || r.StatusCode != 401 {
		t.Fatal("unexpected session failure", e)
	}
	connect := func(token string) *websocket.Conn {
		c, _, e := dial(h.Origin, token)
		if e != nil {
			t.Fatal(e)
		}
		t.Cleanup(func() { _ = c.Close() })
		for _, kind := range []string{"ready", "sync.required"} {
			var v map[string]any
			if e = c.ReadJSON(&v); e != nil || v["type"] != kind {
				t.Fatal("handshake event", e, v)
			}
		}
		return c
	}
	alice, bob := connect(aliceToken), connect(bobToken)
	if e := alice.WriteJSON(map[string]any{"type": "typing", "conversationId": "conversation", "isTyping": true, "userId": "forged"}); e != nil {
		t.Fatal(e)
	}
	_ = bob.SetReadDeadline(time.Now().Add(time.Second))
	var typing Hint
	if e := bob.ReadJSON(&typing); e != nil || typing.Type != "typing" || typing.UserID != "alice" || typing.DisplayName != "Alice" || len(typing.UserIDs) != 0 {
		t.Fatal("typing did not derive authorized sender", e, typing)
	}
	if e := alice.WriteJSON(map[string]any{"type": "typing", "conversationId": "conversation", "isTyping": false}); e != nil {
		t.Fatal(e)
	}
	if e := bob.ReadJSON(&typing); e != nil || typing.IsTyping {
		t.Fatal("immediate typing stop was throttled", e, typing)
	}
	if e := alice.WriteJSON(map[string]any{"type": "typing", "conversationId": "conversation", "isTyping": true}); e != nil {
		t.Fatal(e)
	}
	if e := bob.ReadJSON(&typing); e != nil || !typing.IsTyping {
		t.Fatal("new typing after stop was throttled", e, typing)
	}
	// Existing sockets stop accepting typing once their current session is revoked.
	identity.mu.Lock()
	delete(identity.sessions, aliceToken)
	identity.mu.Unlock()
	_ = alice.SetReadDeadline(time.Now().Add(time.Second))
	if _, _, e := alice.ReadMessage(); e == nil {
		t.Fatal("revoked socket remained usable")
	}
	// Removing Bob's membership prevents live protected typing delivery.
	access.mu.Lock()
	access.allowed["bob"] = false
	access.mu.Unlock()
	identity.mu.Lock()
	identity.sessions[aliceToken] = api.User{ID: "alice", DisplayName: "Alice", Kind: "human"}
	identity.mu.Unlock()
	alice2 := connect(aliceToken)
	if e := alice2.WriteJSON(map[string]any{"type": "typing", "conversationId": "conversation", "isTyping": true}); e != nil {
		t.Fatal(e)
	}
	_ = bob.SetReadDeadline(time.Now().Add(250 * time.Millisecond))
	if e := bob.ReadJSON(&typing); e == nil {
		t.Fatal("removed member received typing payload")
	}
}
