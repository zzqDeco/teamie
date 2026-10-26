package eino

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/cloudwego/eino/compose"
	"github.com/google/uuid"
)

func TestGroupCreationReceiptCrashDoesNotDuplicateConversation(t *testing.T) {
	budget, _ := newRunContextBudget(16384, true, true, false, false, true)
	f := newContextArchiveFixture(t, budget)
	created := 0
	keys := map[string]string{}
	var receivedKeys []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			key := r.Header.Get("Idempotency-Key")
			receivedKeys = append(receivedKeys, key)
			id := keys[key]
			if key == "" || id == "" {
				created++
				id = uuid.NewString()
				keys[key] = id
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"conversation": Conversation{ID: id, WorkspaceID: f.input.WorkspaceID, Type: "group", MemberIDs: []string{f.l.AgentID, uuid.NewString()}}})
			return
		}
		w.WriteHeader(404)
	}))
	defer server.Close()
	f.a.IM.BaseURL = server.URL
	available, err := (&Tools{Store: f.s, Lease: f.l, IM: f.a.IM, AgentID: f.l.AgentID}).BuildDirectMessageTools()
	if err != nil {
		t.Fatal(err)
	}
	node, err := compose.NewToolNode(f.ctx(), &compose.ToolsNodeConfig{Tools: available, ExecuteSequentially: true})
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.s.DB.Exec(f.ctx(), `CREATE FUNCTION review_fail_group_receipt() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected receipt failure'; END; $$; CREATE TRIGGER review_fail_group_receipt BEFORE UPDATE OF result ON runtime_tool_calls FOR EACH ROW EXECUTE FUNCTION review_fail_group_receipt()`)
	if err != nil {
		t.Fatal(err)
	}
	args := createGroupConversationArgs{WorkspaceID: f.input.WorkspaceID, MemberIDs: []string{uuid.NewString()}}
	if _, err = invokeContextTool(node, f.ctx(), "same-group-call", "im_create_group_conversation", args); err == nil {
		t.Fatal("receipt failure not injected")
	}
	_, err = f.s.DB.Exec(f.ctx(), `DROP TRIGGER review_fail_group_receipt ON runtime_tool_calls`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = invokeContextTool(node, f.ctx(), "same-group-call", "im_create_group_conversation", args); err != nil {
		t.Fatal(err)
	}
	if len(receivedKeys) != 2 || receivedKeys[0] == "" || receivedKeys[0] != receivedKeys[1] {
		t.Fatalf("replay did not preserve the IM request key: %v", receivedKeys)
	}
	if created != 1 {
		t.Fatalf("one logical tool call created %d distinct group conversations across receipt replay", created)
	}
}

func TestMemberAdditionReceiptReplayKeepsPerTargetRequestKeys(t *testing.T) {
	budget, _ := newRunContextBudget(16384, true, true, false, false, true)
	f := newContextArchiveFixture(t, budget)
	targets := []string{uuid.NewString(), uuid.NewString()}
	keys := map[string]string{}
	counts := map[string]int{}
	present := map[string]bool{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(404)
			return
		}
		var body struct {
			UserID string `json:"userId"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		key := r.Header.Get("Idempotency-Key")
		counts[body.UserID]++
		if original, ok := keys[body.UserID]; !ok {
			keys[body.UserID] = key
			present[body.UserID] = true
		} else if key == "" || key != original {
			present[body.UserID] = true // A new logical operation would re-admit it.
		}
		members := []string{f.l.AgentID}
		for id, admitted := range present {
			if admitted {
				members = append(members, id)
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"conversation": Conversation{ID: f.input.ConversationID, WorkspaceID: f.input.WorkspaceID, Type: "group", MemberIDs: members}})
	}))
	defer server.Close()
	f.a.IM.BaseURL = server.URL
	available, err := (&Tools{Store: f.s, Lease: f.l, IM: f.a.IM, AgentID: f.l.AgentID}).BuildDirectMessageTools()
	if err != nil {
		t.Fatal(err)
	}
	node, err := compose.NewToolNode(f.ctx(), &compose.ToolsNodeConfig{Tools: available, ExecuteSequentially: true})
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.s.DB.Exec(f.ctx(), `CREATE FUNCTION fail_member_receipt() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected receipt failure'; END; $$; CREATE TRIGGER fail_member_receipt BEFORE UPDATE OF result ON runtime_tool_calls FOR EACH ROW EXECUTE FUNCTION fail_member_receipt()`)
	if err != nil {
		t.Fatal(err)
	}
	args := addConversationMembersArgs{ConversationID: f.input.ConversationID, MemberIDs: targets}
	if _, err = invokeContextTool(node, f.ctx(), "same-member-call", "im_add_conversation_members", args); err == nil {
		t.Fatal("receipt failure not injected")
	}
	present[targets[0]] = false // Moderator removes one invitee before retry.
	if _, err = f.s.DB.Exec(f.ctx(), `DROP TRIGGER fail_member_receipt ON runtime_tool_calls`); err != nil {
		t.Fatal(err)
	}
	if _, err = invokeContextTool(node, f.ctx(), "same-member-call", "im_add_conversation_members", args); err != nil {
		t.Fatal(err)
	}
	if keys[targets[0]] == "" || keys[targets[1]] == "" || keys[targets[0]] == keys[targets[1]] || counts[targets[0]] != 2 || counts[targets[1]] != 2 || present[targets[0]] {
		t.Fatalf("member replay changed request identity: keys=%v counts=%v present=%v", keys, counts, present)
	}
}
