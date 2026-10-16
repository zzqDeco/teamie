package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	"github.com/google/uuid"
	"teamie/agent-protocol/modeltransport"
)

func TestLegacySessionEventsReadNativelyWithoutChangingIdentity(t *testing.T) {
	source := &schema.Message{Role: schema.User, Content: `TEAMIE_NEW_MESSAGE {"eventId":"17","workspaceId":"w","conversationId":"c","messageId":"m","senderId":"s","senderKind":"human","body":"source body"}`, Extra: map[string]any{"_eino_msg_id": "original-message-id", "source": "original-audit"}}
	toolIndex := 0
	assistant := &schema.Message{Role: schema.Assistant, ReasoningContent: "private reasoning", Content: "private text", ToolCalls: []schema.ToolCall{{Index: &toolIndex, ID: "stable-call", Type: "function", Function: schema.FunctionCall{Name: "im_send_message", Arguments: `{"body":"same"}`}}}, ResponseMeta: &schema.ResponseMeta{FinishReason: "tool_calls", Usage: &schema.TokenUsage{PromptTokens: 4, CompletionTokens: 3, TotalTokens: 7}}}
	receipt := &schema.Message{Role: schema.Tool, ToolCallID: "stable-call", ToolName: "im_send_message", Content: "durable tool receipt"}
	replaced := []*schema.Message{{Role: schema.User, Content: `TEAMIE_SESSION_SUMMARY previous summary` + "\nTEAMIE_SOURCE_REFERENCES " + `[{"eventId":"11","workspaceId":"old-w","conversationId":"old-c","messageId":"old-m","senderId":"old-s"}]`}, source, assistant, receipt}
	for _, test := range []struct {
		name    string
		event   *adk.SessionEvent[*schema.Message]
		message func(*adk.SessionEvent[*schema.AgenticMessage]) *schema.AgenticMessage
	}{
		{"message", &adk.SessionEvent[*schema.Message]{Message: source}, func(e *adk.SessionEvent[*schema.AgenticMessage]) *schema.AgenticMessage { return e.Message }},
		{"replacement", &adk.SessionEvent[*schema.Message]{MessagesReplaced: &replaced}, func(e *adk.SessionEvent[*schema.AgenticMessage]) *schema.AgenticMessage {
			return (*e.MessagesReplaced)[1]
		}},
		{"update", &adk.SessionEvent[*schema.Message]{MessageUpdated: &adk.MessageUpdatedEvent[*schema.Message]{MessageID: "target-message", Message: source}}, func(e *adk.SessionEvent[*schema.AgenticMessage]) *schema.AgenticMessage {
			if e.MessageUpdated.MessageID != "target-message" {
				t.Fatal("update target changed")
			}
			return e.MessageUpdated.Message
		}},
		{"insert", &adk.SessionEvent[*schema.Message]{MessageInserted: &adk.MessageInsertedEvent[*schema.Message]{BeforeMessageID: "before-message", Message: source}}, func(e *adk.SessionEvent[*schema.AgenticMessage]) *schema.AgenticMessage {
			if e.MessageInserted.BeforeMessageID != "before-message" {
				t.Fatal("insertion position changed")
			}
			return e.MessageInserted.Message
		}},
		{"incomplete", &adk.SessionEvent[*schema.Message]{MessageStreamIncomplete: &adk.MessageStreamIncompleteEvent[*schema.Message]{Message: source, Error: "safe-error"}}, func(e *adk.SessionEvent[*schema.AgenticMessage]) *schema.AgenticMessage {
			if e.MessageStreamIncomplete.Error != "safe-error" {
				t.Fatal("incomplete error lost")
			}
			return e.MessageStreamIncomplete.Message
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			test.event.EventID = "original-event-id"
			test.event.TurnID = "original-turn-id"
			test.event.Timestamp = time.Unix(10, 0).UTC()
			test.event.Extra = map[string]any{"audit": "original"}
			if err := adk.NormalizeSessionEventKind(test.event); err != nil {
				t.Fatal(err)
			}
			b, err := (&schema.HumanReadableSerializer{}).Marshal(test.event)
			if err != nil {
				t.Fatal(err)
			}
			original := bytes.Clone(b)
			var native adk.SessionEvent[*schema.AgenticMessage]
			if err = decodeSessionPayload(b, &native); err != nil {
				t.Fatal(err)
			}
			message := test.message(&native)
			if native.EventID != test.event.EventID || native.TurnID != test.event.TurnID || native.Kind != test.event.Kind || !native.Timestamp.Equal(test.event.Timestamp) || native.Extra["audit"] != "original" || agenticText(message) != source.Content || message.Extra["_eino_msg_id"] != "original-message-id" || !bytes.Equal(b, original) {
				t.Fatal("legacy event or source identity changed")
			}
			if native.MessagesReplaced != nil {
				state := *native.MessagesReplaced
				if !hasToolCalls(state[2]) || !hasToolResults(state[3]) || state[2].ContentBlocks[0].Reasoning.Text != "private reasoning" || state[3].ContentBlocks[0].FunctionToolResult.CallID != "stable-call" {
					t.Fatal("legacy tool/reasoning group was flattened")
				}
				back, err := legacyMessages(state[2:3])
				if err != nil || back[0].ToolCalls[0].Index == nil || *back[0].ToolCalls[0].Index != 0 {
					t.Fatal("legacy streamed call index lost", err)
				}
				compacted, err := finalizeSummary(context.Background(), state, schema.UserAgenticMessage("second summary"))
				if err != nil {
					t.Fatal(err)
				}
				for _, id := range []string{"17", "w", "c", "m", "s", "old-w", "old-c", "old-m", "old-s"} {
					if !bytes.Contains([]byte(agenticText(compacted[0])), []byte(id)) {
						t.Fatalf("summary lost source %s", id)
					}
				}
			}
		})
	}
}

func TestLegacyModelReceiptNeedsExactHashAndNeverCallsGateway(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		t.Run(map[bool]string{false: "generate", true: "stream"}[streaming], func(t *testing.T) {
			s := testStore(t)
			ctx := context.Background()
			agent := uuid.NewString()
			l, err := s.Acquire(ctx, agent, "legacy-model")
			if err != nil {
				t.Fatal(err)
			}
			defer s.Release(ctx, l)
			in := Input{EventID: "1", Body: "original"}
			if err = s.Ingest(ctx, l, "1", []Input{in}); err != nil {
				t.Fatal(err)
			}
			run, err := s.StartRun(ctx, l, []Input{in})
			if err != nil {
				t.Fatal(err)
			}
			messages := []*schema.Message{{Role: schema.User, Content: "original"}}
			opts := &model.Options{}
			raw, err := legacyChatRequest(messages, nil, opts)
			if err != nil {
				t.Fatal(err)
			}
			if streaming {
				var obj map[string]any
				_ = json.Unmarshal(raw, &obj)
				obj["stream"] = true
				obj["stream_options"] = map[string]any{"include_usage": true}
				raw, _ = json.Marshal(obj)
			}
			response, _ := json.Marshal(&schema.Message{Role: schema.Assistant, Content: "cached private", ReasoningContent: "cached reasoning", ResponseMeta: &schema.ResponseMeta{FinishReason: "stop", Usage: &schema.TokenUsage{PromptTokens: 5, CompletionTokens: 2, TotalTokens: 7}}})
			if err = s.SaveModel(ctx, l, run, 1, digest(raw), response); err != nil {
				t.Fatal(err)
			}
			gateway := &GatewayModel{Store: s, Lease: l, AgentID: agent} // No client or stream URL: any external attempt fails this test.
			if streaming {
				r, e := gateway.Stream(WithRun(ctx, run), nativeMessages(messages))
				if e != nil {
					t.Fatal(e)
				}
				m, e := r.Recv()
				r.Close()
				if e != nil || agenticText(m) != "cached private" {
					t.Fatal("legacy stream receipt was not recovered", e)
				}
			} else {
				m, e := gateway.Generate(WithRun(ctx, run), nativeMessages(messages))
				if e != nil || agenticText(m) != "cached private" || m.ContentBlocks[0].Reasoning.Text != "cached reasoning" || m.ResponseMeta.TokenUsage.TotalTokens != 7 {
					t.Fatal("legacy generate receipt was not recovered", e)
				}
			}
			run.Step.Store(0)
			messages[0].Content = "changed source"
			if streaming {
				_, err = gateway.Stream(WithRun(ctx, run), nativeMessages(messages))
			} else {
				_, err = gateway.Generate(WithRun(ctx, run), nativeMessages(messages))
			}
			if !errors.Is(err, ErrReplayConflict) {
				t.Fatal("divergent legacy replay accepted", err)
			}
		})
	}
}

// Rewrite fixture records into the exact v3 receipt shape, then put an opaque
// classic checkpoint in place. This simulates the old writer stopping at the
// crash boundary; the new typed writer must recover the same logical effects.
func fixtureV3Cutover(t *testing.T, s *Store, request []byte) {
	t.Helper()
	ctx := context.Background()
	var envelope modeltransport.Request
	if err := json.Unmarshal(request, &envelope); err != nil {
		t.Fatal(err)
	}
	var messages []*schema.AgenticMessage
	var opts model.Options
	if err := (&schema.HumanReadableSerializer{}).Unmarshal(envelope.Messages, &messages); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(envelope.Options, &opts); err != nil {
		t.Fatal(err)
	}
	if opts.AgenticToolChoice != nil {
		opts.ToolChoice = &opts.AgenticToolChoice.Type
	}
	// v0.3.4/0ce504f advertised the four original IM tools. Converting the
	// current response format alone would retain v0.3.5's added attachment
	// tool and fail to exercise recovery of a real pre-upgrade request hash.
	previousTools := make([]*schema.ToolInfo, 0, len(opts.Tools))
	for _, info := range opts.Tools {
		if info.Name != "im_get_attachment" {
			previousTools = append(previousTools, info)
		}
	}
	opts.Tools = previousTools
	classic, err := legacyMessages(messages)
	if err != nil {
		t.Fatal(err)
	}
	requestV3, err := legacyChatRequest(classic, opts.Tools, &opts)
	if err != nil {
		t.Fatal(err)
	}
	var runID, agentID string
	var response []byte
	if err = s.DB.QueryRow(ctx, `SELECT c.run_id::text,r.agent_id::text,c.response FROM runtime_model_calls c JOIN runtime_runs r ON r.id=c.run_id WHERE c.step=1`).Scan(&runID, &agentID, &response); err != nil {
		t.Fatal(err)
	}
	native, err := decodeAgenticMessage(response)
	if err != nil {
		t.Fatal(err)
	}
	old, err := legacyMessages([]*schema.AgenticMessage{native})
	if err != nil {
		t.Fatal(err)
	}
	if native.ResponseMeta != nil {
		old[0].ResponseMeta = &schema.ResponseMeta{Usage: native.ResponseMeta.TokenUsage}
	}
	responseV3, err := json.Marshal(old[0])
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.DB.Exec(ctx, `UPDATE runtime_model_calls SET request_hash=$2,response=$3 WHERE run_id=$1 AND step=1`, runID, digest(requestV3), responseV3); err != nil {
		t.Fatal(err)
	}
	metadata, _ := json.Marshal(checkpointMetadata{Version: "eino-v0.10.0-alpha.35/teamie-v3", RunID: runID, Attempt: uuid.NewString(), Revision: 1, Targets: []string{"old-target"}})
	if _, err = s.DB.Exec(ctx, `INSERT INTO runtime_checkpoints(checkpoint_id,payload,metadata)VALUES($1,$2,$3) ON CONFLICT(checkpoint_id)DO UPDATE SET payload=EXCLUDED.payload,metadata=EXCLUDED.metadata`, "turnloop:"+agentID, []byte("opaque classic checkpoint must never be decoded"), metadata); err != nil {
		t.Fatal(err)
	}
	lease, err := s.Acquire(ctx, agentID, "compatibility-probe")
	if err != nil {
		t.Fatal(err)
	}
	store := &SessionStore[*schema.AgenticMessage]{Store: s, Lease: lease}
	_, found, err := store.Get(ctx, "turnloop:"+agentID)
	if err != nil || found {
		t.Fatal("old opaque checkpoint was accepted", err)
	}
	s.Release(ctx, lease)
}
