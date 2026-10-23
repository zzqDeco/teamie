package app

import (
	"context"

	"teamie/services/agent-runtime/internal/domain"
)

// ResolveScheduleRowSources resolves the canonical provenance InputSource set for a schedule row.
func (s *Service) ResolveScheduleRowSources(ctx context.Context, agentID, createdRunID string, sourceRefsJSON []byte, sourceOverflow bool) ([]domain.InputSource, error) {
	return s.policy().ResolveScheduleRowSources(ctx, agentID, createdRunID, sourceRefsJSON, sourceOverflow)
}

// ResolveRunSources recursively resolves the canonical InputSource set for runID from the context archive.
func (s *Service) ResolveRunSources(ctx context.Context, agentID, runID string) ([]domain.InputSource, error) {
	if s == nil || s.Archive == nil {
		return nil, domain.ErrContextUnavailable
	}
	return s.Archive.ResolveRunSources(ctx, agentID, runID)
}
