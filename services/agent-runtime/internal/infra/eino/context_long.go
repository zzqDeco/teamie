package eino

import (
	"context"

	"github.com/cloudwego/eino/schema"
)

func flattenContextUnits(units [][]*schema.AgenticMessage) []*schema.AgenticMessage {
	messages := []*schema.AgenticMessage{}
	for _, unit := range units {
		messages = append(messages, unit...)
	}
	return messages
}

// Search complete-unit boundaries instead of serializing every growing suffix.
// At million-token windows the latter makes compaction quadratic in history.
// Earlier frozen policies retain their exact original partitioning code.
func (p *ContextPipeline) longContextTail(units [][]*schema.AgenticMessage, limit int) (int, []*schema.AgenticMessage) {
	best := len(units) - 1 // Keep at least the newest complete unit, even if oversized.
	for low, high := 0, len(units)-1; low <= high; {
		mid := (low + high) / 2
		candidate := flattenContextUnits(units[mid:])
		measurement, err := p.cfg.Budget.measureContext(candidate, nil, p.cfg.Budget.OutputReserveTokens, false)
		if err == nil && measurement.EstimatedTokens <= limit {
			best = mid
			high = mid - 1
		} else {
			low = mid + 1
		}
	}
	return best, flattenContextUnits(units[best:])
}

func (p *ContextPipeline) longContextSummaryChunks(ctx context.Context, messages []*schema.AgenticMessage) ([][]*schema.AgenticMessage, error) {
	units := contextUnits(messages)
	fits := func(messages []*schema.AgenticMessage) (bool, error) {
		measurement, err := p.cfg.Budget.measureSummaryContext(messages, p.cfg.Budget.SummaryOutputTokens, false)
		return measurement.EstimatedTokens <= p.cfg.Budget.InputTokenLimit/2 && measurement.TransportBytes <= p.cfg.Budget.TransportTargetBytes/2, err
	}
	for i, unit := range units {
		ok, err := fits(unit)
		if err != nil {
			return nil, err
		}
		if !ok {
			notice, err := p.archiveUnit(ctx, unit)
			if err != nil {
				return nil, err
			}
			units[i] = []*schema.AgenticMessage{notice}
		}
	}
	chunks := [][]*schema.AgenticMessage{}
	for start := 0; start < len(units); {
		best := start + 1
		for low, high := start+1, len(units); low <= high; {
			mid := (low + high) / 2
			ok, err := fits(flattenContextUnits(units[start:mid]))
			if err != nil {
				return nil, err
			}
			if ok {
				best = mid
				low = mid + 1
			} else {
				high = mid - 1
			}
		}
		chunks = append(chunks, flattenContextUnits(units[start:best]))
		start = best
	}
	return chunks, nil
}
