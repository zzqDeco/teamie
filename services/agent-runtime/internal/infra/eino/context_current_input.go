package eino

import (
	"context"
	"slices"

	"github.com/cloudwego/eino/schema"
	"teamie/services/agent-runtime/internal/domain"
)

// Match actual Run inputs, not an archive preview or a model-authored source
// marker. These complete source envelopes are the current work, even after a
// long sequence of tool calls or a batch containing several conversations.
func (p *ContextPipeline) currentInputSet(ctx context.Context, messages []*schema.AgenticMessage) (map[*schema.AgenticMessage]bool, error) {
	pinned := map[*schema.AgenticMessage]bool{}
	run, err := CurrentRun(ctx)
	if err != nil || run == nil {
		for i := len(messages) - 1; i >= 0; i-- {
			if isContextRequest(messages[i]) {
				pinned[messages[i]] = true
				break
			}
		}
		return pinned, nil
	}
	wanted := map[string]bool{}
	required := map[string]bool{}
	for index, message := range inputMessages(run.Inputs) {
		// Task, schedule and app notifications are durable machine
		// observations, not new IM bodies. A completed batch can fill the
		// entire window if every repeated source envelope is pinned. Keep its
		// complete archive and provenance, while allowing normal history
		// reduction to shrink it.
		if !domain.ShouldPinCurrentInput(p.cfg.Budget.Version, run.Inputs[index].Kind) {
			continue
		}
		text := agenticText(message)
		wanted[text] = false
		// A native managed-task checkpoint can consume its notification by
		// resuming the suspended tool instead of appending another user block.
		// Keep this legacy required-input rule for frozen budgets before v5;
		// current budgets excluded every machine notification above.
		required[text] = run.Inputs[index].Kind != "task.notification" && run.Inputs[index].Kind != "schedule.wakeup"
	}
	for _, message := range messages {
		if message == nil || message.Role != schema.AgenticRoleTypeUser || hasToolResults(message) || isContextSummary(message) || message.Extra["teamie_context_data"] == contextProfileV1 {
			continue
		}
		text := agenticText(message)
		if _, ok := wanted[text]; ok {
			pinned[message], wanted[text] = true, true
		}
	}
	for text, found := range wanted {
		if required[text] && !found {
			return nil, &ModelError{Code: "context_unrecoverable"}
		}
	}
	return pinned, nil
}

func (p *ContextPipeline) fitCurrentInputs(ctx context.Context, fixed, tail []*schema.AgenticMessage, tools []*schema.ToolInfo, target int, deferred ...[]*schema.ToolInfo) ([]*schema.AgenticMessage, error) {
	allDeferred := flattenToolInfos(deferred)
	out := append(append([]*schema.AgenticMessage{}, fixed...), tail...)
	pinned, err := p.currentInputSet(ctx, out)
	if err != nil {
		return nil, err
	}
	policy, required := []*schema.AgenticMessage{}, []*schema.AgenticMessage{}
	for _, message := range out {
		if message != nil && message.Role == schema.AgenticRoleTypeSystem {
			policy = append(policy, message)
		}
		if pinned[message] {
			required = append(required, message)
		}
	}
	minimum, err := p.cfg.Budget.measureContext(append(append([]*schema.AgenticMessage{}, policy...), required...), tools, p.cfg.Budget.OutputReserveTokens, false, allDeferred)
	if err != nil {
		return nil, err
	}
	if !p.cfg.Budget.fits(minimum) {
		return nil, &ModelError{Code: "context_unrecoverable"}
	}
	target = max(target, minimum.EstimatedTokens)
	fits := func(messages []*schema.AgenticMessage) (bool, error) {
		measurement, err := p.cfg.Budget.measureContext(messages, tools, p.cfg.Budget.OutputReserveTokens, false, allDeferred)
		if slices.Equal(messages, out) {
			measurement, err = p.measureMainContext(ctx, messages, tools, allDeferred)
		}
		return err == nil && p.cfg.Budget.fits(measurement) && measurement.EstimatedTokens <= target, err
	}
	if ok, err := fits(out); ok || err != nil {
		return out, err
	}
	units := contextUnits(out[len(policy):])
	if p.cfg.Budget.Version >= 4 {
		if compacted, err := p.coalesceHistory(ctx, policy, units, pinned, tools, allDeferred); compacted != nil || err != nil {
			return compacted, err
		}
	}
	for i, unit := range units {
		protected := false
		for _, message := range unit {
			protected = protected || pinned[message]
		}
		if protected {
			continue
		}
		measurement, err := p.cfg.Budget.measureContext(unit, nil, p.cfg.Budget.OutputReserveTokens, false)
		if err != nil {
			return nil, err
		}
		if measurement.EstimatedTokens < 384 {
			continue
		}
		notice, err := p.archiveUnit(ctx, unit)
		if err != nil {
			return nil, err
		}
		units[i] = []*schema.AgenticMessage{notice}
		candidate := append([]*schema.AgenticMessage{}, policy...)
		for _, messages := range units {
			candidate = append(candidate, messages...)
		}
		if ok, err := fits(candidate); ok || err != nil {
			return candidate, err
		}
	}
	// A forced compaction target is a heuristic. Preserve the full request when
	// only the soft target is too small; the hard provider/transport limits still
	// apply, and native recovery separately requires an actual reduction.
	candidate := append([]*schema.AgenticMessage{}, policy...)
	for _, messages := range units {
		candidate = append(candidate, messages...)
	}
	measurement, err := p.cfg.Budget.measureContext(candidate, tools, p.cfg.Budget.OutputReserveTokens, false, allDeferred)
	if slices.Equal(candidate, out) {
		measurement, err = p.measureMainContext(ctx, candidate, tools, allDeferred)
	}
	if err != nil {
		return nil, err
	}
	if p.cfg.Budget.fits(measurement) {
		return candidate, nil
	}
	return nil, &ModelError{Code: "context_unrecoverable"}
}
