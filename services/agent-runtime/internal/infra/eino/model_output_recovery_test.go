package eino

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync/atomic"
	"testing"

	"github.com/cloudwego/eino-ext/components/model/agenticopenai"
	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
	"teamie/contracts/gen/agent/hertz_gen/agent/modeltransport"
	wire "teamie/contracts/gen/agent/kitex_gen/agent"
)

type outputRecoveryTool struct{ calls atomic.Int32 }

func (*outputRecoveryTool) Info(context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{Name: "probe", Desc: "Record a verified fixture effect", ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{"value": {Type: schema.String, Required: true}})}, nil
}

func (t *outputRecoveryTool) InvokableRun(_ context.Context, args string, _ ...tool.Option) (string, error) {
	t.calls.Add(1)
	var input struct {
		Value string `json:"value"`
	}
	if json.Unmarshal([]byte(args), &input) != nil || input.Value != "accepted" {
		return "", errors.New("rejected output reached tool dispatch")
	}
	return "effect recorded", nil
}

func truncatedReasoningOutput() *schema.AgenticMessage {
	return &schema.AgenticMessage{Role: schema.AgenticRoleTypeAssistant, ContentBlocks: []*schema.ContentBlock{schema.NewContentBlock(&schema.Reasoning{Text: "private incomplete deliberation"})}, ResponseMeta: &schema.AgenticResponseMeta{Extension: &agenticopenai.ChatResponseMetaExtension{FinishReason: "length"}}}
}

func outputRecoveryCall(args string) *schema.AgenticMessage {
	return &schema.AgenticMessage{Role: schema.AgenticRoleTypeAssistant, ContentBlocks: []*schema.ContentBlock{schema.NewContentBlock(&schema.FunctionToolCall{CallID: "accepted-output-tool", Name: "probe", Arguments: args})}}
}

func TestOutputRecoveryUsesNativeVerdictBeforeEffectsAndReplaysReceipts(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		for _, malformed := range []bool{false, true} {
			name := map[bool]string{false: "generate", true: "stream"}[streaming] + "/" + map[bool]string{false: "reasoning_length", true: "cut_tool_arguments"}[malformed]
			t.Run(name, func(t *testing.T) {
				s, lease, run := contextModelRun(t)
				ctx := WithRun(context.Background(), run)
				var requests atomic.Int32
				limits := []int{}
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					var call modeltransport.CallRequest
					var request modeltransport.Request
					var options model.Options
					if json.NewDecoder(r.Body).Decode(&call) != nil || json.Unmarshal([]byte(call.RequestJSON), &request) != nil || json.Unmarshal(request.Options, &options) != nil || options.MaxTokens == nil {
						t.Error("missing lossless output options")
						w.WriteHeader(400)
						return
					}
					limits = append(limits, *options.MaxTokens)
					var result *schema.AgenticMessage
					switch requests.Add(1) {
					case 1:
						result = truncatedReasoningOutput()
						if malformed {
							result = outputRecoveryCall(`{"value":"cut`)
						}
					case 2:
						result = outputRecoveryCall(`{"value":"accepted"}`)
					default:
						result = assistantText("complete")
					}
					raw, _ := (&schema.HumanReadableSerializer{}).Marshal(result)
					if r.URL.Path == modeltransport.StreamPath {
						_ = json.NewEncoder(w).Encode(map[string]string{"type": "chunk", "responseJson": string(raw)})
						_ = json.NewEncoder(w).Encode(map[string]string{"type": "done"})
					} else {
						text := string(raw)
						_ = json.NewEncoder(w).Encode(&wire.GatewayChatResponse{Ok: true, ResponseJson: &text})
					}
				}))
				defer server.Close()
				client, err := newGatewayHTTPClient(nil, server.URL+modeltransport.StreamPath)
				if err != nil {
					t.Fatal(err)
				}
				gateway := &GatewayModel{Store: s, Lease: lease, AgentID: lease.AgentID, Client: client, StreamURL: server.URL + modeltransport.StreamPath}
				budget, _ := ParseContextBudgetSnapshot(run.ContextBudget)
				archive := &pipelineArchiveFixture{}
				pipeline, err := NewContextPipeline(ctx, ContextPipelineConfig{Model: gateway, Budget: budget, Backend: archive, ArchivePath: archive.path, ArchiveTranscript: archive.transcript})
				if err != nil {
					t.Fatal(err)
				}
				probe := &outputRecoveryTool{}
				for replay := 0; replay < 2; replay++ {
					run.Step.Store(0)
					agent, err := adk.NewTypedChatModelAgent(ctx, &adk.TypedChatModelAgentConfig[*schema.AgenticMessage]{Name: "output_recovery", Model: gateway, MaxIterations: 4, ToolsConfig: adk.ToolsConfig{ToolsNodeConfig: compose.ToolsNodeConfig{Tools: []tool.BaseTool{probe}, ExecuteSequentially: true}}, Handlers: []adk.TypedChatModelAgentMiddleware[*schema.AgenticMessage]{&nativeToolReceipts{Store: s, Lease: lease}}, ModelRetryConfig: pipeline.RetryConfig()})
					if err != nil {
						t.Fatal(err)
					}
					iter := agent.Run(ctx, &adk.TypedAgentInput[*schema.AgenticMessage]{Messages: inputMessages(run.Inputs), EnableStreaming: streaming})
					for {
						event, ok := iter.Next()
						if !ok {
							break
						}
						if event == nil {
							continue
						}
						if event.Err != nil && !modelRetryObservation(event.Err) {
							t.Fatal(event.Err)
						}
						if event.Output != nil && event.Output.MessageOutput != nil {
							if _, err = event.Output.MessageOutput.GetMessage(); err != nil && !modelRetryObservation(err) {
								t.Fatal(err)
							}
						}
					}
					if probe.calls.Load() != 1 || requests.Load() != 3 || count(t, s, `SELECT count(*) FROM runtime_model_calls`) != 3 || count(t, s, `SELECT count(*) FROM runtime_tool_calls WHERE result IS NOT NULL`) != 1 {
						t.Fatal("rejection executed or replay repeated an effect", probe.calls.Load(), requests.Load())
					}
				}
				if !reflect.DeepEqual(limits, []int{2048, 4096, 2048}) {
					t.Fatal("output recovery did not use its frozen reserve", limits)
				}
			})
		}
	}
}

func TestOutputRecoveryPreservesOldPolicyAndDoesNotRetryPartialErrors(t *testing.T) {
	legacy, _ := makeContextBudgetSnapshotVersion(16384, 1)
	current, _ := makeContextBudgetSnapshot(16384)
	if legacy.OutputReserveTokens != 2048 || current.OutputReserveTokens != 4096 {
		t.Fatal("unexpected capacity")
	}
	for _, budget := range []ContextBudgetSnapshot{legacy, current} {
		raw, _ := json.Marshal(budget)
		parsed, err := ParseContextBudgetSnapshot(raw)
		if err != nil || parsed != budget {
			t.Fatal("changed a supported frozen budget", err)
		}
	}
	old := &ContextPipeline{cfg: ContextPipelineConfig{Budget: legacy}}
	if old.retryIncompleteOutput(&adk.TypedRetryContext[*schema.AgenticMessage]{RetryAttempt: 1, OutputMessage: truncatedReasoningOutput()}) != nil {
		t.Fatal("reinterpreted historical output")
	}
	p := &ContextPipeline{cfg: ContextPipelineConfig{Budget: current}}
	if p.retryIncompleteOutput(&adk.TypedRetryContext[*schema.AgenticMessage]{RetryAttempt: 1, OutputMessage: truncatedReasoningOutput(), Err: context.Canceled}) != nil {
		t.Fatal("retried a partial interrupted stream")
	}
	decision := p.retryIncompleteOutput(&adk.TypedRetryContext[*schema.AgenticMessage]{RetryAttempt: 2, Options: []model.Option{model.WithMaxTokens(4096)}, OutputMessage: truncatedReasoningOutput()})
	var failure *ModelError
	if decision == nil || decision.Retry || !errors.As(decision.RewriteError, &failure) || failure.Code != "upstream_response_limit" {
		t.Fatal("reported exhausted output as completed")
	}
	for _, message := range []*schema.AgenticMessage{assistantText("ordinary silence/output"), outputRecoveryCall(`{"value":"accepted"}`)} {
		if incompleteModelOutput(message) {
			t.Fatal("rejected a complete response")
		}
	}
}
