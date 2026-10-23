package eino

import (
	"encoding/json"

	"github.com/cloudwego/eino-ext/components/model/agenticopenai"
	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

// A completed provider response may still contain a cut-off function argument
// or exhaust its output capacity without producing an actionable result. The
// native retry verdict runs before tool dispatch. Its rejected response remains
// an immutable model receipt, so recovery makes the same decision on replay.
func (p *ContextPipeline) retryIncompleteOutput(retry *adk.TypedRetryContext[*schema.AgenticMessage]) *adk.TypedRetryDecision[*schema.AgenticMessage] {
	if p.cfg.Budget.Version < 2 || retry.Err != nil || !incompleteModelOutput(retry.OutputMessage) {
		return nil
	}
	opts := model.GetCommonOptions(nil, retry.Options...)
	current := p.cfg.Budget.initialOutputTokens()
	if opts.MaxTokens != nil {
		current = *opts.MaxTokens
	}
	if retry.RetryAttempt <= 2 && current < p.cfg.Budget.OutputReserveTokens {
		return &adk.TypedRetryDecision[*schema.AgenticMessage]{Retry: true, AdditionalOptions: []model.Option{model.WithMaxTokens(p.cfg.Budget.OutputReserveTokens)}, RejectReason: "output_incomplete"}
	}
	return &adk.TypedRetryDecision[*schema.AgenticMessage]{RewriteError: &ModelError{Code: "upstream_response_limit"}}
}

func incompleteModelOutput(message *schema.AgenticMessage) bool {
	if message == nil {
		return false
	}
	for _, block := range message.ContentBlocks {
		if block == nil || block.FunctionToolCall == nil {
			continue
		}
		var arguments map[string]json.RawMessage
		if json.Unmarshal([]byte(block.FunctionToolCall.Arguments), &arguments) != nil || arguments == nil {
			return true
		}
	}
	if hasToolCalls(message) || message.ResponseMeta == nil {
		return false
	}
	meta, ok := message.ResponseMeta.Extension.(*agenticopenai.ChatResponseMetaExtension)
	return ok && meta != nil && meta.FinishReason == "length"
}
