package engine

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
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

func TestNativeActiveStreamSurvivesForcedGCAndCommitsCompleteReceipt(t *testing.T) {
	s, l, run := nativeModelRun(t)
	proceed := make(chan struct{})
	fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/x-ndjson")
		send := func(block *schema.ContentBlock) bool {
			m := &schema.AgenticMessage{Role: schema.AgenticRoleTypeAssistant, ContentBlocks: []*schema.ContentBlock{block}, Extra: map[string]any{"openai-request-id": "gc-request-header"}}
			b, e := (&schema.HumanReadableSerializer{}).Marshal(m)
			if e != nil {
				t.Error(e)
				return false
			}
			if e = json.NewEncoder(w).Encode(map[string]string{"type": "chunk", "responseJson": string(b)}); e != nil {
				return false
			}
			w.(http.Flusher).Flush()
			return true
		}
		if !send(schema.NewContentBlockChunk(&schema.Reasoning{Text: "thinking "}, &schema.StreamingMeta{Index: 0})) {
			return
		}
		select {
		case <-proceed:
		case <-r.Context().Done():
			return
		}
		for _, block := range []*schema.ContentBlock{
			schema.NewContentBlockChunk(&schema.Reasoning{Text: "continued"}, &schema.StreamingMeta{Index: 0}),
			schema.NewContentBlockChunk(&schema.AssistantGenText{Text: "private "}, &schema.StreamingMeta{Index: 1}),
			schema.NewContentBlockChunk(&schema.AssistantGenText{Text: "answer"}, &schema.StreamingMeta{Index: 1}),
			schema.NewContentBlockChunk(&schema.FunctionToolCall{CallID: "gc-stable-call", Name: "im_send_message", Arguments: `{"body":`}, &schema.StreamingMeta{Index: 2}),
			schema.NewContentBlockChunk(&schema.FunctionToolCall{Arguments: `"explicit"}`}, &schema.StreamingMeta{Index: 2}),
		} {
			if !send(block) {
				return
			}
		}
		meta := &schema.AgenticMessage{Role: schema.AgenticRoleTypeAssistant, ResponseMeta: &schema.AgenticResponseMeta{TokenUsage: &schema.TokenUsage{PromptTokens: 8, CompletionTokens: 4, TotalTokens: 12}, Extension: &agenticopenai.ChatResponseMetaExtension{ID: "gc-response", FinishReason: "tool_calls"}}, Extra: map[string]any{"openai-request-id": "gc-request-header"}}
		b, e := (&schema.HumanReadableSerializer{}).Marshal(meta)
		if e != nil {
			t.Error(e)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"type": "chunk", "responseJson": string(b)})
		_ = json.NewEncoder(w).Encode(map[string]string{"type": "done"})
	}))
	ctx, cancel := context.WithTimeout(WithRun(context.Background(), run), 5*time.Second)
	defer func() { cancel(); fixture.Close() }()
	gateway := &GatewayModel{Store: s, Lease: l, AgentID: l.AgentID, StreamURL: fixture.URL}
	reader, e := gateway.Stream(ctx, inputMessages(run.Inputs))
	if e != nil {
		t.Fatal(e)
	}
	defer reader.Close()
	first, e := reader.Recv()
	if e != nil || first.ContentBlocks[0].Reasoning.Text != "thinking " {
		t.Fatal("initial live chunk missing", e)
	}
	for i := 0; i < 5; i++ {
		runtime.GC()
		runtime.Gosched()
		time.Sleep(10 * time.Millisecond)
	}
	close(proceed)
	chunks := []*schema.AgenticMessage{first}
	for {
		chunk, err := reader.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		chunks = append(chunks, chunk)
	}
	result, e := schema.ConcatAgenticMessages(chunks)
	if e != nil {
		t.Fatal(e)
	}
	if len(result.ContentBlocks) != 3 || result.ContentBlocks[0].Reasoning.Text != "thinking continued" || result.ContentBlocks[1].AssistantGenText.Text != "private answer" || result.ContentBlocks[2].FunctionToolCall.CallID != "gc-stable-call" || result.ContentBlocks[2].FunctionToolCall.Arguments != `{"body":"explicit"}` {
		t.Fatal("GC truncated an active stream or lost structured suffixes")
	}
	if count(t, s, `SELECT count(*) FROM runtime_model_calls`) != 1 {
		t.Fatal("active stream did not persist its complete receipt")
	}
	var saved []byte
	if e = s.DB.QueryRow(ctx, `SELECT response FROM runtime_model_calls WHERE run_id=$1`, run.ID).Scan(&saved); e != nil {
		t.Fatal(e)
	}
	cached, e := decodeAgenticMessage(saved)
	if e != nil {
		t.Fatal(e)
	}
	if len(cached.ContentBlocks) != 3 || cached.ContentBlocks[2].FunctionToolCall.CallID != "gc-stable-call" || cached.ResponseMeta.TokenUsage.TotalTokens != 12 {
		t.Fatal("receipt contains only a prefix after active GC")
	}
	if result.Extra["openai-request-id"] != "gc-request-header" || cached.Extra["openai-request-id"] != "gc-request-header" {
		t.Fatal("stream or persisted receipt concatenated constant request headers")
	}
	run.Step.Store(0)
	replayed, e := gateway.Stream(ctx, inputMessages(run.Inputs))
	if e != nil {
		t.Fatal(e)
	}
	defer replayed.Close()
	message, e := replayed.Recv()
	if e != nil || message.Extra["openai-request-id"] != "gc-request-header" || message.ContentBlocks[2].FunctionToolCall.Arguments != `{"body":"explicit"}` {
		t.Fatal("complete cached stream lost header identity or tool arguments", e)
	}
	if _, e = replayed.Recv(); e != io.EOF || count(t, s, `SELECT count(*) FROM runtime_model_calls`) != 1 {
		t.Fatal("replay started another model receipt", e)
	}
}

func TestNativeAbandonedStreamGCCancelsSilentHTTPWithoutReceipt(t *testing.T) {
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
	ctx, cancel := context.WithCancel(WithRun(context.Background(), run))
	defer func() { cancel(); fixture.Close() }()
	gateway := &GatewayModel{Store: s, Lease: l, AgentID: l.AgentID, StreamURL: fixture.URL}
	func() {
		reader, e := gateway.Stream(ctx, inputMessages(run.Inputs))
		if e != nil {
			t.Fatal(e)
		}
		runtime.KeepAlive(reader) // Deliberately abandon the final reader without Close.
	}()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		runtime.GC()
		runtime.Gosched()
		select {
		case <-canceled:
			if count(t, s, `SELECT count(*) FROM runtime_model_calls`) != 0 {
				t.Fatal("abandoned stream became a complete receipt")
			}
			return
		case <-time.After(10 * time.Millisecond):
		}
	}
	t.Fatal("GC of the final reader left the silent request running")
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
