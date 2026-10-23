package app

import (
	"context"

	"teamie/services/agent-runtime/internal/domain"
)

// GetInboxCursor returns the current IM sync cursor for the leased Agent.
func (s *Service) GetInboxCursor(ctx context.Context, lease *domain.Lease) (string, error) {
	if s == nil || s.Sessions == nil {
		return "", domain.ErrLeaseLost
	}
	return s.Sessions.GetCursor(ctx, lease)
}

// IngestInboxInputs atomically persists inbox inputs before advancing the Agent's IM sync cursor.
func (s *Service) IngestInboxInputs(ctx context.Context, lease *domain.Lease, cursor string, inputs []domain.Input) error {
	if s == nil || s.Sessions == nil {
		return domain.ErrLeaseLost
	}
	return s.Sessions.IngestInputs(ctx, lease, cursor, inputs)
}

// SkipInboxInput marks an inbox event as skipped under the active worker lease fence.
func (s *Service) SkipInboxInput(ctx context.Context, lease *domain.Lease, eventID string) error {
	if s == nil || s.Sessions == nil {
		return domain.ErrLeaseLost
	}
	return s.Sessions.SkipInput(ctx, lease, eventID)
}
