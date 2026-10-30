package eino

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
	"github.com/google/uuid"
	"teamie/services/agent-runtime/internal/domain"
)

func TestCheckpointPresenceRestoresExecutionScopeWithoutReadingUnread(t *testing.T) {
	s, lease, run := nativeModelRun(t)
	ctx := WithRun(context.Background(), run)
	ws, a, b := uuid.NewString(), uuid.NewString(), uuid.NewString()
	initial, _ := json.Marshal(presenceInitSnapshot{WorkspaceID: ws, ConversationID: a, ConvType: "group"})
	if err := s.DB.SaveObservation(ctx, lease, run.ID, presenceInitObservationCallID, presenceInitObservationKind, initial); err != nil {
		t.Fatal(err)
	}
	run.toolProfile = currentToolProfile
	run.setPresenceEnabled(true)
	run.setActiveConversation(ws, b, "group")
	session := &SessionStore[*schema.AgenticMessage]{Store: s, Lease: lease, Revision: 1}
	session.bind(run, []string{"native-target"})
	id := "turnloop:" + lease.AgentID
	if err := session.Set(ctx, id, []byte("native-payload")); err != nil {
		t.Fatal(err)
	}
	payload, raw, ok, err := s.DB.GetCheckpoint(ctx, id)
	if err != nil || !ok || string(payload) != "native-payload" {
		t.Fatalf("checkpoint payload: ok=%v err=%v", ok, err)
	}
	var metadata checkpointMetadata
	if err := json.Unmarshal(raw, &metadata); err != nil {
		t.Fatal(err)
	}
	if metadata.Presence == nil || metadata.Presence.ConversationID != b {
		t.Fatalf("checkpoint did not bind the completed enter: %+v", metadata.Presence)
	}
	presence := newConversationPresenceState(time.Minute, nil)
	presence.Enter(ws, a, "group")
	now := time.Now().UTC()
	presence.RecordUnread(ws, Conversation{ID: b, WorkspaceID: ws, Type: "group"}, &Message{ID: uuid.NewString(), SenderID: uuid.NewString(), Seq: 8}, lease.AgentID, now)
	run.resume = &metadata
	run.setActiveConversation(ws, a, "group")
	builder := &Tools{Store: s, Lease: lease, Presence: presence}
	active, err := builder.initRunPresence(ctx, run)
	if err != nil || !active {
		t.Fatalf("restore: active=%v err=%v", active, err)
	}
	_, current, _ := run.activeConversation()
	if current != b || presence.ActiveConversationID() != b {
		t.Fatal("native resume reset to the initial conversation")
	}
	if builder.checkSendPresence(run, b) != nil || builder.checkSendPresence(run, a) == nil {
		t.Fatal("send scope does not match the checkpoint")
	}
	restored := newConversationPresenceState(time.Minute, nil)
	if err := restored.Restore(ctx, s, lease); err != nil {
		t.Fatal(err)
	}
	entry := restored.unread[b]
	if entry == nil || entry.unreadCount != 1 || entry.lastSeq != 8 || !entry.dirty || !restored.firstDirtyAt.Equal(now) {
		t.Fatal("execution restore consumed or reset a new unread message")
	}
	rawInitial, found, err := s.DB.ReadObservation(ctx, run.ID, presenceInitObservationCallID, presenceInitObservationKind)
	if err != nil || !found || string(rawInitial) != string(initial) {
		t.Fatal("native resume rewrote the immutable replay origin")
	}
	// Full receipt replay still starts at A, independently of the native snapshot.
	run.resume = nil
	if _, err := builder.initRunPresence(ctx, run); err != nil {
		t.Fatal(err)
	}
	_, current, _ = run.activeConversation()
	if current != a {
		t.Fatal("receipt replay no longer uses its original scope")
	}
}

func TestPresenceCheckpointCompatibilityAndLegacyAdmission(t *testing.T) {
	s, lease, run := nativeModelRun(t)
	ctx := WithRun(context.Background(), run)
	run.toolProfile = currentToolProfile
	metadata := &checkpointMetadata{Version: nativeRuntimeVersion, Revision: 1, RunID: run.ID, Attempt: run.Attempt, Targets: []string{"target"}, ToolProfile: currentToolProfile}
	selected, err := s.selectRunResume(ctx, metadata, 1, run)
	if err != nil || selected == nil {
		t.Fatalf("pre-presence checkpoint became incompatible: %v", err)
	}
	run.resume = selected
	builder := &Tools{Store: s, Lease: lease, Presence: newConversationPresenceState(time.Minute, nil)}
	if enabled, err := builder.initRunPresence(ctx, run); err != nil || enabled {
		t.Fatalf("old native checkpoint adopted a new feature: %v %v", enabled, err)
	}
	if err := s.DB.SaveObservation(ctx, lease, run.ID, presenceInitObservationCallID, presenceInitObservationKind, []byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	selected, err = s.selectRunResume(ctx, metadata, 1, run)
	if err != nil || selected != nil {
		t.Fatalf("presence-aware legacy checkpoint must use receipt replay: %v %v", selected, err)
	}
	for _, tc := range []struct {
		name       string
		presence   *domain.CheckpointPresence
		compatible bool
	}{
		{"left", &domain.CheckpointPresence{Version: 1, Enabled: true}, true},
		{"disabled", &domain.CheckpointPresence{Version: 1}, true},
		{"unsupported", &domain.CheckpointPresence{Version: 2, Enabled: true}, false},
		{"partial_scope", &domain.CheckpointPresence{Version: 1, Enabled: true, WorkspaceID: uuid.NewString()}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			metadata.Presence = tc.presence
			selected, err := s.selectRunResume(ctx, metadata, 1, run)
			if err != nil || (selected != nil) != tc.compatible {
				t.Fatalf("compatibility: selected=%v err=%v", selected, err)
			}
		})
	}
}

func TestRestoredPresenceDoesNotGrantRevokedConversationAccess(t *testing.T) {
	s, lease, run := nativeModelRun(t)
	ctx := WithRun(context.Background(), run)
	ws, conversation := uuid.NewString(), uuid.NewString()
	run.resume = &checkpointMetadata{Presence: &domain.CheckpointPresence{Version: 1, Enabled: true, WorkspaceID: ws, ConversationID: conversation, ConvType: "group"}}
	var checks atomic.Int32
	im := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		checks.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":{"code":"forbidden","message":"revoked"}}`))
	}))
	defer im.Close()
	builder := &Tools{Store: s, Lease: lease, Presence: newConversationPresenceState(time.Minute, nil), IM: &IM{BaseURL: im.URL, token: "fixture", expiry: time.Now().Add(time.Hour)}}
	if enabled, err := builder.initRunPresence(ctx, run); err != nil || !enabled {
		t.Fatalf("restore: enabled=%v err=%v", enabled, err)
	}
	tools, err := builder.BuildIM()
	if err != nil {
		t.Fatal(err)
	}
	node, err := compose.NewToolNode(ctx, &compose.ToolsNodeConfig{Tools: tools, ExecuteSequentially: true, UnknownToolsHandler: builder.HandleUnknownMCPTool})
	if err != nil {
		t.Fatal(err)
	}
	result, err := invokeContextTool(node, ctx, "revoked-send", "im_send_message", map[string]any{"conversationId": conversation, "body": "must not send"})
	if err != nil || !strings.Contains(result, `"error":"forbidden"`) || checks.Load() != 1 {
		t.Fatalf("restored send bypassed current IM authorization: %s %v checks=%d", result, err, checks.Load())
	}
	result, err = invokeContextTool(node, ctx, "revoked-app", "mcp__board__mutate", map[string]any{})
	if err != nil || strings.Contains(result, `"ok":true`) || checks.Load() < 2 {
		t.Fatalf("restored MCP dispatch bypassed current IM authorization: %s %v checks=%d", result, err, checks.Load())
	}
}

func TestReceiptReplayPreservesNewlyBufferedUnreadMessages(t *testing.T) {
	s, lease, run := nativeModelRun(t)
	ctx := WithRun(context.Background(), run)
	ws, a, b := uuid.NewString(), uuid.NewString(), uuid.NewString()
	im := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/api/v1/bootstrap":
			_ = json.NewEncoder(w).Encode(Bootstrap{Conversations: []Conversation{
				{ID: a, WorkspaceID: ws, Type: "group", Name: "A"},
				{ID: b, WorkspaceID: ws, Type: "group", Name: "B"},
			}})
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/conversations/"+b+"/messages":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"messages": []Message{{ID: uuid.NewString(), ConversationID: b, SenderID: uuid.NewString(), Body: "old", Seq: 5, CreatedAt: time.Now().UTC().Format(time.RFC3339Nano)}},
				"hasMore":  false,
			})
		case strings.HasSuffix(r.URL.Path, "/read"):
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer im.Close()

	presence := newConversationPresenceState(time.Minute, nil)
	presence.Enter(ws, a, "group")
	builder := &Tools{
		Store:    s,
		Lease:    lease,
		Presence: presence,
		IM:       &IM{BaseURL: im.URL, token: "fixture", expiry: time.Now().Add(time.Hour)},
	}
	if enabled, err := builder.initRunPresence(ctx, run); err != nil || !enabled {
		t.Fatalf("initRunPresence: enabled=%v err=%v", enabled, err)
	}
	imTools, err := builder.BuildIM()
	if err != nil {
		t.Fatal(err)
	}
	presenceTools, err := builder.BuildPresenceTools()
	if err != nil {
		t.Fatal(err)
	}
	node, err := compose.NewToolNode(ctx, &compose.ToolsNodeConfig{
		Tools:               append(imTools, presenceTools...),
		ExecuteSequentially: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Original execution enters B and then leaves B, committing both receipts.
	if out, err := invokeContextTool(node, ctx, "call-enter-b", "im_enter_conversation", map[string]any{"conversationId": b}); err != nil || !strings.Contains(out, `"ok":true`) {
		t.Fatalf("live enter B failed: %s %v", out, err)
	}
	if out, err := invokeContextTool(node, ctx, "call-leave-b", "im_leave_conversation", map[string]any{}); err != nil || !strings.Contains(out, `"ok":true`) {
		t.Fatalf("live leave B failed: %s %v", out, err)
	}

	// Ingestion buffers a newer message in B and persists the updated checkpoint before full receipt replay runs.
	arrivedAt := time.Now().UTC().Add(-2 * time.Minute)
	presence.RecordUnread(ws, Conversation{ID: b, WorkspaceID: ws, Type: "group", Name: "B"}, &Message{
		ID:             uuid.NewString(),
		ConversationID: b,
		SenderID:       uuid.NewString(),
		Body:           "newer arrival after leave",
		Seq:            12,
	}, lease.AgentID, arrivedAt)
	if err := presence.Persist(ctx, s, lease); err != nil {
		t.Fatal(err)
	}

	// Full receipt replay starts from initial scope A and replays cached enter(B) -> leave().
	run.resume = nil
	if enabled, err := builder.initRunPresence(ctx, run); err != nil || !enabled {
		t.Fatalf("replay initRunPresence: enabled=%v err=%v", enabled, err)
	}
	if out, err := invokeContextTool(node, ctx, "call-enter-b", "im_enter_conversation", map[string]any{"conversationId": b}); err != nil || !strings.Contains(out, `"ok":true`) {
		t.Fatalf("replay enter B failed: %s %v", out, err)
	}
	if _, cur, _ := run.activeConversation(); cur != b {
		t.Fatalf("expected active conversation %s after replayed enter, got %s", b, cur)
	}
	if entry := presence.unread[b]; entry == nil || entry.unreadCount != 1 || entry.lastSeq != 12 || !entry.dirty {
		t.Fatalf("replayed enter erased newly buffered unread entry: %+v", entry)
	}

	if out, err := invokeContextTool(node, ctx, "call-leave-b", "im_leave_conversation", map[string]any{}); err != nil || !strings.Contains(out, `"ok":true`) {
		t.Fatalf("replay leave B failed: %s %v", out, err)
	}
	if _, cur, _ := run.activeConversation(); cur != "" {
		t.Fatalf("expected empty active conversation after replayed leave, got %s", cur)
	}

	restored := newConversationPresenceState(time.Minute, nil)
	if err := restored.Restore(ctx, s, lease); err != nil {
		t.Fatal(err)
	}
	if entry := restored.unread[b]; entry == nil || entry.unreadCount != 1 || entry.lastSeq != 12 || !entry.dirty {
		t.Fatalf("persisted checkpoint lost buffered unread entry after receipt replay: %+v", entry)
	}
}

type flakyPresenceCheckpointDB struct {
	domain.RuntimeRepository
	targetID    string
	failRemains atomic.Int32
	calls       atomic.Int32
}

func (f *flakyPresenceCheckpointDB) GetCheckpoint(ctx context.Context, checkpointID string) ([]byte, []byte, bool, error) {
	if checkpointID == f.targetID {
		f.calls.Add(1)
		for {
			rem := f.failRemains.Load()
			if rem <= 0 {
				break
			}
			if f.failRemains.CompareAndSwap(rem, rem-1) {
				return nil, nil, false, errors.New("transient checkpoint read failure")
			}
		}
	}
	return f.RuntimeRepository.GetCheckpoint(ctx, checkpointID)
}

func TestWorkerPresenceRestoreRetriesBeforeStartingIngestion(t *testing.T) {
	ctx := context.Background()
	baseStore := testStore(t)
	e, f, _, _ := fixtureEngine(t, baseStore)
	otherConv := uuid.NewString()

	lease, err := baseStore.Acquire(ctx, f.agent, e.owner)
	if err != nil {
		t.Fatal(err)
	}
	seeded := newConversationPresenceState(time.Hour, nil)
	seeded.Enter(f.workspace, f.conversation, "group")
	seeded.RecordUnread(f.workspace, Conversation{ID: otherConv, WorkspaceID: f.workspace, Type: "group", Name: "Other"}, &Message{
		ID:             uuid.NewString(),
		ConversationID: otherConv,
		SenderID:       uuid.NewString(),
		Body:           "behind-cursor unread",
		Seq:            7,
	}, f.agent, time.Now().UTC())
	if err := seeded.Persist(ctx, baseStore, lease); err != nil {
		t.Fatal(err)
	}
	baseStore.Release(ctx, lease)

	flaky := &flakyPresenceCheckpointDB{
		RuntimeRepository: baseStore.DB,
		targetID:          presenceCheckpointID(f.agent),
	}
	// 1. Persistent failure until worker cancellation must never overwrite the checkpoint.
	flaky.failRemains.Store(1000)
	e.Store = &Store{DB: flaky, Coordinator: baseStore.Coordinator}
	cancel1, done1 := startFixtureWorker(t, e, f)
	waitFor(t, func() bool { return flaky.calls.Load() >= 2 })
	cancel1()
	<-done1

	check := newConversationPresenceState(time.Hour, nil)
	leaseCheck, err := baseStore.Acquire(ctx, f.agent, e.owner)
	if err != nil {
		t.Fatal(err)
	}
	if err := check.Restore(ctx, baseStore, leaseCheck); err != nil {
		t.Fatal(err)
	}
	baseStore.Release(ctx, leaseCheck)
	if entry := check.unread[otherConv]; entry == nil || entry.unreadCount != 1 || entry.lastSeq != 7 {
		t.Fatalf("failed startup overwrote durable presence checkpoint: %+v", entry)
	}

	// 2. Transient failure (2 errors then success) retries and restores before ingestion runs.
	flaky.calls.Store(0)
	flaky.failRemains.Store(2)
	cancel2, done2 := startFixtureWorker(t, e, f)
	waitFor(t, func() bool { return flaky.calls.Load() >= 3 && flaky.failRemains.Load() == 0 })
	cancel2()
	<-done2

	check2 := newConversationPresenceState(time.Hour, nil)
	leaseCheck2, err := baseStore.Acquire(ctx, f.agent, e.owner)
	if err != nil {
		t.Fatal(err)
	}
	if err := check2.Restore(ctx, baseStore, leaseCheck2); err != nil {
		t.Fatal(err)
	}
	baseStore.Release(ctx, leaseCheck2)
	if entry := check2.unread[otherConv]; entry == nil || entry.unreadCount != 1 || entry.lastSeq != 7 {
		t.Fatalf("transient restore retry lost durable unread state: %+v", entry)
	}
}

type memCheckpointDB struct {
	domain.RuntimeRepository
	payload   []byte
	meta      []byte
	ok        bool
	leaseLost bool
	failsLeft atomic.Int32
	gets      atomic.Int32
	sets      atomic.Int32
}

func (m *memCheckpointDB) CheckLease(_ context.Context, _ *domain.Lease) error {
	if m.leaseLost {
		return domain.ErrLeaseLost
	}
	return nil
}

func (m *memCheckpointDB) GetCheckpoint(_ context.Context, _ string) ([]byte, []byte, bool, error) {
	m.gets.Add(1)
	for {
		rem := m.failsLeft.Load()
		if rem <= 0 {
			break
		}
		if m.failsLeft.CompareAndSwap(rem, rem-1) {
			return nil, nil, false, errors.New("transient db error")
		}
	}
	return append([]byte(nil), m.payload...), append([]byte(nil), m.meta...), m.ok, nil
}

func (m *memCheckpointDB) SetCheckpoint(_ context.Context, _ *domain.Lease, _ string, payload, meta []byte) error {
	m.sets.Add(1)
	m.payload = append([]byte(nil), payload...)
	m.meta = append([]byte(nil), meta...)
	m.ok = true
	return nil
}

func TestApplyReplayPresenceAndRestoreRetryUnit(t *testing.T) {
	ctx := context.Background()
	mem := &memCheckpointDB{}
	store := &Store{DB: mem}
	lease := &Lease{AgentID: uuid.NewString(), Owner: "unit", Fence: 1}
	ws, a, b, d := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()

	// 1. applyReplayPresence preserves buffered unread messages across enter -> leave -> open_direct replay.
	presence := newConversationPresenceState(time.Minute, nil)
	presence.Enter(ws, a, "group")
	presence.RecordUnread(ws, Conversation{ID: b, WorkspaceID: ws, Type: "group", Name: "B"}, &Message{
		ID: uuid.NewString(), ConversationID: b, SenderID: uuid.NewString(), Seq: 42,
	}, lease.AgentID, time.Now().UTC())
	presence.RecordUnread(ws, Conversation{ID: d, WorkspaceID: ws, Type: "direct", Name: "D"}, &Message{
		ID: uuid.NewString(), ConversationID: d, SenderID: uuid.NewString(), Seq: 99,
	}, lease.AgentID, time.Now().UTC())

	run := &Run{ID: uuid.NewString(), AgentID: lease.AgentID}
	run.setPresenceEnabled(true)
	run.setActiveConversation(ws, a, "group")
	tools := &Tools{Store: store, Lease: lease, Presence: presence}

	enterCached, _ := json.Marshal(Observation{
		OK:   true,
		Data: enterConversationResult{ActiveConversationID: b, WorkspaceID: ws, Type: "group"},
	})
	tools.applyReplayPresence(ctx, run, "im_enter_conversation", enterCached)
	if _, cur, _ := run.activeConversation(); cur != b {
		t.Fatalf("expected active=%s after replayed enter, got %s", b, cur)
	}
	if entry := presence.unread[b]; entry == nil || entry.unreadCount != 1 || entry.lastSeq != 42 || !entry.dirty {
		t.Fatalf("replayed enter deleted buffered unread entry: %+v", entry)
	}

	leaveCached, _ := json.Marshal(Observation{OK: true})
	tools.applyReplayPresence(ctx, run, "im_leave_conversation", leaveCached)
	if _, cur, _ := run.activeConversation(); cur != "" {
		t.Fatalf("expected empty active conversation after replayed leave, got %s", cur)
	}

	openDirectCached, _ := json.Marshal(Observation{
		OK:   true,
		Data: Conversation{ID: d, WorkspaceID: ws, Type: "direct"},
	})
	tools.applyReplayPresence(ctx, run, "im_open_direct_conversation", openDirectCached)
	if _, cur, _ := run.activeConversation(); cur != d {
		t.Fatalf("expected active=%s after replayed open_direct, got %s", d, cur)
	}
	if entry := presence.unread[d]; entry == nil || entry.unreadCount != 1 || entry.lastSeq != 99 || !entry.dirty {
		t.Fatalf("replayed open_direct deleted buffered unread entry: %+v", entry)
	}

	// 2. agentWorker.restorePresence retries transient GetCheckpoint errors before allowing ingestion.
	mem.gets.Store(0)
	mem.failsLeft.Store(2)
	worker := &agentWorker{
		engine:   &Engine{Store: store},
		lease:    lease,
		presence: newConversationPresenceState(time.Minute, nil),
	}
	if !worker.restorePresence(ctx) {
		t.Fatal("expected restorePresence to succeed after transient retries")
	}
	if mem.gets.Load() != 3 {
		t.Fatalf("expected 3 GetCheckpoint attempts, got %d", mem.gets.Load())
	}
	if entry := worker.presence.unread[b]; entry == nil || entry.unreadCount != 1 || entry.lastSeq != 42 {
		t.Fatalf("expected restored unread entry for B, got %+v", entry)
	}

	// 3. agentWorker.restorePresence aborts without overwriting checkpoint when context cancels or lease is lost.
	setsBefore := mem.sets.Load()
	mem.failsLeft.Store(100)
	cancelCtx, cancel := context.WithTimeout(ctx, 80*time.Millisecond)
	defer cancel()
	worker2 := &agentWorker{
		engine:   &Engine{Store: store},
		lease:    lease,
		presence: newConversationPresenceState(time.Minute, nil),
	}
	if worker2.restorePresence(cancelCtx) {
		t.Fatal("expected restorePresence to return false on canceled context")
	}
	if mem.sets.Load() != setsBefore {
		t.Fatalf("failed restorePresence must not write checkpoint (sets before=%d after=%d)", setsBefore, mem.sets.Load())
	}
}
