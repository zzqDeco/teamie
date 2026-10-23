package eino

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

func runtimeMigrationDatabase(t *testing.T) (string, *pgx.Conn) {
	t.Helper()
	dsn := os.Getenv("TEAMIE_RUNTIME_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEAMIE_RUNTIME_TEST_DATABASE_URL is unset; disposable PostgreSQL required")
	}
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	name := "runtime_fence_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	quoted := pgx.Identifier{name}.Sanitize()
	if _, err = admin.Exec(ctx, "CREATE SCHEMA "+quoted); err != nil {
		admin.Close(ctx)
		t.Fatal(err)
	}
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	query := u.Query()
	query.Set("search_path", name)
	query.Del(runtimeWriterSchemaSetting)
	u.RawQuery = query.Encode()
	raw, err := pgx.Connect(ctx, u.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		raw.Close(ctx)
		_, _ = admin.Exec(ctx, "DROP SCHEMA "+quoted+" CASCADE")
		admin.Close(ctx)
	})
	return u.String(), raw
}

func runtimeV037DDL(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile("testdata/runtime-v037-schema.sql")
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// Snapshot durable execution data and the ledger, including their original
// hashes/timestamps. Metadata below additionally proves earlier DDL rolls back.
func runtimeFenceSnapshot(t *testing.T, conn *pgx.Conn) string {
	t.Helper()
	var raw string
	err := conn.QueryRow(context.Background(), `SELECT jsonb_build_object(
 'agents',(SELECT jsonb_agg(to_jsonb(t) ORDER BY agent_id) FROM runtime_agents t),
 'inputs',(SELECT jsonb_agg(to_jsonb(t) ORDER BY agent_id,event_id) FROM runtime_inputs t),
 'runs',(SELECT jsonb_agg(to_jsonb(t) ORDER BY id) FROM runtime_runs t),
 'models',(SELECT jsonb_agg(to_jsonb(t) ORDER BY run_id,step) FROM runtime_model_calls t),
 'ledger',(SELECT jsonb_agg(to_jsonb(t) ORDER BY version) FROM agent_runtime_schema_migrations t),
 'guard',pg_get_functiondef('agent_runtime_check_writer()'::regprocedure),
 'pendingIndex',to_regclass('runtime_inputs_pending')::text)::text`).Scan(&raw)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestRuntimeSchemaUpgradePreservesDataAndRejectsV037Startup(t *testing.T) {
	legacy := runtimeV037DDL(t)
	for _, version := range []int{1, 2} {
		t.Run(fmt.Sprintf("schema%d", version), func(t *testing.T) {
			dsn, raw := runtimeMigrationDatabase(t)
			ctx := context.Background()
			ddl := legacy
			if version == 1 {
				ddl, _, _ = strings.Cut(ddl, "-- schemaV2SQL\n")
			}
			if _, err := raw.Exec(ctx, ddl); err != nil {
				t.Fatal(err)
			}
			agent, run, attempt := uuid.NewString(), uuid.NewString(), uuid.NewString()
			if _, err := raw.Exec(ctx, `INSERT INTO runtime_agents(agent_id,cursor) VALUES($1,99)`, agent); err != nil {
				t.Fatal(err)
			}
			if _, err := raw.Exec(ctx, `INSERT INTO runtime_runs(id,agent_id,input_key,input_json,attempt_id,state,committed_attempt) VALUES($1,$2,'99','[{"eventId":"99","body":"durable legacy input"}]',$3,'completed',$3)`, run, agent, attempt); err != nil {
				t.Fatal(err)
			}
			if _, err := raw.Exec(ctx, `INSERT INTO runtime_inputs(agent_id,event_id,payload,run_id) VALUES($1,99,'{"eventId":"99","body":"durable legacy input"}',$2)`, agent, run); err != nil {
				t.Fatal(err)
			}
			if _, err := raw.Exec(ctx, `INSERT INTO runtime_model_calls(run_id,step,request_hash,response) VALUES($1,1,'immutable-request-hash',$2)`, run, []byte("immutable-model-response")); err != nil {
				t.Fatal(err)
			}
			current, err := Open(ctx, dsn)
			if err != nil {
				t.Fatal(err)
			}
			defer current.DB.Close()
			var cursor, body, profile, budget, request string
			var response []byte
			var ledger, protocol string
			if err = current.DB.QueryRow(ctx, `SELECT a.cursor::text,i.payload->>'body',r.context_profile,r.context_budget::text,m.request_hash,m.response FROM runtime_agents a JOIN runtime_inputs i ON i.agent_id=a.agent_id JOIN runtime_runs r ON r.id=i.run_id JOIN runtime_model_calls m ON m.run_id=r.id`).Scan(&cursor, &body, &profile, &budget, &request, &response); err != nil {
				t.Fatal(err)
			}
			if cursor != "99" || body != "durable legacy input" || profile != contextProfileLegacy || budget != "{}" || request != "immutable-request-hash" || string(response) != "immutable-model-response" {
				t.Fatal("forward migration changed legacy durable execution data")
			}
			if err = current.DB.QueryRow(ctx, `SELECT string_agg(version::text,',' ORDER BY version),current_setting('teamie.runtime_writer_schema') FROM agent_runtime_schema_migrations`).Scan(&ledger, &protocol); err != nil || ledger != "1,2,3,4,5,6,7,8,9,10,11,12" || protocol != "12" {
				t.Fatal("current writer did not install its schema/protocol", ledger, protocol, err)
			}
			// The old DDL recreates this index before its INSERT(1) ON CONFLICT.
			// Rejection must roll that creation back, not merely skip the INSERT.
			if _, err = current.DB.Exec(ctx, `DROP INDEX runtime_inputs_pending`); err != nil {
				t.Fatal(err)
			}
			before := runtimeFenceSnapshot(t, raw)
			tx, err := raw.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(704205)`); err != nil {
				t.Fatal(err)
			}
			_, err = tx.Exec(ctx, legacy)
			var rejected *pgconn.PgError
			if !errors.As(err, &rejected) || rejected.Code != "55000" || rejected.Message != "unsupported runtime writer protocol" {
				tx.Rollback(ctx)
				t.Fatal("complete v037 Open DDL was not fenced", err)
			}
			if err = tx.Rollback(ctx); err != nil {
				t.Fatal(err)
			}
			if after := runtimeFenceSnapshot(t, raw); after != before {
				t.Fatal("rejected v037 startup changed schema, ledger or durable data")
			}
			restarted, err := Open(ctx, dsn)
			if err != nil {
				t.Fatal("current image could not restart after rejecting v037", err)
			}
			restarted.DB.Close()
		})
	}
}

func TestRuntimeOpenRejectsInvalidLedgerBeforeReplacingGuard(t *testing.T) {
	future := []string{}
	for version := 1; version <= runtimeSchemaVersion+1; version++ {
		future = append(future, fmt.Sprintf("(%d)", version))
	}
	for _, test := range []struct {
		name     string
		versions string
	}{
		{"future", strings.Join(future, ",")},
		{"gap", "(1),(3)"},
		{"missing-first", "(2),(3)"},
		{"nonpositive", "(0),(2),(3)"},
		{"empty", ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			dsn, raw := runtimeMigrationDatabase(t)
			ctx := context.Background()
			current, err := Open(ctx, dsn)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = current.DB.Exec(ctx, `DELETE FROM agent_runtime_schema_migrations`); err != nil {
				t.Fatal(err)
			}
			if test.versions != "" {
				if _, err = current.DB.Exec(ctx, `INSERT INTO agent_runtime_schema_migrations(version) VALUES`+test.versions); err != nil {
					t.Fatal(err)
				}
			}
			// A future database may have a stricter guard. Open must diagnose its
			// ledger before any CREATE OR REPLACE could downgrade that function.
			if _, err = current.DB.Exec(ctx, `CREATE OR REPLACE FUNCTION agent_runtime_check_writer() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'future writer guard must remain unchanged' USING ERRCODE='55000'; END; $$; DROP INDEX runtime_inputs_pending;`); err != nil {
				t.Fatal(err)
			}
			current.DB.Close()
			before := runtimeFenceSnapshot(t, raw)
			rejected, err := Open(ctx, dsn)
			if rejected != nil {
				rejected.DB.Close()
			}
			if !errors.Is(err, errUnsupportedRuntimeSchema) {
				t.Fatal("invalid ledger was not rejected before executing migration DDL", err)
			}
			if after := runtimeFenceSnapshot(t, raw); after != before {
				t.Fatal("unsupported startup repaired a ledger or overwrote the future guard")
			}
		})
	}
}

func TestRuntimeV4PreservesFrozenV1RunAndRejectsV3Startup(t *testing.T) {
	dsn, raw := runtimeMigrationDatabase(t)
	ctx := context.Background()
	if _, err := raw.Exec(ctx, `SET teamie.runtime_writer_schema='3';`+schemaSQL+schemaV2SQL+schemaV3SQL+contextArchiveSchemaSQL+contextRecoverySchemaSQL); err != nil {
		t.Fatal(err)
	}
	agent, run, attempt := uuid.NewString(), uuid.NewString(), uuid.NewString()
	budget, err := os.ReadFile("../../../../../ops/fixtures/runtime-context-budget-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = raw.Exec(ctx, `INSERT INTO runtime_agents(agent_id) VALUES($1)`, agent); err != nil {
		t.Fatal(err)
	}
	if _, err = raw.Exec(ctx, `INSERT INTO runtime_runs(id,agent_id,input_key,input_json,attempt_id,context_profile,context_budget) VALUES($1,$2,'frozen-v1','[]',$3,$4,$5)`, run, agent, attempt, contextProfileV1, budget); err != nil {
		t.Fatal(err)
	}
	if _, err = raw.Exec(ctx, `INSERT INTO runtime_model_calls(run_id,step,request_hash,response) VALUES($1,1,'original-v1-hash','original-v1-response')`, run); err != nil {
		t.Fatal(err)
	}
	current, err := Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer current.DB.Close()
	var unchanged bool
	if err = current.DB.QueryRow(ctx, `SELECT r.context_profile=$2 AND r.context_budget=$3::jsonb AND m.request_hash='original-v1-hash' AND m.response='original-v1-response'::bytea FROM runtime_runs r JOIN runtime_model_calls m ON m.run_id=r.id WHERE r.id=$1`, run, contextProfileV1, budget).Scan(&unchanged); err != nil || !unchanged {
		t.Fatal("schema 4 changed the prior frozen policy or receipt", err)
	}
	before := runtimeFenceSnapshot(t, raw)
	tx, err := raw.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	_, err = tx.Exec(ctx, schemaSQL+schemaV2SQL+schemaV3SQL+contextArchiveSchemaSQL+contextRecoverySchemaSQL)
	var rejected *pgconn.PgError
	if !errors.As(err, &rejected) || rejected.Code != "55000" || rejected.Message != "unsupported runtime writer protocol" {
		t.Fatal("version 3 startup was not fenced", err)
	}
	if err = tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	if after := runtimeFenceSnapshot(t, raw); after != before {
		t.Fatal("rejected old startup changed durable data or the writer guard")
	}
}

func TestRuntimeV6PreservesFrozenV3AndRejectsV5Startup(t *testing.T) {
	dsn, raw := runtimeMigrationDatabase(t)
	ctx := context.Background()
	if _, err := raw.Exec(ctx, `SET teamie.runtime_writer_schema='5';`+schemaSQL+schemaV2SQL+schemaV3SQL+contextArchiveSchemaSQL+contextRecoverySchemaSQL+schemaV4SQL+schemaV5SQL); err != nil {
		t.Fatal(err)
	}
	agent, run, attempt := uuid.NewString(), uuid.NewString(), uuid.NewString()
	budget, err := os.ReadFile("../../../../../ops/fixtures/runtime-context-budget-v3.json")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = raw.Exec(ctx, `INSERT INTO runtime_agents(agent_id) VALUES($1)`, agent); err != nil {
		t.Fatal(err)
	}
	if _, err = raw.Exec(ctx, `INSERT INTO runtime_runs(id,agent_id,input_key,input_json,attempt_id,context_profile,context_budget) VALUES($1,$2,'frozen-v3','[]',$3,$4,$5)`, run, agent, attempt, contextProfileV1, budget); err != nil {
		t.Fatal(err)
	}
	if _, err = raw.Exec(ctx, `INSERT INTO runtime_model_calls(run_id,step,request_hash,response) VALUES($1,1,'original-v3-hash','original-v3-response')`, run); err != nil {
		t.Fatal(err)
	}
	current, err := Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer current.DB.Close()
	var unchanged bool
	if err = current.DB.QueryRow(ctx, `SELECT r.context_budget=$2::jsonb AND m.request_hash='original-v3-hash' AND m.response='original-v3-response'::bytea FROM runtime_runs r JOIN runtime_model_calls m ON m.run_id=r.id WHERE r.id=$1`, run, budget).Scan(&unchanged); err != nil || !unchanged {
		t.Fatal("schema 6 changed a frozen v3 Run or receipt", err)
	}
	before := runtimeFenceSnapshot(t, raw)
	if _, err = raw.Exec(ctx, schemaSQL+schemaV2SQL+schemaV3SQL+contextArchiveSchemaSQL+contextRecoverySchemaSQL+schemaV4SQL+schemaV5SQL); err == nil {
		t.Fatal("schema 5 startup remained authorized after migration")
	} else {
		var rejected *pgconn.PgError
		if !errors.As(err, &rejected) || rejected.Code != "55000" {
			t.Fatal("unexpected old-writer failure", err)
		}
	}
	if after := runtimeFenceSnapshot(t, raw); before != after {
		t.Fatal("rejected startup changed durable data or the writer guard")
	}
}

func TestRuntimeV7PreservesFrozenV4AndRejectsV6Startup(t *testing.T) {
	dsn, raw := runtimeMigrationDatabase(t)
	ctx := context.Background()
	if _, err := raw.Exec(ctx, `SET teamie.runtime_writer_schema='6';`+schemaSQL+schemaV2SQL+schemaV3SQL+contextArchiveSchemaSQL+contextRecoverySchemaSQL+schemaV4SQL+schemaV5SQL+schemaV6SQL); err != nil {
		t.Fatal(err)
	}
	agent, run, attempt := uuid.NewString(), uuid.NewString(), uuid.NewString()
	budget, err := os.ReadFile("../../../../../ops/fixtures/runtime-context-budget-v4.json")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = raw.Exec(ctx, `INSERT INTO runtime_agents(agent_id) VALUES($1)`, agent); err != nil {
		t.Fatal(err)
	}
	if _, err = raw.Exec(ctx, `INSERT INTO runtime_runs(id,agent_id,input_key,input_json,attempt_id,context_profile,context_budget) VALUES($1,$2,'frozen-v4','[]',$3,$4,$5)`, run, agent, attempt, contextProfileV1, budget); err != nil {
		t.Fatal(err)
	}
	if _, err = raw.Exec(ctx, `INSERT INTO runtime_model_calls(run_id,step,request_hash,response) VALUES($1,1,'original-v4-hash','original-v4-response')`, run); err != nil {
		t.Fatal(err)
	}
	current, err := Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer current.DB.Close()
	var unchanged bool
	if err = current.DB.QueryRow(ctx, `SELECT r.context_budget=$2::jsonb AND m.request_hash='original-v4-hash' AND m.response='original-v4-response'::bytea FROM runtime_runs r JOIN runtime_model_calls m ON m.run_id=r.id WHERE r.id=$1`, run, budget).Scan(&unchanged); err != nil || !unchanged {
		t.Fatal("schema 7 changed a frozen v4 Run or receipt", err)
	}
	before := runtimeFenceSnapshot(t, raw)
	if _, err = raw.Exec(ctx, schemaSQL+schemaV2SQL+schemaV3SQL+contextArchiveSchemaSQL+contextRecoverySchemaSQL+schemaV4SQL+schemaV5SQL+schemaV6SQL); err == nil {
		t.Fatal("schema 6 startup remained authorized after migration")
	} else {
		var rejected *pgconn.PgError
		if !errors.As(err, &rejected) || rejected.Code != "55000" {
			t.Fatal("unexpected old-writer failure", err)
		}
	}
	if after := runtimeFenceSnapshot(t, raw); before != after {
		t.Fatal("rejected startup changed durable data or the writer guard")
	}
}

func TestRuntimeV8PreservesFrozenV5AndRejectsV7Startup(t *testing.T) {
	dsn, raw := runtimeMigrationDatabase(t)
	ctx := context.Background()
	if _, err := raw.Exec(ctx, `SET teamie.runtime_writer_schema='7';`+schemaSQL+schemaV2SQL+schemaV3SQL+contextArchiveSchemaSQL+contextRecoverySchemaSQL+schemaV4SQL+schemaV5SQL+schemaV6SQL+schemaV7SQL); err != nil {
		t.Fatal(err)
	}
	agent, run, attempt := uuid.NewString(), uuid.NewString(), uuid.NewString()
	budget, err := os.ReadFile("../../../../../ops/fixtures/runtime-context-budget-v5.json")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = raw.Exec(ctx, `INSERT INTO runtime_agents(agent_id) VALUES($1)`, agent); err != nil {
		t.Fatal(err)
	}
	if _, err = raw.Exec(ctx, `INSERT INTO runtime_runs(id,agent_id,input_key,input_json,attempt_id,context_profile,context_budget) VALUES($1,$2,'frozen-v5','[]',$3,$4,$5)`, run, agent, attempt, contextProfileV1, budget); err != nil {
		t.Fatal(err)
	}
	if _, err = raw.Exec(ctx, `INSERT INTO runtime_model_calls(run_id,step,request_hash,response) VALUES($1,1,'original-v5-hash','original-v5-response')`, run); err != nil {
		t.Fatal(err)
	}
	current, err := Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer current.DB.Close()
	var unchanged bool
	if err = current.DB.QueryRow(ctx, `SELECT r.context_budget=$2::jsonb AND m.request_hash='original-v5-hash' AND m.response='original-v5-response'::bytea FROM runtime_runs r JOIN runtime_model_calls m ON m.run_id=r.id WHERE r.id=$1`, run, budget).Scan(&unchanged); err != nil || !unchanged {
		t.Fatal("schema 8 changed a frozen v5 Run or receipt", err)
	}
	before := runtimeFenceSnapshot(t, raw)
	if _, err = raw.Exec(ctx, schemaSQL+schemaV2SQL+schemaV3SQL+contextArchiveSchemaSQL+contextRecoverySchemaSQL+schemaV4SQL+schemaV5SQL+schemaV6SQL+schemaV7SQL); err == nil {
		t.Fatal("schema 7 startup remained authorized after migration")
	} else {
		var rejected *pgconn.PgError
		if !errors.As(err, &rejected) || rejected.Code != "55000" {
			t.Fatal("unexpected old-writer failure", err)
		}
	}
	if after := runtimeFenceSnapshot(t, raw); before != after {
		t.Fatal("rejected startup changed durable data or the writer guard")
	}
}
