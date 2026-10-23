package eino

import (
	"bytes"
	"context"
	"encoding/gob"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	"github.com/cloudwego/kitex/client/callopt"
	"github.com/google/uuid"
	wire "teamie/contracts/gen/agent/kitex_gen/agent"
)

type cancellationCheckpointModel struct {
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (m *cancellationCheckpointModel) Generate(ctx context.Context, _ []*schema.Message, _ ...model.Option) (*schema.Message, error) {
	m.once.Do(func() { close(m.started) })
	<-ctx.Done()
	<-m.release
	return nil, ctx.Err()
}
func (m *cancellationCheckpointModel) Stream(context.Context, []*schema.Message, ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	return nil, errors.New("unexpected streaming in cancellation fixture")
}
func (m *cancellationCheckpointModel) WithTools([]*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	return m, nil
}

type cancellationCheckpointStore struct {
	*SessionStore[*schema.Message]
	mu             sync.Mutex
	sets           int
	setSawCanceled bool
	beforeSet      func()
}

func (s *cancellationCheckpointStore) Set(ctx context.Context, id string, payload []byte) error {
	if s.beforeSet != nil {
		s.beforeSet()
	}
	s.mu.Lock()
	s.sets++
	s.setSawCanceled = s.setSawCanceled || ctx.Err() != nil
	s.mu.Unlock()
	return s.SessionStore.Set(ctx, id, payload)
}

func TestNativeEinoImmediateStopSavesCheckpointAfterRootCancellation(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	agentID := uuid.NewString()
	lease, err := store.Acquire(ctx, agentID, "cancellation-fixture")
	if err != nil {
		t.Fatal(err)
	}
	input := Input{EventID: "1", MessageID: uuid.NewString(), ConversationID: uuid.NewString(), Body: "processing input survives graceful shutdown"}
	if err = store.Ingest(ctx, lease, "1", []Input{input}); err != nil {
		t.Fatal(err)
	}
	run, err := store.StartRun(ctx, lease, []Input{input})
	if err != nil {
		t.Fatal(err)
	}
	blocking := &cancellationCheckpointModel{started: make(chan struct{}), release: make(chan struct{})}
	root, cancel := context.WithCancel(WithRun(ctx, run))
	defer cancel()
	// The real TurnLoop must decide to save its native checkpoint first. Cancel
	// exactly at the PG-store boundary, preserving the original canceled context.
	session := &cancellationCheckpointStore{SessionStore: &SessionStore[*schema.Message]{Store: store, Lease: lease}, beforeSet: cancel}
	stopContributed := make(chan struct{})
	var stoppedOnce sync.Once
	var nativeCancelObserved atomic.Bool
	loop := adk.NewTurnLoop(adk.TurnLoopConfig[Input, *schema.Message]{
		GenInput: func(ctx context.Context, _ *adk.TurnLoop[Input, *schema.Message], items []Input) (*adk.GenInputResult[Input, *schema.Message], error) {
			return &adk.GenInputResult[Input, *schema.Message]{RunCtx: WithRun(ctx, run), Input: &adk.AgentInput{Messages: []*schema.Message{{Role: schema.User, Content: input.Body}}}, Consumed: items}, nil
		},
		PrepareAgent: func(ctx context.Context, _ *adk.TurnLoop[Input, *schema.Message], _ []Input) (adk.Agent, error) {
			return adk.NewChatModelAgent(ctx, &adk.ChatModelAgentConfig{Name: "checkpoint_fixture", Description: "native blocking model fixture", Model: blocking, MaxIterations: 1})
		},
		OnAgentEvents: func(_ context.Context, turn *adk.TurnContext[Input, *schema.Message], events *adk.AsyncIterator[*adk.AgentEvent]) error {
			callbackDone := make(chan struct{})
			defer close(callbackDone)
			go func() {
				select {
				case <-turn.Stopped:
					stoppedOnce.Do(func() { close(stopContributed) })
				case <-callbackDone:
				}
			}()
			for {
				event, ok := events.Next()
				if !ok {
					return nil
				}
				if event != nil && event.Err != nil {
					var nativeCancel *adk.CancelError
					if errors.As(event.Err, &nativeCancel) {
						nativeCancelObserved.Store(true)
					} else {
						return event.Err
					}
				}
			}
		},
		Store: session, CheckpointID: "turnloop:" + agentID, SessionID: "agent:" + agentID, SessionStore: session,
	})
	loop.Run(root)
	loop.Push(input)
	select {
	case <-blocking.started:
	case <-time.After(5 * time.Second):
		t.Fatal("native model never entered generation")
	}
	// Stop returns before watchStop submits the native cancellation. Wait for its
	// public contribution acknowledgment while the model still blocks. Root stays
	// alive until native cleanup invokes the checkpoint store's Set hook.
	loop.Stop(adk.WithImmediate())
	select {
	case <-stopContributed:
	case <-time.After(5 * time.Second):
		cancel()
		close(blocking.release)
		t.Fatal("native immediate stop did not contribute")
	}
	close(blocking.release)
	stopped := make(chan *adk.TurnLoopExitState[Input, *schema.Message], 1)
	go func() { stopped <- loop.Wait() }()
	var exit *adk.TurnLoopExitState[Input, *schema.Message]
	select {
	case exit = <-stopped:
	case <-time.After(10 * time.Second):
		t.Fatal("native turn loop failed to stop and flush")
	}
	if !nativeCancelObserved.Load() {
		t.Fatalf("native CancelError not observed: exit=%v", exit.ExitReason)
	}
	if !exit.CheckpointAttempted || exit.CheckpointErr != nil {
		t.Fatalf("checkpoint attempted=%v error=%v exit=%v", exit.CheckpointAttempted, exit.CheckpointErr, exit.ExitReason)
	}
	session.mu.Lock()
	sets, sawCanceled := session.sets, session.setSawCanceled
	session.mu.Unlock()
	if sets == 0 || !sawCanceled {
		t.Fatal("fixture did not exercise native checkpoint Set with a canceled root context")
	}
	payload, exists, err := session.Get(WithRun(context.Background(), run), "turnloop:"+agentID)
	if err != nil || !exists || len(payload) == 0 {
		t.Fatal("native checkpoint was not persisted", err)
	}
	// Eino's pinned gob checkpoint has exported fields; decode its real payload.
	var checkpoint struct {
		RunnerCheckpoint                           []byte
		HasRunnerState                             bool
		CanceledItems, UnhandledItems, ResumeItems []Input
	}
	if err = gob.NewDecoder(bytes.NewReader(payload)).Decode(&checkpoint); err != nil {
		t.Fatal("native checkpoint payload is not decodable", err)
	}
	if !checkpoint.HasRunnerState || len(checkpoint.RunnerCheckpoint) == 0 {
		t.Fatal("saved checkpoint omitted interrupted native runner state")
	}
	if count(t, store, "SELECT count(*) FROM runtime_inputs WHERE agent_id=$1 AND event_id=1 AND state='processing'", agentID) != 1 {
		t.Fatal("cancellation dropped or completed the processing input")
	}
	if count(t, store, "SELECT count(*) FROM runtime_session_events WHERE run_id=$1 AND attempt_id=$2 AND kind='cancel'", run.ID, run.Attempt) == 0 {
		t.Fatal("native cancellation terminal event was not flushed")
	}
}

func TestCanceledContextPersistenceStillRejectsStaleLease(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	agentID := uuid.NewString()
	first, err := store.Acquire(ctx, agentID, "old-worker")
	if err != nil {
		t.Fatal(err)
	}
	if err = store.Ingest(ctx, first, "1", []Input{{EventID: "1", Body: "durable"}}); err != nil {
		t.Fatal(err)
	}
	run, err := store.StartRun(ctx, first, []Input{{EventID: "1", Body: "durable"}})
	if err != nil {
		t.Fatal(err)
	}
	old := &SessionStore[*schema.Message]{Store: store, Lease: first}
	id := "turnloop:" + agentID
	original := []byte("existing durable checkpoint")
	if err = old.Set(WithRun(ctx, run), id, original); err != nil {
		t.Fatal(err)
	}
	if _, err = store.DB.Exec(ctx, "UPDATE runtime_agents SET lease_until=now()-interval '1 second' WHERE agent_id=$1", agentID); err != nil {
		t.Fatal(err)
	}
	second, err := store.Acquire(ctx, agentID, "new-worker")
	if err != nil {
		t.Fatal(err)
	}
	canceled, cancel := context.WithCancel(WithRun(ctx, run))
	cancel()
	if err = old.Set(canceled, id, []byte("stale overwrite")); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("stale canceled Set bypassed fencing: %v", err)
	}
	if err = old.Delete(canceled, id); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("stale canceled Delete bypassed fencing: %v", err)
	}
	event := &adk.SessionEvent[*schema.Message]{EventID: uuid.NewString(), Message: &schema.Message{Role: schema.User, Content: "stale event"}}
	if err = old.AppendEvents(canceled, "agent:"+agentID, []*adk.SessionEvent[*schema.Message]{event}); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("stale canceled AppendEvents bypassed fencing: %v", err)
	}
	current := &SessionStore[*schema.Message]{Store: store, Lease: second}
	payload, exists, err := current.Get(ctx, id)
	if err != nil || !exists || !bytes.Equal(payload, original) {
		t.Fatal("stale worker changed checkpoint", err)
	}
	if count(t, store, "SELECT count(*) FROM runtime_session_events WHERE event_id=$1", event.EventID) != 0 {
		t.Fatal("stale worker appended a terminal event")
	}
}

type cancellationWorkerGateway struct {
	*fixtureGateway
	started chan struct{}
	blocked atomic.Bool
}

func (g *cancellationWorkerGateway) Chat(ctx context.Context, r *wire.GatewayChatRequest, opts ...callopt.Option) (*wire.GatewayChatResponse, error) {
	if g.blocked.CompareAndSwap(false, true) {
		close(g.started)
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return g.fixtureGateway.Chat(ctx, r, opts...)
}

func TestCanceledWorkerRestartRetainsProcessingInput(t *testing.T) {
	store := testStore(t)
	worker, imFixture, gateway, _ := fixtureEngine(t, store)
	blocking := &cancellationWorkerGateway{fixtureGateway: gateway, started: make(chan struct{})}
	worker.Gateway = blocking
	imFixture.add("durable cancellation restart fixture", "")
	stop, done := startFixtureWorker(t, worker, imFixture)
	select {
	case <-blocking.started:
	case <-time.After(10 * time.Second):
		t.Fatal("worker did not enter its native model turn")
	}
	stop()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("worker did not finish canceled turn")
	}
	if count(t, store, "SELECT count(*) FROM runtime_inputs WHERE state='processing'") != 1 || count(t, store, "SELECT count(*) FROM runtime_inputs WHERE state IN ('done','failed')") != 0 {
		t.Fatal("canceled worker dropped or completed its in-flight input")
	}
	if _, err := store.DB.Exec(context.Background(), "UPDATE runtime_runs SET next_attempt=now()"); err != nil {
		t.Fatal(err)
	}
	stopAgain, doneAgain := startFixtureWorker(t, worker, imFixture)
	waitFor(t, func() bool { return count(t, store, "SELECT count(*) FROM runtime_inputs WHERE state='done'") == 1 })
	stopAgain()
	<-doneAgain
	if count(t, store, "SELECT count(*) FROM runtime_runs WHERE state='completed'") != 1 {
		t.Fatal("restart failed to resume the durable run")
	}
	imFixture.mu.Lock()
	defer imFixture.mu.Unlock()
	if len(imFixture.sent) != 1 || imFixture.posts != 1 {
		t.Fatalf("restart duplicated explicit message effects: unique=%d posts=%d", len(imFixture.sent), imFixture.posts)
	}
}
