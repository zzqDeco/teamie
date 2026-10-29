package eino

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
	"github.com/google/uuid"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"teamie/services/agent-runtime/internal/domain"
)

func TestMCPGroupSwitchUsesNewToolScope(t *testing.T) {
	s, lease, run := nativeModelRun(t)
	ctx := WithRun(context.Background(), run)
	ws, a, b, appID := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	var sessionGroup atomic.Value
	var calls atomic.Int32
	m := server.NewMCPServer("shared-app", "1", server.WithToolCapabilities(true))
	m.AddTool(mcp.NewTool("mutate"), func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		calls.Add(1)
		return mcp.NewToolResultText("changed " + req.Header.Get("Authorization")), nil
	})
	remote := httptest.NewServer(server.NewStreamableHTTPServer(m))
	defer remote.Close()
	app := imMarketplaceApp{ID: appID, Slug: "shared", MCPURL: remote.URL, Enabled: true}
	imHTTP := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/api/v1/bootstrap":
			_ = json.NewEncoder(w).Encode(Bootstrap{Conversations: []Conversation{{ID: a, WorkspaceID: ws, Type: "group"}, {ID: b, WorkspaceID: ws, Type: "group"}}})
		case strings.HasSuffix(r.URL.Path, "/messages"):
			_ = json.NewEncoder(w).Encode(map[string]any{"messages": []Message{}, "hasMore": false})
		case strings.HasSuffix(r.URL.Path, "/session"):
			group := strings.Split(r.URL.Path, "/")[4]
			sessionGroup.Store(group)
			_ = json.NewEncoder(w).Encode(imConversationAppSession{App: app, Token: group, ExpiresAt: time.Now().Add(time.Hour).Format(time.RFC3339)})
		default:
			_ = json.NewEncoder(w).Encode(map[string]any{"apps": []imMarketplaceApp{app}})
		}
	}))
	defer imHTTP.Close()
	presence := newConversationPresenceState(time.Minute, nil)
	presence.Enter(ws, a, "group")
	builder := &Tools{Store: s, Lease: lease, Presence: presence, IM: &IM{BaseURL: imHTTP.URL, token: "fixture", expiry: time.Now().Add(time.Hour)}}
	run.setPresenceEnabled(true)
	run.setActiveConversation(ws, a, "group")
	name := "mcp__shared__mutate"
	run.setMCPTools(map[string]*mcpToolWrapper{name: {tools: builder, spec: mcpPinnedToolSpec{AppID: appID, AppSlug: "shared", WorkspaceID: ws, ConversationID: a, MCPURL: remote.URL, ExposedName: name, RemoteName: "mutate", ToolInfo: &schema.ToolInfo{Name: name}}}})
	tools, err := builder.BuildPresenceTools()
	if err != nil {
		t.Fatal(err)
	}
	node, err := compose.NewToolNode(ctx, &compose.ToolsNodeConfig{Tools: tools, ExecuteSequentially: true, UnknownToolsHandler: builder.HandleUnknownMCPTool})
	if err != nil {
		t.Fatal(err)
	}
	_, err = node.Invoke(ctx, &schema.Message{Role: schema.Assistant, ToolCalls: []schema.ToolCall{
		{ID: "enter-b", Type: "function", Function: schema.FunctionCall{Name: "im_enter_conversation", Arguments: fmt.Sprintf(`{"conversationId":%q}`, b)}},
		{ID: "mutate-b", Type: "function", Function: schema.FunctionCall{Name: name, Arguments: `{}`}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	_, active, _ := run.activeConversation()
	if active != b || calls.Load() != 1 {
		t.Fatalf("unexpected setup: active=%s calls=%d", active, calls.Load())
	}
	if sessionGroup.Load() != b {
		t.Fatalf("entered B=%s but remote mutation authenticated for A=%v", b, sessionGroup.Load())
	}
}

type pausedPresenceCheckpointRepo struct {
	domain.RuntimeRepository
	claimed atomic.Bool
	blocked chan struct{}
	release chan struct{}
}

func (r *pausedPresenceCheckpointRepo) SetCheckpoint(ctx context.Context, l *Lease, id string, payload, metadata []byte) error {
	if strings.HasPrefix(id, "presence:") && r.claimed.CompareAndSwap(false, true) {
		close(r.blocked)
		<-r.release
	}
	return r.RuntimeRepository.SetCheckpoint(ctx, l, id, payload, metadata)
}

func TestMCPPresenceCheckpointDoesNotLoseIngestedUnread(t *testing.T) {
	s, lease, _ := nativeModelRun(t)
	ctx := context.Background()
	repo := &pausedPresenceCheckpointRepo{RuntimeRepository: s.DB, blocked: make(chan struct{}), release: make(chan struct{})}
	s.DB = repo
	p := newConversationPresenceState(time.Minute, nil)
	ws, conv := uuid.NewString(), uuid.NewString()
	first := make(chan error, 1)
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(repo.release) }) }
	defer release()
	go func() { first <- p.Persist(ctx, s, lease) }()
	<-repo.blocked
	p.RecordUnread(ws, Conversation{ID: conv, WorkspaceID: ws, Type: "group"}, &Message{ID: uuid.NewString(), Seq: 2}, lease.AgentID, time.Now())
	second := make(chan error, 1)
	go func() {
		if err := p.Persist(ctx, s, lease); err != nil {
			second <- err
			return
		}
		second <- s.Ingest(ctx, lease, "2", nil)
	}()
	select {
	case err := <-second:
		release()
		<-first
		t.Fatalf("newer checkpoint bypassed the unfinished older write: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	release()
	if err := <-first; err != nil {
		t.Fatal(err)
	}
	if err := <-second; err != nil {
		t.Fatal(err)
	}
	restored := newConversationPresenceState(time.Minute, nil)
	if err := restored.Restore(ctx, s, lease); err != nil {
		t.Fatal(err)
	}
	cursor, err := s.Cursor(ctx, lease)
	if err != nil {
		t.Fatal(err)
	}
	got := restored.EnrichConversation(Conversation{ID: conv}).UnreadCount
	if got != 1 {
		t.Fatalf("restored unread=%d after cursor advanced to %s; older snapshot overwrote the new state", got, cursor)
	}
}

func TestMCPRevokedRoomDoesNotPoisonOtherUnread(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	lease, err := s.Acquire(ctx, uuid.NewString(), "review")
	if err != nil {
		t.Fatal(err)
	}
	ws, a, b := uuid.NewString(), uuid.NewString(), uuid.NewString()
	p := newConversationPresenceState(time.Millisecond, nil)
	p.RecordUnread(ws, Conversation{ID: a, WorkspaceID: ws, Type: "group"}, &Message{ID: uuid.NewString(), Seq: 1}, lease.AgentID, time.Now().Add(-time.Minute))
	imHTTP := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/v1/bootstrap" {
			_ = json.NewEncoder(w).Encode(Bootstrap{Conversations: []Conversation{{ID: b, WorkspaceID: ws, Type: "group"}}})
		} else {
			_ = json.NewEncoder(w).Encode(map[string]any{"cursor": "2", "events": []any{map[string]any{"id": "2", "type": "message.created", "workspaceId": ws, "conversationId": b, "message": Message{ID: uuid.NewString(), ConversationID: b, SenderID: uuid.NewString(), SenderKind: "human", Body: "new authorized message", Seq: 2}}}})
		}
	}))
	defer imHTTP.Close()
	imc := &IM{BaseURL: imHTTP.URL, token: "fixture", expiry: time.Now().Add(time.Hour)}
	e := &Engine{Store: s}
	if _, err = e.ingestWithPresence(ctx, imc, lease, p); err != nil {
		t.Fatal(err)
	}
	if _, err = p.FlushDueDigest(ctx, s, lease, time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	pending, err := s.Pending(ctx, lease)
	if err != nil || len(pending) != 1 {
		t.Fatalf("pending=%+v err=%v", pending, err)
	}
	if err = e.authorizeUnreadDigestInput(ctx, imc, lease, pending[0]); err != nil {
		t.Fatalf("authorized B message rejected because revoked A stayed in digest: %v refs=%+v", err, pending[0].SourceRefs)
	}
	restored := newConversationPresenceState(time.Millisecond, nil)
	if err := restored.Restore(ctx, s, lease); err != nil {
		t.Fatal(err)
	}
	if restored.EnrichConversation(Conversation{ID: a}).UnreadCount != 0 || restored.EnrichConversation(Conversation{ID: b}).UnreadCount != 1 {
		t.Fatal("restart restored a revoked unread source or lost the authorized one")
	}
}

func TestMCPRevocationReannouncesSurvivorsOfAnAlreadyQueuedDigest(t *testing.T) {
	s, lease, _ := nativeModelRun(t)
	ctx := context.Background()
	ws, a, b := uuid.NewString(), uuid.NewString(), uuid.NewString()
	p := newConversationPresenceState(time.Millisecond, nil)
	now := time.Now().UTC()
	for _, conv := range []string{a, b} {
		p.RecordUnread(ws, Conversation{ID: conv, WorkspaceID: ws}, &Message{Seq: 1}, lease.AgentID, now.Add(-time.Minute))
	}
	if flushed, err := p.FlushDueDigest(ctx, s, lease, now); err != nil || !flushed {
		t.Fatalf("initial digest: flushed=%v err=%v", flushed, err)
	}
	if !p.ReconcileUnread([]Conversation{{ID: b, WorkspaceID: ws}}) {
		t.Fatal("revoked source was not removed")
	}
	if flushed, err := p.FlushDueDigest(ctx, s, lease, now.Add(time.Minute)); err != nil || !flushed {
		t.Fatalf("surviving unread source was not announced again: flushed=%v err=%v", flushed, err)
	}
	pending, err := s.Pending(ctx, lease)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, in := range pending {
		if in.Kind == "im.unread_digest" && len(in.SourceRefs) == 1 && in.SourceRefs[0].ConversationID == b {
			found = true
		}
	}
	if !found {
		t.Fatal("no independent digest for the still-authorized conversation")
	}
}

func TestMCPSubscriptionInitializationBacksOffFailures(t *testing.T) {
	s := newMCPInMemoryStore()
	ctx := context.Background()
	lease, err := s.Acquire(ctx, uuid.NewString(), "retry-fixture")
	if err != nil {
		t.Fatal(err)
	}
	ws, conv, appID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	var attempts atomic.Int32
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	}))
	defer remote.Close()
	app := imMarketplaceApp{ID: appID, Slug: "unavailable", MCPURL: remote.URL, Enabled: true}
	imHTTP := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/session") {
			_ = json.NewEncoder(w).Encode(imConversationAppSession{App: app, Token: "fixture", ExpiresAt: time.Now().Add(time.Hour).Format(time.RFC3339)})
		} else {
			_ = json.NewEncoder(w).Encode(map[string]any{"apps": []imMarketplaceApp{app}})
		}
	}))
	defer imHTTP.Close()
	w := newMCPNotificationWatcher(s, lease, &IM{BaseURL: imHTTP.URL, token: "fixture", expiry: time.Now().Add(time.Hour)}, "")
	defer w.closeAll()
	p := newConversationPresenceState(time.Minute, nil)
	p.Enter(ws, conv, "group")
	w.setPresence(p)
	now := time.Now()
	w.now = func() time.Time { return now }
	for _, step := range []struct {
		advance time.Duration
		want    int32
	}{{0, 1}, {0, 1}, {time.Second, 2}, {time.Second, 2}, {time.Second, 3}} {
		now = now.Add(step.advance)
		w.sync(ctx)
		if got := attempts.Load(); got != step.want {
			t.Fatalf("initialization attempts=%d, want %d after advancing %s", got, step.want, step.advance)
		}
	}
}

func TestMCPUnavailableMCPDoesNotBlockInputDeliveryTick(t *testing.T) {
	s := testStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	lease, err := s.Acquire(ctx, uuid.NewString(), "review")
	if err != nil {
		t.Fatal(err)
	}
	ws, conv, appID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	initializing := make(chan struct{}, 1)
	initCanceled := make(chan struct{}, 1)
	releaseRemote := make(chan struct{})
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			_, _ = io.Copy(io.Discard, r.Body)
			select {
			case initializing <- struct{}{}:
			default:
			}
			select {
			case <-r.Context().Done():
				initCanceled <- struct{}{}
			case <-releaseRemote:
			}
		}
	}))
	defer remote.Close()
	defer close(releaseRemote)
	app := imMarketplaceApp{ID: appID, Slug: "slow", MCPURL: remote.URL, Enabled: true}
	imHTTP := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/session") {
			_ = json.NewEncoder(w).Encode(imConversationAppSession{App: app, Token: "fixture", ExpiresAt: time.Now().Add(time.Hour).Format(time.RFC3339)})
		} else {
			_ = json.NewEncoder(w).Encode(map[string]any{"apps": []imMarketplaceApp{app}})
		}
	}))
	defer imHTTP.Close()
	w := newMCPNotificationWatcher(s, lease, &IM{BaseURL: imHTTP.URL, token: "fixture", expiry: time.Now().Add(time.Hour)}, "")
	p := newConversationPresenceState(time.Minute, w.presenceChanged)
	p.Enter(ws, conv, "group")
	w.setPresence(p)
	stop := w.start(ctx)
	defer stop()
	select {
	case <-initializing:
	case <-time.After(time.Second):
		cancel()
		t.Fatal("test endpoint was not contacted")
	}
	// Exercise the actual tick boundary while the external Initialize is blocked.
	delivered := make(chan []Input, 1)
	go func() {
		w.requestSync()
		in := Input{EventID: "1", WorkspaceID: ws, ConversationID: conv, SenderKind: "human", Body: "new message"}
		if err := s.Ingest(ctx, lease, "1", []Input{in}); err != nil {
			delivered <- nil
			return
		}
		pending, _ := s.Pending(ctx, lease)
		delivered <- pending
	}()
	select {
	case inputs := <-delivered:
		if len(inputs) != 1 || inputs[0].EventID != "1" {
			t.Fatalf("input not available during MCP initialization: %+v", inputs)
		}
	case <-time.After(time.Second):
		t.Fatal("MCP initialization blocked inbox delivery")
	}
	// Switching away also cancels initialization instead of publishing a stale
	// group subscription once its remote request eventually completes.
	p.Leave()
	select {
	case <-initCanceled:
	case <-time.After(time.Second):
		t.Fatal("leaving the group did not cancel the in-flight MCP initialization")
	}
	stop()
	if got := w.activeSubscriptionCount(); got != 0 {
		t.Fatalf("stale subscription survived the scope change: %d", got)
	}
}
