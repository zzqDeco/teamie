package eino

import (
	"encoding/json"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	"teamie/contracts/gen/agent/modeltransport"
)

// Existing behavior fixtures describe synthetic OpenAI completions. This
// helper translates the fixture, while the runtime under test uses only the
// versioned native protocol. Real official codecs are exercised in gateway.
func decodeFixtureRequest(raw string, out any) error {
	var envelope modeltransport.Request
	if err := json.Unmarshal([]byte(raw), &envelope); err != nil {
		return err
	}
	var messages []*schema.AgenticMessage
	var opts model.Options
	if err := (&schema.HumanReadableSerializer{}).Unmarshal(envelope.Messages, &messages); err != nil {
		return err
	}
	if err := json.Unmarshal(envelope.Options, &opts); err != nil {
		return err
	}
	if opts.AgenticToolChoice != nil {
		opts.ToolChoice = &opts.AgenticToolChoice.Type
	}
	classic, err := legacyMessages(messages)
	if err != nil {
		return err
	}
	b, err := legacyChatRequest(classic, opts.Tools, &opts)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, out)
}

func modelFixtureResponse(raw string) *string {
	var response struct {
		Choices []struct {
			Message      chatMessage `json:"message"`
			FinishReason string      `json:"finish_reason"`
		} `json:"choices"`
		Usage struct {
			Prompt     int `json:"prompt_tokens"`
			Completion int `json:"completion_tokens"`
			Total      int `json:"total_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal([]byte(raw), &response); err != nil || len(response.Choices) == 0 {
		panic("invalid model fixture response")
	}
	c := response.Choices[0]
	m, err := classicToAgentic(&schema.Message{Role: schema.Assistant, Content: c.Message.Content, ToolCalls: c.Message.ToolCalls, ResponseMeta: &schema.ResponseMeta{FinishReason: c.FinishReason, Usage: &schema.TokenUsage{PromptTokens: response.Usage.Prompt, CompletionTokens: response.Usage.Completion, TotalTokens: response.Usage.Total}}})
	if err != nil {
		panic(err)
	}
	b, err := (&schema.HumanReadableSerializer{}).Marshal(m)
	if err != nil {
		panic(err)
	}
	value := string(b)
	return &value
}

func nativeMessages(messages []*schema.Message) []*schema.AgenticMessage {
	out := make([]*schema.AgenticMessage, 0, len(messages))
	for _, m := range messages {
		native, err := classicToAgentic(m)
		if err != nil {
			panic(err)
		}
		out = append(out, native)
	}
	return out
}
