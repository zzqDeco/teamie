package gateway_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cloudwego/eino-ext/components/model/agenticopenai"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	"teamie/contracts/gen/agent/modeltransport"
)

func nativeRequest(t *testing.T, messages []*schema.AgenticMessage, opts model.Options, stream bool) string {
	t.Helper()
	b, e := (&schema.HumanReadableSerializer{}).Marshal(messages)
	if e != nil {
		t.Fatal(e)
	}
	o, e := json.Marshal(opts)
	if e != nil {
		t.Fatal(e)
	}
	raw, e := json.Marshal(modeltransport.Request{Protocol: modeltransport.Version, Messages: b, Options: o, Stream: stream})
	if e != nil {
		t.Fatal(e)
	}
	return string(raw)
}

func TestOfficialAgenticUnaryOptionsBlocksAndMetadata(t *testing.T) {
	key := "synthetic-official-model-key"
	fixture := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/custom/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer "+key {
			t.Error("official adapter lost configured endpoint/credential")
		}
		raw, _ := io.ReadAll(r.Body)
		if strings.Contains(string(raw), "private-audit") || strings.Contains(string(raw), "_eino_msg_id") {
			t.Error("private native metadata entered provider JSON")
		}
		var request struct {
			Model       string   `json:"model"`
			Max         int      `json:"max_completion_tokens"`
			Temperature float32  `json:"temperature"`
			TopP        float32  `json:"top_p"`
			Stop        []string `json:"stop"`
			Parallel    bool     `json:"parallel_tool_calls"`
			N           int      `json:"n"`
			ToolChoice  struct {
				Type     string `json:"type"`
				Function struct {
					Name string `json:"name"`
				} `json:"function"`
			} `json:"tool_choice"`
			Messages []struct {
				Role       string
				Content    json.RawMessage
				ToolCallID string `json:"tool_call_id"`
				Reasoning  string `json:"reasoning_content"`
			} `json:"messages"`
			Tools []struct {
				Function struct {
					Name   string          `json:"name"`
					Params json.RawMessage `json:"parameters"`
				} `json:"function"`
			} `json:"tools"`
		}
		if e := json.Unmarshal(raw, &request); e != nil {
			t.Fatal(e)
		}
		if request.Model != "admin-chosen" || request.Max != 33 || request.Temperature != 0.25 || request.TopP != 0.8 || len(request.Stop) != 1 || request.Parallel || request.N != 1 || request.ToolChoice.Function.Name != "im_send_message" || len(request.Tools) != 1 {
			t.Errorf("official request policy/options lost: %s", raw)
		}
		if len(request.Messages) != 4 || request.Messages[2].Reasoning != "prior reasoning" || request.Messages[3].Role != "tool" || request.Messages[3].ToolCallID != "prior-call" || string(request.Messages[3].Content) != `"prior receipt"` {
			t.Error("native message/tool-result history lost")
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"official-fixture","object":"chat.completion","model":"admin-chosen","choices":[{"index":0,"message":{"role":"assistant","reasoning_content":"private thought","content":"private answer","tool_calls":[{"id":"call-stable","type":"function","function":{"name":"im_send_message","arguments":"{\"body\":\"你好\"}"}}]},"finish_reason":"tool_calls","logprobs":{"content":[{"token":"private answer","logprob":-0.25}]}}],"usage":{"prompt_tokens":12,"completion_tokens":7,"total_tokens":19,"prompt_tokens_details":{"cached_tokens":4},"completion_tokens_details":{"reasoning_tokens":3}}}`)
	}))
	defer fixture.Close()
	prior := &schema.AgenticMessage{Role: schema.AgenticRoleTypeAssistant, ContentBlocks: []*schema.ContentBlock{schema.NewContentBlock(&schema.Reasoning{Text: "prior reasoning"}), schema.NewContentBlock(&schema.FunctionToolCall{CallID: "prior-call", Name: "im_send_message", Arguments: "{}"})}}
	result := &schema.AgenticMessage{Role: schema.AgenticRoleTypeUser, ContentBlocks: []*schema.ContentBlock{schema.NewContentBlock(&schema.FunctionToolResult{CallID: "prior-call", Name: "im_send_message", Content: []*schema.FunctionToolResultContentBlock{{Type: schema.FunctionToolResultContentBlockTypeText, Text: &schema.UserInputText{Text: "prior receipt"}}}})}}
	messages := []*schema.AgenticMessage{schema.SystemAgenticMessage("policy"), schema.UserAgenticMessage("source body"), prior, result}
	messages[1].Extra = map[string]any{"_eino_msg_id": "private-audit", "audit": "private-audit"}
	max, temp, topP, bypass := 33, float32(0.25), float32(0.8), "runtime-bypass"
	opts := model.Options{Model: &bypass, MaxTokens: &max, Temperature: &temp, TopP: &topP, Stop: []string{"stop-here"}, Tools: []*schema.ToolInfo{{Name: "im_send_message", Desc: "Send explicitly", ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{"body": {Type: schema.String, Required: true}})}}, AgenticToolChoice: &schema.AgenticToolChoice{Type: schema.ToolChoiceForced, Forced: &schema.AgenticForcedToolChoice{Tools: []*schema.AllowedTool{{FunctionName: "im_send_message"}}}}}
	raw, prompt, completion, e := (Provider{HTTP: fixture.Client()}).Chat(context.Background(), Config{BaseURL: fixture.URL + "/custom/v1", Model: "admin-chosen"}, key, nativeRequest(t, messages, opts, false))
	if e != nil {
		t.Fatal(e)
	}
	var output schema.AgenticMessage
	if e = (&schema.HumanReadableSerializer{}).Unmarshal([]byte(raw), &output); e != nil {
		t.Fatal(e)
	}
	if len(output.ContentBlocks) != 3 || output.ContentBlocks[0].Reasoning.Text != "private thought" || output.ContentBlocks[1].AssistantGenText.Text != "private answer" || output.ContentBlocks[2].FunctionToolCall.CallID != "call-stable" || output.ContentBlocks[2].FunctionToolCall.Arguments != `{"body":"你好"}` {
		t.Fatal("official response blocks were flattened")
	}
	meta, ok := output.ResponseMeta.Extension.(*agenticopenai.ChatResponseMetaExtension)
	usage := output.ResponseMeta.TokenUsage
	if !ok || meta.ID != "official-fixture" || meta.FinishReason != "tool_calls" || meta.LogProbs == nil || len(meta.LogProbs.Content) != 1 || meta.LogProbs.Content[0].Token != "private answer" || meta.LogProbs.Content[0].LogProb != -0.25 || prompt != 12 || completion != 7 || usage.TotalTokens != 19 || usage.PromptTokenDetails.CachedTokens != 4 || usage.CompletionTokensDetails.ReasoningTokens != 3 {
		t.Fatalf("official usage/metadata lost: %+v %+v", meta, usage)
	}
}

func TestOfficialAgenticStreamKeepsOrderedBlocksToolIdentityAndMeta(t *testing.T) {
	fixture := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		for _, delta := range []string{`{"reasoning_content":"private "}`, `{"reasoning_content":"reasoning"}`, `{"content":"answer"}`, `{"tool_calls":[{"index":0,"id":"stable-call","type":"function","function":{"name":"im_send_message","arguments":"{\"body\":"}}]}`, `{"tool_calls":[{"index":0,"function":{"arguments":"\"ok\"}"}}]}`} {
			fmt.Fprintf(w, "data: {\"id\":\"official-stream\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":%s,\"finish_reason\":null}]}\n\n", delta)
			w.(http.Flusher).Flush()
		}
		fmt.Fprint(w, "data: {\"id\":\"official-stream\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"tool_calls\"}]}\n\ndata: {\"id\":\"official-stream\",\"object\":\"chat.completion.chunk\",\"choices\":[],\"usage\":{\"prompt_tokens\":11,\"completion_tokens\":5,\"total_tokens\":16}}\n\ndata: [DONE]\n\n")
	}))
	defer fixture.Close()
	var chunks []*schema.AgenticMessage
	raw := nativeRequest(t, []*schema.AgenticMessage{schema.UserAgenticMessage("hello")}, model.Options{}, true)
	prompt, completion, e := (Provider{HTTP: fixture.Client()}).Stream(context.Background(), Config{BaseURL: fixture.URL + "/v1", Model: "admin"}, "synthetic-stream-key", raw, func(b string) error {
		var m schema.AgenticMessage
		if err := (&schema.HumanReadableSerializer{}).Unmarshal([]byte(b), &m); err != nil {
			return err
		}
		chunks = append(chunks, &m)
		return nil
	})
	if e != nil {
		t.Fatal(e)
	}
	message, e := schema.ConcatAgenticMessages(chunks)
	if e != nil {
		t.Fatal(e)
	}
	if len(message.ContentBlocks) != 3 || message.ContentBlocks[0].Reasoning.Text != "private reasoning" || message.ContentBlocks[1].AssistantGenText.Text != "answer" || message.ContentBlocks[2].FunctionToolCall.CallID != "stable-call" || message.ContentBlocks[2].FunctionToolCall.Arguments != `{"body":"ok"}` {
		t.Fatal("native stream block ordering/identity lost")
	}
	meta, ok := message.ResponseMeta.Extension.(*agenticopenai.ChatResponseMetaExtension)
	if !ok || meta.FinishReason != "tool_calls" || meta.ID != "official-stream" || prompt != 11 || completion != 5 || message.ResponseMeta.TokenUsage.TotalTokens != 16 {
		t.Fatal("native stream metadata/usage lost")
	}
}

func TestModelProtocolMismatchAndToolPolicyFailBeforeProvider(t *testing.T) {
	provider := Provider{HTTP: NewHTTPClient()}
	config := Config{BaseURL: "https://provider.example/v1", Model: "chosen"}
	for _, raw := range []string{`{"messages":[]}`, `{"protocol":"other","messages":[],"options":{},"stream":false}`, nativeRequest(t, []*schema.AgenticMessage{schema.UserAgenticMessage("hi")}, model.Options{AgenticToolChoice: &schema.AgenticToolChoice{Type: schema.ToolChoiceForced, Forced: &schema.AgenticForcedToolChoice{Tools: []*schema.AllowedTool{{ServerTool: &schema.AllowedServerTool{}}}}}}, false)} {
		if _, _, _, e := provider.Chat(context.Background(), config, "synthetic-key", raw); e == nil {
			t.Fatal("incompatible model protocol/tool policy reached provider")
		}
	}
}

func TestOfficialToolChoicesAndManifestsRemainRequestScoped(t *testing.T) {
	var requests []map[string]json.RawMessage
	observed := make(chan map[string]json.RawMessage, 3)
	fixture := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request map[string]json.RawMessage
		if e := json.NewDecoder(r.Body).Decode(&request); e != nil {
			t.Error(e)
		}
		observed <- request
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"tool-choice-fixture","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"fixture only"},"finish_reason":"stop"}]}`)
	}))
	defer fixture.Close()
	provider := Provider{HTTP: fixture.Client()}
	for _, test := range []struct {
		name   string
		choice schema.ToolChoice
		wire   string
	}{{"first", schema.ToolChoiceForbidden, "none"}, {"second", schema.ToolChoiceForced, "required"}, {"third", schema.ToolChoiceAllowed, "auto"}} {
		tools := []*schema.ToolInfo{{Name: test.name}}
		if test.choice == schema.ToolChoiceForced {
			tools = append(tools, &schema.ToolInfo{Name: test.name + "_alternative"})
		}
		raw := nativeRequest(t, []*schema.AgenticMessage{schema.UserAgenticMessage("same source")}, model.Options{Tools: tools, AgenticToolChoice: &schema.AgenticToolChoice{Type: test.choice}}, false)
		if _, _, _, e := provider.Chat(context.Background(), Config{BaseURL: fixture.URL + "/v1", Model: "admin"}, "synthetic-key", raw); e != nil {
			t.Fatal(e)
		}
		last := <-observed
		requests = append(requests, last)
		if string(last["tool_choice"]) != `"`+test.wire+`"` || !strings.Contains(string(last["tools"]), `"name":"`+test.name+`"`) {
			t.Fatal("official adapter lost request-specific tool choice/manifest")
		}
	}
	if strings.Contains(string(requests[2]["tools"]), "first") || strings.Contains(string(requests[2]["tools"]), "second") {
		t.Fatal("tool manifest leaked across requests")
	}
}
