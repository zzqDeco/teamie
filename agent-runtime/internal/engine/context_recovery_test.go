package engine

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	"github.com/cloudwego/kitex/client/callopt"
	wire "teamie/agent-protocol/kitex_gen/agent"
	"teamie/agent-protocol/kitex_gen/agent/modelgatewayservice"
	"teamie/agent-protocol/modeltransport"
)

type recordingContextModel struct {
	inner    *GatewayModel
	mu       sync.Mutex
	requests [][]byte
}

func (m *recordingContextModel) record(ctx context.Context, msgs []*schema.AgenticMessage, stream bool, options []model.Option) {
	r, _ := CurrentRun(ctx)
	opts := model.GetCommonOptions(nil, options...)
	_ = m.inner.capRunOutput(r, opts)
	raw, _ := agenticRequest(msgs, opts, stream)
	m.mu.Lock()
	m.requests = append(m.requests, raw)
	m.mu.Unlock()
}
func (m *recordingContextModel) Generate(ctx context.Context, msgs []*schema.AgenticMessage, opts ...model.Option) (*schema.AgenticMessage, error) {
	m.record(ctx, msgs, false, opts)
	return m.inner.Generate(ctx, msgs, opts...)
}
func (m *recordingContextModel) Stream(ctx context.Context, msgs []*schema.AgenticMessage, opts ...model.Option) (*schema.StreamReader[*schema.AgenticMessage], error) {
	m.record(ctx, msgs, true, opts)
	return m.inner.Stream(ctx, msgs, opts...)
}

func contextModelRun(t *testing.T) (*Store, *Lease, *Run) {
	t.Helper()
	s, lease, run := nativeModelRun(t)
	ctx := context.Background()
	if _, err := s.DB.Exec(ctx, contextRecoverySchemaSQL); err != nil {
		t.Fatal(err)
	}
	budget, err := NewContextBudgetSnapshot(16384)
	if err != nil {
		t.Fatal(err)
	}
	run.ContextProfile = contextProfileV1
	run.ContextBudget = budget
	if _, err = s.DB.Exec(ctx, `UPDATE runtime_runs SET context_profile=$2,context_budget=$3 WHERE id=$1`, run.ID, run.ContextProfile, budget); err != nil {
		t.Fatal(err)
	}
	return s, lease, run
}

type overflowGatewayFixture struct {
	modelgatewayservice.Client
	calls      atomic.Int32
	requestsMu sync.Mutex
	requests   []string
	code       string
}

func (f *overflowGatewayFixture) Chat(_ context.Context, r *wire.GatewayChatRequest, _ ...callopt.Option) (*wire.GatewayChatResponse, error) {
	f.calls.Add(1)
	f.requestsMu.Lock()
	f.requests = append(f.requests, r.RequestJson)
	f.requestsMu.Unlock()
	code := f.code
	return &wire.GatewayChatResponse{Ok: false, ErrorCode: &code}, nil
}

func TestContextRecoveryFailureTapeReplaysAndRejectsChangedRequest(t *testing.T) {
	s, lease, run := contextModelRun(t)
	ctx := WithRun(context.Background(), run)
	f := &overflowGatewayFixture{code: "context_overflow"}
	g := &GatewayModel{Store: s, Lease: lease, Client: f, AgentID: lease.AgentID, ContextWindow: func() int { return 4096 }}
	input := []*schema.AgenticMessage{schema.UserAgenticMessage("stable input")}
	_, err := g.Generate(ctx, input)
	var failure *ModelError
	if !errors.As(err, &failure) || failure.Code != "context_overflow" || failure.Retry {
		t.Fatal("overflow not a durable native decision", err)
	}
	if count(t, s, `SELECT count(*) FROM runtime_context_recoveries`) != 1 || count(t, s, `SELECT count(*) FROM runtime_model_calls`) != 0 {
		t.Fatal("failure/success receipt boundary wrong")
	}
	run.Step.Store(0)
	_, err = g.Generate(ctx, input)
	if !errors.As(err, &failure) || f.calls.Load() != 1 {
		t.Fatal("restart repeated rejected request", err)
	}
	run.Step.Store(0)
	_, err = g.Generate(ctx, []*schema.AgenticMessage{schema.UserAgenticMessage("changed input")})
	if !errors.Is(err, ErrReplayConflict) || f.calls.Load() != 1 {
		t.Fatal("failure tape accepted changed request", err)
	}
	var raw []byte
	if err = s.DB.QueryRow(ctx, `SELECT decision_json FROM runtime_context_recoveries`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "stable input") || strings.Contains(string(raw), "token") {
		t.Fatal("decision stored request content")
	}
	// Saving failure after success must be fenced and mutually exclusive.
	if err = s.SaveModel(ctx, lease, run, 9, "positive", []byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	if err = s.SaveContextRecovery(ctx, lease, run, 9, "positive", false, "context_overflow"); !errors.Is(err, ErrReplayConflict) {
		t.Fatal("positive receipt coexisted with failure", err)
	}
	old := *lease
	old.Fence--
	if err = s.SaveContextRecovery(ctx, &old, run, 10, "stale", false, "context_overflow"); !errors.Is(err, ErrLeaseLost) {
		t.Fatal("stale fence wrote recovery", err)
	}
}

func TestContextRecoveryBoundedHTTPErrorClassification(t *testing.T) {
	for _, body := range []string{`{"ok":false,"errorCode":"context_overflow","errorMessage":"must-not-escape","statusCode":400}`, `{"error":{"code":"context_overflow","message":"must-not-escape"}}`} {
		response := &http.Response{StatusCode: 413, Body: io.NopCloser(strings.NewReader(body))}
		err := readGatewayHTTPFailure(response)
		if err.Code != "context_overflow" || err.Retry || strings.Contains(err.Error(), "must-not-escape") {
			t.Fatal("lost sanitized HTTP overflow", err)
		}
	}
	for _, body := range []string{`{"error":{"code":"unknown-provider-private-code","message":"secret"}}`, `{"ok":false,"errorCode":"context_overflow","extra":"secret"}`, strings.Repeat("x", 4097), `{"ok":false,"errorCode":"context_overflow"} {}`} {
		err := readGatewayHTTPFailure(&http.Response{StatusCode: 400, Body: io.NopCloser(strings.NewReader(body))})
		if err.Code == "context_overflow" || strings.Contains(err.Error(), "secret") {
			t.Fatal("unstructured error enabled recovery", err)
		}
	}
}

func TestContextRecoverySuccessAndFailureAreMutuallyExclusiveUnderConcurrency(t *testing.T) {
	s, lease, run := contextModelRun(t)
	ctx := context.Background()
	for step := 20; step < 30; step++ {
		ready := make(chan struct{})
		results := make(chan error, 2)
		go func() { <-ready; results <- s.SaveModel(ctx, lease, run, step, "same", []byte(`{}`)) }()
		go func() {
			<-ready
			results <- s.SaveContextRecovery(ctx, lease, run, step, "same", false, "context_overflow")
		}()
		close(ready)
		a, b := <-results, <-results
		if (a == nil) == (b == nil) || (a != nil && !errors.Is(a, ErrReplayConflict)) || (b != nil && !errors.Is(b, ErrReplayConflict)) {
			t.Fatal("model step accepted both decisions", step, a, b)
		}
	}
	if count(t, s, `SELECT count(*) FROM runtime_context_recoveries r JOIN runtime_model_calls m ON m.run_id=r.run_id AND m.step=r.model_step`) != 0 {
		t.Fatal("contradictory durable model step")
	}
}

func TestContextRecoveryStreamFirstErrorIsDurableButPartialIsTerminal(t *testing.T) {
	for _, partial := range []bool{false, true} {
		t.Run(map[bool]string{false: "before_output", true: "after_partial"}[partial], func(t *testing.T) {
			s, lease, run := contextModelRun(t)
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if partial {
					_ = lifetimeChunk(t, w, "accepted partial")
				}
				_ = json.NewEncoder(w).Encode(map[string]string{"type": "error", "errorCode": "context_overflow", "errorMessage": "secret"})
			}))
			defer server.Close()
			g := &GatewayModel{Store: s, Lease: lease, AgentID: lease.AgentID, StreamURL: server.URL}
			ctx := WithRun(context.Background(), run)
			reader, err := g.Stream(ctx, inputMessages(run.Inputs))
			if err != nil {
				t.Fatal(err)
			}
			var received bool
			for {
				chunk, recvErr := reader.Recv()
				if recvErr != nil {
					err = recvErr
					break
				}
				received = received || agenticText(chunk) != ""
			}
			reader.Close()
			var failure *ModelError
			if !errors.As(err, &failure) || failure.Retry {
				t.Fatal("stream failure classification wrong", err)
			}
			if partial {
				if !received || failure.Code != "upstream_in_stream_error" || count(t, s, `SELECT count(*) FROM runtime_context_recoveries`) != 0 {
					t.Fatal("partial stream retried as pure overflow")
				}
			} else {
				if received || failure.Code != "context_overflow" || count(t, s, `SELECT count(*) FROM runtime_context_recoveries`) != 1 {
					t.Fatal("first error not recorded")
				}
				run.Step.Store(0)
				_, err = g.Stream(ctx, inputMessages(run.Inputs))
				if !errors.As(err, &failure) || calls.Load() != 1 {
					t.Fatal("replay reissued failed stream", err)
				}
			}
			if count(t, s, `SELECT count(*) FROM runtime_model_calls`) != 0 {
				t.Fatal("failed stream saved success")
			}
		})
	}
}

func TestContextNativeTurnLoopStreamsBeforeProviderCompletionAndRecoversDurably(t *testing.T) {
	for _, summaryOverflow := range []bool{false, true} {
		t.Run(map[bool]string{false: "main_overflow", true: "summary_also_overflows"}[summaryOverflow], func(t *testing.T) { testContextNativeTurnLoopRecovery(t, summaryOverflow) })
	}
}

func testContextNativeTurnLoopRecovery(t *testing.T, summaryOverflow bool) {
	s, lease, run := contextModelRun(t)
	ctx, cancel := context.WithCancel(WithRun(context.Background(), run))
	defer cancel()
	var streamCalls, summaryCalls atomic.Int32
	firstChunk := make(chan struct{})
	release := make(chan struct{})
	var firstOnce sync.Once
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		if r.URL.Path == modeltransport.GeneratePath {
			if summaryCalls.Add(1) == 1 && summaryOverflow {
				code := "context_overflow"
				_ = json.NewEncoder(w).Encode(&wire.GatewayChatResponse{Ok: false, ErrorCode: &code})
				return
			}
			raw, _ := (&schema.HumanReadableSerializer{}).Marshal(assistantText("Earlier resolved work."))
			value := string(raw)
			_ = json.NewEncoder(w).Encode(&wire.GatewayChatResponse{Ok: true, ResponseJson: &value})
			return
		}
		if streamCalls.Add(1) == 1 {
			_ = json.NewEncoder(w).Encode(map[string]string{"type": "error", "errorCode": "context_overflow"})
			return
		}
		_ = lifetimeChunk(t, w, "progressive first chunk")
		w.(http.Flusher).Flush()
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"type": "done"})
	}))
	defer server.Close()
	client, err := newGatewayHTTPClient(nil, server.URL+modeltransport.StreamPath)
	if err != nil {
		t.Fatal(err)
	}
	gateway := &GatewayModel{Store: s, Lease: lease, AgentID: lease.AgentID, Client: client, StreamURL: server.URL + modeltransport.StreamPath}
	recording := &recordingContextModel{inner: gateway}
	archive := &pipelineArchiveFixture{}
	budget, _ := ParseContextBudgetSnapshot(run.ContextBudget)
	p, err := NewContextPipeline(ctx, ContextPipelineConfig{Model: recording, Budget: budget, Backend: archive, ArchivePath: archive.path, ArchiveTranscript: archive.transcript})
	if err != nil {
		t.Fatal(err)
	}
	input := append([]*schema.AgenticMessage{schema.UserAgenticMessage(strings.Repeat("old user data ", 500)), assistantText(strings.Repeat("old completed work ", 400))}, inputMessages(run.Inputs)...)
	session := &SessionStore[*schema.AgenticMessage]{Store: s, Lease: lease}
	completed := make(chan error, 1)
	loop := adk.NewTurnLoop(adk.TurnLoopConfig[Input, *schema.AgenticMessage]{
		GenInput: func(ctx context.Context, _ *adk.TurnLoop[Input, *schema.AgenticMessage], items []Input) (*adk.GenInputResult[Input, *schema.AgenticMessage], error) {
			return &adk.GenInputResult[Input, *schema.AgenticMessage]{RunCtx: WithRun(ctx, run), Input: &adk.TypedAgentInput[*schema.AgenticMessage]{Messages: input, EnableStreaming: true}, Consumed: items}, nil
		},
		PrepareAgent: func(ctx context.Context, _ *adk.TurnLoop[Input, *schema.AgenticMessage], _ []Input) (adk.TypedAgent[*schema.AgenticMessage], error) {
			return adk.NewTypedChatModelAgent(ctx, &adk.TypedChatModelAgentConfig[*schema.AgenticMessage]{Name: "context_fixture", Description: "native context regression", Model: recording, MaxIterations: 1, Handlers: []adk.TypedChatModelAgentMiddleware[*schema.AgenticMessage]{p}, ModelRetryConfig: p.RetryConfig()})
		},
		OnAgentEvents: func(_ context.Context, _ *adk.TurnContext[Input, *schema.AgenticMessage], events *adk.AsyncIterator[*adk.TypedAgentEvent[*schema.AgenticMessage]]) error {
			var failure error
			for {
				event, ok := events.Next()
				if !ok {
					break
				}
				if event == nil {
					continue
				}
				if event.Err != nil {
					var retry *adk.WillRetryError
					if !errors.As(event.Err, &retry) {
						failure = event.Err
					}
				}
				if event.Output != nil && event.Output.MessageOutput != nil && event.Output.MessageOutput.IsStreaming {
					reader := event.Output.MessageOutput.MessageStream
					for {
						chunk, err := reader.Recv()
						if err == io.EOF {
							break
						}
						if err != nil {
							var retry *adk.WillRetryError
							if !errors.As(err, &retry) {
								failure = err
							}
							break
						}
						if strings.Contains(agenticText(chunk), "progressive first chunk") {
							firstOnce.Do(func() { close(firstChunk) })
						}
					}
					reader.Close()
				}
			}
			completed <- failure
			return failure
		},
		Store: session, CheckpointID: "context:" + lease.AgentID, SessionID: "agent:" + lease.AgentID, SessionStore: session,
	})
	loop.Push(run.Inputs[0])
	loop.Run(ctx)
	select {
	case <-firstChunk:
	case err := <-completed:
		t.Fatal("native loop ended before progressive chunk", err)
	case <-time.After(5 * time.Second):
		cancel()
		close(release)
		t.Fatal("native retry buffered stream until provider completion")
	}
	expectedFailures, expectedSummaries := 1, 1
	if summaryOverflow {
		expectedFailures, expectedSummaries = 2, 4
	}
	if count(t, s, `SELECT count(*) FROM runtime_context_recoveries`) != expectedFailures || int(summaryCalls.Load()) != expectedSummaries {
		t.Fatal("reactive branch did not persist failed step and native summary")
	}
	close(release)
	select {
	case err := <-completed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("native context turn did not complete")
	}
	loop.Stop(adk.WithImmediate())
	cancel()
	loop.Wait()
	if streamCalls.Load() != 2 || count(t, s, `SELECT count(*) FROM runtime_model_calls`) != expectedSummaries+2-expectedFailures {
		t.Fatal("failed/success model step tape misaligned")
	}
	var mutations int
	if err = s.DB.QueryRow(context.Background(), `SELECT count(*) FROM runtime_session_events WHERE run_id=$1 AND convert_from(payload,'UTF8') LIKE '%context_overflow_recovered_v1%'`, run.ID).Scan(&mutations); err != nil || mutations != 1 {
		t.Fatal("reactive replacement not durable", err, mutations)
	}
	// Simulate a restarted Attempt before the replacement boundary: every model
	// call in the same deterministic branch now comes from its persisted tape.
	replayedRun, err := s.StartRunWithContextPolicy(context.Background(), lease, run.Inputs, contextProfileLegacy, nil)
	if err != nil {
		t.Fatal(err)
	}
	replayCtx := WithRun(context.Background(), replayedRun)
	agent, err := adk.NewTypedChatModelAgent(replayCtx, &adk.TypedChatModelAgentConfig[*schema.AgenticMessage]{Name: "context_fixture", Description: "native context regression", Model: recording, MaxIterations: 1, Handlers: []adk.TypedChatModelAgentMiddleware[*schema.AgenticMessage]{p}, ModelRetryConfig: p.RetryConfig()})
	if err != nil {
		t.Fatal(err)
	}
	runner := adk.NewTypedRunner(adk.TypedRunnerConfig[*schema.AgenticMessage]{Agent: agent, EnableStreaming: true, SessionID: "agent:" + lease.AgentID, SessionStore: session})
	events := runner.Run(replayCtx, input)
	for {
		event, ok := events.Next()
		if !ok {
			break
		}
		if event.Err != nil {
			var retry *adk.WillRetryError
			if !errors.As(event.Err, &retry) {
				t.Fatal("native replay diverged at step", replayedRun.Step.Load(), event.Err)
			}
		}
		if event.Output != nil && event.Output.MessageOutput != nil {
			_, err := event.Output.MessageOutput.GetMessage()
			if err != nil {
				var retry *adk.WillRetryError
				if !errors.As(err, &retry) {
					t.Fatal("native replay stream diverged", err)
				}
			}
		}
	}
	if streamCalls.Load() != 2 || int(summaryCalls.Load()) != expectedSummaries {
		t.Fatal("restart repeated an overflow, summary or accepted model request")
	}
	recording.mu.Lock()
	defer recording.mu.Unlock()
	steps := expectedSummaries + 2
	if len(recording.requests) != steps*2 {
		t.Fatal("replay skipped a model step", len(recording.requests))
	}
	for i := 0; i < steps; i++ {
		if modelRequestHash(recording.requests[i]) != modelRequestHash(recording.requests[i+steps]) {
			t.Fatal("replayed model hash changed", i)
		}
	}
}
