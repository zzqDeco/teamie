package eino

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"
	"teamie/services/agent-runtime/internal/domain"
)

func (w *agentWorker) genResume(ctx context.Context, _ *adk.TurnLoop[Input, *schema.AgenticMessage], interrupted, unhandled, newItems []Input) (*adk.GenResumeResult[Input, *schema.AgenticMessage], error) {
	metadata, _ := w.engine.Store.ResumeMetadata(ctx, "turnloop:"+w.def.Id)
	all := append(append(append([]Input{}, interrupted...), unhandled...), newItems...)
	run, runctx, remaining, err := w.prepare(ctx, all, metadata)
	if err != nil {
		return nil, err
	}
	if accepted := run.resume; accepted != nil {
		targets := map[string]any{}
		for _, id := range accepted.Targets {
			targets[id] = nil
		}
		return &adk.GenResumeResult[Input, *schema.AgenticMessage]{RunCtx: runctx, Decision: adk.TurnLoopResumeDecisionResume, ResumeParams: &adk.ResumeParams{Targets: targets}, Consumed: run.Inputs, Remaining: remaining}, nil
	}
	return &adk.GenResumeResult[Input, *schema.AgenticMessage]{RunCtx: runctx, Decision: adk.TurnLoopResumeDecisionStartNewTurn, Input: &adk.TypedAgentInput[*schema.AgenticMessage]{Messages: inputMessages(run.Inputs), EnableStreaming: w.engine.Config.GatewayStreamURL != ""}, Consumed: run.Inputs, Remaining: remaining}, nil
}

func finalizeSummary(ctx context.Context, original []*schema.AgenticMessage, summary *schema.AgenticMessage) ([]*schema.AgenticMessage, error) {
	return finalizeSummaryWithSourceFormat(ctx, original, summary, false)
}

// Only runs pinned to the v0.3.4 tool profile use the pre-attachment source
// representation. Native-only runs must keep their v0.3.5 request bytes.
func finalizeV034Summary(ctx context.Context, original []*schema.AgenticMessage, summary *schema.AgenticMessage) ([]*schema.AgenticMessage, error) {
	return finalizeSummaryWithSourceFormat(ctx, original, summary, true)
}

func finalizeSummaryWithSourceFormat(_ context.Context, original []*schema.AgenticMessage, summary *schema.AgenticMessage, omitEmptyAttachmentIDs bool) ([]*schema.AgenticMessage, error) {
	if summary == nil || agenticText(summary) == "" || len(agenticText(summary)) > 32768 || hasToolCalls(summary) {
		return nil, errors.New("invalid_context_summary")
	}
	sources := []map[string]any{}
	seen := map[string]bool{}
	appendSource := func(source map[string]any) {
		id, _ := source["eventId"].(string)
		if id == "" || seen[id] {
			return
		}
		seen[id] = true
		clean := map[string]any{}
		for _, key := range []string{"eventId", "workspaceId", "conversationId", "messageId", "replyToId", "senderId", "senderKind"} {
			if value, ok := source[key].(string); ok {
				clean[key] = value
			}
		}
		// Retain bounded attachment references through repeated compaction;
		// metadata/content can be retrieved explicitly from the source message.
		retained := []string{}
		switch ids := source["attachmentIds"].(type) {
		case []string:
			if !omitEmptyAttachmentIDs {
				// Preserve native-only v0.3.5 receipts, including their empty
				// arrays and original fresh-source representation.
				if len(ids) <= 8 {
					clean["attachmentIds"] = ids
				}
				break
			}
			for _, id := range ids {
				if validUUID(id) && len(retained) < 8 {
					retained = append(retained, id)
				}
			}
		case []any:
			for _, value := range ids {
				if id, ok := value.(string); ok && validUUID(id) && len(retained) < 8 {
					retained = append(retained, id)
				}
			}
		}
		// The v0.3.4 format and parsed previous summaries omit empty references.
		if len(retained) > 0 {
			clean["attachmentIds"] = retained
		}
		sources = append(sources, clean)
	}
	for _, message := range original {
		if message == nil {
			continue
		}
		if strings.HasPrefix(agenticText(message), "TEAMIE_SESSION_SUMMARY ") {
			if index := strings.LastIndex(agenticText(message), "\nTEAMIE_SOURCE_REFERENCES "); index >= 0 {
				var previous []map[string]any
				if json.Unmarshal([]byte(agenticText(message)[index+len("\nTEAMIE_SOURCE_REFERENCES "):]), &previous) == nil {
					for _, source := range previous {
						appendSource(source)
					}
				}
			}
		}
		if !strings.HasPrefix(agenticText(message), "TEAMIE_NEW_MESSAGE ") {
			continue
		}
		var in Input
		if json.Unmarshal([]byte(strings.TrimPrefix(agenticText(message), "TEAMIE_NEW_MESSAGE ")), &in) == nil {
			source := map[string]any{"eventId": in.EventID, "workspaceId": in.WorkspaceID, "conversationId": in.ConversationID, "messageId": in.MessageID, "replyToId": in.ReplyToID, "senderId": in.SenderID, "senderKind": in.SenderKind}
			attachmentIDs := []string{}
			for _, a := range in.Attachments {
				attachmentIDs = append(attachmentIDs, a.ID)
			}
			if !omitEmptyAttachmentIDs || len(attachmentIDs) > 0 {
				source["attachmentIds"] = attachmentIDs
			}
			appendSource(source)
		}
	}
	// Full provenance remains in durable inbox/session audit; the working
	// context keeps a bounded recent reference index through repeated compaction.
	if len(sources) > 32 {
		sources = sources[len(sources)-32:]
	}
	provenance, _ := json.Marshal(sources)
	// Native summarization replaces the complete in-flight model state. Retain
	// the actual leading platform policy; only transcript data is compacted.
	// Summary content remains a user message regardless of the model's role.
	out := make([]*schema.AgenticMessage, 0, len(original)+1)
	for _, message := range original {
		if message == nil || message.Role != schema.AgenticRoleTypeSystem {
			break
		}
		out = append(out, message)
	}
	out = append(out, schema.UserAgenticMessage("TEAMIE_SESSION_SUMMARY (source-scoped memory; verify current access using tools):\n"+agenticText(summary)+"\nTEAMIE_SOURCE_REFERENCES "+string(provenance)))
	start := len(original)
	for i := len(original) - 1; i >= 0; i-- {
		if original[i] != nil && original[i].Role == schema.AgenticRoleTypeUser && !hasToolResults(original[i]) && len(original)-i <= 12 {
			start = i
			break
		}
	}
	if start < len(original) {
		out = append(out, original[start:]...)
	}
	return out, nil
}

// The transport byte ceiling and configured model window are independent.
// Large-context providers retain a conservative platform trigger so a single
// summary request still fits the gateway's bounded transport.
func summaryTrigger(window int) int {
	return domain.SummaryTrigger(window)
}
