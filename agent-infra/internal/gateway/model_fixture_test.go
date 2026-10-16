package gateway

import (
	"encoding/json"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	"github.com/eino-contrib/jsonschema"
	"teamie/agent-protocol/modeltransport"
)

// Human-readable synthetic protocol fixtures are converted at the test seam.
// This is not an alternate production provider codec.
func fixtureNativeRequest(raw string, streaming bool) string {
	var old struct {
		Model    *string `json:"model"`
		Messages []struct {
			Role, Content string
			ToolCallID    string `json:"tool_call_id"`
		} `json:"messages"`
		MaxTokens *int `json:"max_tokens"`
		Tools     []struct {
			Function struct {
				Name, Description string
				Parameters        json.RawMessage
			} `json:"function"`
		} `json:"tools"`
		ToolChoice string `json:"tool_choice"`
	}
	if err := json.Unmarshal([]byte(raw), &old); err != nil {
		panic(err)
	}
	messages := make([]*schema.AgenticMessage, 0, len(old.Messages))
	for _, message := range old.Messages {
		messages = append(messages, schema.UserAgenticMessage(message.Content))
	}
	opts := model.Options{Model: old.Model, MaxTokens: old.MaxTokens}
	choice := schema.ToolChoiceAllowed
	if old.ToolChoice == "none" {
		choice = schema.ToolChoiceForbidden
	}
	if old.ToolChoice == "required" {
		choice = schema.ToolChoiceForced
	}
	opts.AgenticToolChoice = &schema.AgenticToolChoice{Type: choice}
	for _, tool := range old.Tools {
		params := new(jsonschema.Schema)
		if len(tool.Function.Parameters) > 0 {
			if err := json.Unmarshal(tool.Function.Parameters, params); err != nil {
				panic(err)
			}
		}
		opts.Tools = append(opts.Tools, &schema.ToolInfo{Name: tool.Function.Name, Desc: tool.Function.Description, ParamsOneOf: schema.NewParamsOneOfByJSONSchema(params)})
	}
	b, err := (&schema.HumanReadableSerializer{}).Marshal(messages)
	if err != nil {
		panic(err)
	}
	o, err := json.Marshal(opts)
	if err != nil {
		panic(err)
	}
	envelope, err := json.Marshal(modeltransport.Request{Protocol: modeltransport.Version, Messages: b, Options: o, Stream: streaming})
	if err != nil {
		panic(err)
	}
	return string(envelope)
}
