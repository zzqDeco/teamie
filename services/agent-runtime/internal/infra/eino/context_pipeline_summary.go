package eino

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"
)

func (p *ContextPipeline) summaryInput(ctx context.Context, system, instruction *schema.AgenticMessage, original []*schema.AgenticMessage) ([]*schema.AgenticMessage, error) {
	input := append([]*schema.AgenticMessage{system}, original...)
	input = append(input, instruction)
	m, err := p.cfg.Budget.measureSummaryContext(input, p.cfg.Budget.SummaryOutputTokens, false)
	if err != nil {
		return nil, err
	}
	if !p.cfg.Budget.fits(m) {
		return nil, &ModelError{Code: "context_unrecoverable"}
	}
	return input, nil
}

// Build bounded summary requests from complete rounds. Large individual rounds
// first become durable archive references; they cannot defeat the summary
// transport limit. Aggregate summaries in further native calls only while the
// representation shrinks. No inference duration limit is involved.
func (p *ContextPipeline) summarizePrefix(ctx context.Context, prefix []*schema.AgenticMessage) (string, error) {
	current := prefix
	for {
		chunks := [][]*schema.AgenticMessage{}
		chunk := []*schema.AgenticMessage{}
		if p.cfg.Budget.Version >= 6 {
			var err error
			chunks, err = p.longContextSummaryChunks(ctx, current)
			if err != nil {
				return "", err
			}
		}
		units := contextUnits(current)
		if p.cfg.Budget.Version >= 6 {
			units = nil
		}
		for _, unit := range units {
			m, err := p.cfg.Budget.measureSummaryContext(unit, p.cfg.Budget.SummaryOutputTokens, false)
			if err != nil {
				return "", err
			}
			if m.EstimatedTokens > p.cfg.Budget.InputTokenLimit/2 || m.TransportBytes > p.cfg.Budget.TransportTargetBytes/2 {
				notice, err := p.archiveUnit(ctx, unit)
				if err != nil {
					return "", err
				}
				unit = []*schema.AgenticMessage{notice}
			}
			candidate := append(append([]*schema.AgenticMessage{}, chunk...), unit...)
			m, err = p.cfg.Budget.measureSummaryContext(candidate, p.cfg.Budget.SummaryOutputTokens, false)
			if err != nil {
				return "", err
			}
			if len(chunk) > 0 && (m.EstimatedTokens > p.cfg.Budget.InputTokenLimit/2 || m.TransportBytes > p.cfg.Budget.TransportTargetBytes/2) {
				chunks = append(chunks, chunk)
				chunk = append([]*schema.AgenticMessage{}, unit...)
			} else {
				chunk = candidate
			}
		}
		if len(chunk) > 0 {
			chunks = append(chunks, chunk)
		}
		if len(chunks) == 0 {
			return "", errors.New("empty_context_prefix")
		}
		next := []*schema.AgenticMessage{}
		for _, chunk := range chunks {
			result, err := p.summarizeChunk(ctx, chunk)
			if err != nil {
				return "", err
			}
			next = append(next, result...)
		}
		if len(next) == 1 {
			return agenticText(next[0]), nil
		}
		before, err := p.cfg.Budget.measureSummaryContext(current, p.cfg.Budget.SummaryOutputTokens, false)
		if err != nil {
			return "", err
		}
		after, err := p.cfg.Budget.measureSummaryContext(next, p.cfg.Budget.SummaryOutputTokens, false)
		if err != nil {
			return "", err
		}
		if after.EstimatedTokens >= before.EstimatedTokens && after.TransportBytes >= before.TransportBytes {
			return "", errors.New("context_summary_did_not_shrink")
		}
		current = next
	}
}

func (p *ContextPipeline) summarizeChunk(ctx context.Context, chunk []*schema.AgenticMessage) ([]*schema.AgenticMessage, error) {
	result, err := p.summary.(contextSummarizer).Summarize(ctx, &adk.TypedChatModelAgentState[*schema.AgenticMessage]{Messages: chunk})
	if err == nil {
		if len(result) != 1 {
			return nil, errors.New("invalid_context_summary")
		}
		return result, nil
	}
	var failure *ModelError
	if !errors.As(err, &failure) || !isContextOverflow(failure.Code) {
		return nil, err
	}
	units := contextUnits(chunk)
	if len(units) > 1 {
		mid := len(units) / 2
		left, right := []*schema.AgenticMessage{}, []*schema.AgenticMessage{}
		for _, unit := range units[:mid] {
			left = append(left, unit...)
		}
		for _, unit := range units[mid:] {
			right = append(right, unit...)
		}
		a, err := p.summarizeChunk(ctx, left)
		if err != nil {
			return nil, err
		}
		b, err := p.summarizeChunk(ctx, right)
		if err != nil {
			return nil, err
		}
		return append(a, b...), nil
	}
	notice, archiveErr := p.archiveUnit(ctx, chunk)
	if archiveErr != nil {
		return nil, archiveErr
	}
	before, _ := p.cfg.Budget.measureSummaryContext(chunk, p.cfg.Budget.SummaryOutputTokens, false)
	after, _ := p.cfg.Budget.measureSummaryContext([]*schema.AgenticMessage{notice}, p.cfg.Budget.SummaryOutputTokens, false)
	if after.EstimatedTokens >= before.EstimatedTokens && after.TransportBytes >= before.TransportBytes {
		return nil, err
	}
	return p.summary.(contextSummarizer).Summarize(ctx, &adk.TypedChatModelAgentState[*schema.AgenticMessage]{Messages: []*schema.AgenticMessage{notice}})
}

func (p *ContextPipeline) archiveUnit(ctx context.Context, unit []*schema.AgenticMessage) (*schema.AgenticMessage, error) {
	ref, err := p.cfg.ArchiveTranscript(ctx, unit)
	if err != nil {
		return nil, err
	}
	sources := contextSources(unit)
	raw, _ := json.Marshal(sources)
	preview := ""
	for _, message := range unit {
		if preview != "" {
			preview += "\n"
		}
		preview += utf8Prefix(agenticText(message), 256)
		if len(preview) >= 512 {
			preview = utf8Prefix(preview, 512)
			break
		}
	}
	noticeText := contextReadNotice(ref) + "\nQuoted data preview:\n" + preview
	if p.cfg.Budget.Version < 3 {
		noticeText += "\nTEAMIE_SOURCE_REFERENCES " + string(raw)
	}
	out := schema.UserAgenticMessage(noticeText)
	request := false
	for _, message := range unit {
		if isContextRequest(message) {
			request = true
		}
	}
	out.Extra = map[string]any{"teamie_context_data": contextProfileV1, "teamie_context_sources": contextSourceValue(sources), "teamie_context_archive_ref": ref, "teamie_context_request": request}
	return out, nil
}

// A final hard check also includes the current policy and full tool schema.
// If one recent round itself exceeds the window, archive the whole round; never
// leave orphaned tool calls/results. Platform policy and tools are immutable.
func (p *ContextPipeline) fitTail(ctx context.Context, fixed, tail []*schema.AgenticMessage, tools []*schema.ToolInfo, deferred ...[]*schema.ToolInfo) ([]*schema.AgenticMessage, error) {
	return p.fitTailTo(ctx, fixed, tail, tools, p.cfg.Budget.InputTokenLimit, deferred...)
}

func (p *ContextPipeline) fitTailTo(ctx context.Context, fixed, tail []*schema.AgenticMessage, tools []*schema.ToolInfo, target int, deferred ...[]*schema.ToolInfo) ([]*schema.AgenticMessage, error) {
	if p.cfg.Budget.Version >= 3 {
		return p.fitCurrentInputs(ctx, fixed, tail, tools, target, deferred...)
	}
	fits := func(m ContextMeasurement) bool { return p.cfg.Budget.fits(m) && m.EstimatedTokens <= target }
	out := append(append([]*schema.AgenticMessage{}, fixed...), tail...)
	m, err := p.cfg.Budget.measureContext(out, tools, p.cfg.Budget.OutputReserveTokens, false)
	if err != nil {
		return nil, err
	}
	if fits(m) {
		return out, nil
	}
	// First verify that an unshrinkable policy/tool manifest can fit at all.
	policy := []*schema.AgenticMessage{}
	for _, msg := range fixed {
		if msg != nil && msg.Role == schema.AgenticRoleTypeSystem {
			policy = append(policy, msg)
		}
	}
	base, err := p.cfg.Budget.measureContext(policy, tools, p.cfg.Budget.OutputReserveTokens, false)
	if err != nil {
		return nil, err
	}
	if !fits(base) {
		return nil, &ModelError{Code: "context_unrecoverable"}
	}
	units := contextUnits(out[len(policy):])
	for i, unit := range units {
		measure, err := p.cfg.Budget.measureContext(unit, nil, p.cfg.Budget.OutputReserveTokens, false)
		if err != nil {
			return nil, err
		}
		// A compact archive notice is already a stable minimal representation.
		if measure.EstimatedTokens < 384 {
			continue
		}
		notice, err := p.archiveUnit(ctx, unit)
		if err != nil {
			return nil, err
		}
		units[i] = []*schema.AgenticMessage{notice}
		candidate := append([]*schema.AgenticMessage{}, policy...)
		for _, part := range units {
			candidate = append(candidate, part...)
		}
		m, err = p.cfg.Budget.measureContext(candidate, tools, p.cfg.Budget.OutputReserveTokens, false)
		if err != nil {
			return nil, err
		}
		if fits(m) {
			return candidate, nil
		}
	}
	return nil, &ModelError{Code: "context_unrecoverable"}
}
