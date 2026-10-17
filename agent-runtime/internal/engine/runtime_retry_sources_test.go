package engine

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	wire "teamie/agent-protocol/kitex_gen/agent"
)

func runtimeRetryFixture(t *testing.T, inputs []Input) (*Store, *Lease, *Run, *RuntimeRPC, *wire.RuntimeRequest) {
	t.Helper()
	s := testStore(t)
	ctx := context.Background()
	agent, owner := uuid.NewString(), uuid.NewString()
	lease, err := s.Acquire(ctx, agent, "source-retry")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Release(ctx, lease) })
	if err = s.Ingest(ctx, lease, inputs[len(inputs)-1].EventID, inputs); err != nil {
		t.Fatal(err)
	}
	run, err := s.StartRun(ctx, lease, inputs)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.SaveModel(ctx, lease, run, 1, "original-request", []byte("immutable response")); err != nil {
		t.Fatal(err)
	}
	e := &Engine{Store: s, Config: Config{ControlToken: "service"}, Control: &rpcOwnerControl{definition: &wire.AgentDefinition{Id: agent, OwnerId: owner, Enabled: true, Revision: 1}}}
	enableContextFixture(t, e)
	return s, lease, run, &RuntimeRPC{Engine: e}, &wire.RuntimeRequest{ServiceToken: "service", AgentId: agent, OwnerId: owner, ResourceId: &run.ID}
}

func TestRuntimeRetryRejectsInvalidatedDetachedSourcesWithoutMutation(t *testing.T) {
	inputs := []Input{{EventID: "1", Body: "revoked source"}, {EventID: "2", Body: "still valid source"}}
	s, lease, run, rpc, request := runtimeRetryFixture(t, inputs)
	ctx := context.Background()
	if err := s.InvalidateRun(ctx, lease, run, []string{"1"}); err != nil {
		t.Fatal(err)
	}
	snapshot := func() string {
		t.Helper()
		var raw string
		if err := s.DB.QueryRow(ctx, `SELECT to_jsonb(r)::text FROM runtime_runs r WHERE id=$1`, run.ID).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		return raw
	}
	before, events := snapshot(), count(t, s, `SELECT count(*) FROM runtime_public_events`)
	response, err := rpc.RetryRun(ctx, request)
	if err != nil || response.Ok || response.GetStatusCode() != 409 || response.GetErrorCode() != "run_not_retryable" {
		t.Fatal("source-invalidated Run accepted an unrecoverable retry", response, err)
	}
	if snapshot() != before || count(t, s, `SELECT count(*) FROM runtime_public_events`) != events || count(t, s, `SELECT count(*) FROM runtime_inputs WHERE run_id IS NOT NULL`) != 0 {
		t.Fatal("rejected retry changed the Run, reattached revoked inputs or emitted an event")
	}
	if count(t, s, `SELECT count(*) FROM runtime_inputs WHERE event_id=1 AND state='skipped'`) != 1 || count(t, s, `SELECT count(*) FROM runtime_inputs WHERE event_id=2 AND state='pending'`) != 1 {
		t.Fatal("rejected retry changed invalidated input eligibility")
	}
	if receipt, err := s.CachedModel(ctx, run, 1, "original-request"); err != nil || string(receipt) != "immutable response" {
		t.Fatal("source rejection changed the old immutable tape", err)
	}
	pending, err := s.Pending(ctx, lease)
	if err != nil || len(pending) != 1 || pending[0].EventID != "2" {
		t.Fatal("independent valid input was stranded", pending, err)
	}
	fresh, err := s.StartRun(ctx, lease, pending)
	if err != nil || fresh.ID == run.ID {
		t.Fatal("valid input inherited the source-invalidated Run", err)
	}
	if _, err = s.CachedModel(ctx, fresh, 1, "original-request"); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatal("fresh input inherited the rejected Run's model receipt", err)
	}
}

func TestRuntimeRetryRetainsAttachedInputAndImmutableTape(t *testing.T) {
	s, lease, run, rpc, request := runtimeRetryFixture(t, []Input{{EventID: "1", Body: "valid retry source"}})
	ctx := context.Background()
	if err := s.FinishRun(ctx, lease, run, "failed", "gateway_unavailable", false); err != nil {
		t.Fatal(err)
	}
	response, err := rpc.RetryRun(ctx, request)
	if err != nil || !response.Ok {
		t.Fatal("attached failed input could not be retried", response, err)
	}
	if count(t, s, `SELECT count(*) FROM runtime_runs WHERE id=$1 AND state='retry' AND safe_error='' AND finished_at IS NULL`, run.ID) != 1 || count(t, s, `SELECT count(*) FROM runtime_inputs WHERE run_id=$1 AND state='processing'`, run.ID) != 1 {
		t.Fatal("legitimate retry lost its Run/input association")
	}
	pending, err := s.Pending(ctx, lease)
	if err != nil || len(pending) != 1 || pending[0].EventID != "1" {
		t.Fatal("legitimate retry was not discoverable by the Worker", err)
	}
	if receipt, err := s.CachedModel(ctx, run, 1, "original-request"); err != nil || string(receipt) != "immutable response" {
		t.Fatal("retry replaced the immutable model receipt", err)
	}
}
