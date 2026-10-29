package eino

import (
	"context"
	"encoding/json"
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
