package app

import (
	"context"

	"teamie/services/agent-runtime/internal/domain"
)

// AuthorizeScheduleInput validates a TEAMIE_SCHEDULE_WAKEUP input against the schedule's
// current status and live IM source ACLs.
func (s *Service) AuthorizeScheduleInput(ctx context.Context, lease *domain.Lease, in domain.Input) error {
	return s.policy().AuthorizeScheduleInput(ctx, lease, in)
}

// FilterAuthorizedSchedules filters active schedule candidates by re-verifying their
// provenance sources against current IM ACLs, automatically canceling any revoked schedules.
func (s *Service) FilterAuthorizedSchedules(ctx context.Context, agentID string, candidates []domain.ScheduleCandidate) ([]domain.ScheduleItem, error) {
	return s.policy().FilterAuthorizedSchedules(ctx, agentID, candidates)
}

// AuthorizeScheduleListReceipt verifies that every schedule in a cached schedule_list
// tool receipt still has valid live source ACLs, canceling revoked schedules.
func (s *Service) AuthorizeScheduleListReceipt(ctx context.Context, agentID string, cached []byte) error {
	return s.policy().AuthorizeScheduleListReceipt(ctx, agentID, cached)
}

// EvaluateScheduleCreation coordinates idempotent schedule creation within a fenced transaction.
func (s *Service) EvaluateScheduleCreation(ctx context.Context, lease *domain.Lease, runID, callID string, runInputs []domain.Input, spec domain.ScheduleSpec) (*domain.ScheduleItem, string, error) {
	return s.policy().EvaluateScheduleCreation(ctx, lease, runID, callID, runInputs, spec)
}
