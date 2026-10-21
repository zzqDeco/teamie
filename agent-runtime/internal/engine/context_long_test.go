package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	wire "teamie/agent-protocol/kitex_gen/agent"
	"teamie/agent-protocol/modeltransport"
)

func TestMillionTokenHistoryIsNotReducedByTheFormerTransportCeiling(t *testing.T) {
	p, archive, summary := testPipeline(t, 1_000_000)
	inputs := []Input{{EventID: "fresh", ConversationID: "conversation", MessageID: "request", SenderID: "requester", SenderKind: "human", Body: "Continue the current task and preserve its source."}}
	ctx := WithRun(context.Background(), &Run{Inputs: inputs})
	for _, count := range []int{300, 600, 800} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			messages := []*schema.AgenticMessage{schema.SystemAgenticMessage("Keep trusted system behavior and source audience.")}
			for i := 0; i < count; i++ {
				messages = append(messages, schema.UserAgenticMessage(fmt.Sprintf("historical observation %d %s", i, strings.Repeat("x", 4096))))
			}
			messages = append(messages, inputMessages(inputs)...)
			before, err := p.cfg.Budget.measureContext(messages, nil, p.cfg.Budget.OutputReserveTokens, false)
			if err != nil || before.TransportBytes <= contextTransportLimit {
				t.Fatal("fixture did not cross the old 256 KiB request ceiling", before, err)
			}
			_, out, err := p.BeforeModelRewriteState(ctx, &adk.TypedChatModelAgentState[*schema.AgenticMessage]{Messages: messages}, nil)
			if err != nil {
				t.Fatal(err)
			}
			if count <= 600 {
				if len(out.Messages) != len(messages) || len(summary.inputs) != 0 || len(archive.content) != 0 {
					t.Fatal("well-fitting million-token context was summarized or archived early")
				}
			} else if len(summary.inputs) == 0 || len(archive.content) == 0 || len(out.Messages) >= len(messages) {
				t.Fatal("real token pressure did not archive and summarize the older prefix")
			}
			found := false
			for _, message := range out.Messages {
				found = found || agenticText(message) == agenticText(inputMessages(inputs)[0])
			}
			after, err := p.cfg.Budget.measureContext(out.Messages, out.ToolInfos, p.cfg.Budget.OutputReserveTokens, false)
			if !found || agenticText(out.Messages[0]) != agenticText(messages[0]) || err != nil || !p.cfg.Budget.fits(after) {
				t.Fatal("large-window compaction lost current source/system behavior or exceeded capacity", after, err)
			}
		})
	}
}

func TestLongContextOutputEncodingPreservesAllHistoricalRunCaps(t *testing.T) {
	messages := []*schema.AgenticMessage{schema.UserAgenticMessage("preserve the request identity")}
	for version := 1; version <= contextBudgetVersion; version++ {
		budget, err := makeContextBudgetSnapshotVersion(1_000_000, version)
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(budget)
		run := &Run{ContextProfile: contextProfileV1, ContextBudget: raw}
		count := modelInputMessageLimit(run)
		if (version < 6 && count != 0) || (version >= 6 && count != modeltransport.MaxInputMessages) {
			t.Fatal("message count policy was not frozen with the Run", version, count)
		}
		requested := budget.OutputReserveTokens
		actual, err := agenticRequestForRun(run, messages, &model.Options{MaxTokens: &requested}, false)
		if err != nil {
			t.Fatal(err)
		}
		if version < 6 {
			if validateModelResult(assistantText(strings.Repeat("x", 65537)), modelResultByteLimit(run)) == nil {
				t.Fatal("upgrade changed a historical model-result acceptance limit", version)
			}
			legacyOutput := budget.OutputReserveTokens
			legacy, _ := agenticRequest(messages, &model.Options{MaxTokens: &legacyOutput}, false)
			if !bytes.Equal(actual, legacy) {
				t.Fatal("long-context upgrade changed a frozen historical model request", version)
			}
			continue
		}
		if err := validateModelResult(assistantText(strings.Repeat("x", 65537)), modelResultByteLimit(run)); err != nil {
			t.Fatal("new Run retained the old 64 KiB native output limit", err)
		}
		var envelope modeltransport.Request
		var options model.Options
		if json.Unmarshal(actual, &envelope) != nil || json.Unmarshal(envelope.Options, &options) != nil || options.MaxTokens == nil || *options.MaxTokens != 131072 || budget.initialOutputTokens() != 32768 {
			t.Fatal("new Run retained the old 4K output clamp")
		}
		measured, _ := budget.measureContext(messages, nil, budget.OutputReserveTokens, false)
		wire, _ := budget.measureContextRequest(actual)
		if measured != wire {
			t.Fatal("preflight omitted the actual long-output request encoding", measured, wire)
		}
	}
}

func TestLongContextStreamBatchingMatchesNativeConcatenation(t *testing.T) {
	chunks := []*schema.AgenticMessage{}
	for index := 0; index < 4097; index++ {
		block := schema.NewContentBlock(&schema.Reasoning{Text: "r"})
		streamIndex := 0
		if index >= 1025 {
			streamIndex = 1
			block = schema.NewContentBlock(&schema.AssistantGenText{Text: "汉字"})
		}
		if index >= 3071 {
			streamIndex = 2
			call := &schema.FunctionToolCall{Arguments: " "}
			if index == 3071 {
				call.CallID, call.Name, call.Arguments = "stable-call", "im_send_message", `{"body":"done"}`
			}
			block = schema.NewContentBlock(call)
		}
		block.StreamingMeta = &schema.StreamingMeta{Index: streamIndex}
		chunks = append(chunks, &schema.AgenticMessage{Role: schema.AgenticRoleTypeAssistant, ContentBlocks: []*schema.ContentBlock{block}})
	}
	chunks = append(chunks, &schema.AgenticMessage{Role: schema.AgenticRoleTypeAssistant, ResponseMeta: &schema.AgenticResponseMeta{TokenUsage: &schema.TokenUsage{PromptTokens: 700000, CompletionTokens: 131072, TotalTokens: 831072}}})
	want, err := schema.ConcatAgenticMessages(chunks)
	if err != nil {
		t.Fatal(err)
	}
	accumulator := &modelStreamAccumulator{}
	for _, chunk := range chunks {
		if err = accumulator.add(chunk); err != nil {
			t.Fatal(err)
		}
	}
	got, err := accumulator.result()
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatal("batching changed native reasoning, text, tool identity/arguments or usage", err)
	}
	if len(accumulator.segments) > 40 || len(accumulator.pending) != 0 {
		t.Fatal("aggregation retained every token-sized native object")
	}
}

func TestLongContextStreamRejectsOversizedContentBeforeCompletion(t *testing.T) {
	for _, kind := range []string{"text", "reasoning", "tool", "extra"} {
		t.Run(kind, func(t *testing.T) {
			accumulator := &modelStreamAccumulator{}
			text := strings.Repeat("x", 65536)
			for i := 0; i < 160; i++ {
				message := &schema.AgenticMessage{Role: schema.AgenticRoleTypeAssistant}
				var block *schema.ContentBlock
				switch kind {
				case "text":
					block = schema.NewContentBlock(&schema.AssistantGenText{Text: text})
				case "reasoning":
					block = schema.NewContentBlock(&schema.Reasoning{Text: text})
				case "tool":
					block = schema.NewContentBlock(&schema.FunctionToolCall{Name: "tool", Arguments: text})
				case "extra":
					message.Extra = map[string]any{"delta": text}
				}
				if block != nil {
					block.StreamingMeta = &schema.StreamingMeta{Index: 0}
					message.ContentBlocks = []*schema.ContentBlock{block}
				}
				if err := accumulator.add(message); err != nil {
					var failure *ModelError
					if !errors.As(err, &failure) || failure.Code != "upstream_response_limit" || i > 128 {
						t.Fatal("oversized response was not rejected incrementally", i, err)
					}
					return
				}
			}
			t.Fatal("oversized content was retained until stream completion")
		})
	}
}

func TestLongContextStreamCompactsFramingWithoutRejectingValidContent(t *testing.T) {
	accumulator := &modelStreamAccumulator{}
	var previous string
	for i := 0; i < 20000; i++ {
		message := &schema.AgenticMessage{Role: schema.AgenticRoleTypeAssistant,
			ContentBlocks: []*schema.ContentBlock{schema.NewContentBlockChunk(&schema.AssistantGenText{Text: "x"}, &schema.StreamingMeta{Index: 0})},
			Extra:         map[string]any{"openai-request-id": strings.Repeat("header", 100)},
		}
		body, err := (&schema.HumanReadableSerializer{}).Marshal(message)
		if err != nil {
			t.Fatal(err)
		}
		if err = accumulator.add(streamRequestIDDelta(message, &previous), len(body)); err != nil {
			t.Fatal("repeated native framing was mistaken for reconstructed content", err)
		}
	}
	result, err := accumulator.result()
	if err != nil || len(result.ContentBlocks) != 1 || result.ContentBlocks[0].StreamingMeta != nil || result.ContentBlocks[0].AssistantGenText.Text != strings.Repeat("x", 20000) || result.Extra["openai-request-id"] != strings.Repeat("header", 100) {
		t.Fatal("incremental capacity checks changed final native content", err)
	}
}

func TestRestoreFixtureContainsMillionTokenPolicy(t *testing.T) {
	for _, version := range []int{6, 7} {
		raw, err := os.ReadFile(fmt.Sprintf("../../../ops/fixtures/runtime-context-budget-v%d.json", version))
		if err != nil {
			t.Fatal(err)
		}
		got, err := ParseContextBudgetSnapshot(raw)
		want, makeErr := makeContextBudgetSnapshotVersion(1_000_000, version)
		if err != nil || makeErr != nil || got != want || got.InputTokenLimit+got.OutputReserveTokens+256 != 1_000_000 {
			t.Fatal("long-context restore fixture is incomplete or admission exceeds the declared model window", version, err, makeErr)
		}
	}
}

type longSummaryModelFixture struct{ text string }

func (m longSummaryModelFixture) Generate(context.Context, []*schema.AgenticMessage, ...model.Option) (*schema.AgenticMessage, error) {
	return assistantText(m.text), nil
}

func (m longSummaryModelFixture) Stream(ctx context.Context, messages []*schema.AgenticMessage, opts ...model.Option) (*schema.StreamReader[*schema.AgenticMessage], error) {
	message, err := m.Generate(ctx, messages, opts...)
	return schema.StreamReaderFromArray([]*schema.AgenticMessage{message}), err
}

func TestLongContextSummaryCapacityMatchesNativeByteValidation(t *testing.T) {
	for _, version := range []int{5, 6} {
		budget, _ := makeContextBudgetSnapshotVersion(1_000_000, version)
		archive := &pipelineArchiveFixture{}
		text := strings.Repeat("x", 40000)
		pipeline, err := NewContextPipeline(context.Background(), ContextPipelineConfig{Model: longSummaryModelFixture{text}, Budget: budget, Backend: archive, ArchivePath: archive.path, ArchiveTranscript: archive.transcript})
		if err != nil {
			t.Fatal(err)
		}
		got, err := pipeline.summarizePrefix(context.Background(), []*schema.AgenticMessage{schema.UserAgenticMessage("An older complete observation.")})
		if version < 6 && err == nil {
			t.Fatal("historical summary acceptance was changed")
		}
		if version >= 6 && (err != nil || got != text) {
			t.Fatal("valid long summary retained the former 32 KiB ceiling", err)
		}
	}
}

func TestLongNativeResponsePersistsAndReplaysWithoutAnotherRequest(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		t.Run(fmt.Sprint(streaming), func(t *testing.T) {
			s, lease, run := contextModelRun(t)
			run.ContextBudget, _ = NewContextBudgetSnapshot(1_000_000)
			if _, err := s.DB.Exec(context.Background(), `UPDATE runtime_runs SET context_budget=$2 WHERE id=$1`, run.ID, run.ContextBudget); err != nil {
				t.Fatal(err)
			}
			output := assistantText(strings.Repeat("result ", 100000))
			raw, _ := (&schema.HumanReadableSerializer{}).Marshal(output)
			var requests atomic.Int32
			fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				var call modeltransport.CallRequest
				var request modeltransport.Request
				var opts model.Options
				if json.NewDecoder(r.Body).Decode(&call) != nil || call.MessageLimit != modeltransport.MaxInputMessages || len(call.RequestJSON) <= contextTransportLimit || json.Unmarshal([]byte(call.RequestJSON), &request) != nil || json.Unmarshal(request.Options, &opts) != nil || opts.MaxTokens == nil || *opts.MaxTokens != 131072 {
					t.Error("runtime did not transmit the full long request and frozen output reserve")
				}
				if streaming {
					_ = json.NewEncoder(w).Encode(map[string]string{"type": "chunk", "responseJson": string(raw)})
					_ = json.NewEncoder(w).Encode(map[string]string{"type": "done"})
				} else {
					text := string(raw)
					_ = json.NewEncoder(w).Encode(&wire.GatewayChatResponse{Ok: true, ResponseJson: &text})
				}
			}))
			defer fixture.Close()
			client, err := newGatewayHTTPClient(nil, fixture.URL+modeltransport.StreamPath)
			if err != nil {
				t.Fatal(err)
			}
			gateway := &GatewayModel{Store: s, Lease: lease, AgentID: lease.AgentID, Client: client, StreamURL: fixture.URL + modeltransport.StreamPath}
			ctx := WithRun(context.Background(), run)
			input := []*schema.AgenticMessage{schema.UserAgenticMessage(strings.Repeat("source observation ", 25000))}
			for replay := 0; replay < 2; replay++ {
				run.Step.Store(0)
				var got *schema.AgenticMessage
				if streaming {
					reader, err := gateway.Stream(ctx, input, model.WithMaxTokens(131072))
					if err != nil {
						t.Fatal(err)
					}
					chunks := []*schema.AgenticMessage{}
					for {
						chunk, e := reader.Recv()
						if e == io.EOF {
							break
						}
						if e != nil {
							reader.Close()
							t.Fatal(e)
						}
						chunks = append(chunks, chunk)
					}
					reader.Close()
					got, err = schema.ConcatAgenticMessages(chunks)
				} else {
					got, err = gateway.Generate(ctx, input, model.WithMaxTokens(131072))
				}
				if err != nil || agenticText(got) != agenticText(output) {
					t.Fatal("large native response was truncated", err)
				}
			}
			if requests.Load() != 1 || count(t, s, `SELECT count(*) FROM runtime_model_calls`) != 1 || count(t, s, `SELECT count(*) FROM runtime_tool_calls`) != 0 {
				t.Fatal("long response replay repeated inference or produced an IM/tool effect")
			}
		})
	}
}
