package engine

import (
	"context"
	"encoding/json"
	"errors"
	"go.uber.org/zap"
	"slices"
	"strings"
	"teamie/backend/pkg/observability"
	"time"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/adk/middlewares/reduction"
	"github.com/cloudwego/eino/adk/middlewares/summarization"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

type ContextPipelineConfig struct {
	Model             model.AgenticModel
	Budget            ContextBudgetSnapshot
	Backend           reduction.Backend
	ArchivePath       func(context.Context, string, string, string) (string, error)
	ArchiveToolPath   func(context.Context, string, *reduction.ToolDetail, string) (string, error)
	ArchiveTranscript func(context.Context, []*schema.AgenticMessage) (string, error)
}

type contextSummarizer interface {
	Summarize(context.Context, *adk.TypedChatModelAgentState[*schema.AgenticMessage]) ([]*schema.AgenticMessage, error)
}

type modelConfigIDProvider interface {
	CurrentModelConfigID(context.Context) (string, bool)
}

type ContextPipeline struct {
	*adk.TypedBaseChatModelAgentMiddleware[*schema.AgenticMessage]
	cfg           ContextPipelineConfig
	summary       adk.TypedChatModelAgentMiddleware[*schema.AgenticMessage]
	fallbackUsage usageAnchorState
}

func (p *ContextPipeline) usageState(ctx context.Context) *usageAnchorState {
	if run, err := CurrentRun(ctx); err == nil && run != nil {
		return &run.usageState
	}
	return &p.fallbackUsage
}

func (p *ContextPipeline) currentModelConfigID(ctx context.Context) (string, bool) {
	if provider, ok := p.cfg.Model.(modelConfigIDProvider); ok && provider != nil {
		return provider.CurrentModelConfigID(ctx)
	}
	return "", false
}

func (p *ContextPipeline) getAnchor(ctx context.Context) *contextUsageAnchor {
	configID, _ := p.currentModelConfigID(ctx)
	return p.usageState(ctx).getForConfig(configID)
}

func (p *ContextPipeline) clearCurrentAnchor(ctx context.Context) {
	p.usageState(ctx).clear()
}

func (p *ContextPipeline) seedAnchorFromHistory(ctx context.Context, messages []*schema.AgenticMessage, tools []*schema.ToolInfo, deferred ...[]*schema.ToolInfo) {
	if p.cfg.Budget.Version < 7 {
		return
	}
	state := p.usageState(ctx)
	currentConfigID, hasConfig := p.currentModelConfigID(ctx)
	allDeferred := flattenToolInfos(deferred)
	lastIdx := -1
	for i := len(messages) - 1; i >= 0; i-- {
		msg := messages[i]
		if msg == nil || msg.Role != schema.AgenticRoleTypeAssistant {
			continue
		}
		if msg.ResponseMeta != nil && msg.ResponseMeta.TokenUsage != nil && msg.ResponseMeta.TokenUsage.PromptTokens > 0 {
			lastIdx = i
			break
		}
		if _, ok := extractUsageAnchor(msg); ok {
			lastIdx = i
			break
		}
	}
	if lastIdx <= 0 {
		state.clear()
		return
	}
	assistantMsg := messages[lastIdx]
	if assistantMsg.ResponseMeta == nil || assistantMsg.ResponseMeta.TokenUsage == nil || assistantMsg.ResponseMeta.TokenUsage.PromptTokens <= 0 {
		clearMessageUsageAnchor(assistantMsg)
		state.clear()
		return
	}
	savedAnchor, ok := extractUsageAnchor(assistantMsg)
	if !ok || !hasConfig || currentConfigID == "" || savedAnchor.ConfigID != currentConfigID {
		state.clear()
		return
	}
	prefix := messages[:lastIdx]
	visibleBytes, prefixHash, err := visibleContextBytes(prefix, tools, allDeferred, nil, &schema.AgenticToolChoice{Type: schema.ToolChoiceAllowed})
	if err != nil || lastIdx != savedAnchor.MessageCount || prefixHash != savedAnchor.PrefixHash || estimateVisibleTokensV7(visibleBytes) != savedAnchor.LocalTokens || assistantMsg.ResponseMeta.TokenUsage.PromptTokens != savedAnchor.PromptTokens {
		clearMessageUsageAnchor(assistantMsg)
		p.invalidateAnchor(ctx, messages)
		return
	}
	if state.isInvalidated(prefixHash) {
		clearMessageUsageAnchor(assistantMsg)
		state.clear()
		return
	}
	step := savedAnchor.Step
	if step <= 0 {
		if run, err := CurrentRun(ctx); err == nil && run != nil {
			step = int(run.Step.Load())
		}
	}
	anchor := &contextUsageAnchor{
		Step:         step,
		ConfigID:     savedAnchor.ConfigID,
		MessageCount: savedAnchor.MessageCount,
		PrefixHash:   savedAnchor.PrefixHash,
		LocalTokens:  savedAnchor.LocalTokens,
		PromptTokens: savedAnchor.PromptTokens,
	}
	state.set(anchor, true)
}

func (p *ContextPipeline) invalidateAnchor(ctx context.Context, messages []*schema.AgenticMessage) {
	p.usageState(ctx).invalidate(messages)
}

func (p *ContextPipeline) measureMainContext(ctx context.Context, messages []*schema.AgenticMessage, tools []*schema.ToolInfo, deferred ...[]*schema.ToolInfo) (ContextMeasurement, error) {
	return p.cfg.Budget.measureCalibratedContext(p.getAnchor(ctx), messages, tools, p.cfg.Budget.OutputReserveTokens, false, deferred...)
}

func NewContextPipeline(ctx context.Context, cfg ContextPipelineConfig) (*ContextPipeline, error) {
	raw, err := json.Marshal(cfg.Budget)
	if err != nil {
		return nil, err
	}
	if _, err = ParseContextBudgetSnapshot(raw); err != nil {
		return nil, err
	}
	if cfg.Model == nil || cfg.Backend == nil || cfg.ArchivePath == nil || cfg.ArchiveTranscript == nil {
		return nil, errors.New("incomplete context pipeline")
	}
	p := &ContextPipeline{TypedBaseChatModelAgentMiddleware: &adk.TypedBaseChatModelAgentMiddleware[*schema.AgenticMessage]{}, cfg: cfg}
	failovers := 1
	summaryByteLimit := 32768
	if cfg.Budget.Version >= 6 {
		summaryByteLimit = cfg.Budget.SummaryOutputTokens * 16
	}
	p.summary, err = summarization.NewTyped(ctx, &summarization.TypedConfig[*schema.AgenticMessage]{
		Model: cfg.Model, ModelOptions: []model.Option{model.WithMaxTokens(cfg.Budget.SummaryOutputTokens), model.WithAgenticToolChoice(&schema.AgenticToolChoice{Type: schema.ToolChoiceForbidden})},
		UserInstruction: contextSummaryInstruction(cfg.Budget),
		GenModelInput:   p.summaryInput,
		Finalize: func(_ context.Context, _ []*schema.AgenticMessage, msg *schema.AgenticMessage) ([]*schema.AgenticMessage, error) {
			text := strings.TrimSpace(agenticText(msg))
			if msg == nil || text == "" || hasToolCalls(msg) || hasToolResults(msg) || len(text) > summaryByteLimit {
				return nil, errors.New("invalid_context_summary")
			}
			return []*schema.AgenticMessage{schema.UserAgenticMessage(text)}, nil
		},
		Failover: &summarization.TypedFailoverConfig[*schema.AgenticMessage]{MaxRetries: &failovers, ShouldFailover: retryEmptyLengthSummary},
		CustomFormatContextManagementInstruction: func(context.Context) string {
			return contextManagementInstruction(cfg.Budget)
		},
	})
	return p, err
}

func (p *ContextPipeline) BeforeAgent(ctx context.Context, runCtx *adk.ChatModelAgentContext[*schema.AgenticMessage]) (context.Context, *adk.ChatModelAgentContext[*schema.AgenticMessage], error) {
	return p.summary.BeforeAgent(ctx, runCtx)
}

func (p *ContextPipeline) BeforeModelRewriteState(ctx context.Context, state *adk.TypedChatModelAgentState[*schema.AgenticMessage], mc *adk.TypedModelContext[*schema.AgenticMessage]) (context.Context, *adk.TypedChatModelAgentState[*schema.AgenticMessage], error) {
	copyState := *state
	copyState.ToolInfos = contextToolInfos(state.ToolInfos, p.cfg.Budget)
	copyState.DeferredToolInfos = contextToolInfos(state.DeferredToolInfos, p.cfg.Budget)
	state = &copyState
	p.seedAnchorFromHistory(ctx, state.Messages, state.ToolInfos, state.DeferredToolInfos)
	names := []string{}
	for _, tool := range state.ToolInfos {
		names = append(names, tool.Name)
	}
	for _, tool := range state.DeferredToolInfos {
		names = append(names, tool.Name)
	}
	for _, msg := range state.Messages {
		if msg != nil {
			for _, block := range msg.ContentBlocks {
				if block != nil && block.FunctionToolCall != nil {
					names = append(names, block.FunctionToolCall.Name)
				}
			}
		}
	}
	r, err := p.reducer(ctx, names, state.DeferredToolInfos)
	if err != nil {
		return ctx, nil, err
	}
	ctx, reduced, err := r.BeforeModelRewriteState(ctx, state, mc)
	if err != nil {
		return ctx, nil, err
	}
	m, err := p.measureMainContext(ctx, reduced.Messages, reduced.ToolInfos, reduced.DeferredToolInfos)
	if err != nil {
		return ctx, nil, err
	}
	reason := p.cfg.Budget.compactionReason(m, false)
	observability.L(ctx).Info("Context measured", append([]zap.Field{zap.String("event.action", "context-measured"), zap.Bool("custom.context.should_compact", reason != "")}, p.cfg.Budget.measurementLogFields(m, reason)...)...)
	if !p.cfg.Budget.shouldCompact(m) {
		return ctx, reduced, nil
	}
	msgs, err := p.compact(ctx, reduced.Messages, reduced.ToolInfos, false, reduced.DeferredToolInfos)
	if err != nil {
		return ctx, nil, err
	}
	out := *reduced
	out.Messages = msgs
	if err = emitContextReplacement(ctx, msgs, "context_compacted_v1"); err != nil {
		return ctx, nil, err
	}
	return ctx, &out, nil
}

func (p *ContextPipeline) RetryConfig() *adk.TypedModelRetryConfig[*schema.AgenticMessage] {
	return &adk.TypedModelRetryConfig[*schema.AgenticMessage]{MaxRetries: 2, BackoffFunc: func(context.Context, int) time.Duration { return 0 }, ShouldRetry: func(ctx context.Context, retry *adk.TypedRetryContext[*schema.AgenticMessage]) *adk.TypedRetryDecision[*schema.AgenticMessage] {
		if decision := p.retryIncompleteOutput(retry); decision != nil {
			return decision
		}
		var failure *ModelError
		if !errors.As(retry.Err, &failure) || !isContextOverflow(failure.Code) {
			return nil
		}
		if retry.RetryAttempt > 2 || (retry.OutputMessage != nil && len(retry.OutputMessage.ContentBlocks) > 0) {
			return nil
		}
		opts := model.GetCommonOptions(nil, retry.Options...)
		msgs, err := p.compact(ctx, retry.InputMessages, opts.Tools, true, opts.DeferredTools)
		if err != nil {
			return &adk.TypedRetryDecision[*schema.AgenticMessage]{RewriteError: err}
		}
		before, err := p.cfg.Budget.measureContext(retry.InputMessages, opts.Tools, p.cfg.Budget.OutputReserveTokens, false, opts.DeferredTools)
		if err != nil {
			return &adk.TypedRetryDecision[*schema.AgenticMessage]{RewriteError: err}
		}
		after, err := p.cfg.Budget.measureContext(msgs, opts.Tools, p.cfg.Budget.OutputReserveTokens, false, opts.DeferredTools)
		if err != nil {
			return &adk.TypedRetryDecision[*schema.AgenticMessage]{RewriteError: err}
		}
		if !p.cfg.Budget.fits(after) || (after.EstimatedTokens >= before.EstimatedTokens && after.TransportBytes >= before.TransportBytes) {
			return &adk.TypedRetryDecision[*schema.AgenticMessage]{RewriteError: &ModelError{Code: "context_unrecoverable"}}
		}
		if err = emitContextReplacement(ctx, msgs, "context_overflow_recovered_v1"); err != nil {
			return &adk.TypedRetryDecision[*schema.AgenticMessage]{RewriteError: err}
		}
		return &adk.TypedRetryDecision[*schema.AgenticMessage]{Retry: true, ModifiedInputMessages: msgs, PersistModifiedInputMessages: true, RejectReason: "context_overflow"}
	}}
}

func emitContextReplacement(ctx context.Context, messages []*schema.AgenticMessage, reason string) error {
	start := 0
	for start < len(messages) && messages[start] != nil && messages[start].Role == schema.AgenticRoleTypeSystem {
		start++
	}
	msgs := messages[start:]
	return adk.TypedSendEvent(ctx, &adk.TypedAgentEvent[*schema.AgenticMessage]{SessionEventVariant: &adk.SessionEventVariant[*schema.AgenticMessage]{Event: &adk.SessionEvent[*schema.AgenticMessage]{Kind: adk.SessionEventMessagesReplaced, Extra: map[string]any{"_eino_reason": reason}, MessagesReplaced: &msgs}}})
}

// A complete assistant/tool round is indivisible, including multiple results.
// Ordinary messages are one unit; malformed/incomplete rounds stay together.
func contextUnits(messages []*schema.AgenticMessage) [][]*schema.AgenticMessage {
	out := [][]*schema.AgenticMessage{}
	for i := 0; i < len(messages); {
		end := i + 1
		msg := messages[i]
		if msg != nil && msg.Role == schema.AgenticRoleTypeAssistant && hasToolCalls(msg) {
			for end < len(messages) && messages[end] != nil && hasToolResults(messages[end]) {
				end++
			}
		}
		out = append(out, messages[i:end])
		i = end
	}
	return out
}

func isContextRequest(msg *schema.AgenticMessage) bool {
	if msg == nil || msg.Role != schema.AgenticRoleTypeUser || hasToolResults(msg) || isContextSummary(msg) {
		return false
	}
	if msg.Extra["teamie_context_data"] == contextProfileV1 {
		request, _ := msg.Extra["teamie_context_request"].(bool)
		return request
	}
	return true
}
func isContextSummary(msg *schema.AgenticMessage) bool {
	return msg != nil && msg.Role == schema.AgenticRoleTypeUser && msg.Extra["teamie_context_summary"] == contextProfileV1
}

func (p *ContextPipeline) partition(messages []*schema.AgenticMessage, force bool) (systems, prefix, tail []*schema.AgenticMessage) {
	start := 0
	for start < len(messages) && messages[start] != nil && messages[start].Role == schema.AgenticRoleTypeSystem {
		start++
	}
	systems = messages[:start]
	units := contextUnits(messages[start:])
	if len(units) == 0 {
		return
	}
	limit := p.cfg.Budget.TailTokenLimit
	if force {
		limit /= 2
		m, err := p.cfg.Budget.measureContext(messages[start:], nil, p.cfg.Budget.OutputReserveTokens, false)
		if err == nil {
			limit = min(limit, m.EstimatedTokens/2)
		}
	}
	cut := len(units)
	if p.cfg.Budget.Version >= 6 {
		cut, tail = p.longContextTail(units, limit)
	}
	for p.cfg.Budget.Version < 6 && cut > 0 {
		candidate := append(append([]*schema.AgenticMessage{}, units[cut-1]...), tail...)
		m, err := p.cfg.Budget.measureContext(candidate, nil, p.cfg.Budget.OutputReserveTokens, false)
		if len(tail) > 0 && (err != nil || m.EstimatedTokens > limit) {
			break
		}
		tail = candidate
		cut--
	}
	for _, unit := range units[:cut] {
		prefix = append(prefix, unit...)
	}
	// Preserve the latest actual input independently of the number of tool
	// messages since it. A long task can therefore compact earlier rounds.
	for _, msg := range tail {
		if isContextRequest(msg) {
			return
		}
	}
	for i := len(prefix) - 1; i >= 0; i-- {
		if isContextRequest(prefix[i]) {
			tail = append([]*schema.AgenticMessage{prefix[i]}, tail...)
			prefix = append(append([]*schema.AgenticMessage{}, prefix[:i]...), prefix[i+1:]...)
			break
		}
	}
	return
}

func (p *ContextPipeline) compact(ctx context.Context, messages []*schema.AgenticMessage, tools []*schema.ToolInfo, force bool, deferred ...[]*schema.ToolInfo) (result []*schema.AgenticMessage, failure error) {
	allDeferred := flattenToolInfos(deferred)
	initial, err := p.measureMainContext(ctx, messages, tools, allDeferred)
	if err != nil {
		return nil, err
	}
	compactionFields := append([]zap.Field{zap.Int("custom.context.messages_before", len(messages)), zap.Bool("custom.context.forced", force)}, p.cfg.Budget.measurementLogFields(initial, p.cfg.Budget.compactionReason(initial, force))...)
	ctx, operation := observability.Begin(ctx, "context-compaction", compactionFields...)
	observability.L(ctx).Info("Context compaction started", zap.String("event.action", "context-compaction-started"))
	defer func() {
		if failure == nil && !slices.Equal(result, messages) {
			p.invalidateAnchor(ctx, messages)
			p.usageState(ctx).pruneAfterCompaction(result)
		}
		operation.End(failure, zap.Int("custom.context.messages_after", len(result)))
	}()
	target := p.cfg.Budget.InputTokenLimit
	shrinkLocal := force || (p.cfg.Budget.Version >= 7 && initial.CounterMode == "usage_delta" && initial.EstimatedTokens > initial.LocalTokens)
	if shrinkLocal {
		measurement, err := p.cfg.Budget.measureContext(messages, tools, p.cfg.Budget.OutputReserveTokens, false, allDeferred)
		if err != nil {
			return nil, err
		}
		target = min(target, measurement.EstimatedTokens*3/4)
	}
	systems, prefix, tail := p.partition(messages, shrinkLocal)
	if p.cfg.Budget.Version >= 3 {
		pinned, err := p.currentInputSet(ctx, messages)
		if err != nil {
			return nil, err
		}
		kept, older := []*schema.AgenticMessage{}, []*schema.AgenticMessage{}
		for _, message := range prefix {
			if pinned[message] {
				kept = append(kept, message)
			} else {
				older = append(older, message)
			}
		}
		prefix, tail = older, append(kept, tail...)
	}
	if len(prefix) == 0 {
		return p.fitTailTo(ctx, systems, tail, tools, target, allDeferred)
	}
	ref, err := p.cfg.ArchiveTranscript(ctx, prefix)
	if err != nil {
		return nil, err
	}
	text, err := p.summarizePrefix(ctx, prefix)
	if err != nil {
		return nil, err
	}
	sources := contextSources(prefix)
	sourceJSON, _ := json.Marshal(sources)
	summaryText := "TEAMIE_SESSION_SUMMARY (source-scoped memory; verify current access using tools):\n" + text + "\n" + contextReadNotice(ref)
	if p.cfg.Budget.Version < 3 {
		summaryText += "\nTEAMIE_SOURCE_REFERENCES " + string(sourceJSON)
	}
	summary := schema.UserAgenticMessage(summaryText)
	summary.Extra = map[string]any{"teamie_context_summary": contextProfileV1, "teamie_context_sources": contextSourceValue(sources), "teamie_context_archive_ref": ref}
	return p.fitTailTo(ctx, append(append([]*schema.AgenticMessage{}, systems...), summary), tail, tools, target, allDeferred)
}

func contextSourceValue(sources []InputSource) any {
	raw, _ := json.Marshal(sources)
	var value any
	_ = json.Unmarshal(raw, &value)
	return value
}

func contextSources(messages []*schema.AgenticMessage) []InputSource {
	refs := []InputSource{}
	seen := map[string]bool{}
	appendRef := func(ref InputSource) {
		key := ref.WorkspaceID + "/" + ref.ConversationID + "/" + ref.MessageID + "/" + ref.EventID
		if ref.ConversationID != "" && ref.MessageID != "" && !seen[key] {
			seen[key] = true
			refs = append(refs, ref)
		}
	}
	for _, msg := range messages {
		if isContextSummary(msg) || (msg != nil && msg.Role == schema.AgenticRoleTypeUser && msg.Extra["teamie_context_data"] == contextProfileV1) {
			raw, _ := json.Marshal(msg.Extra["teamie_context_sources"])
			var prev []InputSource
			if json.Unmarshal(raw, &prev) == nil {
				for _, ref := range prev {
					appendRef(ref)
				}
			}
			continue
		}
		if msg == nil || msg.Role != schema.AgenticRoleTypeUser || hasToolResults(msg) {
			continue
		}
		text := agenticText(msg)
		for _, marker := range []string{"TEAMIE_NEW_MESSAGE ", "TEAMIE_TASK_NOTIFICATION "} {
			if strings.HasPrefix(text, marker) {
				var in Input
				if json.Unmarshal([]byte(strings.TrimPrefix(text, marker)), &in) == nil {
					sources, _ := inputSources([]Input{in})
					for _, ref := range sources {
						appendRef(ref)
					}
				}
				break
			}
		}
	}
	if len(refs) > 32 {
		refs = refs[len(refs)-32:]
	}
	return refs
}
