package engine

import (
	"context"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cloudwego/kitex/client/callopt"
	wire "teamie/agent-protocol/kitex_gen/agent"
)

type receiptCancellationGateway struct {
	*fixtureGateway
	started chan struct{}
	calls   atomic.Int32
}

func (g *receiptCancellationGateway) Chat(ctx context.Context, request *wire.GatewayChatRequest, opts ...callopt.Option) (*wire.GatewayChatResponse, error) {
	// The second model call begins only after the real IM tool has returned and
	// persisted its completed receipt. Cancel the actual Worker's root there.
	if g.calls.Add(1) == 2 {
		close(g.started)
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return g.fixtureGateway.Chat(ctx, request, opts...)
}

func cancellationReplayReceipts(t *testing.T, store *Store, runID string) []replayReceipt {
	t.Helper()
	rows, err := store.DB.Query(context.Background(), `SELECT 'model:'||step::text,request_hash,response FROM runtime_model_calls WHERE run_id=$1 AND step=1 UNION ALL SELECT 'tool:'||call_id,request_hash,result FROM runtime_tool_calls WHERE run_id=$1 ORDER BY 1`, runID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var receipts []replayReceipt
	for rows.Next() {
		var receipt replayReceipt
		if err = rows.Scan(&receipt.Key, &receipt.Hash, &receipt.Response); err != nil {
			t.Fatal(err)
		}
		receipts = append(receipts, receipt)
	}
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
	return receipts
}

func TestWorkerRootCancellationReplaysCommittedSendWithoutCheckpoint(t *testing.T) {
	store := testStore(t)
	worker, imFixture, base, _ := fixtureEngine(t, store)
	enableContextFixture(t, worker)
	gateway := &receiptCancellationGateway{fixtureGateway: base, started: make(chan struct{})}
	worker.Gateway = gateway
	imFixture.add("persist this explicit send before cancellation", "")
	stop, done := startFixtureWorker(t, worker, imFixture)
	select {
	case <-gateway.started:
	case <-time.After(10 * time.Second):
		t.Fatal("worker did not reach the model after its explicit send")
	}
	var runID, firstAttempt string
	if err := store.DB.QueryRow(context.Background(), `SELECT id,attempt_id FROM runtime_runs WHERE context_profile=$1`, contextProfileV1).Scan(&runID, &firstAttempt); err != nil {
		t.Fatal(err)
	}
	if count(t, store, `SELECT count(*) FROM runtime_model_calls WHERE run_id=$1`, runID) != 1 || count(t, store, `SELECT count(*) FROM runtime_tool_calls WHERE run_id=$1 AND name='im_send_message' AND result IS NOT NULL`, runID) != 1 {
		t.Fatal("cancellation fixture did not first commit both model and send receipts")
	}
	original := cancellationReplayReceipts(t, store, runID)
	if len(original) != 2 {
		t.Fatalf("expected one model and one tool receipt, got %d", len(original))
	}
	stop()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("worker did not exit after external root cancellation")
	}
	if count(t, store, `SELECT count(*) FROM runtime_inputs WHERE run_id=$1 AND state='processing'`, runID) != 1 {
		t.Fatal("external cancellation lost the durable in-flight input")
	}
	// Checkpoint persistence depends on native cancellation timing. Recovery must
	// still work when none is available; do not infer a checkpoint guarantee from
	// the SDK's cooperative Stop path.
	if _, err := store.DB.Exec(context.Background(), `DELETE FROM runtime_checkpoints WHERE checkpoint_id=$1`, "turnloop:"+imFixture.agent); err != nil {
		t.Fatal(err)
	}
	if count(t, store, `SELECT count(*) FROM runtime_checkpoints`) != 0 {
		t.Fatal("restart fixture retained a native checkpoint")
	}
	if _, err := store.DB.Exec(context.Background(), `UPDATE runtime_runs SET next_attempt=now() WHERE id=$1`, runID); err != nil {
		t.Fatal(err)
	}
	stopAgain, doneAgain := startFixtureWorker(t, worker, imFixture)
	waitFor(t, func() bool {
		return count(t, store, `SELECT count(*) FROM runtime_runs WHERE id=$1 AND state='completed'`, runID) == 1
	})
	stopAgain()
	<-doneAgain
	if count(t, store, `SELECT count(*) FROM runtime_runs`) != 1 || count(t, store, `SELECT count(*) FROM runtime_runs WHERE id=$1 AND attempts=2 AND attempt_id<>$2`, runID, firstAttempt) != 1 || count(t, store, `SELECT count(*) FROM runtime_inputs WHERE run_id=$1 AND state='done'`, runID) != 1 {
		t.Fatal("restart did not complete the same durable Run in a new attempt")
	}
	if !reflect.DeepEqual(original, cancellationReplayReceipts(t, store, runID)) {
		t.Fatal("restart rewrote the committed model or send receipt")
	}
	if count(t, store, `SELECT count(DISTINCT session_id) FROM runtime_session_events`) != 1 {
		t.Fatal("receipt recovery created another Agent Session")
	}
	imFixture.mu.Lock()
	defer imFixture.mu.Unlock()
	if imFixture.posts != 1 || len(imFixture.sent) != 1 {
		t.Fatalf("committed send was executed again: posts=%d unique=%d", imFixture.posts, len(imFixture.sent))
	}
	for _, sent := range imFixture.sent {
		if strings.Contains(sent.Body, "PRIVATE_FINAL") {
			t.Fatal("private final text became an automatic IM post")
		}
	}
	t.Log("root cancellation recovered the same Run without a checkpoint; original receipt bytes/hash unchanged and exactly one explicit IM POST")
}
