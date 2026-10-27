package gateway_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/cloudwego/eino-ext/components/model/agenticopenai"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

func streamToolDelta(index int, id, name, arguments string) map[string]any {
	function := map[string]any{"arguments": arguments}
	call := map[string]any{"index": index, "function": function}
	if id != "" {
		call["id"], call["type"], function["name"] = id, "function", name
	}
	return map[string]any{"tool_calls": []any{call}}
}

func streamIndexFixture(t *testing.T, deltas []map[string]any, terminal bool) *httptest.Server {
	t.Helper()
	return httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		for i, delta := range deltas {
			var finish any
			if !terminal && i == len(deltas)-1 {
				finish = "tool_calls"
			}
			choice := map[string]any{"index": 0, "delta": delta, "finish_reason": finish}
			if content, _ := delta["content"].(string); content != "" {
				choice["logprobs"] = map[string]any{"content": []any{map[string]any{"token": content, "logprob": -0.25}}}
			}
			body, _ := json.Marshal(map[string]any{"id": "stream-index-fixture", "object": "chat.completion.chunk", "choices": []any{choice}})
			fmt.Fprintf(w, "data: %s\n\n", body)
			w.(http.Flusher).Flush()
		}
		if terminal {
			fmt.Fprint(w, "data: {\"id\":\"stream-index-fixture\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"tool_calls\"}]}\n\ndata: {\"id\":\"stream-index-fixture\",\"object\":\"chat.completion.chunk\",\"choices\":[],\"usage\":{\"prompt_tokens\":11,\"completion_tokens\":5,\"total_tokens\":16,\"completion_tokens_details\":{\"reasoning_tokens\":3}}}\n\n")
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
}

func TestOfficialStreamPreservesInterleavedToolIdentity(t *testing.T) {
	for _, test := range []struct {
		name                      string
		reasoning, tools, mixedAt int
		textBefore, emptyText     bool
	}{
		{name: "text_before_tools", reasoning: 1, tools: 2, textBefore: true},
		{name: "empty_text_during_tools", reasoning: 1, tools: 2, mixedAt: 1, emptyText: true},
		{name: "text_inside_one_tool", reasoning: 1, tools: 2, mixedAt: 1},
		{name: "production_batch_failure_shape", reasoning: 288, tools: 224, mixedAt: 150},
		{name: "production_terminal_failure_shape", reasoning: 10, tools: 143, mixedAt: 131},
	} {
		t.Run(test.name, func(t *testing.T) {
			var deltas []map[string]any
			for i := 0; i < test.reasoning; i++ {
				deltas = append(deltas, map[string]any{"reasoning_content": "r"})
			}
			if test.textBefore {
				deltas = append(deltas, map[string]any{"content": "\n"})
			}
			for i := 0; i < test.tools; i++ {
				id, name, args := "", "", "x"
				if i == 0 {
					id, name, args = "write-0", "write_file", `{"path":"/workspace/result.txt","content":"`
				}
				if i == test.tools-1 {
					args = `"}`
				}
				delta := streamToolDelta(0, id, name, args)
				if !test.textBefore && i == test.mixedAt {
					text := "\n"
					if test.emptyText {
						text = ""
					}
					delta["content"] = text
				}
				deltas = append(deltas, delta)
			}
			fixture := streamIndexFixture(t, deltas, true)
			defer fixture.Close()
			var chunks []*schema.AgenticMessage
			typesByIndex := map[int]schema.ContentBlockType{}
			toolIndices := map[int]bool{}
			raw := nativeRequest(t, []*schema.AgenticMessage{schema.UserAgenticMessage("synthetic file request")}, model.Options{}, true)
			prompt, completion, err := (Provider{HTTP: fixture.Client()}).Stream(context.Background(), ResolvedModel{BaseURL: fixture.URL + "/v1", Model: "fixture"}, "synthetic-stream-key", raw, func(body string) error {
				var chunk schema.AgenticMessage
				if err := (&schema.HumanReadableSerializer{}).Unmarshal([]byte(body), &chunk); err != nil {
					return err
				}
				for _, block := range chunk.ContentBlocks {
					if block.StreamingMeta == nil {
						t.Fatal("live chunk lost its streaming identity")
					}
					index := block.StreamingMeta.Index
					if prior, ok := typesByIndex[index]; ok && prior != block.Type {
						return fmt.Errorf("one stream index groups different content types")
					}
					typesByIndex[index] = block.Type
					if block.Type == schema.ContentBlockTypeFunctionToolCall {
						toolIndices[index] = true
					}
				}
				chunks = append(chunks, &chunk)
				return nil
			})
			if err != nil {
				t.Fatal("interleaved provider stream did not survive gateway serialization", err)
			}
			message, err := schema.ConcatAgenticMessages(chunks)
			if err != nil {
				t.Fatal(err)
			}
			var reasoning, text string
			var calls []*schema.FunctionToolCall
			for _, block := range message.ContentBlocks {
				switch block.Type {
				case schema.ContentBlockTypeReasoning:
					reasoning += block.Reasoning.Text
				case schema.ContentBlockTypeAssistantGenText:
					text += block.AssistantGenText.Text
				case schema.ContentBlockTypeFunctionToolCall:
					calls = append(calls, block.FunctionToolCall)
				}
			}
			wantText := "\n"
			if test.emptyText {
				wantText = ""
			}
			wantArgs := `{"path":"/workspace/result.txt","content":"` + strings.Repeat("x", test.tools-2) + `"}`
			if reasoning != strings.Repeat("r", test.reasoning) || text != wantText || len(calls) != 1 || len(toolIndices) != 1 || calls[0].CallID != "write-0" || calls[0].Name != "write_file" || calls[0].Arguments != wantArgs || !json.Valid([]byte(calls[0].Arguments)) {
				t.Fatal("stream normalization split a tool call or changed its content/identity")
			}
			meta, ok := message.ResponseMeta.Extension.(*agenticopenai.ChatResponseMetaExtension)
			if !ok || meta.ID != "stream-index-fixture" || meta.FinishReason != "tool_calls" || prompt != 11 || completion != 5 || message.ResponseMeta.TokenUsage.CompletionTokensDetails.ReasoningTokens != 3 {
				t.Fatal("typed metadata or token details changed")
			}
			if !test.emptyText && (meta.LogProbs == nil || len(meta.LogProbs.Content) != 1 || meta.LogProbs.Content[0].Token != wantText || meta.LogProbs.Content[0].LogProb != -0.25) {
				t.Fatal("stream log probabilities were lost or duplicated")
			}
		})
	}
}

func TestOfficialStreamConcurrentCallsAndNilTerminalMetadata(t *testing.T) {
	for _, terminal := range []bool{false, true} {
		t.Run(fmt.Sprint(terminal), func(t *testing.T) {
			deltas := []map[string]any{
				{"reasoning_content": "r1"},
				streamToolDelta(0, "call-a", "tool_a", `{"n":`),
				streamToolDelta(1, "call-b", "tool_b", `{"n":`),
			}
			middle := streamToolDelta(0, "", "", "1")
			middle["reasoning_content"] = "r2"
			deltas = append(deltas, middle, map[string]any{"content": "text-a"}, streamToolDelta(1, "", "", "2"), streamToolDelta(0, "", "", "}"), map[string]any{"content": "text-b"}, streamToolDelta(1, "", "", "}"))
			fixture := streamIndexFixture(t, deltas, terminal)
			t.Cleanup(fixture.Close)
			provider := Provider{HTTP: fixture.Client()}
			// Reuse the Provider concurrently to detect shared stream state under -race.
			for request := 0; request < 4; request++ {
				t.Run(fmt.Sprint(request), func(t *testing.T) {
					t.Parallel()
					var chunks []*schema.AgenticMessage
					raw := nativeRequest(t, []*schema.AgenticMessage{schema.UserAgenticMessage("synthetic interleaving")}, model.Options{}, true)
					_, _, err := provider.Stream(context.Background(), ResolvedModel{BaseURL: fixture.URL + "/v1", Model: "fixture"}, "synthetic-key", raw, func(body string) error {
						var chunk schema.AgenticMessage
						if err := (&schema.HumanReadableSerializer{}).Unmarshal([]byte(body), &chunk); err != nil {
							return err
						}
						chunks = append(chunks, &chunk)
						return nil
					})
					if err != nil {
						t.Fatal(err)
					}
					message, err := schema.ConcatAgenticMessages(chunks)
					if err != nil {
						t.Fatal(err)
					}
					calls := map[string]string{}
					reasoning, text := "", ""
					for _, block := range message.ContentBlocks {
						switch block.Type {
						case schema.ContentBlockTypeFunctionToolCall:
							call := block.FunctionToolCall
							if _, exists := calls[call.CallID]; exists {
								t.Fatal("one logical call became multiple calls")
							}
							calls[call.CallID] = call.Name + ":" + call.Arguments
						case schema.ContentBlockTypeReasoning:
							reasoning += block.Reasoning.Text
						case schema.ContentBlockTypeAssistantGenText:
							text += block.AssistantGenText.Text
						}
					}
					if !reflect.DeepEqual(calls, map[string]string{"call-a": `tool_a:{"n":1}`, "call-b": `tool_b:{"n":2}`}) || reasoning != "r1r2" || text != "text-atext-b" {
						t.Fatal("interleaved tool identities or content changed")
					}
					meta, ok := message.ResponseMeta.Extension.(*agenticopenai.ChatResponseMetaExtension)
					if !ok || meta.FinishReason != "tool_calls" {
						t.Fatal("finish metadata was lost on a content-ending stream")
					}
				})
			}
		})
	}
}
