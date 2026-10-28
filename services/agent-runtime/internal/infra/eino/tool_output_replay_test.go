package eino

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/adk/middlewares/summarization"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
	"github.com/google/uuid"
)

func replayResultMessage(callID, name, text string) *schema.AgenticMessage {
	return &schema.AgenticMessage{Role: schema.AgenticRoleTypeUser, ContentBlocks: []*schema.ContentBlock{schema.NewContentBlock(&schema.FunctionToolResult{CallID: callID, Name: name, Content: []*schema.FunctionToolResultContentBlock{{Type: schema.FunctionToolResultContentBlockTypeText, Text: &schema.UserInputText{Text: text}}}})}}
}

func saveObservedOutputs(t *testing.T, s *Store, lease *Lease, run *Run, classic bool, outputs ...*schema.AgenticMessage) {
	t.Helper()
	old := &Run{ID: run.ID, AgentID: run.AgentID, Attempt: uuid.NewString()}
	if classic {
		store := &SessionStore[*schema.Message]{Store: s, Lease: lease}
		messages, err := legacyMessages(outputs)
		if err != nil {
			t.Fatal(err)
		}
		for _, message := range messages {
			if err := store.AppendEvents(WithRun(context.Background(), old), "agent:"+run.AgentID, []*adk.SessionEvent[*schema.Message]{{EventID: uuid.NewString(), Message: message}}); err != nil {
				t.Fatal(err)
			}
		}
		return
	}
	store := &SessionStore[*schema.AgenticMessage]{Store: s, Lease: lease}
	for _, message := range outputs {
		if err := store.AppendEvents(WithRun(context.Background(), old), "agent:"+run.AgentID, []*adk.SessionEvent[*schema.AgenticMessage]{{EventID: uuid.NewString(), Message: message}}); err != nil {
			t.Fatal(err)
		}
	}
}

func oldUnescapedOutput(t *testing.T, output any) string {
	t.Helper()
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(output); err != nil {
		t.Fatal(err)
	}
	return strings.TrimSuffix(buffer.String(), "\n")
}

func TestCommittedIMToolOutputIsByteStableAndKeepsLargeIntegers(t *testing.T) {
	s, lease, run := nativeModelRun(t)
	conversationID, messageID := uuid.NewString(), uuid.NewString()
	imHTTP := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"message": Message{ID: messageID, ConversationID: conversationID, Body: "<tag>&中文😀", Seq: 9007199254740993}})
	}))
	defer imHTTP.Close()
	tools := &Tools{Store: s, Lease: lease, IM: &IM{BaseURL: imHTTP.URL, token: "fixture-principal-token", expiry: time.Now().Add(time.Hour)}}
	var effects int
	item, err := utils.InferTool("im_get_message", "Synthetic structured observation", func(ctx context.Context, args *getMessageArgs) (*Observation, error) {
		return tools.execute(ctx, "im_get_message", args, func(context.Context, string) (*Observation, error) {
			effects++
			return &Observation{OK: true, Data: Message{ID: args.MessageID, ConversationID: args.ConversationID, Body: "<tag>&中文😀", Seq: 9007199254740993}}, nil
		})
	}, utils.WithMarshalOutput(marshalObservation))
	if err != nil {
		t.Fatal(err)
	}
	ctx := WithRun(context.Background(), run)
	node, err := compose.NewToolNode(ctx, &compose.ToolsNodeConfig{Tools: []tool.BaseTool{item}, ExecuteSequentially: true})
	if err != nil {
		t.Fatal(err)
	}
	args, _ := json.Marshal(getMessageArgs{ConversationID: conversationID, MessageID: messageID})
	call := &schema.Message{Role: schema.Assistant, ToolCalls: []schema.ToolCall{{ID: "large-output", Type: "function", Function: schema.FunctionCall{Name: "im_get_message", Arguments: string(args)}}}}
	first, err := node.Invoke(ctx, call)
	if err != nil {
		t.Fatal(err)
	}
	second, err := node.Invoke(ctx, call)
	if err != nil {
		t.Fatal(err)
	}
	var receipt []byte
	if err = s.DB.QueryRow(ctx, `SELECT result FROM runtime_tool_calls WHERE run_id=$1 AND call_id='large-output'`, run.ID).Scan(&receipt); err != nil {
		t.Fatal(err)
	}
	if effects != 1 || len(first) != 1 || len(second) != 1 || first[0].Content != string(receipt) || second[0].Content != string(receipt) || !bytes.Contains(receipt, []byte(`9007199254740993`)) || !bytes.Contains(receipt, []byte(`\u003ctag\u003e\u0026中文😀`)) {
		t.Fatal("live/cached output changed committed JSON, integer precision, Unicode or escaping")
	}
}

func TestObservedMixedAttemptRestoresOnlyExactModelRequests(t *testing.T) {
	for _, classic := range []bool{true, false} {
		for _, streaming := range []bool{false, true} {
			t.Run(fmt.Sprintf("classic=%v/stream=%v", classic, streaming), func(t *testing.T) {
				s, lease, run := nativeModelRun(t)
				ctx := WithRun(context.Background(), run)
				name := "im_get_message"
				var receipts [2][]byte
				var live, cached [2]string
				for i, id := range []string{"read-a", "read-b"} {
					output := &Observation{OK: true, Data: Message{ID: uuid.NewString(), Body: "<tag>&中文😀", Seq: 9007199254740993 - int64(i)}}
					receipts[i], _ = json.Marshal(output)
					live[i] = oldUnescapedOutput(t, output)
					var roundTrip Observation
					if err := json.Unmarshal(receipts[i], &roundTrip); err != nil {
						t.Fatal(err)
					}
					cached[i] = oldUnescapedOutput(t, &roundTrip)
					if _, err := s.ToolIntent(ctx, lease, run, id, name, []byte(`{}`)); err != nil {
						t.Fatal(err)
					}
					if err := s.ToolResult(ctx, lease, run, id, receipts[i]); err != nil {
						t.Fatal(err)
					}
				}
				// Attempt 1 completed A only. Attempt 2 replayed cached A then
				// first executed B; its next model receipt is the one we retain.
				// Attempt 3 replayed both cached results before another crash.
				saveObservedOutputs(t, s, lease, run, classic, replayResultMessage("read-a", name, live[0]))
				saveObservedOutputs(t, s, lease, run, classic, replayResultMessage("read-a", name, cached[0]), replayResultMessage("read-b", name, live[1]))
				saveObservedOutputs(t, s, lease, run, classic, replayResultMessage("read-a", name, cached[0]), replayResultMessage("read-b", name, cached[1]))
				prefix := []*schema.AgenticMessage{schema.SystemAgenticMessage("unchanged private policy"), schema.UserAgenticMessage("unchanged source")}
				// Native reasoning, extensions and tool identity must survive the
				// observation-only overlay; do not project native requests to classic.
				assistant, err := classicToAgentic(&schema.Message{Role: schema.Assistant, ReasoningContent: "private reasoning", ToolCalls: []schema.ToolCall{{ID: "read-a", Type: "function", Function: schema.FunctionCall{Name: name, Arguments: `{}`}}, {ID: "read-b", Type: "function", Function: schema.FunctionCall{Name: name, Arguments: `{}`}}}})
				if err != nil {
					t.Fatal(err)
				}
				prefix = append(prefix, assistant)
				original := append(append([]*schema.AgenticMessage{}, prefix...), replayResultMessage("read-a", name, cached[0]), replayResultMessage("read-b", name, live[1]))
				var options model.Options
				request, err := agenticRequest(original, &options, streaming)
				if err != nil {
					t.Fatal(err)
				}
				hash := modelRequestHash(request)
				response, _ := classicToAgentic(&schema.Message{Role: schema.Assistant, Content: "cached private result"})
				raw, _ := (&schema.HumanReadableSerializer{}).Marshal(response)
				if classic {
					legacy := v034FixtureRequest(t, string(request))
					hash = digest(legacy)
					raw, _ = json.Marshal(&schema.Message{Role: schema.Assistant, Content: "cached private result"})
				}
				if err = s.SaveModel(ctx, lease, run, 1, hash, raw); err != nil {
					t.Fatal(err)
				}
				var current []*schema.AgenticMessage
				current = append(current, prefix...)
				for i, id := range []string{"read-a", "read-b"} {
					wire, err := s.replayToolWire(ctx, run, id, name, receipts[i])
					if err != nil || string(wire) != cached[i] {
						t.Fatal("old wire was not restored before token accounting", err)
					}
					current = append(current, replayResultMessage(id, name, string(wire)))
				}
				serializer := &schema.HumanReadableSerializer{}
				before, _ := serializer.Marshal(current)
				gateway := &GatewayModel{Store: s, Lease: lease} // No provider transport: every success must be a strict cache hit.
				if streaming {
					reader, err := gateway.Stream(ctx, current)
					if err != nil {
						t.Fatal(err)
					}
					message, err := reader.Recv()
					reader.Close()
					if err != nil || agenticText(message) != "cached private result" {
						t.Fatal("mixed old output stream did not match its receipt", err)
					}
				} else if message, err := gateway.Generate(ctx, current); err != nil || agenticText(message) != "cached private result" {
					t.Fatal("mixed old output did not match its receipt", err)
				}
				after, _ := serializer.Marshal(current)
				if !bytes.Equal(before, after) {
					t.Fatal("historical candidate mutated Eino's live native state")
				}
				for _, change := range []string{"policy", "source", "tool arguments", "tool output"} {
					var changed []*schema.AgenticMessage
					if err := serializer.Unmarshal(before, &changed); err != nil {
						t.Fatal(err)
					}
					switch change {
					case "policy":
						changed[0] = schema.SystemAgenticMessage("different policy")
					case "source":
						changed[1] = schema.UserAgenticMessage("different source")
					case "tool arguments":
						changed[2].ContentBlocks[1].FunctionToolCall.Arguments = `{"changed":true}`
					case "tool output":
						changed[3].ContentBlocks[0].FunctionToolResult.Content[0].Text.Text = `{"ok":true,"data":"different private result"}`
					}
					run.Step.Store(0)
					if streaming {
						_, err = gateway.Stream(ctx, changed)
					} else {
						_, err = gateway.Generate(ctx, changed)
					}
					if !errors.Is(err, ErrReplayConflict) {
						t.Fatalf("%s bypassed the complete request hash: %v", change, err)
					}
				}
			})
		}
	}
}

func TestOldToolWireKeepsNativeSummaryTriggerAtTheBoundary(t *testing.T) {
	s, lease, run := nativeModelRun(t)
	ctx := WithRun(context.Background(), run)
	output := &Observation{OK: true, Data: Message{ID: uuid.NewString(), Body: strings.Repeat("<tag>&中文😀", 40), Seq: 42}}
	receipt, _ := json.Marshal(output)
	oldWire := oldUnescapedOutput(t, output)
	if len(receipt)/4 <= len(oldWire)/4 {
		t.Fatal("fixture did not cross the old serializer's HTML token boundary")
	}
	saveObservedOutputs(t, s, lease, run, true, replayResultMessage("old-html", "im_get_message", oldWire))
	restored, err := s.replayToolWire(ctx, run, "old-html", "im_get_message", receipt)
	if err != nil || string(restored) != oldWire {
		t.Fatal("historical wire was not selected before summary accounting", err)
	}
	gateway := &replayProfileGateway{fixtureGateway: &fixtureGateway{ready: true}, steps: []replayFixtureStep{{summary: true, message: chatMessage{Role: "assistant", Content: "private boundary summary"}, tokens: 25}}}
	middleware, err := summarization.NewTyped(ctx, &summarization.TypedConfig[*schema.AgenticMessage]{Model: &GatewayModel{Client: gateway, Store: s, Lease: lease}, ModelOptions: []model.Option{model.WithAgenticToolChoice(&schema.AgenticToolChoice{Type: schema.ToolChoiceForbidden})}, Trigger: &summarization.TriggerCondition{ContextTokens: 1000 + len(oldWire)/4}, Finalize: finalizeV034Summary})
	if err != nil {
		t.Fatal(err)
	}
	baseline, _ := classicToAgentic(&schema.Message{Role: schema.Assistant, Content: "private baseline", ResponseMeta: &schema.ResponseMeta{Usage: &schema.TokenUsage{TotalTokens: 1000}}})
	state := &adk.TypedChatModelAgentState[*schema.AgenticMessage]{Messages: []*schema.AgenticMessage{baseline, replayResultMessage("old-html", "im_get_message", string(restored))}}
	_, after, err := middleware.BeforeModelRewriteState(ctx, state, nil)
	if err != nil || after != state || len(gateway.snapshot(t)) != 0 {
		t.Fatal("restored old wire advanced a summary/model step", err)
	}
	// This is the exact failure the pre-token restore prevents: substituting
	// stored escaped bytes would invoke summary despite unchanged visible data.
	state.Messages[1] = replayResultMessage("old-html", "im_get_message", string(receipt))
	if _, _, err = middleware.BeforeModelRewriteState(ctx, state, nil); err != nil || len(gateway.snapshot(t)) != 1 {
		t.Fatal("real pinned Eino counter did not expose the escaped-byte boundary", err)
	}
}

func TestAmbiguousHistoricalToolTokenCountFailsClosed(t *testing.T) {
	s, lease, run := nativeModelRun(t)
	output := &Observation{OK: true, Data: Message{Body: strings.Repeat("<>&", 20)}}
	receipt, _ := json.Marshal(output)
	saveObservedOutputs(t, s, lease, run, true, replayResultMessage("same-call", "im_get_message", oldUnescapedOutput(t, output)))
	saveObservedOutputs(t, s, lease, run, false, replayResultMessage("same-call", "im_get_message", string(receipt)))
	if _, err := s.replayToolWire(WithRun(context.Background(), run), run, "same-call", "im_get_message", receipt); !errors.Is(err, ErrReplayConflict) {
		t.Fatal("ambiguous token accounting silently moved the replay boundary", err)
	}
}

func TestObservationCandidatesRejectAmbiguousAndForeignOutputs(t *testing.T) {
	for _, kind := range []string{"duplicate current call", "ambiguous past call", "foreign Run", "native receipt at classic hash"} {
		t.Run(kind, func(t *testing.T) {
			s, lease, run := nativeModelRun(t)
			ctx := WithRun(context.Background(), run)
			output := &Observation{OK: true, Data: Message{ID: "synthetic", Body: "private"}}
			receipt, _ := json.Marshal(output)
			var cached Observation
			_ = json.Unmarshal(receipt, &cached)
			reordered := oldUnescapedOutput(t, &cached)
			if _, err := s.ToolIntent(ctx, lease, run, "same-call", "im_get_message", []byte(`{}`)); err != nil {
				t.Fatal(err)
			}
			if err := s.ToolResult(ctx, lease, run, "same-call", receipt); err != nil {
				t.Fatal(err)
			}
			message := replayResultMessage("same-call", "im_get_message", string(receipt))
			messages := []*schema.AgenticMessage{message}
			switch kind {
			case "duplicate current call":
				saveObservedOutputs(t, s, lease, run, false, replayResultMessage("same-call", "im_get_message", reordered))
				messages = append(messages, message)
			case "ambiguous past call":
				saveObservedOutputs(t, s, lease, run, false, message, replayResultMessage("same-call", "im_get_message", reordered))
				if _, err := s.replayToolWire(ctx, run, "same-call", "im_get_message", receipt); !errors.Is(err, ErrReplayConflict) {
					t.Fatal("same-attempt ambiguous output did not fail before token accounting", err)
				}
			case "foreign Run":
				input := Input{EventID: "2", Body: "another Run"}
				if err := s.Ingest(ctx, lease, "2", []Input{input}); err != nil {
					t.Fatal(err)
				}
				foreign, err := s.StartRun(ctx, lease, []Input{input})
				if err != nil {
					t.Fatal(err)
				}
				saveObservedOutputs(t, s, lease, foreign, false, replayResultMessage("same-call", "im_get_message", reordered))
			case "native receipt at classic hash":
				saveObservedOutputs(t, s, lease, run, false, replayResultMessage("same-call", "im_get_message", reordered))
				legacyInput := []*schema.AgenticMessage{replayResultMessage("same-call", "im_get_message", reordered)}
				request := v034FixtureRequest(t, string(mustAgenticRequest(t, legacyInput)))
				native, _ := classicToAgentic(&schema.Message{Role: schema.Assistant, Content: "native cached result"})
				raw, _ := (&schema.HumanReadableSerializer{}).Marshal(native)
				if err := s.SaveModel(ctx, lease, run, 1, digest(request), raw); err != nil {
					t.Fatal(err)
				}
				if _, err := (&GatewayModel{Store: s, Lease: lease}).Generate(ctx, messages); !errors.Is(err, ErrReplayConflict) {
					t.Fatal("native receipt bypassed its native request identity", err)
				}
				return
			}
			candidates, err := s.observedToolReplayInputs(ctx, run, messages)
			if err != nil || len(candidates) != 0 {
				t.Fatal("observation compatibility accepted an ambiguous/foreign state", err)
			}
		})
	}
}
