package eino

import (
	"context"
	"encoding/json"
	"github.com/google/uuid"
	"strings"
	"teamie/services/agent-runtime/internal/infra/pg"
	"testing"
)

func TestAcceptanceEvidenceFollowsCausalRunsAndRedactsPrivateData(t *testing.T) {
	dsn, _ := runtimeMigrationDatabase(t)
	ctx := context.Background()
	repo, err := pg.OpenRepository(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()
	agent, message, root, wake, unrelated := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	for _, id := range []string{root, wake, unrelated} {
		if _, err = repo.Exec(ctx, `INSERT INTO runtime_runs(id,agent_id,input_key,input_json,attempt_id,state) VALUES($1::uuid,$2,$1::text,'[]',$3,'completed')`, id, agent, uuid.NewString()); err != nil {
			t.Fatal(err)
		}
	}
	for i, entry := range []struct{ run, payload string }{{root, `{"messageId":"` + message + `","body":"SECRET-PROMPT"}`}, {wake, `{"kind":"schedule.wakeup","parentRunId":"` + root + `","scheduleId":"sched-test","body":"SECRET-PROMPT"}`}, {unrelated, `{"messageId":"unrelated","body":"SECRET-PROMPT"}`}} {
		if _, err = repo.Exec(ctx, `INSERT INTO runtime_inputs(agent_id,event_id,payload,run_id,state) VALUES($1,$2,$3,$4,'done')`, agent, i+1, entry.payload, entry.run); err != nil {
			t.Fatal(err)
		}
	}
	args := json.RawMessage(`{"conversationId":"restricted"}`)
	saved, _ := json.Marshal(map[string]any{"name": "im_list_messages", "arguments": args})
	if _, err = repo.Exec(ctx, `INSERT INTO runtime_tool_calls(run_id,call_id,name,request_hash,result) VALUES($1,'acl','im_list_messages',$2,$3)`, root, digest(args), []byte(`{"ok":false,"error":"forbidden"}`)); err != nil {
		t.Fatal(err)
	}
	if _, err = repo.Exec(ctx, `INSERT INTO runtime_tool_observations(run_id,call_id,observation_key,response) VALUES($1,'acl','context-im-arguments-v1',$2)`, root, saved); err != nil {
		t.Fatal(err)
	}
	for _, run := range []string{wake, unrelated} {
		if _, err = repo.Exec(ctx, `INSERT INTO runtime_tool_calls(run_id,call_id,name,request_hash,result) VALUES($1,'send','im_send_message','hash',$2)`, run, []byte(`{"ok":true,"data":{"id":"reply","conversationId":"channel","body":"SECRET-OUTPUT"}}`)); err != nil {
			t.Fatal(err)
		}
	}
	out, err := repo.ReadAcceptanceEvidence(ctx, agent, message)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Runs) != 2 || len(out.Tools) != 2 {
		t.Fatalf("wrong scope: %+v", out)
	}
	denied := false
	for _, call := range out.Tools {
		if call.Name == "im_list_messages" {
			denied = call.Denied && call.ConversationID == "restricted"
		}
		if call.RunID == unrelated {
			t.Fatal("unrelated run leaked")
		}
	}
	if !denied {
		t.Fatal("missing attested denied read")
	}
	raw, _ := json.Marshal(out)
	if strings.Contains(string(raw), "SECRET") || strings.Contains(string(raw), "arguments") {
		t.Fatal("private output leaked")
	}
	if _, err = repo.Exec(ctx, `UPDATE runtime_tool_calls SET request_hash='mismatched' WHERE run_id=$1 AND call_id='acl'`, root); err != nil {
		t.Fatal(err)
	}
	out, err = repo.ReadAcceptanceEvidence(ctx, agent, message)
	if err != nil {
		t.Fatal(err)
	}
	for _, call := range out.Tools {
		if call.Name == "im_list_messages" && call.ConversationID != "" {
			t.Fatal("unverified arguments accepted")
		}
	}
	out, err = repo.ReadAcceptanceEvidence(ctx, uuid.NewString(), message)
	if err != nil || len(out.Runs) != 0 {
		t.Fatal("cross-Agent evidence", err)
	}
}
