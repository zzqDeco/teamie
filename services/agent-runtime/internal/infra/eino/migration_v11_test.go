package eino

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestRuntimeV11PreservesV10ExecutionAndRejectsV10Startup(t *testing.T) {
	dsn, raw := runtimeMigrationDatabase(t)
	ctx := context.Background()
	oldDDL := schemaSQL + schemaV2SQL + schemaV3SQL + contextArchiveSchemaSQL + contextRecoverySchemaSQL + schemaV4SQL + schemaV5SQL + schemaV6SQL + schemaV7SQL + schemaV8SQL + schemaV9SQL + schemaV10SQL
	if _, err := raw.Exec(ctx, `SET teamie.runtime_writer_schema='10';`+oldDDL); err != nil {
		t.Fatal(err)
	}
	agent, runID, attempt := uuid.NewString(), uuid.NewString(), uuid.NewString()
	budget, err := os.ReadFile("../../../../../ops/fixtures/runtime-context-budget-v7.json")
	if err != nil {
		t.Fatal(err)
	}
	_, canonical, err := canonicalContextPolicy(contextProfileV1, budget)
	if err != nil {
		t.Fatal(err)
	}
	checkpoint := checkpointMetadata{Version: nativeRuntimeVersion, Revision: 1, RunID: runID, Attempt: attempt, Targets: []string{"model"}, ToolProfile: currentToolProfile, ContextProfile: contextProfileV1, ContextBudgetHash: digest(canonical)}
	metadata, _ := json.Marshal(checkpoint)
	for _, statement := range []struct {
		query string
		args  []any
	}{
		{`INSERT INTO runtime_agents(agent_id,cursor) VALUES($1,42)`, []any{agent}},
		{`INSERT INTO runtime_runs(id,agent_id,input_key,input_json,attempt_id,context_profile,context_budget,context_head,acceptance_head) VALUES($1,$2,'frozen-v7-pre-web','[{"eventId":"42","body":"unchanged source"}]',$3,$4,$5,0,0)`, []any{runID, agent, attempt, contextProfileV1, canonical}},
		{`INSERT INTO runtime_inputs(agent_id,event_id,payload,run_id,state) VALUES($1,42,'{"eventId":"42","body":"unchanged source"}',$2,'processing')`, []any{agent, runID}},
		{`INSERT INTO runtime_model_calls(run_id,step,request_hash,response) VALUES($1,1,'old-v10-request-hash','old-v10-model-response')`, []any{runID}},
		{`INSERT INTO runtime_tool_calls(run_id,call_id,name,request_hash,result) VALUES($1,'old-v10-send','im_send_message','old-v10-tool-hash','old-v10-send-receipt')`, []any{runID}},
		{`INSERT INTO runtime_checkpoints(checkpoint_id,payload,metadata) VALUES($1,'old-v10-native-checkpoint',$2)`, []any{"turnloop:" + agent, metadata}},
		{`INSERT INTO runtime_context_recoveries(run_id,model_step,context_profile,request_hash,streaming,error_code,decision_version,decision_json) VALUES($1,2,$2,'old-v10-recovery-hash',false,'context_overflow',1,jsonb_build_object('version',1,'action','compact_prefix','budget',$3::jsonb))`, []any{runID, contextProfileV1, canonical}},
	} {
		if _, err = raw.Exec(ctx, statement.query, statement.args...); err != nil {
			t.Fatal(err)
		}
	}
	before := runtimeExecutionSnapshot(t, raw)
	current, err := Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer current.DB.Close()
	if after := runtimeExecutionSnapshot(t, raw); after != before {
		t.Fatal("schema 11 migration changed old Run, input, receipt or checkpoint bytes")
	}
	var ledger, protocol string
	if err = current.DB.QueryRow(ctx, `SELECT string_agg(version::text,',' ORDER BY version),current_setting('teamie.runtime_writer_schema') FROM agent_runtime_schema_migrations`).Scan(&ledger, &protocol); err != nil || ledger != "1,2,3,4,5,6,7,8,9,10,11,12,13" || protocol != "13" {
		t.Fatal("schema-10 reader was not fenced by the new ledger/protocol", ledger, protocol, err)
	}
	oldRun := &Run{ID: runID, toolProfile: currentToolProfile, ContextProfile: contextProfileV1, ContextBudget: canonical}
	if !resumeCompatibleForRun(&checkpoint, 1, oldRun) || webSearchForRun(oldRun) {
		t.Fatal("schema 11 writer fence invalidated an old v10 native checkpoint or enabled web search on an old Run")
	}
	withoutWeb, err := newRunContextBudget(1_000_000, false, false)
	if err != nil || strings.Contains(string(withoutWeb), "webSearch") {
		t.Fatal("omitted webSearch flag changed historical budget snapshot bytes", string(withoutWeb), err)
	}
	withWeb, err := newRunContextBudget(1_000_000, true, true, true)
	if err != nil {
		t.Fatal(err)
	}
	parsedWeb, err := ParseContextBudgetSnapshot(withWeb)
	if err != nil || !parsedWeb.WebSearch || !parsedWeb.FileDelivery || !parsedWeb.StructuredMentions || !parsedWeb.EventDrivenInputs {
		t.Fatal("new Run admission did not freeze webSearch in budget snapshot", parsedWeb, err)
	}
	if !webSearchForRun(&Run{ContextProfile: contextProfileV1, ContextBudget: withWeb}) {
		t.Fatal("webSearchForRun did not recognize admitted webSearch snapshot")
	}
	if _, err = current.DB.Exec(ctx, `INSERT INTO runtime_runs(id,agent_id,input_key,input_json,attempt_id,context_profile,context_budget) VALUES($1,$2,'new-v11-web-budget','[]',$3,$4,$5)`, uuid.NewString(), agent, uuid.NewString(), contextProfileV1, withWeb); err != nil {
		t.Fatal(err)
	}
	beforeFence := runtimeFenceSnapshot(t, raw)
	beforeExecution := runtimeExecutionSnapshot(t, raw)
	_, err = raw.Exec(ctx, oldDDL)
	var rejected *pgconn.PgError
	if !errors.As(err, &rejected) || rejected.Code != "55000" || rejected.Message != "unsupported runtime writer protocol" {
		t.Fatal("complete schema-10 startup was not rejected", err)
	}
	if runtimeFenceSnapshot(t, raw) != beforeFence || runtimeExecutionSnapshot(t, raw) != beforeExecution {
		t.Fatal("rejected schema-10 startup changed the writer fence or durable execution")
	}
}
