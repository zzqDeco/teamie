package gateway

import (
	"context"
	"fmt"

	"github.com/cloudwego/eino-ext/components/model/agenticopenai"
	"github.com/cloudwego/eino-ext/libs/acl/openai"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

// The pinned ChatModel wrapper replaces caller modifiers with its metadata
// modifiers. Use the official AgenticClient's public hooks instead. Both hooks
// run serially inside one SDK call, before SDK output callbacks see the message.
func agenticResponseOptions(streaming bool) []model.Option {
	if streaming {
		adapter := &agenticStreamAdapter{toolBlocks: map[int]int{}}
		return []model.Option{
			openai.WithResponseChunkMessageModifier(adapter.capture),
			openai.WithResponseChunkAgenticMessageModifier(adapter.normalize),
		}
	}
	var metadata *agenticopenai.ChatResponseMetaExtension
	return []model.Option{
		openai.WithResponseMessageModifier(func(_ context.Context, message *schema.Message, _ []byte) (*schema.Message, error) {
			metadata = chatResponseMetadata(message)
			return message, nil
		}),
		openai.WithResponseAgenticMessageModifier(func(_ context.Context, message *schema.AgenticMessage) (*schema.AgenticMessage, error) {
			return withChatResponseMetadata(message, metadata), nil
		}),
	}
}

func chatResponseMetadata(message *schema.Message) *agenticopenai.ChatResponseMetaExtension {
	if message == nil || message.ResponseMeta == nil {
		return nil
	}
	return &agenticopenai.ChatResponseMetaExtension{
		ID:           openai.GetRequestID(message),
		FinishReason: message.ResponseMeta.FinishReason,
		LogProbs:     message.ResponseMeta.LogProbs,
	}
}

func withChatResponseMetadata(message *schema.AgenticMessage, metadata *agenticopenai.ChatResponseMetaExtension) *schema.AgenticMessage {
	if message == nil {
		return nil
	}
	copy := *message
	if metadata != nil {
		copy.ResponseMeta = &schema.AgenticResponseMeta{}
		if message.ResponseMeta != nil {
			*copy.ResponseMeta = *message.ResponseMeta
		}
		copy.ResponseMeta.Extension = metadata
	}
	return &copy
}

// The official converter drops original tool indices. Capture only structural
// identities before conversion; never put temporary markers into native Extra,
// callbacks, transport messages or durable receipts.
type streamBlockSource struct {
	tool  bool
	index int
}

type streamChunkPlan struct {
	sources  []streamBlockSource
	metadata *agenticopenai.ChatResponseMetaExtension
}

type agenticStreamAdapter struct {
	pending       *streamChunkPlan
	nextIndex     int
	toolBlocks    map[int]int
	contentActive bool
	contentType   schema.ContentBlockType
	contentSource int
	contentIndex  int
}

func (a *agenticStreamAdapter) capture(_ context.Context, message *schema.Message, _ []byte, _ bool) (*schema.Message, error) {
	a.pending = nil
	if message == nil { // EOF may have no buffered metadata chunk.
		return nil, nil
	}
	plan := &streamChunkPlan{metadata: chatResponseMetadata(message)}
	multiReasoning, multiText := false, false
	for _, part := range message.AssistantGenMultiContent {
		multiReasoning = multiReasoning || part.Type == schema.ChatMessagePartTypeReasoning
		multiText = multiText || part.Type == schema.ChatMessagePartTypeText
	}
	// Match the pinned SDK's block order, without interpreting content payloads.
	if !multiReasoning && message.ReasoningContent != "" {
		plan.sources = append(plan.sources, streamBlockSource{index: -1})
	}
	for _, part := range message.AssistantGenMultiContent {
		index := 0
		if part.StreamingMeta != nil {
			index = part.StreamingMeta.Index
		}
		plan.sources = append(plan.sources, streamBlockSource{index: index})
	}
	if !multiText && message.Content != "" {
		plan.sources = append(plan.sources, streamBlockSource{index: -1})
	}
	for _, call := range message.ToolCalls {
		index := 0 // Same default as the official converter when Index is absent.
		if call.Index != nil {
			index = *call.Index
		}
		plan.sources = append(plan.sources, streamBlockSource{tool: true, index: index})
	}
	a.pending = plan
	return message, nil
}

func (a *agenticStreamAdapter) allocate() int {
	index := a.nextIndex
	a.nextIndex++
	return index
}

func (a *agenticStreamAdapter) normalize(_ context.Context, message *schema.AgenticMessage) (*schema.AgenticMessage, error) {
	plan := a.pending
	a.pending = nil
	if message == nil || plan == nil {
		return nil, fmt.Errorf("native stream conversion has no matching chunk plan")
	}
	if len(message.ContentBlocks) != len(plan.sources) {
		return nil, fmt.Errorf("native stream block count mismatch: expected %d, got %d", len(plan.sources), len(message.ContentBlocks))
	}
	copy := withChatResponseMetadata(message, plan.metadata)
	copy.ContentBlocks = make([]*schema.ContentBlock, len(message.ContentBlocks))
	for i, block := range message.ContentBlocks {
		if block == nil || block.StreamingMeta == nil {
			return nil, fmt.Errorf("native stream conversion produced a non-streaming block")
		}
		source := plan.sources[i]
		if source.tool != (block.Type == schema.ContentBlockTypeFunctionToolCall) {
			return nil, fmt.Errorf("native stream conversion changed tool block ordering")
		}
		var index int
		if source.tool {
			var ok bool
			index, ok = a.toolBlocks[source.index]
			if !ok {
				index = a.allocate()
				a.toolBlocks[source.index] = index
			}
			a.contentActive = false
		} else {
			if !a.contentActive || a.contentType != block.Type || a.contentSource != source.index {
				a.contentIndex = a.allocate()
				a.contentType, a.contentSource = block.Type, source.index
				a.contentActive = true
			}
			index = a.contentIndex
		}
		cloned := *block
		cloned.StreamingMeta = &schema.StreamingMeta{Index: index}
		copy.ContentBlocks[i] = &cloned
	}
	return copy, nil
}
