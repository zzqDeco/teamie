package eino

import (
	"bytes"
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
	def := &wire.AgentDefinition{Id: "agent", OwnerId: "owner", Name: "Agent"}
	oldInstruction := contextInstruction(def, ContextBudgetSnapshot{EventDrivenInputs: true})
	if strings.Contains(oldInstruction, "im_open_direct_conversation") {
		t.Fatal("old instruction leaked directMessageGuidance")
	}
	if !strings.Contains(directMessageGuidance, "im_list_workspace_members") || !strings.Contains(directMessageGuidance, "im_open_direct_conversation") {
		t.Fatal("directMessageGuidance missing tool instructions")
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
