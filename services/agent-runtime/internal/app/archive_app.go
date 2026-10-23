package app

import (
	"context"
	"encoding/json"

	"teamie/services/agent-runtime/internal/domain"
)

// ResolveScheduleRowSources resolves the canonical provenance InputSource set for a schedule row.
func (s *Service) ResolveScheduleRowSources(ctx context.Context, agentID, createdRunID string, sourceRefsJSON []byte, sourceOverflow bool) ([]domain.InputSource, error) {
	var refs []domain.InputSource
	if len(sourceRefsJSON) > 0 {
		if json.Unmarshal(sourceRefsJSON, &refs) != nil {
			return nil, domain.ErrReplayConflict
		}
	}
	if sourceOverflow || len(refs) == 0 {
		if !domain.ValidUUID(createdRunID) || s == nil || s.Archive == nil {
			return nil, domain.ErrContextUnavailable
		}
		return s.Archive.ResolveRunSources(ctx, agentID, createdRunID)
	}
	canonical, err := domain.CanonicalContextSources(refs)
	if err != nil {
		return nil, err
	}
	if len(canonical) == 0 {
		if !domain.ValidUUID(createdRunID) || s == nil || s.Archive == nil {
			return nil, domain.ErrContextUnavailable
		}
		return s.Archive.ResolveRunSources(ctx, agentID, createdRunID)
	}
	return canonical, nil
}

// ResolveRunSources recursively resolves the canonical InputSource set for runID from the context archive.
func (s *Service) ResolveRunSources(ctx context.Context, agentID, runID string) ([]domain.InputSource, error) {
	if s == nil || s.Archive == nil {
		return nil, domain.ErrContextUnavailable
	}
	return s.Archive.ResolveRunSources(ctx, agentID, runID)
}
