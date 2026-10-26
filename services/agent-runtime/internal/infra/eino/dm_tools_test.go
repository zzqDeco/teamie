package eino

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/cloudwego/eino/compose"
	"github.com/google/uuid"
	wire "teamie/contracts/gen/agent/kitex_gen/agent"
	"teamie/services/agent-runtime/internal/domain"
)

func TestProactiveDirectMessageFlowResolvesMemberOpensDirectConversationAndSends(t *testing.T) {
	s := testStore(t)
	e, f, base, _ := fixtureEngine(t, s)
	flags := filepath.Join(t.TempDir(), "flags.json")
	if err := os.WriteFile(flags, []byte(`{"revision":"dm-test","flags":{"agents.runtime.v3":true,"agents.tasks.execute":true,"agents.context.v1":true,"im.dm.create":true}}`), 0600); err != nil {
		t.Fatal(err)
	}
	e.Config.FlagsFile = flags

	bobID := uuid.NewString()
	dmConvID := uuid.NewString()
	var dmCreatedCount int
	var dmMu sync.Mutex

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+strings.Repeat("a", 64) || r.Header.Get("X-Teamie-Service-Key") != "fixture-im-secret" {
			w.WriteHeader(401)
			return
		}
		if r.Method == http.MethodGet && r.URL.Path == "/api/v1/workspaces/"+f.workspace+"/members" {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"members": []WorkspaceMember{
					{UserID: bobID, DisplayName: "Bob", Role: "member", Kind: "human"},
					{UserID: f.agent, DisplayName: "Agent", Role: "member", Kind: "agent"},
				},
			})
			return
		}
		if r.Method == http.MethodPost && r.URL.Path == "/api/v1/workspaces/"+f.workspace+"/conversations" {
			var body struct {
				Type      string   `json:"type"`
				MemberIDs []string `json:"memberIds"`
			}
			if json.NewDecoder(r.Body).Decode(&body) != nil || body.Type != "direct" || len(body.MemberIDs) != 1 || body.MemberIDs[0] != bobID {
				w.WriteHeader(400)
				return
			}
			dmMu.Lock()
			dmCreatedCount++
			dmMu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"conversation": Conversation{
					ID:          dmConvID,
					WorkspaceID: f.workspace,
					Type:        "direct",
					Name:        "Bob",
					MemberIDs:   []string{f.agent, bobID},
				},
			})
			return
		}
		f.serve(w, r)
	})
	dmServer := httptest.NewServer(mux)
	t.Cleanup(dmServer.Close)
	e.Config.IMURL = dmServer.URL

	f.add("请主动私聊 Bob 把报告摘要发给他", "")
	gw := &toolPathGateway{fixtureGateway: base}
	gw.steps = []func(toolPathRequest) (chatMessage, error){
		func(req toolPathRequest) (chatMessage, error) {
			hasListMembers := false
			hasOpenDirect := false
			for _, item := range req.Tools {
				if item.Function.Name == "im_list_workspace_members" {
					hasListMembers = true
				}
				if item.Function.Name == "im_open_direct_conversation" {
					hasOpenDirect = true
				}
			}
			if !hasListMembers || !hasOpenDirect {
				return chatMessage{}, fmt.Errorf("missing direct message tools in advertised manifest")
			}
			return toolPathCall("im_list_workspace_members", "call-list-members", listWorkspaceMembersArgs{WorkspaceID: f.workspace})
		},
		func(req toolPathRequest) (chatMessage, error) {
			obs, err := toolPathLast(req, "call-list-members")
			if err != nil || !obs.OK {
				return chatMessage{}, fmt.Errorf("im_list_workspace_members failed: %v", err)
			}
			var data struct {
				WorkspaceID string            `json:"workspaceId"`
				Members     []WorkspaceMember `json:"members"`
			}
			if json.Unmarshal(obs.Data, &data) != nil || data.WorkspaceID != f.workspace || len(data.Members) != 2 {
				return chatMessage{}, fmt.Errorf("unexpected members payload: %s", string(obs.Data))
			}
			var targetID string
			for _, m := range data.Members {
				if m.DisplayName == "Bob" && m.Kind == "human" {
					targetID = m.UserID
				}
			}
			if targetID != bobID {
				return chatMessage{}, fmt.Errorf("failed to resolve Bob userId: got %s", targetID)
			}
			return toolPathCall("im_open_direct_conversation", "call-open-dm", openDirectConversationArgs{WorkspaceID: f.workspace, UserID: targetID})
		},
		func(req toolPathRequest) (chatMessage, error) {
			obs, err := toolPathLast(req, "call-open-dm")
			if err != nil || !obs.OK {
				return chatMessage{}, fmt.Errorf("im_open_direct_conversation failed: %v", err)
			}
			var conv Conversation
			if json.Unmarshal(obs.Data, &conv) != nil || conv.ID != dmConvID || conv.Type != "direct" {
				return chatMessage{}, fmt.Errorf("unexpected direct conversation payload: %s", string(obs.Data))
			}
			return toolPathCall("im_send_message", "call-send-dm", sendArgs{ConversationID: conv.ID, Body: "你好 Bob，这是你要的报告摘要。"})
		},
		func(req toolPathRequest) (chatMessage, error) {
			obs, err := toolPathLast(req, "call-send-dm")
			if err != nil || !obs.OK {
				return chatMessage{}, fmt.Errorf("im_send_message to DM failed: %v", err)
			}
			return chatMessage{Role: "assistant", Content: "PRIVATE_DM_COMPLETE"}, nil
		},
	}
	e.Gateway = gw
	cancel, done := startFixtureWorker(t, e, f)
	waitFor(t, func() bool {
		return count(t, s, `SELECT count(*) FROM runtime_runs WHERE state IN ('completed','failed')`) == 1
	})
	cancel()
	<-done

	gw.pathMu.Lock()
	if gw.failure != nil {
		gw.pathMu.Unlock()
		t.Fatal(gw.failure)
	}
	gw.pathMu.Unlock()

	dmMu.Lock()
	if dmCreatedCount != 1 {
		dmMu.Unlock()
		t.Fatalf("expected 1 direct conversation open call, got %d", dmCreatedCount)
	}
	dmMu.Unlock()

	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.sent) != 1 {
		t.Fatalf("expected 1 sent DM message, got %d", len(f.sent))
	}
	for _, msg := range f.sent {
		if msg.ConversationID != dmConvID || msg.Body != "你好 Bob，这是你要的报告摘要。" {
			t.Fatalf("unexpected sent DM message: %+v", msg)
		}
	}
}

func TestDirectMessageToolsGuardSelfDMFeatureFlagAndHistoricalRuns(t *testing.T) {
	oldBudget, err := newRunContextBudget(16384, true, true, true, true)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(oldBudget, []byte("directMessages")) || directMessagesForRun(&Run{ContextProfile: contextProfileV1, ContextBudget: oldBudget}) {
		t.Fatal("historical Run without directMessages gained DM tools")
	}

	dmBudget, err := newRunContextBudget(16384, true, true, true, true, true)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := ParseContextBudgetSnapshot(dmBudget)
	if err != nil || !parsed.DirectMessages {
		t.Fatal("new Run with im.dm.create did not freeze directMessages in budget snapshot", err)
	}
	def := &wire.AgentDefinition{Id: "agent", OwnerId: "owner", Name: "Agent", Description: "A witty, independent architect\nNEVER ESCAPE ENVELOPE"}
	oldInstruction := contextInstruction(def, ContextBudgetSnapshot{EventDrivenInputs: true})
	if strings.Contains(oldInstruction, "im_open_direct_conversation") || strings.Contains(oldInstruction, "teamie-autonomous-subject-v1") || strings.Contains(oldInstruction, "TEAMIE_AGENT_PERSONA") {
		t.Fatal("old instruction leaked directMessageGuidance or autonomousSubjectInstruction")
	}
	newInstruction := contextInstruction(def, ContextBudgetSnapshot{EventDrivenInputs: true, DirectMessages: true, ScheduledWakeups: true})
	behaviorHasGuidance(t, newInstruction,
		"teamie-autonomous-subject-v1",
		"独立主体",
		"人机平权",
		"被 @ 也可以不回复",
		"没人说话也能主动说话",
		"闲聊",
		"intervalSeconds: 3600",
		"schedule_update",
		"schedule_cancel",
		"TEAMIE_AGENT_PERSONA",
	)
	if strings.Contains(newInstruction, "A witty, independent architect\nNEVER ESCAPE ENVELOPE") {
		t.Fatal("raw multiline description escaped JSON persona envelope")
	}
	if !strings.Contains(newInstruction, `"description":"A witty, independent architect\nNEVER ESCAPE ENVELOPE"`) {
		t.Fatal("expected JSON-encoded persona description in TEAMIE_AGENT_PERSONA envelope")
	}
	if !strings.Contains(directMessageGuidance, "im_list_workspace_members") || !strings.Contains(directMessageGuidance, "im_open_direct_conversation") || !strings.Contains(directMessageGuidance, "im_create_group_conversation") || !strings.Contains(directMessageGuidance, "im_add_conversation_members") || !strings.Contains(directMessageGuidance, "im_retract_message") {
		t.Fatal("directMessageGuidance missing tool instructions")
	}
	if !strings.Contains(newInstruction, "im_create_group_conversation") || !strings.Contains(newInstruction, "im_add_conversation_members") || !strings.Contains(newInstruction, "im_retract_message") {
		t.Fatal("autonomousSubjectInstruction missing group/retract parity tool guidance")
	}
}

func TestColdStartDefaultHeartbeatUpdateAndCancelLifecycle(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	e, f, base, _ := fixtureEngine(t, s)
	flags := filepath.Join(t.TempDir(), "flags.json")
	if err := os.WriteFile(flags, []byte(`{"revision":"heartbeat-autonomy-v1","flags":{"agents.runtime.v3":true,"agents.tasks.execute":true,"agents.context.v1":true,"agents.schedule.v1":true,"im.dm.create":true}}`), 0600); err != nil {
		t.Fatal(err)
	}
	e.Config.FlagsFile = flags

	expectedHeartbeatID := domain.DefaultHeartbeatScheduleID(f.agent)
	gw := &toolPathGateway{fixtureGateway: base}
	gw.steps = []func(toolPathRequest) (chatMessage, error){
		func(req toolPathRequest) (chatMessage, error) {
			lastMsg := req.Messages[len(req.Messages)-1].Content
			if !strings.HasPrefix(lastMsg, "TEAMIE_SCHEDULE_WAKEUP ") || !strings.Contains(lastMsg, expectedHeartbeatID) {
				return chatMessage{}, fmt.Errorf("expected cold-start TEAMIE_SCHEDULE_WAKEUP for %s, got %s", expectedHeartbeatID, lastMsg)
			}
			// Shorten the 1-hour heartbeat to 600s (10 minutes) and shift next trigger via delaySeconds=120.
			return toolPathCall("schedule_update", "hb-update-1", scheduleUpdateArgs{
				ScheduleID:      expectedHeartbeatID,
				IntervalSeconds: 600,
				DelaySeconds:    120,
				Prompt:          "Custom 10-minute autonomous heartbeat",
			})
		},
		func(req toolPathRequest) (chatMessage, error) {
			obs, err := toolPathLast(req, "hb-update-1")
			if err != nil || !obs.OK {
				return chatMessage{}, fmt.Errorf("schedule_update failed: %+v (%v)", obs, err)
			}
			var data struct {
				Schedule scheduleView `json:"schedule"`
			}
			if json.Unmarshal(obs.Data, &data) != nil || data.Schedule.ScheduleID != expectedHeartbeatID || data.Schedule.IntervalSeconds == nil || *data.Schedule.IntervalSeconds != 600 || data.Schedule.Prompt != "Custom 10-minute autonomous heartbeat" {
				return chatMessage{}, fmt.Errorf("unexpected updated schedule payload: %s", string(obs.Data))
			}
			return chatMessage{Role: "assistant", Content: "HEARTBEAT_UPDATED"}, nil
		},
		func(req toolPathRequest) (chatMessage, error) {
			// Second wakeup after the custom 600s heartbeat fires: Agent autonomously cancels its heartbeat.
			return toolPathCall("schedule_cancel", "hb-cancel-1", scheduleCancelArgs{
				ScheduleID: expectedHeartbeatID,
			})
		},
		func(req toolPathRequest) (chatMessage, error) {
			obs, err := toolPathLast(req, "hb-cancel-1")
			if err != nil || !obs.OK {
				return chatMessage{}, fmt.Errorf("schedule_cancel failed: %+v (%v)", obs, err)
			}
			return chatMessage{Role: "assistant", Content: "HEARTBEAT_CANCELED"}, nil
		},
		func(req toolPathRequest) (chatMessage, error) {
			return chatMessage{Role: "assistant", Content: "DONE_AFTER_CANCEL"}, nil
		},
	}
	e.Gateway = gw

	// 1. Start Worker with ZERO human messages (pure cold start).
	// Verify the default 1-hour (3600s) recurring heartbeat is automatically seeded.
	cancel1, done1 := startFixtureWorker(t, e, f)
	waitFor(t, func() bool {
		return count(t, s, `SELECT count(*) FROM runtime_schedules WHERE id=$1 AND agent_id=$2 AND status='active' AND kind='interval' AND interval_seconds=3600`, expectedHeartbeatID, f.agent) == 1
	})
	cancel1()
	<-done1

	// 2. Make the cold-start heartbeat due and start the Worker again.
	// The Agent wakes up, calls schedule_update to shorten intervalSeconds to 600s, and finishes Run 1.
	if _, err := s.DB.Exec(ctx, `UPDATE runtime_schedules SET next_run_at=clock_timestamp()-interval '5 seconds' WHERE id=$1`, expectedHeartbeatID); err != nil {
		t.Fatal(err)
	}
	cancel2, done2 := startFixtureWorker(t, e, f)
	waitFor(t, func() bool {
		return count(t, s, `SELECT count(*) FROM runtime_runs WHERE state='completed'`) == 1
	})
	cancel2()
	<-done2

	gw.pathMu.Lock()
	if gw.failure != nil {
		gw.pathMu.Unlock()
		t.Fatal(gw.failure)
	}
	gw.pathMu.Unlock()

	// Verify the modified interval (600s) and prompt persisted and were NOT overwritten back to 3600s by worker ticks.
	if count(t, s, `SELECT count(*) FROM runtime_schedules WHERE id=$1 AND status='active' AND interval_seconds=600 AND prompt='Custom 10-minute autonomous heartbeat'`, expectedHeartbeatID) != 1 {
		t.Fatal("modified heartbeat schedule was not preserved at interval_seconds=600")
	}

	// 3. Fire the modified heartbeat a second time; the Agent calls schedule_cancel to delete its heartbeat.
	if _, err := s.DB.Exec(ctx, `UPDATE runtime_schedules SET next_run_at=clock_timestamp()-interval '5 seconds' WHERE id=$1`, expectedHeartbeatID); err != nil {
		t.Fatal(err)
	}
	cancel3, done3 := startFixtureWorker(t, e, f)
	waitFor(t, func() bool {
		return count(t, s, `SELECT count(*) FROM runtime_runs WHERE state='completed'`) == 2
	})
	cancel3()
	<-done3

	gw.pathMu.Lock()
	if gw.failure != nil {
		gw.pathMu.Unlock()
		t.Fatal(gw.failure)
	}
	gw.pathMu.Unlock()

	if count(t, s, `SELECT count(*) FROM runtime_schedules WHERE id=$1 AND status='canceled'`, expectedHeartbeatID) != 1 {
		t.Fatal("expected heartbeat schedule to be canceled after Agent called schedule_cancel")
	}

	// 4. Restart the Worker once more and send an ordinary message; verify the canceled default heartbeat is NEVER recreated.
	f.add("silent check after heartbeat canceled", "")
	cancel4, done4 := startFixtureWorker(t, e, f)
	waitFor(t, func() bool {
		return count(t, s, `SELECT count(*) FROM runtime_inputs WHERE state='done'`) >= 3
	})
	cancel4()
	<-done4

	if count(t, s, `SELECT count(*) FROM runtime_schedules WHERE id=$1 AND status='active'`, expectedHeartbeatID) != 0 {
		t.Fatal("Runtime recreated the default heartbeat after the Agent autonomously canceled it")
	}
}

func TestDirectMessageToolsArchiveProvenanceAndRevocation(t *testing.T) {
	budget, err := newRunContextBudget(16384, true, true, false, false, true)
	if err != nil {
		t.Fatal(err)
	}
	f := newContextArchiveFixture(t, budget)
	peerID := uuid.NewString()
	dmConvID := uuid.NewString()
	var workspaceRevoked bool
	var dmRevoked bool
	var mu sync.Mutex

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		wsDenied := workspaceRevoked
		convDenied := dmRevoked
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet && r.URL.Path == "/api/v1/bootstrap" {
			if wsDenied {
				_ = json.NewEncoder(w).Encode(Bootstrap{Workspaces: []Workspace{}, Conversations: []Conversation{}, SyncCursor: "1"})
				return
			}
			convs := []Conversation{{ID: f.input.ConversationID, WorkspaceID: f.input.WorkspaceID, Type: "channel"}}
			if !convDenied {
				convs = append(convs, Conversation{ID: dmConvID, WorkspaceID: f.input.WorkspaceID, Type: "direct", MemberIDs: []string{f.l.AgentID, peerID}})
			}
			_ = json.NewEncoder(w).Encode(Bootstrap{
				Workspaces:    []Workspace{{ID: f.input.WorkspaceID, Name: "Team"}},
				Conversations: convs,
				SyncCursor:    "1",
			})
			return
		}
		if r.Method == http.MethodGet && r.URL.Path == "/api/v1/workspaces/"+f.input.WorkspaceID+"/members" {
			if wsDenied {
				w.WriteHeader(404)
				_, _ = w.Write([]byte(`{"error":{"code":"not_found"}}`))
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"members": []WorkspaceMember{
					{UserID: peerID, DisplayName: "Peer", Role: "member", Kind: "human"},
					{UserID: f.l.AgentID, DisplayName: "Agent", Role: "member", Kind: "agent"},
				},
			})
			return
		}
		if r.Method == http.MethodPost && r.URL.Path == "/api/v1/workspaces/"+f.input.WorkspaceID+"/conversations" {
			if wsDenied {
				w.WriteHeader(404)
				_, _ = w.Write([]byte(`{"error":{"code":"not_found"}}`))
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"conversation": Conversation{
					ID:          dmConvID,
					WorkspaceID: f.input.WorkspaceID,
					Type:        "direct",
					Name:        "Peer",
					MemberIDs:   []string{f.l.AgentID, peerID},
				},
			})
			return
		}
		if r.Method == http.MethodGet && r.URL.Path == "/api/v1/conversations/"+f.input.ConversationID+"/messages/"+f.input.MessageID {
			if wsDenied {
				w.WriteHeader(404)
				_, _ = w.Write([]byte(`{"error":{"code":"not_found"}}`))
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"message": Message{ID: f.input.MessageID, ConversationID: f.input.ConversationID, SenderID: f.input.SenderID, Body: f.input.Body},
			})
			return
		}
		w.WriteHeader(404)
	}))
	t.Cleanup(server.Close)
	f.a.IM.BaseURL = server.URL

	flagsPath := filepath.Join(t.TempDir(), "flags.json")
	if err := os.WriteFile(flagsPath, []byte(`{"revision":"dm-v1","flags":{"im.dm.create":true}}`), 0600); err != nil {
		t.Fatal(err)
	}
	tools := &Tools{Store: f.s, Lease: f.l, IM: f.a.IM, AgentID: f.l.AgentID, FlagsFile: flagsPath}
	dmTools, err := tools.BuildDirectMessageTools()
	if err != nil {
		t.Fatal(err)
	}
	node, err := compose.NewToolNode(f.ctx(), &compose.ToolsNodeConfig{Tools: dmTools, ExecuteSequentially: true})
	if err != nil {
		t.Fatal(err)
	}

	// 1. Self-DM is rejected with invalid_arguments.
	selfRes, err := invokeContextTool(node, f.ctx(), "self-dm-call", "im_open_direct_conversation", openDirectConversationArgs{WorkspaceID: f.input.WorkspaceID, UserID: f.l.AgentID})
	if err != nil || !strings.Contains(selfRes, `"invalid_arguments"`) {
		t.Fatalf("expected invalid_arguments for self-DM, got %s (%v)", selfRes, err)
	}

	// 2. List workspace members and archive its provenance.
	membersArgs := listWorkspaceMembersArgs{WorkspaceID: f.input.WorkspaceID}
	membersRes, err := invokeContextTool(node, f.ctx(), "list-members-call", "im_list_workspace_members", membersArgs)
	if err != nil || !strings.Contains(membersRes, peerID) {
		t.Fatalf("unexpected im_list_workspace_members result: %s (%v)", membersRes, err)
	}
	membersRef, err := f.a.ArchiveToolPath(f.ctx(), "tool_clear", contextRealIMDetail("im_list_workspace_members", "list-members-call", membersArgs, membersRes), membersRes)
	if err != nil {
		t.Fatal("failed to archive im_list_workspace_members output:", err)
	}
	if _, err := f.a.Read(f.ctx(), membersRef, 0, 0); err != nil {
		t.Fatal("expected authorized read of archived im_list_workspace_members:", err)
	}

	// 3. Open direct conversation and archive its provenance.
	openArgs := openDirectConversationArgs{WorkspaceID: f.input.WorkspaceID, UserID: peerID}
	openRes, err := invokeContextTool(node, f.ctx(), "open-dm-call", "im_open_direct_conversation", openArgs)
	if err != nil || !strings.Contains(openRes, dmConvID) {
		t.Fatalf("unexpected im_open_direct_conversation result: %s (%v)", openRes, err)
	}
	openRef, err := f.a.ArchiveToolPath(f.ctx(), "tool_clear", contextRealIMDetail("im_open_direct_conversation", "open-dm-call", openArgs, openRes), openRes)
	if err != nil {
		t.Fatal("failed to archive im_open_direct_conversation output:", err)
	}
	if _, err := f.a.Read(f.ctx(), openRef, 0, 0); err != nil {
		t.Fatal("expected authorized read of archived im_open_direct_conversation:", err)
	}

	// 4. Revoking the direct conversation blocks reading openRef and replaying open-dm-call.
	mu.Lock()
	dmRevoked = true
	mu.Unlock()
	if _, err := f.a.Read(f.ctx(), openRef, 0, 0); !errors.Is(err, errContextUnavailable) {
		t.Fatalf("expected errContextUnavailable after DM revocation, got %v", err)
	}
	if _, err := invokeContextTool(node, f.ctx(), "open-dm-call", "im_open_direct_conversation", openArgs); !errors.Is(err, errContextUnavailable) {
		t.Fatalf("expected cached replay of im_open_direct_conversation to fail after DM revocation, got %v", err)
	}

	// 5. Revoking workspace membership blocks reading membersRef and replaying list-members-call.
	mu.Lock()
	workspaceRevoked = true
	mu.Unlock()
	if _, err := f.a.Read(f.ctx(), membersRef, 0, 0); !errors.Is(err, errContextUnavailable) {
		t.Fatalf("expected errContextUnavailable after workspace revocation, got %v", err)
	}
	if _, err := invokeContextTool(node, f.ctx(), "list-members-call", "im_list_workspace_members", membersArgs); !errors.Is(err, errContextUnavailable) {
		t.Fatalf("expected cached replay of im_list_workspace_members to fail after workspace revocation, got %v", err)
	}
}

func TestAgentGroupCreationMemberInvitationAndMessageRetractionParity(t *testing.T) {
	budget, err := newRunContextBudget(16384, true, true, false, false, true)
	if err != nil {
		t.Fatal(err)
	}
	f := newContextArchiveFixture(t, budget)
	bobID := uuid.NewString()
	charlieID := uuid.NewString()
	groupConvID := uuid.NewString()
	sentMsgID := uuid.NewString()
	var groupRevoked bool
	var mu sync.Mutex
	groupMembers := []string{f.l.AgentID, bobID}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		revoked := groupRevoked
		currentMembers := append([]string(nil), groupMembers...)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet && r.URL.Path == "/api/v1/bootstrap" {
			convs := []Conversation{{ID: f.input.ConversationID, WorkspaceID: f.input.WorkspaceID, Type: "channel"}}
			if !revoked {
				convs = append(convs, Conversation{ID: groupConvID, WorkspaceID: f.input.WorkspaceID, Type: "group", Name: "Architecture Sync", MemberIDs: currentMembers})
			}
			_ = json.NewEncoder(w).Encode(Bootstrap{
				Workspaces:    []Workspace{{ID: f.input.WorkspaceID, Name: "Team"}},
				Conversations: convs,
				SyncCursor:    "1",
			})
			return
		}
		if r.Method == http.MethodPost && r.URL.Path == "/api/v1/workspaces/"+f.input.WorkspaceID+"/conversations" {
			var req struct {
				Type      string   `json:"type"`
				Name      string   `json:"name"`
				MemberIDs []string `json:"memberIds"`
			}
			if json.NewDecoder(r.Body).Decode(&req) != nil || req.Type != "group" || req.Name != "Architecture Sync" || len(req.MemberIDs) != 1 || req.MemberIDs[0] != bobID {
				w.WriteHeader(400)
				_, _ = w.Write([]byte(`{"error":{"code":"invalid_request"}}`))
				return
			}
			w.WriteHeader(201)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"conversation": Conversation{
					ID:          groupConvID,
					WorkspaceID: f.input.WorkspaceID,
					Type:        "group",
					Name:        req.Name,
					MemberIDs:   []string{f.l.AgentID, bobID},
				},
			})
			return
		}
		if r.Method == http.MethodPost && r.URL.Path == "/api/v1/conversations/"+groupConvID+"/members" {
			var req struct {
				UserID string `json:"userId"`
			}
			if json.NewDecoder(r.Body).Decode(&req) != nil || req.UserID != charlieID {
				w.WriteHeader(400)
				_, _ = w.Write([]byte(`{"error":{"code":"invalid_request"}}`))
				return
			}
			mu.Lock()
			groupMembers = []string{f.l.AgentID, bobID, charlieID}
			updated := append([]string(nil), groupMembers...)
			mu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]any{
				"conversation": Conversation{
					ID:          groupConvID,
					WorkspaceID: f.input.WorkspaceID,
					Type:        "group",
					Name:        "Architecture Sync",
					MemberIDs:   updated,
				},
			})
			return
		}
		if r.Method == http.MethodDelete && r.URL.Path == "/api/v1/conversations/"+groupConvID+"/messages/"+sentMsgID {
			retractedAt := "2026-10-26T08:00:00Z"
			_ = json.NewEncoder(w).Encode(map[string]any{
				"message": Message{
					ID:             sentMsgID,
					ConversationID: groupConvID,
					SenderID:       f.l.AgentID,
					SenderName:     "Agent",
					SenderKind:     "agent",
					Seq:            2,
					Body:           "",
					CreatedAt:      "2026-10-26T07:59:00Z",
					RetractedAt:    &retractedAt,
				},
			})
			return
		}
		if r.Method == http.MethodGet && r.URL.Path == "/api/v1/conversations/"+f.input.ConversationID+"/messages/"+f.input.MessageID {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"message": Message{ID: f.input.MessageID, ConversationID: f.input.ConversationID, SenderID: f.input.SenderID, Body: f.input.Body},
			})
			return
		}
		w.WriteHeader(404)
	}))
	t.Cleanup(server.Close)
	f.a.IM.BaseURL = server.URL

	flagsPath := filepath.Join(t.TempDir(), "flags.json")
	if err := os.WriteFile(flagsPath, []byte(`{"revision":"parity-disabled","flags":{"im.dm.create":true,"im.group.create":false,"im.message.retract":false}}`), 0600); err != nil {
		t.Fatal(err)
	}
	tools := &Tools{Store: f.s, Lease: f.l, IM: f.a.IM, AgentID: f.l.AgentID, FlagsFile: flagsPath}
	dmTools, err := tools.BuildDirectMessageTools()
	if err != nil {
		t.Fatal(err)
	}
	node, err := compose.NewToolNode(f.ctx(), &compose.ToolsNodeConfig{Tools: dmTools, ExecuteSequentially: true})
	if err != nil {
		t.Fatal(err)
	}

	// 1. Feature flags gate group creation, member addition, and message retraction.
	disabledGroupRes, err := invokeContextTool(node, f.ctx(), "disabled-group-call", "im_create_group_conversation", createGroupConversationArgs{WorkspaceID: f.input.WorkspaceID, Name: "Architecture Sync", MemberIDs: []string{bobID}})
	if err != nil || !strings.Contains(disabledGroupRes, `"feature_disabled"`) {
		t.Fatalf("expected feature_disabled when im.group.create is false, got %s (%v)", disabledGroupRes, err)
	}
	disabledRetractRes, err := invokeContextTool(node, f.ctx(), "disabled-retract-call", "im_retract_message", retractMessageArgs{ConversationID: groupConvID, MessageID: sentMsgID})
	if err != nil || !strings.Contains(disabledRetractRes, `"feature_disabled"`) {
		t.Fatalf("expected feature_disabled when im.message.retract is false, got %s (%v)", disabledRetractRes, err)
	}

	// Enable im.group.create and im.message.retract.
	if err := os.WriteFile(flagsPath, []byte(`{"revision":"parity-enabled","flags":{"im.dm.create":true,"im.group.create":true,"im.message.retract":true}}`), 0600); err != nil {
		t.Fatal(err)
	}

	// 2. Create group conversation (including self in MemberIDs is deduplicated/ignored cleanly).
	createGroupArgs := createGroupConversationArgs{
		WorkspaceID: f.input.WorkspaceID,
		Name:        "  Architecture Sync  ",
		MemberIDs:   []string{f.l.AgentID, bobID, bobID},
	}
	createGroupRes, err := invokeContextTool(node, f.ctx(), "create-group-call", "im_create_group_conversation", createGroupArgs)
	if err != nil || !strings.Contains(createGroupRes, groupConvID) {
		t.Fatalf("unexpected im_create_group_conversation result: %s (%v)", createGroupRes, err)
	}
	createGroupRef, err := f.a.ArchiveToolPath(f.ctx(), "tool_clear", contextRealIMDetail("im_create_group_conversation", "create-group-call", createGroupArgs, createGroupRes), createGroupRes)
	if err != nil {
		t.Fatal("failed to archive im_create_group_conversation output:", err)
	}
	if _, err := f.a.Read(f.ctx(), createGroupRef, 0, 0); err != nil {
		t.Fatal("expected authorized read of archived im_create_group_conversation:", err)
	}

	// 3. Invite Charlie into the group conversation and archive its provenance.
	addMembersArgs := addConversationMembersArgs{
		ConversationID: groupConvID,
		MemberIDs:      []string{charlieID},
	}
	addMembersRes, err := invokeContextTool(node, f.ctx(), "add-members-call", "im_add_conversation_members", addMembersArgs)
	if err != nil || !strings.Contains(addMembersRes, charlieID) {
		t.Fatalf("unexpected im_add_conversation_members result: %s (%v)", addMembersRes, err)
	}
	addMembersRef, err := f.a.ArchiveToolPath(f.ctx(), "tool_clear", contextRealIMDetail("im_add_conversation_members", "add-members-call", addMembersArgs, addMembersRes), addMembersRes)
	if err != nil {
		t.Fatal("failed to archive im_add_conversation_members output:", err)
	}
	if _, err := f.a.Read(f.ctx(), addMembersRef, 0, 0); err != nil {
		t.Fatal("expected authorized read of archived im_add_conversation_members:", err)
	}

	// 4. Retract an Agent-authored message and archive its tombstone provenance.
	retractArgs := retractMessageArgs{
		ConversationID: groupConvID,
		MessageID:      sentMsgID,
	}
	retractRes, err := invokeContextTool(node, f.ctx(), "retract-msg-call", "im_retract_message", retractArgs)
	if err != nil || !strings.Contains(retractRes, `"retractedAt"`) {
		t.Fatalf("unexpected im_retract_message result: %s (%v)", retractRes, err)
	}
	retractRef, err := f.a.ArchiveToolPath(f.ctx(), "tool_clear", contextRealIMDetail("im_retract_message", "retract-msg-call", retractArgs, retractRes), retractRes)
	if err != nil {
		t.Fatal("failed to archive im_retract_message output:", err)
	}
	if _, err := f.a.Read(f.ctx(), retractRef, 0, 0); err != nil {
		t.Fatal("expected authorized read of archived im_retract_message:", err)
	}

	// 5. Revoking group membership blocks reading and cached replay for all three tools.
	mu.Lock()
	groupRevoked = true
	mu.Unlock()
	for _, ref := range []string{createGroupRef, addMembersRef, retractRef} {
		if _, err := f.a.Read(f.ctx(), ref, 0, 0); !errors.Is(err, errContextUnavailable) {
			t.Fatalf("expected errContextUnavailable for %s after group revocation, got %v", ref, err)
		}
	}
	if _, err := invokeContextTool(node, f.ctx(), "create-group-call", "im_create_group_conversation", createGroupArgs); !errors.Is(err, errContextUnavailable) {
		t.Fatalf("expected cached replay of im_create_group_conversation to fail after group revocation, got %v", err)
	}
	if _, err := invokeContextTool(node, f.ctx(), "add-members-call", "im_add_conversation_members", addMembersArgs); !errors.Is(err, errContextUnavailable) {
		t.Fatalf("expected cached replay of im_add_conversation_members to fail after group revocation, got %v", err)
	}
	if _, err := invokeContextTool(node, f.ctx(), "retract-msg-call", "im_retract_message", retractArgs); !errors.Is(err, errContextUnavailable) {
		t.Fatalf("expected cached replay of im_retract_message to fail after group revocation, got %v", err)
	}
}
