package realtime

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"teamie/contracts/gen/im/kitex_gen/im"
	"teamie/pkg/httpx"
)

type workspacePresenceAccess struct {
	mu              sync.Mutex
	convAllowed     map[string]bool
	wsAllowed       map[string]bool
	wsHumanMembers  []string
	sharedMembers   []string
	presenceEnabled bool
}

func (a *workspacePresenceAccess) CheckAccess(_ context.Context, user, conversation string) (*im.AccessResponse, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return &im.AccessResponse{
		Allowed:       conversation == "conv-1" && a.convAllowed[user],
		MemberIds:     []string{"alice", "bob"},
		TypingEnabled: true,
	}, nil
}

func (a *workspacePresenceAccess) CheckWorkspaceAccess(_ context.Context, user, workspace string) (*im.WorkspaceAccessResponse, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if workspace != "ws-1" || !a.wsAllowed[user] {
		return &im.WorkspaceAccessResponse{Allowed: false}, nil
	}
	return &im.WorkspaceAccessResponse{
		Allowed:         true,
		MemberIds:       append([]string(nil), a.wsHumanMembers...),
		PresenceEnabled: a.presenceEnabled,
	}, nil
}

func (a *workspacePresenceAccess) GetSharedWorkspaceMembers(_ context.Context, user string) (*im.SharedWorkspaceMembersResponse, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.wsAllowed[user] {
		return &im.SharedWorkspaceMembersResponse{PresenceEnabled: a.presenceEnabled}, nil
	}
	return &im.SharedWorkspaceMembersResponse{
		MemberIds:       append([]string(nil), a.sharedMembers...),
		PresenceEnabled: a.presenceEnabled,
	}, nil
}

func (*workspacePresenceAccess) Ping(context.Context) error { return nil }

func TestPresenceLifecycleMultiTabAndWorkspaceQuery(t *testing.T) {
	aliceToken := strings.Repeat("a", 64)
	bobToken := strings.Repeat("b", 64)
	carolToken := strings.Repeat("c", 64)
	agentToken := strings.Repeat("d", 64)

	identity := &testIdentity{sessions: map[string]httpx.User{
		aliceToken: {ID: "alice", DisplayName: "Alice", Kind: "human"},
		bobToken:   {ID: "bob", DisplayName: "Bob", Kind: "human"},
		carolToken: {ID: "carol", DisplayName: "Carol", Kind: "human"},
		agentToken: {ID: "agent-1", DisplayName: "Helper", Kind: "agent"},
	}}
	access := &workspacePresenceAccess{
		convAllowed:     map[string]bool{"alice": true, "bob": true},
		wsAllowed:       map[string]bool{"alice": true, "bob": true, "agent-1": true},
		wsHumanMembers:  []string{"alice", "bob"},
		sharedMembers:   []string{"alice", "bob", "agent-1"},
		presenceEnabled: true,
	}

	now := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	var nowMu sync.Mutex
	h := &Hub{
		Identity: identity,
		Chat:     access,
		Origin:   "https://teamie.example",
	}
	h.presence.Now = func() time.Time {
		nowMu.Lock()
		defer nowMu.Unlock()
		return now
	}
	h.presence.IdleTimeout = 5 * time.Minute
	h.presence.OfflineGrace = 80 * time.Millisecond

	server := httptest.NewServer(h.Routes())
	defer server.Close()
	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/api/v1/ws"

	connect := func(token string) *websocket.Conn {
		t.Helper()
		headers := http.Header{"Origin": {h.Origin}, "Cookie": {httpx.CookieName + "=" + token}}
		c, _, err := websocket.DefaultDialer.Dial(wsURL, headers)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = c.Close() })
		for _, kind := range []string{"ready", "sync.required"} {
			var v map[string]any
			if err = c.ReadJSON(&v); err != nil || v["type"] != kind {
				t.Fatalf("handshake event = %#v, err = %v", v, err)
			}
		}
		return c
	}

	readPresence := func(c *websocket.Conn, wantUser, wantState string) {
		t.Helper()
		_ = c.SetReadDeadline(time.Now().Add(time.Second))
		var ev map[string]any
		if err := c.ReadJSON(&ev); err != nil {
			t.Fatalf("read presence error: %v", err)
		}
		if ev["type"] != "presence" || ev["userId"] != wantUser || ev["state"] != wantState {
			t.Fatalf("presence frame = %#v, want userId=%s state=%s", ev, wantUser, wantState)
		}
	}

	bobWS := connect(bobToken)
	aliceTab1 := connect(aliceToken)
	readPresence(bobWS, "alice", "online")

	// Agent connection never publishes human presence.
	_ = connect(agentToken)

	// Second tab for Alice does not re-emit "online" because Alice is already online.
	aliceTab2 := connect(aliceToken)

	// Tab 1 goes idle while Tab 2 remains active -> Alice stays online.
	if err := aliceTab1.WriteJSON(map[string]any{"type": "presence", "state": "idle"}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(25 * time.Millisecond)
	if snap := h.presence.Snapshot([]string{"alice"}); len(snap) != 1 || snap[0].State != "online" {
		t.Fatalf("Alice should remain online while Tab 2 is active, got %#v", snap)
	}

	// Unknown fields in presence frame must be rejected without changing state.
	if err := aliceTab2.WriteJSON(map[string]any{"type": "presence", "state": "idle", "userId": "forged"}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(25 * time.Millisecond)
	if snap := h.presence.Snapshot([]string{"alice"}); len(snap) != 1 || snap[0].State != "online" {
		t.Fatalf("presence frame with unknown field changed state: %#v", snap)
	}

	// Tab 2 goes idle -> all Alice tabs are idle -> transitions to "away".
	if err := aliceTab2.WriteJSON(map[string]any{"type": "presence", "state": "idle"}); err != nil {
		t.Fatal(err)
	}
	readPresence(bobWS, "alice", "away")
	readPresence(aliceTab1, "alice", "away")
	readPresence(aliceTab2, "alice", "away")

	// GET /api/v1/workspaces/ws-1/presence returns Alice (away) and Bob (online), omitting Agent.
	getPresence := func(token string) (*http.Response, []map[string]any) {
		t.Helper()
		req, _ := http.NewRequest(http.MethodGet, server.URL+"/api/v1/workspaces/ws-1/presence", nil)
		req.Header.Set("Cookie", httpx.CookieName+"="+token)
		res, err := server.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		var body struct {
			Items []map[string]any `json:"items"`
		}
		_ = json.NewDecoder(res.Body).Decode(&body)
		return res, body.Items
	}

	res, items := getPresence(bobToken)
	if res.StatusCode != 200 || len(items) != 2 || items[0]["userId"] != "alice" || items[0]["state"] != "away" || items[1]["userId"] != "bob" || items[1]["state"] != "online" {
		t.Fatalf("workspace presence = %d %#v", res.StatusCode, items)
	}

	// Non-member Carol gets 404 not_found.
	resCarol, _ := getPresence(carolToken)
	if resCarol.StatusCode != 404 {
		t.Fatalf("non-member status = %d, want 404", resCarol.StatusCode)
	}

	// Disconnect both of Alice's tabs -> after grace period transitions to "offline".
	_ = aliceTab1.Close()
	_ = aliceTab2.Close()
	readPresence(bobWS, "alice", "offline")

	// Disabled feature flag returns 200 {items: []}.
	flagsFile := filepath.Join(t.TempDir(), "flags.json")
	if err := os.WriteFile(flagsFile, []byte(`{"revision":"disabled","flags":{"im.presence.publish":false}}`), 0600); err != nil {
		t.Fatal(err)
	}
	h.Flags = httpx.Flags{Path: flagsFile}
	resDisabled, itemsDisabled := getPresence(bobToken)
	if resDisabled.StatusCode != 200 || len(itemsDisabled) != 0 {
		t.Fatalf("disabled presence = %d %#v, want 200 []", resDisabled.StatusCode, itemsDisabled)
	}
}

func TestReceiptsChangedCoalescingAndAccessCheck(t *testing.T) {
	access := &workspacePresenceAccess{
		convAllowed: map[string]bool{"alice": true, "bob": false},
	}
	pAlice := &peer{user: "alice", token: "tok-a", queue: make(chan Hint, 8), done: make(chan struct{})}
	pBob := &peer{user: "bob", token: "tok-b", queue: make(chan Hint, 8), done: make(chan struct{})}
	h := &Hub{
		Chat:  access,
		peers: map[*peer]struct{}{pAlice: {}, pBob: {}},
	}
	h.policy.ReceiptInterval = 80 * time.Millisecond

	hint := Hint{
		Type:           "receipts.changed",
		UserIDs:        []string{"alice", "bob"},
		ConversationID: "conv-1",
	}

	// First hint is delivered to authorized member Alice, not unauthorized Bob.
	h.deliver(context.Background(), hint)
	select {
	case ev := <-pAlice.queue:
		if ev.Type != "receipts.changed" || ev.ConversationID != "conv-1" {
			t.Fatalf("alice received %#v", ev)
		}
	default:
		t.Fatal("alice did not receive receipts.changed")
	}
	select {
	case ev := <-pBob.queue:
		t.Fatalf("unauthorized bob received %#v", ev)
	default:
	}

	// Second hint within coalescing interval is suppressed.
	h.deliver(context.Background(), hint)
	select {
	case ev := <-pAlice.queue:
		t.Fatalf("coalesced receipts.changed was not suppressed: %#v", ev)
	default:
	}

	// After coalescing interval expires, hint is delivered again.
	time.Sleep(100 * time.Millisecond)
	h.deliver(context.Background(), hint)
	select {
	case ev := <-pAlice.queue:
		if ev.Type != "receipts.changed" {
			t.Fatalf("alice received %#v", ev)
		}
	default:
		t.Fatal("alice did not receive receipts.changed after interval")
	}
}
