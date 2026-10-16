package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"

	"github.com/cloudwego/eino/schema"
	"github.com/cloudwego/kitex/client/callopt"
	wire "teamie/agent-protocol/kitex_gen/agent"
)

// Native Eino and GatewayModel handle the complete Session/summary/tool flow.
// Only the model gateway transport is replaced with a protocol fixture.
type summaryGatewayFixture struct {
	*fixtureGateway
	summaryMu          sync.Mutex
	mode               string
	conversation       string
	failure            error
	summaryCalls       int
	normalAfterSummary int
}

func (f *summaryGatewayFixture) Chat(ctx context.Context, r *wire.GatewayChatRequest, opts ...callopt.Option) (*wire.GatewayChatResponse, error) {
	f.summaryMu.Lock()
	defer f.summaryMu.Unlock()
	var request struct {
		Messages   []chatMessage `json:"messages"`
		Tools      []chatTool    `json:"tools"`
		ToolChoice string        `json:"tool_choice"`
	}
	if err := decodeFixtureRequest(r.RequestJson, &request); err != nil {
		f.failure = err
		return nil, err
	}
	if len(request.Tools) > 0 {
		if request.ToolChoice != "auto" {
			f.failure = fmt.Errorf("ordinary Agent lost its tool choice: %q", request.ToolChoice)
			return nil, f.failure
		}
		if f.summaryCalls > 0 {
			f.normalAfterSummary++
		}
		return f.fixtureGateway.Chat(ctx, r, opts...)
	}
	f.summaryCalls++
	if request.ToolChoice != "none" {
		f.failure = fmt.Errorf("summary did not forbid tool calls: %q", request.ToolChoice)
		return nil, f.failure
	}
	historySend := false
	for _, message := range request.Messages {
		for _, call := range message.ToolCalls {
			historySend = historySend || call.Function.Name == "im_send_message"
		}
	}
	if !historySend {
		f.failure = fmt.Errorf("summary fixture did not receive prior IM tool history")
		return nil, f.failure
	}
	if f.mode == "upstream_unavailable" {
		code := "upstream_unavailable"
		return &wire.GatewayChatResponse{Ok: false, ErrorCode: &code}, nil
	}
	message := chatMessage{Role: "assistant", Content: "Prior IM reply was sent. Preserve source references and continue the current work."}
	finish := "stop"
	switch f.mode {
	case "empty":
		message.Content = ""
	case "tool_call":
		args, _ := json.Marshal(sendArgs{ConversationID: f.conversation, Body: "SUMMARY_TOOL_MUST_NOT_RUN"})
		message.ToolCalls = []schema.ToolCall{{ID: "summary-send", Type: "function", Function: schema.FunctionCall{Name: "im_send_message", Arguments: string(args)}}}
		finish = "tool_calls"
	}
	raw, err := json.Marshal(map[string]any{
		"choices": []any{map[string]any{"message": message, "finish_reason": finish}},
		"usage":   map[string]int{"prompt_tokens": 20, "completion_tokens": 5, "total_tokens": 25},
	})
	if err != nil {
		f.failure = err
		return nil, err
	}
	response := string(raw)
	return &wire.GatewayChatResponse{Ok: true, ResponseJson: modelFixtureResponse(response)}, nil
}

func TestNativeSummaryForbidsToolsAndKeepsOrdinaryIMTools(t *testing.T) {
	for _, test := range []struct {
		name, mode, state, code string
		posts                   int
	}{
		{name: "text summary", state: "completed", posts: 2},
		{name: "empty summary rejected", mode: "empty", state: "failed", code: "execution_failed", posts: 1},
		{name: "summary tool never executes", mode: "tool_call", state: "failed", code: "execution_failed", posts: 1},
		{name: "upstream failure retains its code", mode: "upstream_unavailable", state: "retry", code: "upstream_unavailable", posts: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			s := testStore(t)
			e, imf, base, _ := fixtureEngine(t, s)
			gateway := &summaryGatewayFixture{fixtureGateway: base, mode: test.mode, conversation: imf.conversation}
			e.Gateway = gateway
			imf.add("first explicit reply", "")
			cancel, done := startFixtureWorker(t, e, imf)
			waitFor(t, func() bool { return count(t, s, `SELECT count(*) FROM runtime_runs WHERE state='completed'`) == 1 })
			cancel()
			<-done

			// Restart with enough fresh inputs to trigger native summarization.
			// The accepted Session already contains an im_send_message and receipt.
			for i := 0; i < 32; i++ {
				imf.add(fmt.Sprintf("continue after compaction %d", i), "")
			}
			cancel, done = startFixtureWorker(t, e, imf)
			waitFor(t, func() bool {
				gateway.summaryMu.Lock()
				failure := gateway.failure
				gateway.summaryMu.Unlock()
				if failure != nil {
					t.Fatal(failure)
				}
				if test.state == "completed" {
					return count(t, s, `SELECT count(*) FROM runtime_inputs WHERE state='done'`) == 33
				}
				return count(t, s, `SELECT count(*) FROM runtime_runs WHERE state=$1 AND safe_error=$2`, test.state, test.code) == 1
			})
			cancel()
			<-done

			gateway.summaryMu.Lock()
			calls, ordinaryCalls, failure := gateway.summaryCalls, gateway.normalAfterSummary, gateway.failure
			gateway.summaryMu.Unlock()
			if failure != nil || calls != 1 {
				t.Fatalf("unexpected summary protocol: calls=%d failure=%v", calls, failure)
			}
			imf.mu.Lock()
			posts, sent := imf.posts, len(imf.sent)
			imf.mu.Unlock()
			if posts != test.posts || sent != test.posts {
				t.Fatalf("unexpected IM effects: requests=%d messages=%d want=%d", posts, sent, test.posts)
			}
			if count(t, s, `SELECT count(*) FROM runtime_tool_calls WHERE call_id='summary-send'`) != 0 {
				t.Fatal("a summary tool call was executed")
			}
			replacements := count(t, s, `SELECT count(*) FROM runtime_session_events WHERE kind='messages_replaced'`)
			if test.state == "completed" {
				if replacements != 1 || ordinaryCalls != 2 {
					t.Fatalf("summary did not return to the native IM tool loop: replacements=%d calls=%d", replacements, ordinaryCalls)
				}
			} else if replacements != 0 || ordinaryCalls != 0 {
				t.Fatalf("invalid summary advanced the Agent: replacements=%d calls=%d", replacements, ordinaryCalls)
			}
		})
	}
}
