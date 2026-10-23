package app

import (
	"context"
	"encoding/json"

	"teamie/services/agent-runtime/internal/domain"
)

// ValidateLease verifies that a worker lease is well-formed and currently held in storage.
func (s *Service) ValidateLease(ctx context.Context, lease *domain.Lease) error {
	if lease == nil || !lease.Valid() {
		return domain.ErrLeaseLost
	}
	if s == nil || s.Sessions == nil {
		return nil
	}
	return s.Sessions.CheckLease(ctx, lease)
}

// CheckRevisionPreemption reports whether an active worker must be canceled due to
// agent disablement, deletion, or revision advancement.
func (s *Service) CheckRevisionPreemption(enabled, deleted bool, currentRevision, workerRevision int64) bool {
	return domain.ShouldPreemptWorker(enabled, deleted, currentRevision, workerRevision)
}

// ResolveRunContextPolicy validates and canonicalizes a Run's context profile and budget snapshot.
func (s *Service) ResolveRunContextPolicy(profile string, raw json.RawMessage) (string, json.RawMessage, error) {
	return domain.CanonicalContextPolicy(profile, raw)
}

// WorkerOwnerID returns the unique worker owner identifier for this runtime instance.
func (s *Service) WorkerOwnerID() string {
	if s != nil && s.Engine != nil {
		return s.Engine.OwnerID()
	}
	return ""
}

// ListAgentProfiles fetches all current Agent profiles from the Control Plane.
func (s *Service) ListAgentProfiles(ctx context.Context) ([]domain.AgentProfile, error) {
	if s == nil || s.Control == nil {
		return nil, nil
	}
	return s.Control.ListAgentProfiles(ctx)
}

// AcquireWorkerLease attempts to acquire a single-writer worker lease for an Agent.
func (s *Service) AcquireWorkerLease(ctx context.Context, agentID string) (*domain.Lease, error) {
	if s == nil || s.Sessions == nil {
		return nil, nil
	}
	return s.Sessions.AcquireLease(ctx, agentID, s.WorkerOwnerID())
}

// ReleaseWorkerLease releases a previously acquired single-writer worker lease.
func (s *Service) ReleaseWorkerLease(ctx context.Context, lease *domain.Lease) {
	if s != nil && s.Sessions != nil && lease != nil {
		s.Sessions.ReleaseLease(ctx, lease)
	}
}

// CancelAgentTasks cancels all active background tasks belonging to a disabled or deleted Agent.
func (s *Service) CancelAgentTasks(ctx context.Context, agentID string) {
	if s != nil && s.Engine != nil {
		s.Engine.CancelAgentTasks(ctx, agentID)
	}
}

// WorkAgent executes the single-writer TurnLoop worker for an Agent while holding lease.
func (s *Service) WorkAgent(ctx context.Context, profile domain.AgentProfile, lease *domain.Lease) {
	if s != nil && s.Engine != nil && lease != nil {
		s.Engine.WorkAgent(ctx, profile, lease)
	}
}
