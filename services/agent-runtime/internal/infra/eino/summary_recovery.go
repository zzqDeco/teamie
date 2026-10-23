package eino

import (
	"context"

	"github.com/cloudwego/eino-ext/components/model/agenticopenai"
	"github.com/cloudwego/eino/schema"
)

// A reasoning-only response exhausted before producing summary text. Native
// failover appends another model step, retaining the original receipt and hash
// when an existing Run is replayed. Nonempty historical length summaries keep
// their original behavior so later committed model/tool steps still match.
func retryEmptyLengthSummary(_ context.Context, message *schema.AgenticMessage, err error) bool {
	if err != nil || message == nil || agenticText(message) != "" || hasToolCalls(message) || hasToolResults(message) || message.ResponseMeta == nil {
		return false
	}
	meta, ok := message.ResponseMeta.Extension.(*agenticopenai.ChatResponseMetaExtension)
	return ok && meta != nil && meta.FinishReason == "length"
}
