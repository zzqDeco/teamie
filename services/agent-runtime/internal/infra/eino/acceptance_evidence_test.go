package eino

import (
	"context"
	"encoding/json"
	"github.com/google/uuid"
	"strings"
	"teamie/services/agent-runtime/internal/domain"
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
	// Serialize the production tool observation DTO, not an invented projection fixture.
	scheduleResult, err := json.Marshal(Observation{OK: true, Data: map[string]any{"schedule": domain.ScheduleItem{ScheduleID: "sched-test", Kind: "once", Prompt: "SECRET-SCHEDULE", Status: "active"}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = repo.Exec(ctx, `INSERT INTO runtime_tool_calls(run_id,call_id,name,request_hash,result) VALUES($1,'create-schedule','schedule_create','hash',$2)`, root, scheduleResult); err != nil {
		t.Fatal(err)
	}
	if _, err = repo.Exec(ctx, `INSERT INTO runtime_schedules(id,agent_id,created_run_id,kind,prompt,status,run_count,next_run_at,last_run_at) VALUES('sched-test',$1,$2,'once','SECRET-SCHEDULE','completed',1,now(),now())`, agent, root); err != nil {
		t.Fatal(err)
	}
	out, err := repo.ReadAcceptanceEvidence(ctx, agent, message)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Runs) != 2 || len(out.Tools) != 3 {
		t.Fatalf("wrong scope: %+v", out)
	}
	scheduled := false
	denied := false
	for _, call := range out.Tools {
		if call.Name == "schedule_create" {
			scheduled = call.OK && call.Completed && call.ScheduleID == "sched-test" && call.RunID == root
		}
		if call.Name == "im_list_messages" {
			denied = call.Denied && call.ConversationID == "restricted"
		}
		if call.RunID == unrelated {
			t.Fatal("unrelated run leaked")
		}
	}
	if !scheduled || len(out.Schedules) != 1 || out.Schedules[0].ID != "sched-test" || out.Schedules[0].RunID != root || out.Schedules[0].RunCount != 1 || out.Schedules[0].LastRunAt == nil {
		t.Fatalf("schedule evidence lost its creation/firing identity: %+v", out)
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
