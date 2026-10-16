package realtime

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"teamie/backend/internal/api"
	"teamie/backend/internal/config"
	"teamie/backend/kitex_gen/im"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func (i *testIdentity) ValidatePrincipal(ctx context.Context, token, key string) (api.Session, error) {
	if key != "fixture-service-key" {
		return api.Session{}, api.Err(401, "unauthenticated", "Invalid service credential")
	}
	s, err := i.Validate(ctx, token)
	s.ServicePrincipal = true
	return s, err
}

type typingAccess struct {
	mu      sync.Mutex
	allowed map[string]bool
	enabled bool
}

func (a *typingAccess) CheckAccess(_ context.Context, user, conversation string) (*im.AccessResponse, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return &im.AccessResponse{Allowed: conversation == "conversation" && a.allowed[user], MemberIds: []string{"alice", "bob", "agent"}, TypingEnabled: a.enabled}, nil
}
func (*typingAccess) Ping(context.Context) error { return nil }

func TestHTTPTypingUsesOrdinaryAuthenticationAndConversationACL(t *testing.T) {
	aliceToken, bobToken, agentToken := strings.Repeat("a", 64), strings.Repeat("b", 64), strings.Repeat("c", 64)
	identity := &testIdentity{sessions: map[string]api.User{
		aliceToken: {ID: "alice", DisplayName: "Alice", Kind: "human"},
		bobToken:   {ID: "bob", DisplayName: "Bob", Kind: "human"},
		agentToken: {ID: "agent", DisplayName: "Team helper", Kind: "agent"},
	}}
	access := &typingAccess{allowed: map[string]bool{"alice": true, "bob": true, "agent": true}, enabled: true}
	h := &Hub{Identity: identity, Chat: access, Origin: "https://teamie.example"}
	server := httptest.NewServer(h.Routes())
	defer server.Close()
	ws, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/api/v1/ws", http.Header{"Origin": {h.Origin}, "Cookie": {api.CookieName + "=" + bobToken}})
	if err != nil {
		t.Fatal(err)
	}
	defer ws.Close()
	for range 2 {
		if _, _, err = ws.ReadMessage(); err != nil {
			t.Fatal(err)
		}
	}
	request := func(path, body string, headers http.Header, status int) {
		t.Helper()
		r, err := http.NewRequest(http.MethodPost, server.URL+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		r.Header = headers
		res, err := server.Client().Do(r)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		if res.StatusCode != status {
			t.Fatalf("typing status = %d, want %d", res.StatusCode, status)
		}
	}
	path := "/api/v1/conversations/conversation/typing"
	machine := http.Header{"Authorization": {"Bearer " + agentToken}, "X-Teamie-Service-Key": {"fixture-service-key"}}
	for _, tc := range []struct {
		name, body string
		headers    http.Header
		status     int
	}{
		{"anonymous", `{"isTyping":true}`, nil, 401},
		{"invalid key", `{"isTyping":true}`, http.Header{"Authorization": {"Bearer " + agentToken}}, 401},
		{"browser impersonation", `{"isTyping":true}`, http.Header{"Authorization": {"Bearer " + agentToken}, "X-Teamie-Service-Key": {"fixture-service-key"}, "Origin": {h.Origin}}, 403},
		{"fetch impersonation", `{"isTyping":true}`, http.Header{"Authorization": {"Bearer " + agentToken}, "X-Teamie-Service-Key": {"fixture-service-key"}, "Sec-Fetch-Site": {"same-origin"}}, 403},
		{"missing csrf", `{"isTyping":true}`, http.Header{"Cookie": {api.CookieName + "=" + aliceToken}, "Origin": {h.Origin}}, 403},
		{"foreign origin", `{"isTyping":true}`, http.Header{"Cookie": {api.CookieName + "=" + aliceToken}, "Origin": {"https://evil.example"}, "X-CSRF-Token": {"csrf"}}, 403},
		{"forged sender", `{"isTyping":true,"userId":"alice"}`, machine, 400},
		{"runtime metadata", `{"isTyping":true,"runId":"private"}`, machine, 400},
		{"missing state", `{}`, machine, 400},
		{"invalid state", `{"isTyping":"true"}`, machine, 400},
	} {
		t.Run(tc.name, func(t *testing.T) { request(path, tc.body, tc.headers, tc.status) })
	}
	request("/api/v1/conversations/another/typing", `{"isTyping":true}`, machine, 403)
	read := func(id, name string, active bool) {
		t.Helper()
		_ = ws.SetReadDeadline(time.Now().Add(time.Second))
		var event map[string]any
		if err := ws.ReadJSON(&event); err != nil {
			t.Fatal(err)
		}
		if len(event) != 5 || event["type"] != "typing" || event["conversationId"] != "conversation" || event["userId"] != id || event["displayName"] != name || event["isTyping"] != active {
			t.Fatalf("typing must contain only ordinary IM participant fields: %#v", event)
		}
	}
	request(path, `{"isTyping":true}`, machine, 204)
	read("agent", "Team helper", true)
	// HTTP uses the same positive-hint cadence as WS, but an immediate stop
	// remains observable even if a repeated positive hint was coalesced.
	request(path, `{"isTyping":true}`, machine, 204)
	request(path, `{"isTyping":false}`, machine, 204)
	read("agent", "Team helper", false)
	request(path, `{"isTyping":true}`, machine, 204)
	read("agent", "Team helper", true)
	request(path, `{"isTyping":false}`, machine, 204)
	read("agent", "Team helper", false)
	human := http.Header{"Cookie": {api.CookieName + "=" + aliceToken}, "Origin": {h.Origin}, "X-CSRF-Token": {"csrf"}}
	request(path, `{"isTyping":true}`, human, 204)
	read("alice", "Alice", true)
	access.mu.Lock()
	access.allowed["agent"] = false
	access.mu.Unlock()
	request(path, `{"isTyping":true}`, machine, 403)
	access.mu.Lock()
	access.allowed["agent"] = true
	access.enabled = false
	access.mu.Unlock()
	request(path, `{"isTyping":true}`, machine, 403)
	access.mu.Lock()
	access.enabled = true
	access.mu.Unlock()
	flags := filepath.Join(t.TempDir(), "flags.json")
	if err = os.WriteFile(flags, []byte(`{"revision":"disabled","flags":{"im.typing.publish":false}}`), 0600); err != nil {
		t.Fatal(err)
	}
	h.Flags = config.Flags{Path: flags}
	request(path, `{"isTyping":true}`, machine, 403)
	identity.mu.Lock()
	delete(identity.sessions, agentToken)
	identity.mu.Unlock()
	request(path, `{"isTyping":true}`, machine, 401)
}

func TestQueuedTypingRechecksSenderAndRecipientMembership(t *testing.T) {
	access := &typingAccess{allowed: map[string]bool{"agent": true, "bob": true}, enabled: true}
	identity := &testIdentity{sessions: map[string]api.User{"session": {ID: "bob", DisplayName: "Bob", Kind: "human"}}}
	p := &peer{user: "bob", token: "session", queue: make(chan Hint, 4), done: make(chan struct{})}
	h := &Hub{Identity: identity, Chat: access, peers: map[*peer]struct{}{p: {}}}
	hint := Hint{Type: "typing", UserIDs: []string{"bob", "agent"}, UserID: "agent", DisplayName: "Team helper", ConversationID: "conversation", IsTyping: true}
	h.deliver(context.Background(), hint)
	select {
	case <-p.queue:
	default:
		t.Fatal("current member did not receive typing")
	}
	for _, user := range []string{"agent", "bob"} {
		access.mu.Lock()
		access.allowed[user] = false
		access.mu.Unlock()
		h.deliver(context.Background(), hint)
		select {
		case <-p.queue:
			t.Fatalf("queued typing survived revoked %s membership", user)
		default:
		}
		access.mu.Lock()
		access.allowed[user] = true
		access.mu.Unlock()
	}
}

type delayedTypingAccess struct {
	*typingAccess
	entered chan struct{}
	release chan struct{}
	delayed atomic.Bool
}

func (a *delayedTypingAccess) CheckAccess(ctx context.Context, user, conversation string) (*im.AccessResponse, error) {
	if a.delayed.CompareAndSwap(false, true) {
		close(a.entered)
		<-a.release
	}
	return a.typingAccess.CheckAccess(ctx, user, conversation)
}

func TestConcurrentStartCannotOvertakeFinalClear(t *testing.T) {
	access := &delayedTypingAccess{typingAccess: &typingAccess{allowed: map[string]bool{"alice": true, "bob": true}, enabled: true}, entered: make(chan struct{}), release: make(chan struct{})}
	identity := &testIdentity{sessions: map[string]api.User{"session": {ID: "bob", Kind: "human"}}}
	p := &peer{user: "bob", token: "session", queue: make(chan Hint, 4), done: make(chan struct{})}
	h := &Hub{Identity: identity, Chat: access, peers: map[*peer]struct{}{p: {}}}
	user := api.User{ID: "alice", DisplayName: "Alice", Kind: "human"}
	started := make(chan error, 1)
	go func() { started <- h.publishTyping(context.Background(), user, "conversation", true) }()
	<-access.entered
	stopped := make(chan error, 1)
	go func() { stopped <- h.publishTyping(context.Background(), user, "conversation", false) }()
	select {
	case <-p.queue:
		close(access.release)
		t.Fatal("clear overtook an in-flight earlier start")
	case <-time.After(30 * time.Millisecond):
	}
	close(access.release)
	if err := <-started; err != nil {
		t.Fatal(err)
	}
	if err := <-stopped; err != nil {
		t.Fatal(err)
	}
	for _, active := range []bool{true, false} {
		select {
		case event := <-p.queue:
			if event.IsTyping != active {
				t.Fatalf("in-flight start overtook final clear: %#v", event)
			}
		default:
			t.Fatal("typing lifecycle lost a signal")
		}
	}
}

func TestCanceledQueuedHeartbeatCannotPublishAfterClear(t *testing.T) {
	access := &delayedTypingAccess{typingAccess: &typingAccess{allowed: map[string]bool{"alice": true, "bob": true}, enabled: true}, entered: make(chan struct{}), release: make(chan struct{})}
	identity := &testIdentity{sessions: map[string]api.User{"session": {ID: "bob", Kind: "human"}}}
	p := &peer{user: "bob", token: "session", queue: make(chan Hint, 4), done: make(chan struct{})}
	h := &Hub{Identity: identity, Chat: access, peers: map[*peer]struct{}{p: {}}}
	user := api.User{ID: "alice", Kind: "human"}
	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan error, 1)
	go func() { started <- h.publishTyping(ctx, user, "conversation", true) }()
	<-access.entered
	cancel()
	stopped := make(chan error, 1)
	go func() { stopped <- h.publishTyping(context.Background(), user, "conversation", false) }()
	close(access.release)
	if err := <-started; err == nil {
		t.Fatal("canceled heartbeat was accepted")
	}
	if err := <-stopped; err != nil {
		t.Fatal(err)
	}
	select {
	case event := <-p.queue:
		if event.IsTyping {
			t.Fatal("late canceled heartbeat resurrected typing")
		}
	default:
		t.Fatal("clear was not delivered")
	}
	select {
	case event := <-p.queue:
		t.Fatalf("canceled heartbeat followed the clear: %#v", event)
	default:
	}
}
