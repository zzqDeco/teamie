package engine

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/cloudwego/eino-ext/components/model/agenticopenai"
	"github.com/cloudwego/eino/schema"
	"github.com/google/uuid"
	wire "teamie/agent-protocol/kitex_gen/agent"
)

func exhaustedSummary(text string) *schema.AgenticMessage {
	message := &schema.AgenticMessage{
		Role: schema.AgenticRoleTypeAssistant,
		ContentBlocks: []*schema.ContentBlock{
			schema.NewContentBlock(&schema.Reasoning{Text: "synthetic private reasoning"}),
		},
		ResponseMeta: &schema.AgenticResponseMeta{
			Extension:  &agenticopenai.ChatResponseMetaExtension{FinishReason: "length"},
			TokenUsage: &schema.TokenUsage{PromptTokens: 20, CompletionTokens: 2048, TotalTokens: 2068},
		},
	}
	if text != "" {
		message.ContentBlocks = append(message.ContentBlocks, schema.NewContentBlock(&schema.AssistantGenText{Text: text}))
	}
	return message
}

func TestSummaryFailoverOnlyForEmptyLengthWithoutTools(t *testing.T) {
	for _, test := range []struct {
		name    string
		message *schema.AgenticMessage
		err     error
		want    bool
	}{
		{"reasoning exhausted", exhaustedSummary(""), nil, true},
		{"nonempty historical truncation", exhaustedSummary("Preserved historical summary"), nil, false},
		{"transport failure", exhaustedSummary(""), errors.New("fixture transport failure"), false},
		{"nil response", nil, nil, false},
		{"no metadata", &schema.AgenticMessage{Role: schema.AgenticRoleTypeAssistant}, nil, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := retryEmptyLengthSummary(context.Background(), test.message, test.err); got != test.want {
				t.Fatalf("retry=%v want=%v", got, test.want)
			}
		})
	}
	for _, block := range []*schema.ContentBlock{
		schema.NewContentBlock(&schema.FunctionToolCall{CallID: "forbidden-call", Name: "im_send_message", Arguments: "{}"}),
		schema.NewContentBlock(&schema.FunctionToolResult{CallID: "forbidden-result", Name: "im_send_message"}),
	} {
		message := exhaustedSummary("")
		message.ContentBlocks = append(message.ContentBlocks, block)
		if retryEmptyLengthSummary(context.Background(), message, nil) {
			t.Fatal("tool-bearing response entered summary failover")
		}
	}
	message := exhaustedSummary("")
	message.ResponseMeta.Extension.(*agenticopenai.ChatResponseMetaExtension).FinishReason = "stop"
	if retryEmptyLengthSummary(context.Background(), message, nil) {
		t.Fatal("ordinary empty summary entered length recovery")
	}
}

func TestEmptyLengthSummaryReplaysFiveReceiptsAndAppendsRecoveryOnSameRun(t *testing.T) {
	s := testStore(t)
	e, imf, base, _ := fixtureEngine(t, s)
	def := &wire.AgentDefinition{Id: imf.agent, OwnerId: uuid.NewString(), Name: "Agent", Enabled: true, Revision: 1}
	e.Control = &rpcOwnerControl{definition: def}
	seedReplayProfileHistory(t, s, imf)
	historyIM := newToolPathIM(t, e, imf)
	for range 3 {
		imf.add("synthetic request after accepted history", "")
	}
	read, _ := toolPathCall("im_list_messages", "old-history-read", listMessagesArgs{ConversationID: historyIM.conversation, Limit: 2})
	send, _ := toolPathCall("im_send_message", "old-explicit-send", sendArgs{ConversationID: imf.conversation, Body: "one explicit synthetic reply"})
	gateway := &replayProfileGateway{fixtureGateway: base, steps: []replayFixtureStep{
		{summary: true, message: chatMessage{Role: "assistant", Content: "First private summary"}, tokens: 25},
		{message: read, tokens: 20000},
		{summary: true, native: exhaustedSummary("Historical nonempty length summary")},
		{message: send, tokens: 20000},
		{summary: true, native: exhaustedSummary("")},
		// Stop at the same five-model/two-tool persisted boundary as the old
		// writer. No sixth response is committed by this transport failure.
		{summary: true, errorCode: "invalid_request"},
		{summary: true, message: chatMessage{Role: "assistant", Content: "Recovered source-aware private summary"}, tokens: 25},
		{errorCode: "invalid_request"}, // restart again after recovery step 6
		{message: chatMessage{Role: "assistant", Content: "PRIVATE_COMPLETED"}, tokens: 25},
	}}
	e.Gateway = gateway
	stop, done := startReplayProfileWorker(t, e, def)
	waitFor(t, func() bool {
		gateway.snapshot(t)
		return count(t, s, `SELECT count(*) FROM runtime_runs WHERE state='failed' AND safe_error='invalid_request'`) == 1
	})
	stop()
	<-done
	var runID string
	if err := s.DB.QueryRow(context.Background(), `SELECT id::text FROM runtime_runs WHERE state='failed'`).Scan(&runID); err != nil {
		t.Fatal(err)
	}
	original := snapshotReplayReceipts(t, s, runID, 5)
	if len(original) != 7 || count(t, s, `SELECT count(*) FROM runtime_model_calls WHERE run_id=$1`, runID) != 5 {
		t.Fatal("fixture did not persist five model and two tool receipts")
	}
	if count(t, s, `SELECT count(*) FROM runtime_session_events WHERE run_id=$1 AND kind='messages_replaced'`, runID) != 2 {
		t.Fatal("empty summary replaced the Session or historical length summary was rejected")
	}
	for attempt, modelCount := range []int{6, 7} {
		retryReplayProfileRun(t, e, def, runID)
		stop, done = startReplayProfileWorker(t, e, def)
		state := "failed"
		if attempt == 1 {
			state = "completed"
		}
		waitFor(t, func() bool {
			gateway.snapshot(t)
			return count(t, s, `SELECT count(*) FROM runtime_runs WHERE id=$1 AND state=$2 AND attempts=$3`, runID, state, attempt+2) == 1
		})
		stop()
		<-done
		if !reflect.DeepEqual(original, snapshotReplayReceipts(t, s, runID, 5)) {
			t.Fatal("recovery rewrote an original hash, response or tool receipt")
		}
		if count(t, s, `SELECT count(*) FROM runtime_model_calls WHERE run_id=$1`, runID) != modelCount || count(t, s, `SELECT count(*) FROM runtime_tool_calls WHERE run_id=$1`, runID) != 2 {
			t.Fatal("recovery repeated a model/tool side effect or skipped its next durable step")
		}
		if count(t, s, `SELECT count(*) FROM runtime_model_calls old JOIN runtime_model_calls recovered ON old.run_id=recovered.run_id
			WHERE old.run_id=$1 AND old.step=5 AND recovered.step=6 AND old.request_hash=recovered.request_hash`, runID) != 1 {
			t.Fatal("native failover changed the original summary input or options")
		}
	}
	if len(gateway.snapshot(t)) != 9 || count(t, s, `SELECT count(*) FROM runtime_runs`) != 2 || count(t, s, `SELECT count(*) FROM runtime_inputs WHERE run_id=$1 AND state='done'`, runID) != 3 {
		t.Fatal("restart did not reuse the same Run and cached recovery step")
	}
	imf.mu.Lock()
	posts, messages := imf.posts, len(imf.sent)
	imf.mu.Unlock()
	if posts != 1 || messages != 1 {
		t.Fatalf("recovery duplicated the committed send: posts=%d messages=%d", posts, messages)
	}
}
