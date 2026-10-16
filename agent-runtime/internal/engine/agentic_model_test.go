package engine

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cloudwego/eino-ext/components/model/agenticopenai"
	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	"github.com/cloudwego/kitex/client/callopt"
	"github.com/google/uuid"
	wire "teamie/agent-protocol/kitex_gen/agent"
	"teamie/agent-protocol/kitex_gen/agent/modelgatewayservice"
	"teamie/agent-protocol/modeltransport"
)

type nativeRichGateway struct {
	modelgatewayservice.Client
	calls  atomic.Int32
	output *schema.AgenticMessage
}

func (g *nativeRichGateway) Chat(_ context.Context, _ *wire.GatewayChatRequest, _ ...callopt.Option) (*wire.GatewayChatResponse, error) {
	g.calls.Add(1)
	b, e := (&schema.HumanReadableSerializer{}).Marshal(g.output)
	if e != nil {
		return nil, e
	}
	raw := string(b)
	return &wire.GatewayChatResponse{Ok: true, ResponseJson: &raw}, nil
}

func nativeModelRun(t *testing.T) (*Store, *Lease, *Run) {
	t.Helper()
	s := testStore(t)
	ctx := context.Background()
	agent := uuid.NewString()
	l, e := s.Acquire(ctx, agent, "native-model-test")
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { s.Release(context.Background(), l) })
	input := []Input{{EventID: "1", Body: "source", ConversationID: "source-conversation", MessageID: "source-message", SenderID: "source-sender"}}
	if e = s.Ingest(ctx, l, "1", input); e != nil {
		t.Fatal(e)
	}
	run, e := s.StartRun(ctx, l, input)
	if e != nil {
		t.Fatal(e)
	}
	return s, l, run
}

func TestNativeRichMessageSurvivesReceiptAndSessionPersistence(t *testing.T) {
	s, l, run := nativeModelRun(t)
	output := &schema.AgenticMessage{Role: schema.AgenticRoleTypeAssistant, ContentBlocks: []*schema.ContentBlock{schema.NewContentBlock(&schema.Reasoning{Text: "reasoning"}), schema.NewContentBlock(&schema.AssistantGenText{Text: "private text"}), schema.NewContentBlock(&schema.FunctionToolCall{CallID: "stable-call", Name: "im_send_message", Arguments: `{"body":"explicit"}`})}, ResponseMeta: &schema.AgenticResponseMeta{TokenUsage: &schema.TokenUsage{PromptTokens: 8, CompletionTokens: 3, TotalTokens: 11}, Extension: &agenticopenai.ChatResponseMetaExtension{ID: "response-id", FinishReason: "tool_calls"}}, Extra: map[string]any{"source": "source-message"}}
	g := &nativeRichGateway{output: output}
	gateway := &GatewayModel{Store: s, Lease: l, AgentID: l.AgentID, Client: g}
	ctx := WithRun(context.Background(), run)
	input := inputMessages(run.Inputs)
	result, e := gateway.Generate(ctx, input, model.WithTools([]*schema.ToolInfo{{Name: "im_send_message"}}))
	if e != nil {
		t.Fatal(e)
	}
	session := &SessionStore[*schema.AgenticMessage]{Store: s, Lease: l}
	if e = session.AppendEvents(ctx, "agent:"+l.AgentID, []*adk.SessionEvent[*schema.AgenticMessage]{{EventID: "native-rich-output", Message: result}}); e != nil {
		t.Fatal(e)
	}
	loaded, e := session.LoadEvents(ctx, "agent:"+l.AgentID, nil)
	if e != nil {
		t.Fatal(e)
	}
	run.Step.Store(0)
	cached, e := gateway.Generate(ctx, input, model.WithTools([]*schema.ToolInfo{{Name: "im_send_message"}}))
	if e != nil {
		t.Fatal(e)
	}
	if g.calls.Load() != 1 || len(loaded.Events) != 1 {
		t.Fatal("native response was not replayed from receipt")
	}
	for _, message := range []*schema.AgenticMessage{cached, loaded.Events[0].Message} {
		meta, ok := message.ResponseMeta.Extension.(*agenticopenai.ChatResponseMetaExtension)
		if len(message.ContentBlocks) != 3 || message.ContentBlocks[0].Reasoning.Text != "reasoning" || message.ContentBlocks[1].AssistantGenText.Text != "private text" || message.ContentBlocks[2].FunctionToolCall.CallID != "stable-call" || !ok || meta.ID != "response-id" || meta.FinishReason != "tool_calls" || message.ResponseMeta.TokenUsage.TotalTokens != 11 {
			t.Fatal("native model/Session persistence flattened structured output")
		}
	}
}

func TestNativeStreamCloseCancelsSilentHTTPWithoutReceipt(t *testing.T) {
	s, l, run := nativeModelRun(t)
	canceled := make(chan struct{})
	fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/x-ndjson")
		w.WriteHeader(200)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
		close(canceled)
	}))
	defer fixture.Close()
	gateway := &GatewayModel{Store: s, Lease: l, AgentID: l.AgentID, StreamURL: fixture.URL}
	reader, e := gateway.Stream(WithRun(context.Background(), run), inputMessages(run.Inputs))
	if e != nil {
		t.Fatal(e)
	}
	reader.Close() // Caller may close before receiving even the first chunk.
	select {
	case <-canceled:
	case <-time.After(time.Second):
		t.Fatal("Close left the silent gateway request running")
	}
	if count(t, s, `SELECT count(*) FROM runtime_model_calls`) != 0 {
		t.Fatal("closed partial stream became a complete model receipt")
	}
}

func TestNativeRequestFingerprintKeepsSourcesAndIgnoresOnlyFrameworkMessageID(t *testing.T) {
	input := []*schema.AgenticMessage{schema.UserAgenticMessage(`TEAMIE_NEW_MESSAGE {"eventId":"1","messageId":"source"}`)}
	input[0].Extra = map[string]any{"_eino_msg_id": "first", "sourceAudit": "keep"}
	a, e := agenticRequest(input, &model.Options{}, false)
	if e != nil {
		t.Fatal(e)
	}
	input[0].Extra["_eino_msg_id"] = "second"
	b, e := agenticRequest(input, &model.Options{}, false)
	if e != nil {
		t.Fatal(e)
	}
	if modelRequestHash(a) != modelRequestHash(b) {
		t.Fatal("ephemeral Eino ID changed receipt identity")
	}
	input[0].Extra["sourceAudit"] = "changed"
	c, _ := agenticRequest(input, &model.Options{}, false)
	if modelRequestHash(a) == modelRequestHash(c) {
		t.Fatal("audit data lost from native receipt identity")
	}
	input[0].ContentBlocks[0].UserInputText.Text = `TEAMIE_NEW_MESSAGE {"eventId":"2","messageId":"source"}`
	d, _ := agenticRequest(input, &model.Options{}, false)
	if modelRequestHash(c) == modelRequestHash(d) {
		t.Fatal("changed source ID accepted by receipt")
	}
	var envelope modeltransport.Request
	if json.Unmarshal(a, &envelope) != nil {
		t.Fatal("invalid native envelope")
	}
	var preserved []*schema.AgenticMessage
	if e = (&schema.HumanReadableSerializer{}).Unmarshal(envelope.Messages, &preserved); e != nil || preserved[0].Extra["_eino_msg_id"] != "first" {
		t.Fatal("fingerprinting changed transport/audit data", e)
	}
}

func TestNativeOutputLimitCoversAllOrderedTextBlocks(t *testing.T) {
	message := &schema.AgenticMessage{Role: schema.AgenticRoleTypeAssistant, ContentBlocks: []*schema.ContentBlock{schema.NewContentBlock(&schema.AssistantGenText{Text: strings.Repeat("a", 40000)}), schema.NewContentBlock(&schema.Reasoning{Text: "between text blocks"}), schema.NewContentBlock(&schema.AssistantGenText{Text: strings.Repeat("b", 40000)})}}
	if e := validateModelResult(message); e == nil {
		t.Fatal("splitting text into native blocks bypassed the response limit")
	}
}
