package eino

import (
	"encoding/json"
	"errors"
	"strings"

	"github.com/cloudwego/eino-ext/components/model/agenticopenai"
	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"
)

func agenticText(message *schema.AgenticMessage) string {
	if message == nil {
		return ""
	}
	var text strings.Builder
	for _, block := range message.ContentBlocks {
		if block == nil {
			continue
		}
		if block.UserInputText != nil {
			text.WriteString(block.UserInputText.Text)
		}
		if block.AssistantGenText != nil {
			text.WriteString(block.AssistantGenText.Text)
		}
	}
	return text.String()
}
func hasToolCalls(message *schema.AgenticMessage) bool {
	for _, block := range message.ContentBlocks {
		if block != nil && block.FunctionToolCall != nil {
			return true
		}
	}
	return false
}
func hasToolResults(message *schema.AgenticMessage) bool {
	for _, block := range message.ContentBlocks {
		if block != nil && block.FunctionToolResult != nil {
			return true
		}
	}
	return false
}

func validateModelResult(message *schema.AgenticMessage, byteLimits ...int) error {
	limit := 65536
	if len(byteLimits) > 0 {
		limit = byteLimits[0]
	}
	if message == nil || message.Role != schema.AgenticRoleTypeAssistant {
		return modelDiagnostic("validate-result", "unexpected_message_role", nil)
	}
	seen := map[string]bool{}
	textBytes, reasoningBytes := 0, 0
	for _, block := range message.ContentBlocks {
		if block == nil {
			return &ModelError{Code: "invalid_upstream_response", Stage: "validate-result", Reason: "nil_content_block"}
		}
		switch block.Type {
		case schema.ContentBlockTypeReasoning:
			if block.Reasoning == nil || len(block.Reasoning.Text) > limit {
				return &ModelError{Code: "upstream_response_limit", Stage: "validate-result", Reason: "invalid_or_oversized_reasoning_block"}
			}
			reasoningBytes += len(block.Reasoning.Text)
		case schema.ContentBlockTypeAssistantGenText:
			if block.AssistantGenText == nil || len(block.AssistantGenText.Text) > limit {
				return &ModelError{Code: "upstream_response_limit", Stage: "validate-result", Reason: "invalid_or_oversized_text_block"}
			}
			textBytes += len(block.AssistantGenText.Text)
		case schema.ContentBlockTypeFunctionToolCall:
			c := block.FunctionToolCall
			if c == nil || c.CallID == "" || seen[c.CallID] || c.Name == "" || len(c.Arguments) > limit {
				return &ModelError{Code: "invalid_tool_call", Stage: "validate-result", Reason: "invalid_tool_identity_or_argument_size"}
			}
			seen[c.CallID] = true
		default:
			return &ModelError{Code: "invalid_upstream_response", Stage: "validate-result", Reason: "unsupported_content_block"}
		}
	}
	if len(seen) > 16 || len(message.ContentBlocks) > 128 || textBytes > limit || reasoningBytes > limit {
		return &ModelError{Code: "upstream_response_limit", Stage: "validate-result", Reason: "aggregate_content_limit_exceeded"}
	}
	return nil
}

func isLegacyMessage(raw []byte) bool {
	var fields map[string]json.RawMessage
	value, _ := serializedValue(raw)
	if json.Unmarshal(value, &fields) != nil {
		return false
	}
	if _, ok := fields["content_blocks"]; ok {
		return false
	}
	for _, field := range []string{"content", "tool_calls", "tool_call_id", "reasoning_content", "user_input_multi_content", "assistant_output_multi_content", "multi_content"} {
		if _, ok := fields[field]; ok {
			return true
		}
	}
	return string(fields["role"]) == `"tool"` || strings.Contains(string(fields["response_meta"]), `"usage"`)
}

func classicToAgentic(message *schema.Message) (*schema.AgenticMessage, error) {
	if message == nil {
		return nil, nil
	}
	out := &schema.AgenticMessage{Extra: message.Extra}
	if message.Name != "" {
		out.Extra = cloneExtra(message.Extra)
		out.Extra["teamie_legacy_name"] = message.Name
	}
	switch message.Role {
	case schema.System:
		out.Role = schema.AgenticRoleTypeSystem
	case schema.User, schema.Tool:
		out.Role = schema.AgenticRoleTypeUser
	case schema.Assistant:
		out.Role = schema.AgenticRoleTypeAssistant
	default:
		return nil, errors.New("unsupported legacy message role")
	}
	if message.ReasoningContent != "" {
		out.ContentBlocks = append(out.ContentBlocks, schema.NewContentBlock(&schema.Reasoning{Text: message.ReasoningContent}))
	}
	if message.Role == schema.Tool {
		out.ContentBlocks = append(out.ContentBlocks, schema.NewContentBlock(&schema.FunctionToolResult{CallID: message.ToolCallID, Name: message.ToolName, Content: []*schema.FunctionToolResultContentBlock{{Type: schema.FunctionToolResultContentBlockTypeText, Text: &schema.UserInputText{Text: message.Content}}}}))
	} else if message.Content != "" {
		if message.Role == schema.Assistant {
			out.ContentBlocks = append(out.ContentBlocks, schema.NewContentBlock(&schema.AssistantGenText{Text: message.Content}))
		} else {
			out.ContentBlocks = append(out.ContentBlocks, schema.NewContentBlock(&schema.UserInputText{Text: message.Content}))
		}
	}
	for _, call := range message.ToolCalls {
		block := schema.NewContentBlock(&schema.FunctionToolCall{CallID: call.ID, Name: call.Function.Name, Arguments: call.Function.Arguments})
		block.Extra = call.Extra
		if call.Index != nil {
			block.Extra = cloneExtra(call.Extra)
			block.Extra["teamie_legacy_call_index"] = *call.Index
		}
		if call.Type != "function" {
			block.Extra = cloneExtra(block.Extra)
			block.Extra["teamie_legacy_call_type"] = call.Type
		}
		out.ContentBlocks = append(out.ContentBlocks, block)
	}
	if message.ResponseMeta != nil {
		out.ResponseMeta = &schema.AgenticResponseMeta{TokenUsage: message.ResponseMeta.Usage, Extension: &agenticopenai.ChatResponseMetaExtension{FinishReason: message.ResponseMeta.FinishReason, LogProbs: message.ResponseMeta.LogProbs}}
	}
	// These were not produced by v3's text-only provider boundary. Refuse an
	// unknown historical shape instead of silently erasing durable content.
	if len(message.UserInputMultiContent) > 0 || len(message.AssistantGenMultiContent) > 0 || len(message.MultiContent) > 0 {
		return nil, errors.New("unsupported legacy multimodal message")
	}
	return out, nil
}

func decodeAgenticMessage(raw []byte) (*schema.AgenticMessage, error) {
	ser := &schema.HumanReadableSerializer{}
	if isLegacyMessage(raw) {
		var old schema.Message
		if err := ser.Unmarshal(raw, &old); err != nil {
			return nil, err
		}
		return classicToAgentic(&old)
	}
	var result schema.AgenticMessage
	if err := ser.Unmarshal(raw, &result); err != nil {
		return nil, modelDiagnostic("message-decode", "native_deserialize_failed", err)
	}
	if result.Role != schema.AgenticRoleTypeAssistant {
		return nil, modelDiagnostic("message-decode", "unexpected_message_role", nil)
	}
	return &result, nil
}

// Only known message positions are converted. Event identities, timestamps,
// rollback targets, extras and source envelopes remain byte-for-byte data.
func decodeSessionPayload[M adk.MessageType](raw []byte, event *adk.SessionEvent[M]) error {
	native, ok := any(event).(*adk.SessionEvent[*schema.AgenticMessage])
	if !ok {
		return (&schema.HumanReadableSerializer{}).Unmarshal(raw, event)
	}
	value, wrapper := serializedValue(raw)
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(value, &fields); err != nil {
		return err
	}
	legacy := false
	if wrapper != nil {
		var name string
		_ = json.Unmarshal(wrapper["$type"], &name)
		legacy = strings.Contains(name, "_eino_adk_session_event") && !strings.Contains(name, "_eino_adk_agentic_session_event")
	} else {
		legacy = isLegacyMessage(fields["message"])
		for _, key := range []string{"message_updated", "message_inserted", "message_stream_incomplete"} {
			var nested map[string]json.RawMessage
			_ = json.Unmarshal(fields[key], &nested)
			legacy = legacy || isLegacyMessage(nested["message"])
		}
		var replaced []json.RawMessage
		_ = json.Unmarshal(fields["messages_replaced"], &replaced)
		for _, message := range replaced {
			legacy = legacy || isLegacyMessage(message)
		}
	}
	if !legacy {
		return (&schema.HumanReadableSerializer{}).Unmarshal(raw, event)
	}
	var old adk.SessionEvent[*schema.Message]
	if err := (&schema.HumanReadableSerializer{}).Unmarshal(raw, &old); err != nil {
		return err
	}
	*native = adk.SessionEvent[*schema.AgenticMessage]{EventID: old.EventID, TurnID: old.TurnID, Timestamp: old.Timestamp, Kind: old.Kind, Extra: old.Extra, MessagesDeleted: old.MessagesDeleted, ModelContext: old.ModelContext, Rollback: old.Rollback, Lifecycle: old.Lifecycle, Error: old.Error, Span: old.Span, Cancel: old.Cancel, Interrupt: old.Interrupt, Extension: old.Extension}
	var err error
	if native.Message, err = classicToAgentic(old.Message); err != nil {
		return err
	}
	if old.MessagesReplaced != nil {
		messages := make([]*schema.AgenticMessage, 0, len(*old.MessagesReplaced))
		for _, message := range *old.MessagesReplaced {
			m, e := classicToAgentic(message)
			if e != nil {
				return e
			}
			messages = append(messages, m)
		}
		native.MessagesReplaced = &messages
	}
	if old.MessageUpdated != nil {
		m, e := classicToAgentic(old.MessageUpdated.Message)
		if e != nil {
			return e
		}
		native.MessageUpdated = &adk.MessageUpdatedEvent[*schema.AgenticMessage]{MessageID: old.MessageUpdated.MessageID, Message: m}
	}
	if old.MessageInserted != nil {
		m, e := classicToAgentic(old.MessageInserted.Message)
		if e != nil {
			return e
		}
		native.MessageInserted = &adk.MessageInsertedEvent[*schema.AgenticMessage]{BeforeMessageID: old.MessageInserted.BeforeMessageID, Message: m}
	}
	if old.MessageStreamIncomplete != nil {
		m, e := classicToAgentic(old.MessageStreamIncomplete.Message)
		if e != nil {
			return e
		}
		native.MessageStreamIncomplete = &adk.MessageStreamIncompleteEvent[*schema.AgenticMessage]{Message: m, Error: old.MessageStreamIncomplete.Error}
	}
	return nil
}

// Eino's serializer also wraps registered concrete root values and slices.
func serializedValue(raw []byte) (json.RawMessage, map[string]json.RawMessage) {
	var wrapper map[string]json.RawMessage
	if json.Unmarshal(raw, &wrapper) == nil && len(wrapper["$type"]) > 0 && len(wrapper["value"]) > 0 {
		return wrapper["value"], wrapper
	}
	return raw, nil
}

// This projection exists only to check a v3 receipt's original request hash.
// Active provider calls and Session persistence always retain native blocks.
func legacyMessages(input []*schema.AgenticMessage) ([]*schema.Message, error) {
	out := make([]*schema.Message, 0, len(input))
	for _, message := range input {
		if message == nil {
			continue
		}
		m := &schema.Message{Role: schema.RoleType(message.Role), Extra: message.Extra}
		m.Name, _ = message.Extra["teamie_legacy_name"].(string)
		for _, block := range message.ContentBlocks {
			if block == nil {
				return nil, errors.New("nil content block")
			}
			switch block.Type {
			case schema.ContentBlockTypeReasoning:
				m.ReasoningContent += block.Reasoning.Text
			case schema.ContentBlockTypeUserInputText:
				m.Content += block.UserInputText.Text
			case schema.ContentBlockTypeAssistantGenText:
				m.Content += block.AssistantGenText.Text
			case schema.ContentBlockTypeFunctionToolCall:
				c := block.FunctionToolCall
				call := schema.ToolCall{ID: c.CallID, Type: "function", Function: schema.FunctionCall{Name: c.Name, Arguments: c.Arguments}, Extra: block.Extra}
				if index, ok := block.Extra["teamie_legacy_call_index"]; ok {
					b, _ := json.Marshal(index)
					var n int
					if json.Unmarshal(b, &n) != nil {
						return nil, errors.New("invalid legacy tool index")
					}
					call.Index = &n
					call.Extra = cloneExtra(block.Extra)
					delete(call.Extra, "teamie_legacy_call_index")
					if len(call.Extra) == 0 {
						call.Extra = nil
					}
				}
				if oldType, ok := block.Extra["teamie_legacy_call_type"].(string); ok {
					call.Type = oldType
					call.Extra = cloneExtra(call.Extra)
					delete(call.Extra, "teamie_legacy_call_type")
					if len(call.Extra) == 0 {
						call.Extra = nil
					}
				}
				m.ToolCalls = append(m.ToolCalls, call)
			case schema.ContentBlockTypeFunctionToolResult:
				if m.Content != "" || len(m.ToolCalls) > 0 {
					return nil, errors.New("mixed legacy tool result")
				}
				r := block.FunctionToolResult
				m.Role = schema.Tool
				m.ToolCallID = r.CallID
				m.ToolName = r.Name
				for _, part := range r.Content {
					if part == nil || part.Type != schema.FunctionToolResultContentBlockTypeText || part.Text == nil {
						return nil, errors.New("non-text legacy tool result")
					}
					m.Content += part.Text.Text
				}
			default:
				return nil, errors.New("non-legacy content block")
			}
		}
		out = append(out, m)
	}
	return out, nil
}

func cloneExtra(extra map[string]any) map[string]any {
	out := make(map[string]any, len(extra)+1)
	for key, value := range extra {
		out[key] = value
	}
	return out
}
