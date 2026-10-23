package eino

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
	"github.com/cloudwego/kitex/client/callopt"
	"github.com/google/uuid"
	wire "teamie/contracts/gen/agent/kitex_gen/agent"
	"teamie/contracts/gen/agent/modeltransport"
)

func TestRunToolProfileUsesOnlyCommittedClassicReceipts(t *testing.T) {
	s, lease, run := nativeModelRun(t)
	ctx := context.Background()
	assertProfile := func(want string) {
		t.Helper()
		got, err := s.runToolProfile(ctx, run.ID)
		if err != nil || got != want {
			t.Fatalf("tool profile = %s, want %s: %v", got, want, err)
		}
	}
	assertProfile(currentToolProfile)
	nativeMessage, err := classicToAgentic(&schema.Message{Role: schema.Assistant, Content: "native receipt"})
	if err != nil {
		t.Fatal(err)
	}
	native, err := (&schema.HumanReadableSerializer{}).Marshal(nativeMessage)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.SaveModel(ctx, lease, run, 1, "native-hash", native); err != nil {
		t.Fatal(err)
	}
	assertProfile(currentToolProfile)
	classic, _ := json.Marshal(&schema.Message{Role: schema.Assistant, Content: "v034 receipt"})
	if err = s.SaveModel(ctx, lease, run, 2, "classic-hash", classic); err != nil {
		t.Fatal(err)
	}
	assertProfile(v034ToolProfile)
	if err = s.SaveModel(ctx, lease, run, 3, "next-native-hash", native); err != nil {
		t.Fatal(err)
	}
	assertProfile(v034ToolProfile)
	for _, raw := range [][]byte{native, []byte(`{"role":"assistant","content_blocks":[],"content":"not a classic receipt"}`), []byte(`{"role":"user","content":"source"}`), []byte(`{"$type":"*schema.AgenticMessage","value":{"role":"assistant","content":"nested extension"}}`)} {
		if classicModelReceipt(raw) {
			t.Fatalf("non-classic response selected v034: %s", raw)
		}
	}
}

func TestResumeCompatibilityBindsTheRunToolProfile(t *testing.T) {
	for _, test := range []struct {
		name, runProfile, checkpointProfile string
		compatible                          bool
	}{
		{"classic native checkpoint", v034ToolProfile, v034ToolProfile, true},
		{"classic rejects v035 checkpoint", v034ToolProfile, currentToolProfile, false},
		{"classic rejects unlabelled v035 checkpoint", v034ToolProfile, "", false},
		{"native checkpoint", currentToolProfile, currentToolProfile, true},
		{"existing native checkpoint", currentToolProfile, "", true},
		{"native rejects different profile", currentToolProfile, v034ToolProfile, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			run := &Run{ID: uuid.NewString(), toolProfile: test.runProfile}
			metadata := &checkpointMetadata{Version: nativeRuntimeVersion, Revision: 1, RunID: run.ID, Attempt: uuid.NewString(), ModelStep: 4, Targets: []string{"target"}, ToolProfile: test.checkpointProfile}
			if resumeCompatibleForRun(metadata, 1, run) != test.compatible {
				t.Fatal("checkpoint did not respect the execution profile")
			}
			metadata.Revision++
			if resumeCompatibleForRun(metadata, 1, run) {
				t.Fatal("profile bypassed revision compatibility")
			}
		})
	}
}

func TestV034ToolExecutionDoesNotAcquireTheNewAttachmentTool(t *testing.T) {
	s, lease, run := nativeModelRun(t)
	ctx := WithRun(context.Background(), run)
	available, err := (&Tools{Store: s, Lease: lease}).BuildIM()
	if err != nil {
		t.Fatal(err)
	}
	run.toolProfile = v034ToolProfile
	active, err := toolsForRun(ctx, available, run)
	if err != nil || len(available) != 5 || len(active) != 4 {
		t.Fatal("v034 catalog did not retain exactly the four original tools", err)
	}
	node, err := compose.NewToolNode(ctx, &compose.ToolsNodeConfig{Tools: active, ExecuteSequentially: true})
	if err != nil {
		t.Fatal(err)
	}
	// A previously hallucinated tool name must not become authorized merely
	// because the new binary knows how to execute that name.
	_, err = node.Invoke(ctx, &schema.Message{Role: schema.Assistant, ToolCalls: []schema.ToolCall{{ID: "old-unknown-tool", Type: "function", Function: schema.FunctionCall{Name: "im_get_attachment", Arguments: `{}`}}}})
	if err == nil || count(t, s, `SELECT count(*) FROM runtime_tool_calls`) != 0 {
		t.Fatal("old tool profile executed a newly introduced tool", err)
	}
}

type replayFixtureStep struct {
	summary   bool
	message   chatMessage
	native    *schema.AgenticMessage
	tokens    int
	errorCode string
	entered   chan struct{}
	block     bool
}

// These decisions reproduce the old writer's tape using only synthetic data.
// Eino, PostgreSQL, IM tools, summaries, Run retries and receipt checks are real.
type replayProfileGateway struct {
	*fixtureGateway
	mu       sync.Mutex
	steps    []replayFixtureStep
	requests []wire.GatewayChatRequest
	failure  error
}

func (g *replayProfileGateway) Chat(ctx context.Context, binding *wire.GatewayChatRequest, _ ...callopt.Option) (*wire.GatewayChatResponse, error) {
	g.mu.Lock()
	index := len(g.requests)
	g.requests = append(g.requests, *binding)
	var request behaviorRequest
	err := decodeFixtureRequest(binding.RequestJson, &request)
	if err == nil && index >= len(g.steps) {
		err = fmt.Errorf("unexpected model request %d", index+1)
	}
	var step replayFixtureStep
	if err == nil {
		step = g.steps[index]
		if (request.ToolChoice == "none") != step.summary || (len(request.Tools) == 0) != step.summary {
			err = fmt.Errorf("model request %d: summary=%v choice=%s tools=%d", index+1, step.summary, request.ToolChoice, len(request.Tools))
		}
	}
	if err != nil {
		g.failure = err
		g.mu.Unlock()
		code := "invalid_request"
		return &wire.GatewayChatResponse{ErrorCode: &code}, nil
	}
	g.mu.Unlock()
	if step.entered != nil {
		close(step.entered)
	}
	if step.block {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	if step.errorCode != "" {
		return &wire.GatewayChatResponse{ErrorCode: &step.errorCode}, nil
	}
	if step.native != nil {
		raw, err := (&schema.HumanReadableSerializer{}).Marshal(step.native)
		if err != nil {
			return nil, err
		}
		response := string(raw)
		return &wire.GatewayChatResponse{Ok: true, ResponseJson: &response}, nil
	}
	response, err := json.Marshal(map[string]any{
		"choices": []any{map[string]any{"message": step.message, "finish_reason": "stop"}},
		"usage":   map[string]int{"prompt_tokens": step.tokens, "total_tokens": step.tokens},
	})
	if err != nil {
		return nil, err
	}
	return &wire.GatewayChatResponse{Ok: true, ResponseJson: modelFixtureResponse(string(response))}, nil
}

func (g *replayProfileGateway) snapshot(t *testing.T) []wire.GatewayChatRequest {
	t.Helper()
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.failure != nil {
		t.Fatal(g.failure)
	}
	return append([]wire.GatewayChatRequest{}, g.requests...)
}

func startReplayProfileWorker(t *testing.T, e *Engine, def *wire.AgentDefinition) (context.CancelFunc, chan struct{}) {
	t.Helper()
	lease, err := e.Store.Acquire(context.Background(), def.Id, e.owner)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		e.work(ctx, def, lease)
		e.Store.Release(context.Background(), lease)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Error("worker did not stop")
		}
	})
	return cancel, done
}

// Persist accepted classic history, as already present before the runtime
// upgrade. Thirty old source messages plus three fresh ones force summary 1.
func seedReplayProfileHistory(t *testing.T, s *Store, f *fixtureIM) []Input {
	t.Helper()
	ctx := context.Background()
	lease, err := s.Acquire(ctx, f.agent, "old-history-fixture")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Release(ctx, lease)
	inputs := []Input{{EventID: "1000", Body: "historical accepted turn"}}
	if err = s.Ingest(ctx, lease, "0", inputs); err != nil {
		t.Fatal(err)
	}
	run, err := s.StartRun(ctx, lease, inputs)
	if err != nil {
		t.Fatal(err)
	}
	store := &SessionStore[*schema.Message]{Store: s, Lease: lease}
	var history []Input
	var events []*adk.SessionEvent[*schema.Message]
	for i := 0; i < 30; i++ {
		in := Input{EventID: fmt.Sprint(1001 + i), WorkspaceID: f.workspace, ConversationID: f.conversation, MessageID: uuid.NewString(), SenderID: uuid.NewString(), SenderKind: "human", Body: fmt.Sprintf("synthetic historical source %d", i)}
		history = append(history, in)
		body, _ := json.Marshal(in)
		events = append(events, &adk.SessionEvent[*schema.Message]{EventID: uuid.NewString(), TurnID: "old-turn", Message: &schema.Message{Role: schema.User, Content: "TEAMIE_NEW_MESSAGE " + string(body)}})
	}
	if err = store.AppendEvents(WithRun(ctx, run), "agent:"+f.agent, events); err != nil {
		t.Fatal(err)
	}
	if err = s.FinishRun(ctx, lease, run, "completed", "", false); err != nil {
		t.Fatal(err)
	}
	return history
}

func v034FixtureRequest(t *testing.T, raw string) []byte {
	t.Helper()
	var request modeltransport.Request
	if err := json.Unmarshal([]byte(raw), &request); err != nil {
		t.Fatal(err)
	}
	var messages []*schema.AgenticMessage
	var opts model.Options
	if err := (&schema.HumanReadableSerializer{}).Unmarshal(request.Messages, &messages); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(request.Options, &opts); err != nil {
		t.Fatal(err)
	}
	classic, err := legacyMessages(messages)
	if err != nil {
		t.Fatal(err)
	}
	for _, message := range classic {
		// These are generated synthetic provenance JSON objects. The added
		// v035 empty field did not exist in v034, including inside summary input.
		message.Content = strings.ReplaceAll(message.Content, `"attachmentIds":[],`, "")
	}
	if opts.AgenticToolChoice != nil {
		opts.ToolChoice = &opts.AgenticToolChoice.Type
	}
	previous := make([]*schema.ToolInfo, 0, len(opts.Tools))
	for _, info := range opts.Tools {
		if info.Name != "im_get_attachment" {
			previous = append(previous, info)
		}
	}
	requestV034, err := legacyChatRequest(classic, previous, &opts)
	if err != nil {
		t.Fatal(err)
	}
	if request.Stream {
		var object map[string]any
		_ = json.Unmarshal(requestV034, &object)
		object["stream"] = true
		object["stream_options"] = map[string]any{"include_usage": true}
		requestV034, _ = json.Marshal(object)
	}
	return requestV034
}

func installV034ModelTape(t *testing.T, s *Store, runID string, requests []wire.GatewayChatRequest) {
	t.Helper()
	ctx := context.Background()
	for index, request := range requests {
		var response []byte
		if err := s.DB.QueryRow(ctx, `SELECT response FROM runtime_model_calls WHERE run_id=$1 AND step=$2`, runID, index+1).Scan(&response); err != nil {
			t.Fatal(err)
		}
		message, err := decodeAgenticMessage(response)
		if err != nil {
			t.Fatal(err)
		}
		classic, err := legacyMessages([]*schema.AgenticMessage{message})
		if err != nil {
			t.Fatal(err)
		}
		if message.ResponseMeta != nil {
			classic[0].ResponseMeta = &schema.ResponseMeta{Usage: message.ResponseMeta.TokenUsage}
		}
		raw, err := json.Marshal(classic[0])
		if err != nil {
			t.Fatal(err)
		}
		if _, err = s.DB.Exec(ctx, `UPDATE runtime_model_calls SET request_hash=$3,response=$4 WHERE run_id=$1 AND step=$2`, runID, index+1, digest(v034FixtureRequest(t, request.RequestJson)), raw); err != nil {
			t.Fatal(err)
		}
	}
}

type replayReceipt struct {
	Key, Hash string
	Response  []byte
}

func snapshotReplayReceipts(t *testing.T, s *Store, runID string, steps int) []replayReceipt {
	t.Helper()
	rows, err := s.DB.Query(context.Background(), `SELECT 'model:'||step::text,request_hash,response FROM runtime_model_calls WHERE run_id=$1 AND step<=$2 UNION ALL SELECT 'tool:'||call_id,request_hash,result FROM runtime_tool_calls WHERE run_id=$1 AND call_id IN ('old-history-read','old-explicit-send') ORDER BY 1`, runID, steps)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var result []replayReceipt
	for rows.Next() {
		var receipt replayReceipt
		if err = rows.Scan(&receipt.Key, &receipt.Hash, &receipt.Response); err != nil {
			t.Fatal(err)
		}
		result = append(result, receipt)
	}
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
	return result
}

func retryReplayProfileRun(t *testing.T, e *Engine, def *wire.AgentDefinition, runID string) {
	t.Helper()
	response, err := (&RuntimeRPC{Engine: e}).RetryRun(context.Background(), &wire.RuntimeRequest{ServiceToken: e.Config.ControlToken, AgentId: def.Id, OwnerId: def.OwnerId, ResourceId: &runID})
	if err != nil || !response.Ok {
		t.Fatalf("same-Run retry was rejected: %v %v", response, err)
	}
}

func TestStartedRunProfilePreservesSummariesAndReceiptsAcrossRetries(t *testing.T) {
	for _, test := range []struct {
		name               string
		classic, streaming bool
	}{
		{"v034_classic_generate", true, false},
		{"v034_classic_stream", true, true},
		{"v035_native_generate", false, false},
		{"v035_native_stream", false, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			classic := test.classic
			s := testStore(t)
			e, f, base, _ := fixtureEngine(t, s)
			def := &wire.AgentDefinition{Id: f.agent, OwnerId: uuid.NewString(), Name: "Agent", Enabled: true, Revision: 1}
			e.Control = &rpcOwnerControl{definition: def}
			history := seedReplayProfileHistory(t, s, f)
			historyIM := newToolPathIM(t, e, f)
			for i := 0; i < 3; i++ {
				f.add(fmt.Sprintf("synthetic source %d", i), "")
			}
			read, _ := toolPathCall("im_list_messages", "old-history-read", listMessagesArgs{ConversationID: historyIM.conversation, Limit: 2})
			send, _ := toolPathCall("im_send_message", "old-explicit-send", sendArgs{ConversationID: f.conversation, Body: "synthetic public result"})
			nextRead, _ := toolPathCall("im_list_messages", "native-history-read", listMessagesArgs{ConversationID: historyIM.conversation, Limit: 2})
			gateway := &replayProfileGateway{fixtureGateway: base, steps: []replayFixtureStep{
				{summary: true, message: chatMessage{Role: "assistant", Content: "First private source summary"}, tokens: 25},
				{message: read, tokens: 20000}, // real Eino usage accounting triggers summary 2
				{summary: true, message: chatMessage{Role: "assistant", Content: "Second private source summary"}, tokens: 25},
				{message: send, tokens: 25},
				{errorCode: "invalid_request"}, // fail after all four model and both tool receipts commit
				{message: nextRead, tokens: 25},
				{errorCode: "upstream_unavailable"}, // append native receipt, then restart the same Run again
				{message: chatMessage{Role: "assistant", Content: "PRIVATE_COMPLETED"}, tokens: 25},
				{message: chatMessage{Role: "assistant", Content: "PRIVATE_NEXT_RUN"}, tokens: 25},
			}}
			e.Gateway = gateway
			if test.streaming {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.Header.Get("Authorization") != "Bearer "+e.Config.GatewayToken {
						w.WriteHeader(http.StatusUnauthorized)
						return
					}
					var call modeltransport.CallRequest
					if err := json.NewDecoder(r.Body).Decode(&call); err != nil {
						w.WriteHeader(http.StatusBadRequest)
						return
					}
					response, err := gateway.Chat(r.Context(), &wire.GatewayChatRequest{OwnerId: call.OwnerID, AgentId: call.AgentID, SessionId: call.SessionID, RunId: call.RunID, RequestJson: call.RequestJSON})
					encoder := json.NewEncoder(w)
					if err != nil || response == nil {
						_ = encoder.Encode(map[string]string{"type": "error", "errorCode": "gateway_unavailable"})
						return
					}
					if !response.Ok {
						_ = encoder.Encode(map[string]string{"type": "error", "errorCode": response.GetErrorCode()})
						return
					}
					_ = encoder.Encode(map[string]string{"type": "chunk", "responseJson": response.GetResponseJson()})
					_ = encoder.Encode(map[string]string{"type": "done"})
				}))
				t.Cleanup(server.Close)
				e.Config.GatewayStreamURL = server.URL
			}
			cancel, done := startReplayProfileWorker(t, e, def)
			waitFor(t, func() bool {
				gateway.snapshot(t)
				return count(t, s, `SELECT count(*) FROM runtime_runs WHERE state='failed' AND safe_error='invalid_request'`) == 1
			})
			cancel()
			<-done
			var runID string
			if err := s.DB.QueryRow(context.Background(), `SELECT id::text FROM runtime_runs WHERE state='failed'`).Scan(&runID); err != nil {
				t.Fatal(err)
			}
			if count(t, s, `SELECT count(*) FROM runtime_model_calls WHERE run_id=$1`, runID) != 4 || count(t, s, `SELECT count(*) FROM runtime_tool_calls WHERE run_id=$1 AND result IS NOT NULL`, runID) != 2 || count(t, s, `SELECT count(*) FROM runtime_inputs WHERE run_id=$1 AND state='failed'`, runID) != 3 {
				t.Fatal("fixture did not reach the four-model/two-tool failed-Run boundary")
			}
			if classic {
				installV034ModelTape(t, s, runID, gateway.snapshot(t)[:4])
			}
			original := snapshotReplayReceipts(t, s, runID, 4)
			if len(original) != 6 {
				t.Fatal("fixture omitted committed receipts")
			}
			retryReplayProfileRun(t, e, def, runID)
			cancel, done = startReplayProfileWorker(t, e, def)
			waitFor(t, func() bool {
				gateway.snapshot(t)
				return count(t, s, `SELECT count(*) FROM runtime_runs WHERE id=$1 AND state='retry' AND safe_error='upstream_unavailable'`, runID) == 1
			})
			cancel()
			<-done
			if count(t, s, `SELECT count(*) FROM runtime_model_calls WHERE run_id=$1`, runID) != 5 {
				t.Fatal("mixed-format retry did not append the next native model receipt")
			}
			retryReplayProfileRun(t, e, def, runID)
			cancel, done = startReplayProfileWorker(t, e, def)
			waitFor(t, func() bool {
				gateway.snapshot(t)
				return count(t, s, `SELECT count(*) FROM runtime_runs WHERE id=$1 AND state='completed' AND attempts=3`, runID) == 1
			})
			cancel()
			<-done
			if !reflect.DeepEqual(original, snapshotReplayReceipts(t, s, runID, 4)) {
				t.Fatal("recovery rewrote an existing model/tool hash or receipt")
			}
			if count(t, s, `SELECT count(*) FROM runtime_runs`) != 2 || count(t, s, `SELECT count(*) FROM runtime_inputs WHERE run_id=$1 AND state='done'`, runID) != 3 || count(t, s, `SELECT count(*) FROM runtime_runs WHERE id=$1 AND cardinality(context_attempts)=0`, runID) != 1 {
				t.Fatal("replay replaced the Run or imported failed-attempt context")
			}
			requests := gateway.snapshot(t)
			if len(requests) != 8 {
				t.Fatalf("cached model generations were repeated: requests=%d", len(requests))
			}
			var before behaviorRequest
			if err := decodeFixtureRequest(requests[3].RequestJson, &before); err != nil {
				t.Fatal(err)
			}
			var wantSummary string
			for _, message := range before.Messages {
				if strings.HasPrefix(message.Content, "TEAMIE_SESSION_SUMMARY ") {
					wantSummary = message.Content
				}
			}
			if classic {
				wantSummary = strings.ReplaceAll(wantSummary, `"attachmentIds":[],`, "")
			}
			if wantSummary == "" {
				t.Fatal("fixture omitted its repeated summary")
			}
			for _, request := range requests[5:] {
				var decoded behaviorRequest
				if err := decodeFixtureRequest(request.RequestJson, &decoded); err != nil {
					t.Fatal(err)
				}
				advertised := false
				for _, tool := range decoded.Tools {
					advertised = advertised || tool.Function.Name == "im_get_attachment"
				}
				if advertised == classic || request.SessionId != "agent:"+f.agent {
					t.Fatal("started Run changed its execution tool profile or Session")
				}
				matchedSummary := false
				for _, message := range decoded.Messages {
					matchedSummary = matchedSummary || message.Content == wantSummary
				}
				if !matchedSummary {
					t.Fatal("started Run changed its exact repeated summary wire")
				}
				body, _ := json.Marshal(decoded.Messages)
				for _, id := range []string{history[len(history)-1].MessageID, f.workspace, f.conversation, f.events[0]["message"].(*Message).ID, f.events[2]["message"].(*Message).ID} {
					if !bytes.Contains(body, []byte(id)) {
						t.Fatalf("two summaries lost original source %s", id)
					}
				}
			}
			if count(t, s, `SELECT count(*) FROM runtime_session_events e JOIN runtime_runs r ON r.id=e.run_id WHERE r.id=$1 AND e.attempt_id=r.committed_attempt AND e.kind='messages_replaced'`, runID) != 2 {
				t.Fatal("completed replay did not preserve both real native summary steps")
			}
			// A subsequent Run belongs to the new catalog; the old profile must
			// never leak from Session history into fresh work.
			f.add("silent fresh Run after old tape", "")
			cancel, done = startReplayProfileWorker(t, e, def)
			waitFor(t, func() bool {
				gateway.snapshot(t)
				return count(t, s, `SELECT count(*) FROM runtime_runs WHERE state='completed'`) == 3
			})
			cancel()
			<-done
			var fresh behaviorRequest
			if err := decodeFixtureRequest(gateway.snapshot(t)[8].RequestJson, &fresh); err != nil {
				t.Fatal(err)
			}
			found := false
			for _, tool := range fresh.Tools {
				found = found || tool.Function.Name == "im_get_attachment"
			}
			if !found {
				t.Fatal("fresh Run inherited an obsolete tool profile")
			}
			f.mu.Lock()
			posts, sent := f.posts, len(f.sent)
			f.mu.Unlock()
			historyIM.mu.Lock()
			historyReads := len(historyIM.requests)
			historyIM.mu.Unlock()
			if posts != 1 || sent != 1 || historyReads != 2 {
				t.Fatalf("cached side effects repeated: sends=%d unique=%d historyReads=%d", posts, sent, historyReads)
			}
		})
	}
}

func TestLegacyProfileDoesNotRelaxCachedModelHash(t *testing.T) {
	s, lease, run := nativeModelRun(t)
	ctx := context.Background()
	classic, _ := json.Marshal(&schema.Message{Role: schema.Assistant, Content: "cached"})
	messages := []*schema.AgenticMessage{schema.UserAgenticMessage("unchanged source")}
	request := v034FixtureRequest(t, string(mustAgenticRequest(t, messages)))
	if err := s.SaveModel(ctx, lease, run, 1, digest(request), classic); err != nil {
		t.Fatal(err)
	}
	run.toolProfile = v034ToolProfile
	gateway := &GatewayModel{Store: s, Lease: lease}
	if _, err := gateway.Generate(WithRun(ctx, run), []*schema.AgenticMessage{schema.UserAgenticMessage("changed source")}); !errors.Is(err, ErrReplayConflict) {
		t.Fatal("legacy profile accepted changed source/context", err)
	}
}

func TestV034RunReopensIncompatibleV035NativeCheckpoint(t *testing.T) {
	s := testStore(t)
	e, f, base, _ := fixtureEngine(t, s)
	def := &wire.AgentDefinition{Id: f.agent, OwnerId: uuid.NewString(), Name: "Agent", Enabled: true, Revision: 1}
	e.Control = &rpcOwnerControl{definition: def}
	e.Config.FlagsFile = t.TempDir() + "/flags.json"
	writeFlags := func(enabled bool) {
		t.Helper()
		raw, _ := json.Marshal(map[string]any{"flags": map[string]bool{"agents.runtime.v3": enabled, "agents.tasks.execute": true}})
		if err := os.WriteFile(e.Config.FlagsFile, raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	writeFlags(true)
	f.add("synthetic checkpoint source", "")
	send, _ := toolPathCall("im_send_message", "old-explicit-send", sendArgs{ConversationID: f.conversation, Body: "one synthetic checkpoint result"})
	blocked := make(chan struct{})
	gateway := &replayProfileGateway{fixtureGateway: base, steps: []replayFixtureStep{
		{message: send, tokens: 25},
		{entered: blocked, block: true},
		{message: chatMessage{Role: "assistant", Content: "PRIVATE_CHECKPOINT_RECOVERED"}, tokens: 25},
	}}
	e.Gateway = gateway
	cancel, done := startReplayProfileWorker(t, e, def)
	select {
	case <-blocked:
	case <-time.After(5 * time.Second):
		t.Fatal("worker did not reach the checkpoint boundary")
	}
	writeFlags(false)
	var metadata *checkpointMetadata
	waitFor(t, func() bool {
		value, err := s.ResumeMetadata(context.Background(), "turnloop:"+f.agent)
		if err == nil && value != nil && len(value.Targets) > 0 {
			metadata = value
			return true
		}
		return false
	})
	cancel()
	<-done
	if metadata.ToolProfile != currentToolProfile || !resumeCompatible(metadata, 1, metadata.RunID) {
		t.Fatal("fixture did not produce a valid native checkpoint with five tools")
	}
	installV034ModelTape(t, s, metadata.RunID, gateway.snapshot(t)[:1])
	// Published v035 checkpoints had no profile label. The native payload
	// remains valid, but its captured five-tool Agent must not resume this Run.
	metadata.ToolProfile = ""
	raw, _ := json.Marshal(metadata)
	if _, err := s.DB.Exec(context.Background(), `UPDATE runtime_checkpoints SET metadata=$2 WHERE checkpoint_id=$1`, "turnloop:"+f.agent, raw); err != nil {
		t.Fatal(err)
	}
	writeFlags(true)
	retryReplayProfileRun(t, e, def, metadata.RunID)
	cancel, done = startReplayProfileWorker(t, e, def)
	waitFor(t, func() bool {
		gateway.snapshot(t)
		return count(t, s, `SELECT count(*) FROM runtime_runs WHERE id=$1 AND state='completed' AND cardinality(context_attempts)=0`, metadata.RunID) == 1
	})
	cancel()
	<-done
	if count(t, s, `SELECT count(*) FROM runtime_model_calls WHERE run_id=$1`, metadata.RunID) != 2 || count(t, s, `SELECT count(*) FROM runtime_model_calls WHERE run_id=$1 AND step=2`, metadata.RunID) != 1 {
		t.Fatal("incompatible checkpoint skipped or advanced the original receipt sequence")
	}
	var request behaviorRequest
	if err := decodeFixtureRequest(gateway.snapshot(t)[2].RequestJson, &request); err != nil {
		t.Fatal(err)
	}
	for _, info := range request.Tools {
		if info.Function.Name == "im_get_attachment" {
			t.Fatal("native checkpoint bypassed the fixed old tool profile")
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.posts != 1 || len(f.sent) != 1 {
		t.Fatal("incompatible checkpoint recovery repeated an already committed send")
	}
}

func mustAgenticRequest(t *testing.T, messages []*schema.AgenticMessage) []byte {
	t.Helper()
	request, err := agenticRequest(messages, &model.Options{}, false)
	if err != nil {
		t.Fatal(err)
	}
	return request
}
