package eino

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

// Seed a Run using the previous admission default, without changing production
// code or the worker's new default. It has no new behavior selection in storage.
func seedPreviousContextRun(t *testing.T, e *Engine, f *fixtureIM, source Message) *Run {
	t.Helper()
	ctx := context.Background()
	lease, err := e.Store.Acquire(ctx, f.agent, e.owner)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Store.Release(ctx, lease)
	input := Input{EventID: "1", WorkspaceID: f.workspace, ConversationID: source.ConversationID, MessageID: source.ID, SenderID: source.SenderID, SenderName: source.SenderName, SenderKind: source.SenderKind, Body: source.Body, ReplyToID: source.ReplyToID, Seq: source.Seq, CreatedAt: source.CreatedAt}
	if err = e.Store.Ingest(ctx, lease, "1", []Input{input}); err != nil {
		t.Fatal(err)
	}
	budget, err := NewContextBudgetSnapshot(16384)
	if err != nil {
		t.Fatal(err)
	}
	run, err := e.Store.StartRunWithContextPolicy(ctx, lease, []Input{input}, contextProfileV1, budget)
	if err != nil {
		t.Fatal(err)
	}
	return run
}

func TestEventDrivenRuntimeDeliversNewInputsWithoutModelHistoryCalls(t *testing.T) {
	s := testStore(t)
	e, f, base, _ := fixtureEngine(t, s)
	enableContextFixture(t, e)
	gateway := &blockingGatewayFixture{fixtureGateway: base, first: make(chan struct{}), release: make(chan struct{})}
	e.Gateway = gateway
	f.add("silent current work", "")
	cancel, done := startFixtureWorker(t, e, f)
	select {
	case <-gateway.first:
	case <-time.After(5 * time.Second):
		t.Fatal("initial model call did not start")
	}
	// A different source buffers during the active turn. The model never asks
	// for history or conversation lists to learn about this arriving message.
	f.add("silent later input", uuid.NewString())
	waitFor(t, func() bool { return count(t, s, `SELECT count(*) FROM runtime_inputs WHERE state='pending'`) == 1 })
	close(gateway.release)
	waitFor(t, func() bool { return count(t, s, `SELECT count(*) FROM runtime_runs WHERE state='completed'`) == 2 })
	cancel()
	<-done
	if count(t, s, `SELECT count(*) FROM runtime_tool_calls`) != 0 || count(t, s, `SELECT count(*) FROM runtime_inputs WHERE state='done'`) != 2 {
		t.Fatal("delivery required a model history call or lost a durable input")
	}
	gateway.mu.Lock()
	requests := append([]toolPathRequest(nil), gateway.requests...)
	gateway.mu.Unlock()
	if len(requests) != 2 {
		t.Fatal("unexpected model work", len(requests))
	}
	for _, request := range requests {
		behaviorHasGuidance(t, behaviorSystem(t, behaviorRequest{Messages: request.Messages}), "teamie-event-driven-inputs-v1", "Runtime will wake the Session")
	}
	if !strings.Contains(requests[1].Messages[len(requests[1].Messages)-1].Content, "silent later input") {
		t.Fatal("later source body did not enter the serial Session")
	}
	if count(t, s, `SELECT count(*) FROM runtime_runs WHERE context_budget->>'eventDrivenInputs'='true'`) != 2 {
		t.Fatal("worker did not freeze the new selection at admission")
	}
}

func TestEventDrivenPolicyAllowsRequestedOriginalVerification(t *testing.T) {
	s := testStore(t)
	e, f, base, _ := fixtureEngine(t, s)
	enableContextFixture(t, e)
	historical := Message{ID: uuid.NewString(), ConversationID: f.conversation, SenderID: uuid.NewString(), SenderKind: "human", Body: "The original agreed amount is 42.", Seq: 1, CreatedAt: "2026-10-01T00:00:00Z"}
	f.messages[historical.ID] = &historical
	source := behaviorSource(t, f, "Please verify the original agreement and report its exact amount.", "", "")
	gateway := &behaviorGateway{fixtureGateway: base}
	gateway.respond = func(index int, request behaviorRequest) (chatMessage, error) {
		if !strings.Contains(request.Messages[0].Content, "explicitly asks to verify the original") {
			return chatMessage{}, fmt.Errorf("new guidance dropped explicit verification")
		}
		switch index {
		case 0:
			return toolPathCall("im_get_message", "requested-original", getMessageArgs{ConversationID: historical.ConversationID, MessageID: historical.ID})
		case 1:
			observation, err := toolPathLast(toolPathRequest{Messages: request.Messages}, "requested-original")
			if err != nil || !observation.OK || !strings.Contains(request.Messages[len(request.Messages)-1].Content, historical.Body) {
				return chatMessage{}, fmt.Errorf("authorized original was not retrieved: %v", err)
			}
			return toolPathCall("im_send_message", "verified-amount", sendArgs{ConversationID: source.ConversationID, ReplyToID: source.ID, Body: "The original agreed amount is 42."})
		case 2:
			return chatMessage{Role: "assistant", Content: "PRIVATE_DONE"}, nil
		default:
			return chatMessage{}, fmt.Errorf("unexpected additional model request")
		}
	}
	e.Gateway = gateway
	cancel, done := startFixtureWorker(t, e, f)
	waitFor(t, func() bool {
		return count(t, s, `SELECT count(*) FROM runtime_runs WHERE state IN ('completed','failed')`) == 1
	})
	cancel()
	<-done
	if len(gateway.snapshot(t)) != 3 || count(t, s, `SELECT count(*) FROM runtime_runs WHERE state='completed'`) != 1 || count(t, s, `SELECT count(*) FROM runtime_tool_calls WHERE name='im_get_message' AND result IS NOT NULL`) != 1 {
		t.Fatal("event-driven guidance blocked needed original verification")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.posts != 1 || len(f.sent) != 1 {
		t.Fatal("verified result was not delivered exactly once")
	}
}

func TestEventDrivenDefaultPreservesPreviousRunReplayAndSendReceipts(t *testing.T) {
	s := testStore(t)
	e, f, base, _ := fixtureEngine(t, s)
	enableContextFixture(t, e)
	source := behaviorSource(t, f, "a previous-policy request", "", "")
	old := seedPreviousContextRun(t, e, f, source)
	send, err := toolPathCall("im_send_message", "frozen-send", sendArgs{ConversationID: source.ConversationID, ReplyToID: source.ID, Body: "Original result"})
	if err != nil {
		t.Fatal(err)
	}
	gateway := &replayProfileGateway{fixtureGateway: base, steps: []replayFixtureStep{
		{message: send},
		{errorCode: "gateway_unavailable"},
		{message: chatMessage{Role: "assistant", Content: "PRIVATE_NEW_RUN"}},
		{message: chatMessage{Role: "assistant", Content: "PRIVATE_RECOVERED"}},
	}}
	e.Gateway = gateway
	cancel, done := startFixtureWorker(t, e, f)
	waitFor(t, func() bool {
		return count(t, s, `SELECT count(*) FROM runtime_runs WHERE id=$1 AND state='retry'`, old.ID) == 1
	})
	cancel()
	<-done
	var originalHash string
	if err = s.DB.QueryRow(context.Background(), `SELECT request_hash FROM runtime_model_calls WHERE run_id=$1 AND step=1`, old.ID).Scan(&originalHash); err != nil {
		t.Fatal(err)
	}
	// Accept a new-policy Run in the same Session before recovering the old
	// one. Neither that later policy nor its accepted context may leak back.
	if _, err = s.DB.Exec(context.Background(), `UPDATE runtime_runs SET next_attempt=now()+interval '1 hour' WHERE id=$1`, old.ID); err != nil {
		t.Fatal(err)
	}
	f.add("silent current event-driven request", "")
	cancel, done = startFixtureWorker(t, e, f)
	waitFor(t, func() bool {
		return count(t, s, `SELECT count(*) FROM runtime_runs WHERE id<>$1 AND state='completed'`, old.ID) == 1
	})
	cancel()
	<-done
	if _, err = s.DB.Exec(context.Background(), `UPDATE runtime_runs SET next_attempt=now() WHERE id=$1`, old.ID); err != nil {
		t.Fatal(err)
	}
	cancel, done = startFixtureWorker(t, e, f)
	waitFor(t, func() bool {
		return count(t, s, `SELECT count(*) FROM runtime_runs WHERE id=$1 AND state IN ('completed','failed')`, old.ID) == 1
	})
	cancel()
	<-done
	requests := gateway.snapshot(t)
	if len(requests) != 4 || count(t, s, `SELECT count(*) FROM runtime_runs WHERE id=$1 AND state='completed'`, old.ID) != 1 {
		t.Fatal("previous-policy model tape did not replay under the new default")
	}
	for i, request := range requests {
		var decoded behaviorRequest
		if err = decodeFixtureRequest(request.RequestJson, &decoded); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(behaviorSystem(t, decoded), "teamie-event-driven-inputs-v1") != (i == 2) {
			t.Fatal("new and recovered Run policies crossed the Session boundary", i)
		}
		if i == 3 && strings.Contains(request.RequestJson, "silent current event-driven request") {
			t.Fatal("later accepted Session context entered the old replay")
		}
	}
	if count(t, s, `SELECT count(DISTINCT session_id) FROM runtime_session_events`) != 1 {
		t.Fatal("policy selection created a second Session")
	}
	var retainedBudget []byte
	var retainedHash string
	if err = s.DB.QueryRow(context.Background(), `SELECT context_budget FROM runtime_runs WHERE id=$1`, old.ID).Scan(&retainedBudget); err != nil {
		t.Fatal(err)
	}
	if err = s.DB.QueryRow(context.Background(), `SELECT request_hash FROM runtime_model_calls WHERE run_id=$1 AND step=1`, old.ID).Scan(&retainedHash); err != nil {
		t.Fatal(err)
	}
	_, canonical, err := canonicalContextPolicy(contextProfileV1, retainedBudget)
	if err != nil || string(canonical) != string(old.ContextBudget) || retainedHash != originalHash || count(t, s, `SELECT count(*) FROM runtime_tool_calls WHERE run_id=$1 AND name='im_send_message' AND result IS NOT NULL`, old.ID) != 1 {
		t.Fatal("replay changed frozen budget/hash or lost the send receipt")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.posts != 1 || len(f.sent) != 1 {
		t.Fatal("replay repeated a committed IM effect")
	}
}

func TestEventDrivenDefaultKeepsPreviousNativeCheckpointAndSelectsNextRun(t *testing.T) {
	s := testStore(t)
	e, f, base, _ := fixtureEngine(t, s)
	enableContextFixture(t, e)
	source := behaviorSource(t, f, "silent old checkpoint source", "", "")
	old := seedPreviousContextRun(t, e, f, source)
	gateway := &blockingGatewayFixture{fixtureGateway: base, first: make(chan struct{}), release: make(chan struct{})}
	e.Gateway = gateway
	writeEnabled := func(enabled bool) {
		t.Helper()
		flags, _ := json.Marshal(map[string]any{"flags": map[string]bool{"agents.runtime.v3": enabled, "agents.tasks.execute": true, "agents.context.v1": true}})
		if err := os.WriteFile(e.Config.FlagsFile, flags, 0600); err != nil {
			t.Fatal(err)
		}
	}
	cancel, done := startFixtureWorker(t, e, f)
	select {
	case <-gateway.first:
	case <-time.After(5 * time.Second):
		t.Fatal("previous-policy model call did not start")
	}
	writeEnabled(false)
	var checkpoint *checkpointMetadata
	waitFor(t, func() bool {
		value, err := s.ResumeMetadata(context.Background(), "turnloop:"+f.agent)
		if err == nil && value != nil && resumeCompatible(value, 1, old.ID) {
			checkpoint = value
			return true
		}
		return false
	})
	cancel()
	<-done
	if checkpoint.ContextBudgetHash != digest(old.ContextBudget) {
		t.Fatal("checkpoint changed the old budget hash")
	}
	if _, err := s.DB.Exec(context.Background(), `UPDATE runtime_runs SET next_attempt=now() WHERE id=$1`, old.ID); err != nil {
		t.Fatal(err)
	}
	writeEnabled(true)
	cancel, done = startFixtureWorker(t, e, f)
	waitFor(t, func() bool {
		return count(t, s, `SELECT count(*) FROM runtime_runs WHERE id=$1 AND state='completed' AND cardinality(context_attempts)>0`, old.ID) == 1
	})
	f.add("silent next new Run", "")
	waitFor(t, func() bool { return count(t, s, `SELECT count(*) FROM runtime_runs WHERE state='completed'`) == 2 })
	cancel()
	<-done
	gateway.mu.Lock()
	requests := append([]toolPathRequest(nil), gateway.requests...)
	gateway.mu.Unlock()
	if len(requests) != 3 {
		t.Fatal("native checkpoint was reset or unexpectedly replayed", len(requests))
	}
	first := behaviorSystem(t, behaviorRequest{Messages: requests[0].Messages})
	resumed := behaviorSystem(t, behaviorRequest{Messages: requests[1].Messages})
	fresh := behaviorSystem(t, behaviorRequest{Messages: requests[2].Messages})
	if first != resumed || !reflect.DeepEqual(requests[0].Tools, requests[1].Tools) || strings.Contains(resumed, "teamie-event-driven-inputs-v1") || !strings.Contains(fresh, "teamie-event-driven-inputs-v1") {
		t.Fatal("native Resume changed the captured policy/tools or next Run missed new guidance")
	}
	var budget []byte
	if err := s.DB.QueryRow(context.Background(), `SELECT context_budget FROM runtime_runs WHERE id=$1`, old.ID).Scan(&budget); err != nil {
		t.Fatal("native Resume rewrote old snapshot", err)
	}
	_, canonical, err := canonicalContextPolicy(contextProfileV1, budget)
	if err != nil || string(canonical) != string(old.ContextBudget) {
		t.Fatal("native Resume rewrote old snapshot", err)
	}
}
