// Package app orchestrates Agent Runtime application use cases and transaction
// boundaries against pure domain models and outbound repository/gateway ports.
package app

import (
	"context"
	"encoding/json"

	"teamie/services/agent-runtime/internal/domain"
)

// Service coordinates Agent Runtime application use cases across session leases,
// durable inbox ingestion, schedule wakeup validation, provenance/context policy,
// and runtime inspection/lifecycle commands.
type Service struct {
	Tx           domain.TxManager
	Sessions     domain.SessionRepository
	Schedules    domain.ScheduleRepository
	Archive      domain.ArchiveRepository
	ToolReceipts domain.ToolReceiptRepository
	IM           domain.IMGateway
	Control      domain.ControlGateway
	Model        domain.ModelGateway
	Sandbox      domain.SandboxGateway
	Engine       domain.RuntimeEngine
}

func (s *Service) policy() *domain.PolicyEngine {
	if s == nil {
		return &domain.PolicyEngine{}
	}
	return &domain.PolicyEngine{
		Tx:           s.Tx,
		Sessions:     s.Sessions,
		Schedules:    s.Schedules,
		Archive:      s.Archive,
		ToolReceipts: s.ToolReceipts,
		IM:           s.IM,
		Control:      s.Control,
		Model:        s.Model,
		Sandbox:      s.Sandbox,
	}
}

// Ready checks whether the runtime engine and its upstream dependencies are ready.
func (s *Service) Ready(ctx context.Context) error {
	if s != nil && s.Engine != nil {
		return s.Engine.Ready(ctx)
	}
	if s != nil && s.Tx != nil {
		return s.Tx.Ping(ctx)
	}
	return nil
}

// Run starts the background runtime TurnLoop scheduler until ctx is canceled.
func (s *Service) Run(ctx context.Context) {
	if s != nil && s.Engine != nil {
		s.Engine.Run(ctx)
	}
}

func unavailableResult() domain.RuntimeResult {
	return domain.RuntimeResult{
		StatusCode:   503,
		ErrorCode:    "runtime_unavailable",
		ErrorMessage: "runtime_unavailable",
	}
}

// State returns the current runtime state for an Agent.
func (s *Service) State(ctx context.Context, q domain.RuntimeQuery) (domain.RuntimeResult, error) {
	if s == nil || s.Engine == nil {
		return unavailableResult(), nil
	}
	return s.Engine.State(ctx, q)
}

// Runs lists paginated runtime runs for an Agent.
func (s *Service) Runs(ctx context.Context, q domain.RuntimeQuery) (domain.RuntimeResult, error) {
	if s == nil || s.Engine == nil {
		return unavailableResult(), nil
	}
	return s.Engine.Runs(ctx, q)
}

// RunDetail returns a single runtime run by resource ID.
func (s *Service) RunDetail(ctx context.Context, q domain.RuntimeQuery) (domain.RuntimeResult, error) {
	if s == nil || s.Engine == nil {
		return unavailableResult(), nil
	}
	return s.Engine.RunDetail(ctx, q)
}

// Tasks lists paginated background tasks for an Agent.
func (s *Service) Tasks(ctx context.Context, q domain.RuntimeQuery) (domain.RuntimeResult, error) {
	if s == nil || s.Engine == nil {
		return unavailableResult(), nil
	}
	return s.Engine.Tasks(ctx, q)
}

// TaskDetail returns a single background task by resource ID.
func (s *Service) TaskDetail(ctx context.Context, q domain.RuntimeQuery) (domain.RuntimeResult, error) {
	if s == nil || s.Engine == nil {
		return unavailableResult(), nil
	}
	return s.Engine.TaskDetail(ctx, q)
}

// Events lists paginated public runtime events for an Agent.
func (s *Service) Events(ctx context.Context, q domain.RuntimeQuery) (domain.RuntimeResult, error) {
	if s == nil || s.Engine == nil {
		return unavailableResult(), nil
	}
	return s.Engine.Events(ctx, q)
}

// CancelRun requests cancellation of an active or pending run.
func (s *Service) CancelRun(ctx context.Context, q domain.RuntimeQuery) (domain.RuntimeResult, error) {
	if s == nil || s.Engine == nil {
		return unavailableResult(), nil
	}
	return s.Engine.CancelRun(ctx, q)
}

// RetryRun schedules a retry for a failed or interrupted run.
func (s *Service) RetryRun(ctx context.Context, q domain.RuntimeQuery) (domain.RuntimeResult, error) {
	if s == nil || s.Engine == nil {
		return unavailableResult(), nil
	}
	return s.Engine.RetryRun(ctx, q)
}

// CancelTask requests cancellation of an active background task.
func (s *Service) CancelTask(ctx context.Context, q domain.RuntimeQuery) (domain.RuntimeResult, error) {
	if s == nil || s.Engine == nil {
		return unavailableResult(), nil
	}
	return s.Engine.CancelTask(ctx, q)
}

// ValidateLease verifies that a worker lease is well-formed and currently held in storage.
func (s *Service) ValidateLease(ctx context.Context, lease *domain.Lease) error {
	return s.policy().ValidateLease(ctx, lease)
}

// CheckRevisionPreemption reports whether an active worker must be canceled due to
// agent disablement, deletion, or revision advancement.
func (s *Service) CheckRevisionPreemption(enabled, deleted bool, currentRevision, workerRevision int64) bool {
	return s.policy().CheckRevisionPreemption(enabled, deleted, currentRevision, workerRevision)
}

// ResolveRunContextPolicy validates and canonicalizes a Run's context profile and budget snapshot.
func (s *Service) ResolveRunContextPolicy(profile string, raw json.RawMessage) (string, json.RawMessage, error) {
	return s.policy().ResolveRunContextPolicy(profile, raw)
}

// ResolveScheduleRowSources resolves the canonical provenance InputSource set for a schedule row.
func (s *Service) ResolveScheduleRowSources(ctx context.Context, agentID, createdRunID string, sourceRefsJSON []byte, sourceOverflow bool) ([]domain.InputSource, error) {
	return s.policy().ResolveScheduleRowSources(ctx, agentID, createdRunID, sourceRefsJSON, sourceOverflow)
}

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
