package main

import (
	"context"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	"teamie/pkg/postgres"
	"teamie/services/agent-runtime/internal/infra/pg"
)

func TestExecuteOrphanRunsSetsWriterSchemaProtocolForReconcile(t *testing.T) {
	baseDSN := os.Getenv("TEAMIE_RUNTIME_TEST_DATABASE_URL")
	if baseDSN == "" {
		t.Skip("TEAMIE_RUNTIME_TEST_DATABASE_URL is unset; disposable PostgreSQL required")
	}

	ctx := context.Background()
	adminPool, err := postgres.Open(ctx, baseDSN)
	if err != nil {
		t.Fatal(err)
	}
	schemaName := "runtimectl_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err := adminPool.Exec(ctx, "CREATE SCHEMA "+schemaName); err != nil {
		adminPool.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = adminPool.Exec(ctx, "DROP SCHEMA "+schemaName+" CASCADE")
		adminPool.Close()
	})

	u, err := url.Parse(baseDSN)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	q.Set("search_path", schemaName)
	u.RawQuery = q.Encode()
	schemaDSN := u.String()

	// Apply full runtime migrations (including 015_human_presence_v2 writer guard)
	// and seed a superseded orphan retry Run plus its completed successor Run.
	seededRepo, err := pg.OpenRepository(ctx, schemaDSN)
	if err != nil {
		t.Fatal(err)
	}
	agentID := uuid.NewString()
	orphanRunID := uuid.NewString()
	successorRunID := uuid.NewString()
	orphanAttemptID := uuid.NewString()
	successorAttemptID := uuid.NewString()

	if _, err := seededRepo.Exec(ctx, `
		INSERT INTO runtime_runs(id, agent_id, input_key, input_json, state, attempts, attempt_id, safe_error)
		VALUES
		  ($1, $3, 'k-orphan', '[{"eventId":"42"}]'::jsonb, 'retry', 1, $4, 'gateway_unavailable'),
		  ($2, $3, 'k-successor', '[{"eventId":"42"}]'::jsonb, 'completed', 1, $5, '')`,
		orphanRunID, successorRunID, agentID, orphanAttemptID, successorAttemptID,
	); err != nil {
		seededRepo.Close()
		t.Fatal(err)
	}
	if _, err := seededRepo.Exec(ctx, `
		INSERT INTO runtime_inputs(agent_id, event_id, payload, state, run_id)
		VALUES ($1, 42, '{}'::jsonb, 'done', $2)`,
		agentID, successorRunID,
	); err != nil {
		seededRepo.Close()
		t.Fatal(err)
	}
	seededRepo.Close()

	// Confirm the schema 15 writer guard actively blocks raw pools lacking teamie.runtime_writer_schema.
	unguardedPool, err := postgres.Open(ctx, schemaDSN)
	if err != nil {
		t.Fatal(err)
	}
	_, guardErr := pg.NewFromPool(unguardedPool).ReconcileOrphanRuns(ctx, agentID)
	unguardedPool.Close()
	if guardErr == nil || !strings.Contains(guardErr.Error(), "unsupported runtime writer protocol") {
		t.Fatalf("expected unguarded pool to fail writer protocol trigger, got %v", guardErr)
	}

	// Audit mode succeeds under default_transaction_read_only=on without mutating state.
	auditReport, opened, err := executeOrphanRuns(ctx, schemaDSN, "audit", agentID)
	if err != nil || !opened {
		t.Fatalf("audit failed (opened=%v): %v", opened, err)
	}
	if len(auditReport.Runs) != 1 || auditReport.Runs[0].Classification != "superseded_completed" || auditReport.Runs[0].Reconciled {
		t.Fatalf("unexpected audit report: %+v", auditReport)
	}

	// Reconcile mode sets teamie.runtime_writer_schema and atomically cancels the superseded orphan Run.
	reconcileReport, opened, err := executeOrphanRuns(ctx, schemaDSN, "reconcile", agentID)
	if err != nil || !opened {
		t.Fatalf("reconcile failed (opened=%v): %v", opened, err)
	}
	if reconcileReport.ReconciledCount != 1 || len(reconcileReport.Runs) != 1 || !reconcileReport.Runs[0].Reconciled {
		t.Fatalf("unexpected reconcile report: %+v", reconcileReport)
	}

	verifyPool, err := postgres.Open(ctx, schemaDSN, postgres.WithRuntimeParam("default_transaction_read_only", "on"))
	if err != nil {
		t.Fatal(err)
	}
	defer verifyPool.Close()
	var state, safeErr string
	if err := verifyPool.QueryRow(ctx, `SELECT state, safe_error FROM runtime_runs WHERE id = $1`, orphanRunID).Scan(&state, &safeErr); err != nil {
		t.Fatal(err)
	}
	if state != "canceled" || safeErr != "superseded_orphan_run" {
		t.Fatalf("expected canceled/superseded_orphan_run, got state=%q safe_error=%q", state, safeErr)
	}
}
