package engine

import (
	"context"

	"github.com/cloudwego/eino/schema"
)

// Individually small observations and archive notices can exhaust the window
// together. Coalesce older complete units before sacrificing recent results;
// otherwise an Agent can repeatedly read a file whose result is immediately
// archived again. Current source inputs never enter the historical archive.
func (p *ContextPipeline) coalesceHistory(ctx context.Context, policy []*schema.AgenticMessage, units [][]*schema.AgenticMessage, pinned map[*schema.AgenticMessage]bool, tools []*schema.ToolInfo) ([]*schema.AgenticMessage, error) {
	indices := []int{}
	for index, unit := range units {
		protected := false
		for _, message := range unit {
			protected = protected || pinned[message]
		}
		if !protected {
			indices = append(indices, index)
		}
	}
	// Prefer retaining the two newest complete units. Only consume those when
	// the actual hard capacity requires it, not just a soft reduction target.
	for keep := min(2, len(indices)); keep >= 0; keep-- {
		count := len(indices) - keep
		if count < 2 {
			continue
		}
		older := []*schema.AgenticMessage{}
		selected := map[int]bool{}
		for _, index := range indices[:count] {
			older = append(older, units[index]...)
			selected[index] = true
		}
		notice, err := p.archiveUnit(ctx, older)
		if err != nil {
			return nil, err
		}
		// A coalesced pointer needs no repeated preview. Its immutable archive
		// contains the full observations and nested refs; source ACL metadata
		// remains attached to the notice and every later read is authorized.
		ref := notice.Extra["teamie_context_archive_ref"].(string)
		notice.ContentBlocks = schema.UserAgenticMessage(contextReadNotice(ref)).ContentBlocks
		before, err := p.cfg.Budget.measureContext(older, nil, p.cfg.Budget.OutputReserveTokens, false)
		if err != nil {
			return nil, err
		}
		after, err := p.cfg.Budget.measureContext([]*schema.AgenticMessage{notice}, nil, p.cfg.Budget.OutputReserveTokens, false)
		if err != nil {
			return nil, err
		}
		if after.EstimatedTokens >= before.EstimatedTokens || after.TransportBytes >= before.TransportBytes {
			continue
		}
		candidate := append([]*schema.AgenticMessage{}, policy...)
		for index, unit := range units {
			if index == indices[0] {
				candidate = append(candidate, notice)
			}
			if !selected[index] {
				candidate = append(candidate, unit...)
			}
		}
		measurement, err := p.cfg.Budget.measureContext(candidate, tools, p.cfg.Budget.OutputReserveTokens, false)
		if err != nil {
			return nil, err
		}
		if p.cfg.Budget.fits(measurement) {
			return candidate, nil
		}
	}
	return nil, nil
}
