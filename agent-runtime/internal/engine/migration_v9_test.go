package engine

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

func runtimeExecutionSnapshot(t *testing.T, conn *pgx.Conn) string {
	t.Helper()
	var snapshot string
	err := conn.QueryRow(context.Background(), `SELECT jsonb_build_object(
 'agents',(SELECT jsonb_agg(to_jsonb(t) ORDER BY agent_id) FROM runtime_agents t),
 'inputs',(SELECT jsonb_agg(to_jsonb(t) ORDER BY agent_id,event_id) FROM runtime_inputs t),
 'runs',(SELECT jsonb_agg(to_jsonb(t) ORDER BY id) FROM runtime_runs t),
 'events',(SELECT jsonb_agg(to_jsonb(t) ORDER BY seq) FROM runtime_session_events t),
 'models',(SELECT jsonb_agg(to_jsonb(t) ORDER BY run_id,step) FROM runtime_model_calls t),
 'tools',(SELECT jsonb_agg(to_jsonb(t) ORDER BY run_id,call_id) FROM runtime_tool_calls t),
 'checkpoints',(SELECT jsonb_agg(to_jsonb(t) ORDER BY checkpoint_id) FROM runtime_checkpoints t),
 'recoveries',(SELECT jsonb_agg(to_jsonb(t) ORDER BY run_id,model_step) FROM runtime_context_recoveries t))::text`).Scan(&snapshot)
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func TestRuntimeV9PreservesV8ExecutionAndRejectsV8Startup(t *testing.T) {
	dsn, raw := runtimeMigrationDatabase(t)
	ctx := context.Background()
	oldDDL := schemaSQL + schemaV2SQL + schemaV3SQL + contextArchiveSchemaSQL + contextRecoverySchemaSQL + schemaV4SQL + schemaV5SQL + schemaV6SQL + schemaV7SQL + schemaV8SQL
	if _, err := raw.Exec(ctx, `SET teamie.runtime_writer_schema='8';`+oldDDL); err != nil {
		t.Fatal(err)
	}
	agent, runID, attempt := uuid.NewString(), uuid.NewString(), uuid.NewString()
	budget, err := os.ReadFile("../../../ops/fixtures/runtime-context-budget-v6.json")
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
		{`INSERT INTO runtime_runs(id,agent_id,input_key,input_json,attempt_id,context_profile,context_budget,context_head,acceptance_head) VALUES($1,$2,'frozen-v6','[{"eventId":"42","body":"unchanged source"}]',$3,$4,$5,0,0)`, []any{runID, agent, attempt, contextProfileV1, canonical}},
		{`INSERT INTO runtime_inputs(agent_id,event_id,payload,run_id,state) VALUES($1,42,'{"eventId":"42","body":"unchanged source"}',$2,'processing')`, []any{agent, runID}},
		{`INSERT INTO runtime_model_calls(run_id,step,request_hash,response) VALUES($1,1,'old-request-hash','old-model-response')`, []any{runID}},
		{`INSERT INTO runtime_tool_calls(run_id,call_id,name,request_hash,result) VALUES($1,'old-send','im_send_message','old-tool-hash','old-send-receipt')`, []any{runID}},
		{`INSERT INTO runtime_checkpoints(checkpoint_id,payload,metadata) VALUES($1,'old-native-checkpoint',$2)`, []any{"turnloop:" + agent, metadata}},
		{`INSERT INTO runtime_context_recoveries(run_id,model_step,context_profile,request_hash,streaming,error_code,decision_version,decision_json) VALUES($1,2,$2,'old-recovery-hash',false,'context_overflow',1,jsonb_build_object('version',1,'action','compact_prefix','budget',$3::jsonb))`, []any{runID, contextProfileV1, canonical}},
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
		t.Fatal("writer fence migration changed old Run, input, receipt or checkpoint bytes")
	}
	var ledger, protocol string
	if err = current.DB.QueryRow(ctx, `SELECT string_agg(version::text,',' ORDER BY version),current_setting('teamie.runtime_writer_schema') FROM agent_runtime_schema_migrations`).Scan(&ledger, &protocol); err != nil || ledger != "1,2,3,4,5,6,7,8,9,10,11,12" || protocol != "12" {
		t.Fatal("schema-8 reader was not fenced by the new ledger/protocol", ledger, protocol, err)
	}
	oldRun := &Run{ID: runID, toolProfile: currentToolProfile, ContextProfile: contextProfileV1, ContextBudget: canonical}
	if !resumeCompatibleForRun(&checkpoint, 1, oldRun) {
		t.Fatal("writer fence invalidated an old native checkpoint")
	}
	newBudget, err := newRunContextBudget(16384, false, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = current.DB.Exec(ctx, `INSERT INTO runtime_runs(id,agent_id,input_key,input_json,attempt_id,context_profile,context_budget) VALUES($1,$2,'new-event-driven','[]',$3,$4,$5)`, uuid.NewString(), agent, uuid.NewString(), contextProfileV1, newBudget); err != nil {
		t.Fatal(err)
	}
	beforeFence := runtimeFenceSnapshot(t, raw)
	beforeExecution := runtimeExecutionSnapshot(t, raw)
	// A v8 Open first rejects ledger 9. Even bypassing that read and executing
	// its complete, unchanged startup DDL must fail before replacing the guard.
	_, err = raw.Exec(ctx, oldDDL)
	var rejected *pgconn.PgError
	if !errors.As(err, &rejected) || rejected.Code != "55000" || rejected.Message != "unsupported runtime writer protocol" {
		t.Fatal("complete schema-8 startup was not rejected", err)
	}
	if runtimeFenceSnapshot(t, raw) != beforeFence || runtimeExecutionSnapshot(t, raw) != beforeExecution {
		t.Fatal("rejected old startup changed the writer fence or durable execution")
	}
	restarted, err := Open(ctx, dsn)
	if err != nil {
		t.Fatal("current writer could not restart after rejecting v8", err)
	}
	restarted.DB.Close()
}

func TestEventDrivenRestoreFixtureKeepsCapacityVersionSix(t *testing.T) {
	raw, err := os.ReadFile("../../../ops/fixtures/runtime-context-budget-event-driven-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	got, err := ParseContextBudgetSnapshot(raw)
	if err != nil {
		t.Fatal(err)
	}
	expected, err := makeContextBudgetSnapshotVersion(1_000_000, 6)
	expected.EventDrivenInputs = true
	if err != nil || got != expected {
		t.Fatal("new restore fixture changed capacity or missed the frozen guidance selection", err)
	}
}
