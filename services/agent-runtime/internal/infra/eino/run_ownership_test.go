package eino

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/google/uuid"
	"teamie/services/agent-runtime/internal/domain"
	"teamie/services/agent-runtime/internal/infra/pg"
)

func TestMixedFreshAndRetryBatchPreservesRunOwnershipAndSendDeduplication(t *testing.T) {
	s := testStore(t)
	e, f, _, _ := fixtureEngine(t, s)
	f.failAfterSend = true
	f.add("first recoverable message", "")

	cancel1, done1 := startFixtureWorker(t, e, f)
	waitFor(t, func() bool {
		return count(t, s, "SELECT count(*) FROM runtime_runs WHERE state='retry'") == 1
	})
	cancel1()
	<-done1

	var runAID string
	if err := s.DB.QueryRow(context.Background(), "SELECT id::text FROM runtime_runs WHERE state='retry'").Scan(&runAID); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	if len(f.sent) != 1 {
		f.mu.Unlock()
		t.Fatalf("expected 1 committed IM message before retry, got %d", len(f.sent))
	}
	f.mu.Unlock()

	// Fresh message arrives while Run A becomes due on the same worker tick:
	// FreshPending pushes event 2 first, Pending pushes event 1 second.
	f.add("second fresh message", "")
	if _, err := s.DB.Exec(context.Background(), "UPDATE runtime_runs SET next_attempt=now() WHERE id=$1", runAID); err != nil {
		t.Fatal(err)
	}

	cancel2, done2 := startFixtureWorker(t, e, f)
	waitFor(t, func() bool {
		return count(t, s, "SELECT count(*) FROM runtime_inputs WHERE state='done'") == 2
	})
	cancel2()
	<-done2

	if got := count(t, s, "SELECT count(*) FROM runtime_runs WHERE state='retry'"); got != 0 {
		t.Fatalf("expected 0 orphan retry runs, got %d", got)
	}
	if got := count(t, s, "SELECT count(*) FROM runtime_runs WHERE state='completed'"); got != 2 {
		t.Fatalf("expected 2 completed runs (RunA resumed + RunB fresh), got %d", got)
	}

	var boundRun1, boundRun2 string
	if err := s.DB.QueryRow(context.Background(), "SELECT run_id::text FROM runtime_inputs WHERE event_id=1").Scan(&boundRun1); err != nil {
		t.Fatal(err)
	}
	if err := s.DB.QueryRow(context.Background(), "SELECT run_id::text FROM runtime_inputs WHERE event_id=2").Scan(&boundRun2); err != nil {
		t.Fatal(err)
	}
	if boundRun1 != runAID {
		t.Fatalf("recoverable input 1 was stolen from %s into %s", runAID, boundRun1)
	}
	if boundRun2 == "" || boundRun2 == runAID {
		t.Fatalf("fresh input 2 did not get its own Run: %q", boundRun2)
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.sent) != 2 {
		t.Fatalf("expected exactly 2 deduplicated IM replies (1 per input), got %d: %#v", len(f.sent), f.sent)
	}
}

func TestMultipleDueRetryRunsRecoverSeriallyWithoutMerging(t *testing.T) {
	s := testStore(t)
	e, f, _, _ := fixtureEngine(t, s)

	f.failAfterSend = true
	f.add("recoverable alpha", "")
	cancel1, done1 := startFixtureWorker(t, e, f)
	waitFor(t, func() bool {
		return count(t, s, "SELECT count(*) FROM runtime_runs WHERE state='retry'") == 1
	})
	cancel1()
	<-done1

	// Leave Run 1 in future backoff while Run 2 enters retry, then make both due alongside a 3rd fresh input.
	var run1ID string
	if err := s.DB.QueryRow(context.Background(), "SELECT id::text FROM runtime_runs WHERE state='retry'").Scan(&run1ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec(context.Background(), "UPDATE runtime_runs SET next_attempt=now()+interval '10 minutes' WHERE id=$1", run1ID); err != nil {
		t.Fatal(err)
	}

	f.mu.Lock()
	f.failAfterSend = true
	f.mu.Unlock()
	f.add("recoverable beta", "")
	cancel2, done2 := startFixtureWorker(t, e, f)
	waitFor(t, func() bool {
		return count(t, s, "SELECT count(*) FROM runtime_runs WHERE state='retry'") == 2
	})
	cancel2()
	<-done2

	f.add("fresh gamma", "")
	if _, err := s.DB.Exec(context.Background(), "UPDATE runtime_runs SET next_attempt=now() WHERE state='retry'"); err != nil {
		t.Fatal(err)
	}

	cancel3, done3 := startFixtureWorker(t, e, f)
	waitFor(t, func() bool {
		return count(t, s, "SELECT count(*) FROM runtime_inputs WHERE state='done'") == 3
	})
	cancel3()
	<-done3

	if got := count(t, s, "SELECT count(*) FROM runtime_runs WHERE state='completed'"); got != 3 {
		t.Fatalf("expected 3 independent completed runs, got %d", got)
	}
	if got := count(t, s, "SELECT count(DISTINCT run_id) FROM runtime_inputs WHERE state='done'"); got != 3 {
		t.Fatalf("expected 3 distinct run bindings across inputs, got %d", got)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.sent) != 3 {
		t.Fatalf("expected 3 deduplicated IM sends, got %d", len(f.sent))
	}
}

func TestStartOrResumeRunSQLFenceRejectsMixedOwnership(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	agentID := uuid.NewString()
	lease, err := s.Acquire(ctx, agentID, "fence-worker")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Release(ctx, lease) })

	if err = s.Ingest(ctx, lease, "3", []Input{
		{EventID: "1", Body: "bound-1"},
		{EventID: "2", Body: "bound-2"},
		{EventID: "3", Body: "fresh-3"},
	}); err != nil {
		t.Fatal(err)
	}
	run1, err := s.StartRun(ctx, lease, []Input{{EventID: "1", Body: "bound-1"}})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.FinishRun(ctx, lease, run1, "retry", "gateway_unavailable", true); err != nil {
		t.Fatal(err)
	}
	run2, err := s.StartRun(ctx, lease, []Input{{EventID: "2", Body: "bound-2"}})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.FinishRun(ctx, lease, run2, "retry", "gateway_unavailable", true); err != nil {
		t.Fatal(err)
	}

	// Attempt 1: [fresh, bound] mixed batch must fail closed without stealing input 1.
	_, err = s.StartRun(ctx, lease, []Input{{EventID: "3", Body: "fresh-3"}, {EventID: "1", Body: "bound-1"}})
	if !errors.Is(err, domain.ErrMixedInputRunOwnership) {
		t.Fatalf("expected ErrMixedInputRunOwnership for [fresh, bound], got %v", err)
	}

	// Attempt 2: [bound1, bound2] mixed batch must also fail closed.
	_, err = s.StartRun(ctx, lease, []Input{{EventID: "1", Body: "bound-1"}, {EventID: "2", Body: "bound-2"}})
	if !errors.Is(err, domain.ErrMixedInputRunOwnership) {
		t.Fatalf("expected ErrMixedInputRunOwnership for [bound1, bound2], got %v", err)
	}

	var owner1, owner2, owner3 string
	if err := s.DB.QueryRow(ctx, "SELECT COALESCE(run_id::text,'') FROM runtime_inputs WHERE agent_id=$1 AND event_id=1", agentID).Scan(&owner1); err != nil {
		t.Fatal(err)
	}
	if err := s.DB.QueryRow(ctx, "SELECT COALESCE(run_id::text,'') FROM runtime_inputs WHERE agent_id=$1 AND event_id=2", agentID).Scan(&owner2); err != nil {
		t.Fatal(err)
	}
	if err := s.DB.QueryRow(ctx, "SELECT COALESCE(run_id::text,'') FROM runtime_inputs WHERE agent_id=$1 AND event_id=3", agentID).Scan(&owner3); err != nil {
		t.Fatal(err)
	}
	if owner1 != run1.ID || owner2 != run2.ID || owner3 != "" {
		t.Fatalf("SQL fence mutated input ownership: owner1=%s owner2=%s owner3=%s", owner1, owner2, owner3)
	}
}

func TestAuditAndReconcileSupersededOrphanRetryRuns(t *testing.T) {
	s := testStore(t)
	pgRepo, ok := s.DB.(*pg.Repository)
	if !ok {
		t.Fatalf("expected *pg.Repository, got %T", s.DB)
	}
	ctx := context.Background()
	agentID := uuid.NewString()
	lease, err := s.Acquire(ctx, agentID, "orphan-audit")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Release(ctx, lease) })

	if err = s.Ingest(ctx, lease, "103", []Input{
		{EventID: "101", Body: "superseded"},
		{EventID: "102", Body: "unconfirmed"},
		{EventID: "103", Body: "healthy-retry"},
	}); err != nil {
		t.Fatal(err)
	}

	// 1. Create a superseded orphan retry Run with a completed tool receipt.
	orphanSupersededID := uuid.NewString()
	orphanSupersededJSON, _ := json.Marshal([]Input{{EventID: "101", Body: "superseded"}})
	if _, err = s.DB.Exec(ctx, `INSERT INTO runtime_runs(id,agent_id,input_key,input_json,state,attempt_id,context_head,acceptance_head,safe_error) VALUES($1,$2,'legacy-101',$3,'retry',$4,0,0,'gateway_unavailable')`, orphanSupersededID, agentID, orphanSupersededJSON, uuid.NewString()); err != nil {
		t.Fatal(err)
	}
	if _, err = s.DB.Exec(ctx, `INSERT INTO runtime_model_calls(run_id,step,request_hash,response) VALUES($1,1,'h1',$2)`, orphanSupersededID, []byte("cached-model")); err != nil {
		t.Fatal(err)
	}
	if _, err = s.DB.Exec(ctx, `INSERT INTO runtime_tool_calls(run_id,call_id,name,request_hash,result) VALUES($1,'call_1','im_send_message','th1',$2)`, orphanSupersededID, []byte(`{"ok":true}`)); err != nil {
		t.Fatal(err)
	}
	successorRun, err := s.StartRun(ctx, lease, []Input{{EventID: "101", Body: "superseded"}})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.FinishRun(ctx, lease, successorRun, "completed", "", false); err != nil {
		t.Fatal(err)
	}

	// 2. Create an orphan retry Run with an unconfirmed tool intent (result IS NULL) -> manual_review_required.
	orphanUnconfirmedID := uuid.NewString()
	orphanUnconfirmedJSON, _ := json.Marshal([]Input{{EventID: "102", Body: "unconfirmed"}})
	if _, err = s.DB.Exec(ctx, `INSERT INTO runtime_runs(id,agent_id,input_key,input_json,state,attempt_id,context_head,acceptance_head,safe_error) VALUES($1,$2,'legacy-102',$3,'retry',$4,0,0,'gateway_unavailable')`, orphanUnconfirmedID, agentID, orphanUnconfirmedJSON, uuid.NewString()); err != nil {
		t.Fatal(err)
	}
	if _, err = s.DB.Exec(ctx, `INSERT INTO runtime_tool_calls(run_id,call_id,name,request_hash,result) VALUES($1,'call_2','im_send_message','th2',NULL)`, orphanUnconfirmedID); err != nil {
		t.Fatal(err)
	}
	successorRun2, err := s.StartRun(ctx, lease, []Input{{EventID: "102", Body: "unconfirmed"}})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.FinishRun(ctx, lease, successorRun2, "completed", "", false); err != nil {
		t.Fatal(err)
	}

	// 3. Create a healthy retry Run that still owns input 103 -> not an orphan.
	healthyRetryRun, err := s.StartRun(ctx, lease, []Input{{EventID: "103", Body: "healthy-retry"}})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.FinishRun(ctx, lease, healthyRetryRun, "retry", "gateway_unavailable", true); err != nil {
		t.Fatal(err)
	}

	auditReport, err := pgRepo.AuditOrphanRuns(ctx, agentID)
	if err != nil {
		t.Fatal(err)
	}
	if auditReport.TotalOrphans != 2 || auditReport.SupersededCompleted != 1 || auditReport.ManualReview != 1 || auditReport.ReconciledCount != 0 {
		t.Fatalf("unexpected audit report: %+v", auditReport)
	}

	reconcileReport, err := pgRepo.ReconcileOrphanRuns(ctx, agentID)
	if err != nil {
		t.Fatal(err)
	}
	if reconcileReport.ReconciledCount != 1 {
		t.Fatalf("expected 1 reconciled orphan run, got %+v", reconcileReport)
	}

	if got := count(t, s, "SELECT count(*) FROM runtime_runs WHERE id=$1 AND state='canceled' AND safe_error='superseded_orphan_run' AND finished_at IS NOT NULL", orphanSupersededID); got != 1 {
		t.Fatalf("superseded orphan run was not transitioned cleanly")
	}
	if got := count(t, s, "SELECT count(*) FROM runtime_tool_calls WHERE run_id=$1 AND result IS NOT NULL", orphanSupersededID); got != 1 {
		t.Fatalf("historical tool receipt was deleted during reconciliation")
	}
	if got := count(t, s, "SELECT count(*) FROM runtime_model_calls WHERE run_id=$1", orphanSupersededID); got != 1 {
		t.Fatalf("historical model receipt was deleted during reconciliation")
	}
	if got := count(t, s, "SELECT count(*) FROM runtime_runs WHERE id=$1 AND state='retry'", orphanUnconfirmedID); got != 1 {
		t.Fatalf("manual-review orphan run was unexpectedly modified")
	}
	if got := count(t, s, "SELECT count(*) FROM runtime_runs WHERE id=$1 AND state='retry'", healthyRetryRun.ID); got != 1 {
		t.Fatalf("healthy retry run was unexpectedly modified")
	}
}
