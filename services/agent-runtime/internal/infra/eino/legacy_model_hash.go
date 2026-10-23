package eino

// Read compatibility only. These wire structs and the byte-exact v3 request
// builder verify existing receipts; new calls never use this provider codec.
import (
	"encoding/json"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

type chatMessage struct {
	Role       string            `json:"role"`
	Content    string            `json:"content"`
	Name       string            `json:"name,omitempty"`
	ToolCallID string            `json:"tool_call_id,omitempty"`
	ToolCalls  []schema.ToolCall `json:"tool_calls,omitempty"`
}
type chatTool struct {
	Type     string       `json:"type"`
	Function toolFunction `json:"function"`
}
type toolFunction struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

func legacyChatRequest(messages []*schema.Message, tools []*schema.ToolInfo, opts *model.Options) ([]byte, error) {
	input := make([]chatMessage, 0, len(messages))
	for _, msg := range messages {
		if msg != nil {
			input = append(input, chatMessage{Role: string(msg.Role), Content: msg.Content, Name: msg.Name, ToolCallID: msg.ToolCallID, ToolCalls: msg.ToolCalls})
		}
	}
	toolDefs := []chatTool{}
	for _, info := range tools {
		params := json.RawMessage(`{"type":"object","properties":{}}`)
		if info.ParamsOneOf != nil {
			js, e := info.ToJSONSchema()
			if e != nil {
				return nil, e
			}
			b, e := json.Marshal(js)
			if e != nil {
				return nil, e
			}
			params = b
		}
		toolDefs = append(toolDefs, chatTool{Type: "function", Function: toolFunction{Name: info.Name, Description: info.Desc, Parameters: params}})
	}
	maxTokens := 2048
	if opts.MaxTokens != nil && *opts.MaxTokens > 0 && *opts.MaxTokens <= 4096 {
		maxTokens = *opts.MaxTokens
	}
	req := map[string]any{"messages": input, "max_tokens": maxTokens, "stream": false}
	if len(toolDefs) > 0 {
		req["tools"] = toolDefs
		req["tool_choice"] = "auto"
		req["parallel_tool_calls"] = false
	}
	if opts.ToolChoice != nil {
		switch *opts.ToolChoice {
		case schema.ToolChoiceForbidden:
			req["tool_choice"] = "none"
		case schema.ToolChoiceForced:
			req["tool_choice"] = "required"
		}
	}
	return json.Marshal(req)
}
