package eino

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
	"github.com/cloudwego/kitex/client/callopt"
	"github.com/google/uuid"
	wire "teamie/contracts/gen/agent/kitex_gen/agent"
	"teamie/contracts/gen/agent/kitex_gen/agent/modelgatewayservice"
)

func TestContextV1CacheRejectsHistoricalObservationSubstitution(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%v", streaming), func(t *testing.T) {
			s, lease, run := contextModelRun(t)
			ctx := WithRun(context.Background(), run)
			output := &Observation{OK: true, Data: Message{ID: uuid.NewString(), Body: "<tag>&中文😀", Seq: 9007199254740993}}
			receipt, _ := json.Marshal(output)
			oldWire := oldUnescapedOutput(t, output)
			if string(receipt) == oldWire {
				t.Fatal("fixture needs two actual JSON representations")
			}
			if _, err := s.ToolIntent(ctx, lease, run, "exact-call", "im_get_message", []byte(`{}`)); err != nil {
				t.Fatal(err)
			}
			if err := s.ToolResult(ctx, lease, run, "exact-call", receipt); err != nil {
				t.Fatal(err)
			}
			saveObservedOutputs(t, s, lease, run, false, replayResultMessage("exact-call", "im_get_message", oldWire))
			prefix := []*schema.AgenticMessage{schema.SystemAgenticMessage("unchanged policy"), schema.UserAgenticMessage("unchanged source"), {Role: schema.AgenticRoleTypeAssistant, ContentBlocks: []*schema.ContentBlock{schema.NewContentBlock(&schema.FunctionToolCall{CallID: "exact-call", Name: "im_get_message", Arguments: `{}`})}}}
			original := append(append([]*schema.AgenticMessage{}, prefix...), replayResultMessage("exact-call", "im_get_message", oldWire))
			current := append(append([]*schema.AgenticMessage{}, prefix...), replayResultMessage("exact-call", "im_get_message", string(receipt)))
			options := &model.Options{}
			request, err := agenticRequest(original, options, streaming)
			if err != nil {
				t.Fatal(err)
			}
			raw, _ := (&schema.HumanReadableSerializer{}).Marshal(assistantText("cached result"))
			if err = s.SaveModel(ctx, lease, run, 1, modelRequestHash(request), raw); err != nil {
				t.Fatal(err)
			}
			candidates, err := s.observedToolReplayInputs(ctx, run, current)
			if err != nil || len(candidates) != 1 {
				t.Fatal("fixture did not offer old fallback candidate", err, len(candidates))
			}
			candidateOptions := &model.Options{}
			candidateRequest, err := agenticRequest(candidates[0], candidateOptions, streaming)
			if err != nil || modelRequestHash(candidateRequest) != modelRequestHash(request) {
				t.Fatal("old observation does not match stored receipt", err)
			}
			gateway := &GatewayModel{Store: s, Lease: lease} // No transport: rejected requests cannot reach a provider.
			if streaming {
				_, err = gateway.Stream(ctx, current)
			} else {
				_, err = gateway.Generate(ctx, current)
			}
			if !errors.Is(err, ErrReplayConflict) {
				t.Fatal("new profile accepted changed request via historical overlay", err)
			}
			// The identical historical fallback remains available to legacy Runs.
			legacy := &Run{ID: run.ID, AgentID: run.AgentID, Attempt: run.Attempt, ContextProfile: contextProfileLegacy}
			legacyCtx := WithRun(context.Background(), legacy)
			if streaming {
				reader, err := gateway.Stream(legacyCtx, current)
				if err != nil {
					t.Fatal("legacy fallback changed", err)
				}
				message, err := reader.Recv()
				reader.Close()
				if err != nil || agenticText(message) != "cached result" {
					t.Fatal(err)
				}
			} else {
				message, err := gateway.Generate(legacyCtx, current)
				if err != nil || agenticText(message) != "cached result" {
					t.Fatal("legacy fallback changed", err)
				}
			}
		})
	}
}

type contextIMReplayGateway struct {
	modelgatewayservice.Client
	calls atomic.Int32
	args  string
}

func (g *contextIMReplayGateway) Chat(_ context.Context, _ *wire.GatewayChatRequest, _ ...callopt.Option) (*wire.GatewayChatResponse, error) {
	var result *schema.AgenticMessage
	switch g.calls.Add(1) {
	case 1:
		result = &schema.AgenticMessage{Role: schema.AgenticRoleTypeAssistant, ContentBlocks: []*schema.ContentBlock{schema.NewContentBlock(&schema.FunctionToolCall{CallID: "real-im-read", Name: "im_get_message", Arguments: g.args})}}
	case 2:
		result = assistantText("Private result inspected.")
	default:
		return nil, errors.New("replay repeated provider call")
	}
	raw, err := (&schema.HumanReadableSerializer{}).Marshal(result)
	if err != nil {
		return nil, err
	}
	value := string(raw)
	return &wire.GatewayChatResponse{Ok: true, ResponseJson: &value}, nil
}

func TestContextV1RealIMReceiptReplaysBeforeCountingAndExactCache(t *testing.T) {
	s, lease, run := contextModelRun(t)
	conversation, message := uuid.NewString(), uuid.NewString()
	var reads atomic.Int32
	imHTTP := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reads.Add(1)
		if r.Method != http.MethodGet || r.URL.Path != "/api/v1/conversations/"+conversation+"/messages/"+message {
			t.Error("IM tool used an unexpected request")
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"message": Message{ID: message, ConversationID: conversation, Body: "<tag>&中文😀", Seq: 9007199254740993}})
	}))
	defer imHTTP.Close()
	tools, err := (&Tools{Store: s, Lease: lease, IM: &IM{BaseURL: imHTTP.URL, token: "fixture-principal-token", expiry: time.Now().Add(time.Hour)}}).BuildIM()
	if err != nil {
		t.Fatal(err)
	}
	args, _ := json.Marshal(getMessageArgs{ConversationID: conversation, MessageID: message})
	fixture := &contextIMReplayGateway{args: string(args)}
	gateway := &GatewayModel{Store: s, Lease: lease, Client: fixture, AgentID: lease.AgentID}
	recording := &recordingContextModel{inner: gateway}
	archive := &pipelineArchiveFixture{}
	budget, _ := ParseContextBudgetSnapshot(run.ContextBudget)
	pipeline, err := NewContextPipeline(context.Background(), ContextPipelineConfig{Model: recording, Budget: budget, Backend: archive, ArchivePath: archive.path, ArchiveTranscript: archive.transcript})
	if err != nil {
		t.Fatal(err)
	}
	session := &SessionStore[*schema.AgenticMessage]{Store: s, Lease: lease}
	for attempt := 0; attempt < 2; attempt++ {
		if attempt > 0 {
			run, err = s.StartRunWithContextPolicy(context.Background(), lease, run.Inputs, contextProfileLegacy, nil)
			if err != nil {
				t.Fatal(err)
			}
		}
		ctx := WithRun(context.Background(), run)
		agent, err := adk.NewTypedChatModelAgent(ctx, &adk.TypedChatModelAgentConfig[*schema.AgenticMessage]{Name: "im_replay", Description: "real IM receipt fixture", Instruction: contextV1Instruction(&wire.AgentDefinition{Id: lease.AgentID, OwnerId: "owner", Name: "Fixture"}), Model: recording, ToolsConfig: adk.ToolsConfig{ToolsNodeConfig: compose.ToolsNodeConfig{Tools: tools, ExecuteSequentially: true}}, MaxIterations: 2, Handlers: []adk.TypedChatModelAgentMiddleware[*schema.AgenticMessage]{pipeline}})
		if err != nil {
			t.Fatal(err)
		}
		runner := adk.NewTypedRunner(adk.TypedRunnerConfig[*schema.AgenticMessage]{Agent: agent, SessionID: "agent:" + lease.AgentID, SessionStore: session})
		events := runner.Run(ctx, inputMessages(run.Inputs))
		for {
			event, ok := events.Next()
			if !ok {
				break
			}
			if event.Err != nil {
				t.Fatal("native IM receipt replay diverged", event.Err)
			}
			if event.Output != nil && event.Output.MessageOutput != nil {
				if _, err = event.Output.MessageOutput.GetMessage(); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
	if reads.Load() != 2 || fixture.calls.Load() != 2 || count(t, s, `SELECT count(*) FROM runtime_tool_calls`) != 1 || count(t, s, `SELECT count(*) FROM runtime_model_calls`) != 2 {
		t.Fatal("replay repeated IM/provider effects", reads.Load(), fixture.calls.Load())
	}
	var receipt []byte
	if err = s.DB.QueryRow(context.Background(), `SELECT result FROM runtime_tool_calls WHERE call_id='real-im-read'`).Scan(&receipt); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(receipt, []byte(`9007199254740993`)) || !bytes.Contains(receipt, []byte(`\u003ctag\u003e\u0026中文😀`)) {
		t.Fatal("IM receipt lost original escaping/integer precision")
	}
	recording.mu.Lock()
	defer recording.mu.Unlock()
	if len(recording.requests) != 4 {
		t.Fatal("model step count changed", len(recording.requests))
	}
	for step := 0; step < 2; step++ {
		a, b := recording.requests[step], recording.requests[step+2]
		if modelRequestHash(a) != modelRequestHash(b) {
			t.Fatal("exact model hash changed after IM observation replay", step)
		}
		before, err := measureContextRequest(a)
		if err != nil {
			t.Fatal(err)
		}
		after, err := measureContextRequest(b)
		if err != nil || before != after {
			t.Fatal("replayed observation moved token/transport count", step, before, after, err)
		}
	}
	if !bytes.Contains(recording.requests[1], receipt) {
		// Native JSON serializes the tool result text once more; inspect its
		// decoded form so the assertion is about the actual model observation.
		var envelope struct {
			Messages json.RawMessage `json:"messages"`
		}
		var msgs []*schema.AgenticMessage
		_ = json.Unmarshal(recording.requests[1], &envelope)
		_ = (&schema.HumanReadableSerializer{}).Unmarshal(envelope.Messages, &msgs)
		found := false
		for _, msg := range msgs {
			for _, block := range msg.ContentBlocks {
				result, ok := observationText(block)
				found = found || (ok && result.Content[0].Text.Text == string(receipt))
			}
		}
		if !found {
			t.Fatal("model did not receive original committed IM observation")
		}
	}
}
