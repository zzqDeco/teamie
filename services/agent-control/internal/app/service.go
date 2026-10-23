// Package app contains application use-case orchestration for the Agent Control
// Plane bounded context. It depends only on the Go standard library and
// teamie/services/agent-control/internal/domain.
package app

import (
	"context"
	"slices"
	"time"

	"teamie/services/agent-control/internal/domain"
)

// Service coordinates Agent lifecycle, visibility revisions, workspace discovery,
// and IM reconciliation.
type Service struct {
	Tx           domain.TxManager
	Agents       domain.AgentRepository
	IM           domain.IMAuthority
	Flags        domain.FeatureFlags
	Notifier     domain.RuntimeNotifier
	RuntimeToken string
}

func (s *Service) flagEnabled(key string) bool {
	return s.Flags != nil && s.Flags.Enabled(key)
}

func (s *Service) requireFlag(key string) error {
	if !s.flagEnabled(key) {
		return domain.ErrFeatureDisabled()
	}
	return nil
}

// ValidateChanges validates and normalizes owner changes and verifies workspace membership.
func (s *Service) ValidateChanges(ctx context.Context, owner string, in *domain.Changes, create bool) error {
	if err := domain.ValidateFields(in, create); err != nil {
		return err
	}
	if in.WorkspaceIDs != nil {
		allowed, err := s.IM.GetWorkspaces(ctx, owner)
		if err != nil {
			return err
		}
		if err := domain.ValidateWorkspaceSelection(*in.WorkspaceIDs, allowed); err != nil {
			return err
		}
	}
	return nil
}

// Create provisions a new Agent definition and initiates IM reconciliation.
func (s *Service) Create(ctx context.Context, owner string, in domain.Changes) (domain.Agent, error) {
	if err := s.requireFlag("agents.enabled"); err != nil {
		return domain.Agent{}, err
	}
	if err := s.requireFlag("agents.create"); err != nil {
		return domain.Agent{}, err
	}
	if err := s.ValidateChanges(ctx, owner, &in, true); err != nil {
		return domain.Agent{}, err
	}
	enabled := true
	if in.Enabled != nil {
		enabled = *in.Enabled
	}
	description := ""
	if in.Description != nil {
		description = *in.Description
	}
	a, err := s.Agents.Insert(ctx, domain.Agent{
		ID:            domain.NewID(),
		OwnerID:       owner,
		Name:          *in.Name,
		Description:   description,
		WorkspaceIDs:  *in.WorkspaceIDs,
		Enabled:       enabled,
		PolicyEnabled: s.flagEnabled("agents.enabled"),
	})
	if err != nil {
		return a, err
	}
	_ = s.Reconcile(ctx, a.ID)
	return s.Owned(ctx, owner, a.ID)
}

// Owned returns an undeleted Agent owned by owner.
func (s *Service) Owned(ctx context.Context, owner, id string) (domain.Agent, error) {
	return s.Agents.GetOwned(ctx, owner, id)
}

// Update modifies an owned Agent profile/visibility/enabled state and reconciles with IM.
func (s *Service) Update(ctx context.Context, owner, id string, in domain.Changes) (domain.Agent, error) {
	if err := s.requireFlag("agents.update"); err != nil {
		return domain.Agent{}, err
	}
	if in.Name == nil && in.Description == nil && in.WorkspaceIDs == nil && in.Enabled == nil {
		return domain.Agent{}, domain.ErrInvalidRequest("No Agent changes provided")
	}
	if in.Enabled != nil && *in.Enabled {
		if err := s.requireFlag("agents.enabled"); err != nil {
			return domain.Agent{}, err
		}
	}
	if err := s.ValidateChanges(ctx, owner, &in, false); err != nil {
		return domain.Agent{}, err
	}
	var updated domain.Agent
	if err := s.Tx.WithinTx(ctx, func(txCtx context.Context) error {
		a, err := s.Agents.GetForUpdate(txCtx, owner, id)
		if err != nil {
			return err
		}
		if in.Name != nil {
			a.Name = *in.Name
		}
		if in.Description != nil {
			a.Description = *in.Description
		}
		if in.WorkspaceIDs != nil && !slices.Equal(domain.Normalized(a.WorkspaceIDs), *in.WorkspaceIDs) {
			a.WorkspaceIDs = *in.WorkspaceIDs
			a.MembershipRevision++
		}
		if in.Enabled != nil {
			a.Enabled = *in.Enabled
		}
		updated = a
		return s.Agents.UpdateProfile(txCtx, a)
	}); err != nil {
		return updated, err
	}
	err := s.Reconcile(ctx, id)
	if err != nil && in.Enabled != nil && !*in.Enabled {
		return updated, domain.ErrControlUnavailable("Disable is pending while IM authority is unavailable")
	}
	return s.Owned(ctx, owner, id)
}

// Delete tombstones an owned Agent and revokes its IM credentials and memberships.
func (s *Service) Delete(ctx context.Context, owner, id string) error {
	if err := s.requireFlag("agents.delete"); err != nil {
		return err
	}
	found, err := s.Agents.MarkDeleted(ctx, owner, id)
	if err != nil {
		return err
	}
	if !found {
		return domain.ErrNotFound("Agent is unavailable")
	}
	if err := s.Reconcile(ctx, id); err != nil {
		return domain.ErrControlUnavailable("Deletion is pending while IM authority is unavailable")
	}
	return nil
}

// ListOwned returns all undeleted Agents owned by owner.
func (s *Service) ListOwned(ctx context.Context, owner string) ([]domain.Agent, error) {
	return s.Agents.ListOwned(ctx, owner)
}

// Discover returns visible Agents in workspace for requester user.
func (s *Service) Discover(ctx context.Context, workspace, user string) ([]domain.Agent, error) {
	ids, err := s.IM.GetParticipants(ctx, workspace, user)
	if err != nil {
		return nil, err
	}
	if !s.flagEnabled("agents.discover") {
		return []domain.Agent{}, nil
	}
	return s.Agents.ListWorkspaceAgents(ctx, workspace, ids)
}

// Decorate resolves owner display names via IM and normalizes disabled runtime state.
func (s *Service) Decorate(ctx context.Context, agents []domain.Agent) ([]domain.Agent, error) {
	ids := make([]string, 0, len(agents))
	for _, a := range agents {
		ids = append(ids, a.OwnerID)
	}
	profiles, err := s.IM.GetUsers(ctx, ids)
	if err != nil {
		return nil, err
	}
	globalEnabled := s.flagEnabled("agents.enabled")
	for n := range agents {
		if p, ok := profiles[agents[n].OwnerID]; ok {
			agents[n].OwnerName = p.DisplayName
		}
		if !agents[n].Enabled || !globalEnabled || agents[n].DeletedAt != nil {
			agents[n].RuntimeState = "disabled"
		}
	}
	return agents, nil
}

// Reconcile records the desired revision before making idempotent RPC changes.
// It disables the identity first, applies ordinary grants, then enables only the
// fully applied revision. A process crash resumes the same fenced operation.
func (s *Service) Reconcile(ctx context.Context, id string) (failure error) {
	var end func(changed bool, attempts int, err error)
	if s.Notifier != nil {
		ctx, end = s.Notifier.BeginReconcile(ctx, id)
	}
	changed := false
	attempts := 0
	defer func() {
		if end != nil {
			end(changed, attempts, failure)
		}
	}()

	for attempt := 0; attempt < 4; attempt++ {
		attempts = attempt + 1
		retry := false
		markFailed := false
		err := s.Tx.WithinTx(ctx, func(txCtx context.Context) error {
			a, err := s.Agents.GetByIDForUpdate(txCtx, id)
			if err != nil {
				return err
			}
			policy := s.flagEnabled("agents.enabled")
			if a.PolicyEnabled != policy {
				if err := s.Agents.UpdatePolicyEnabled(txCtx, id, policy); err != nil {
					markFailed = true
					return err
				}
				retry = true
				return nil
			}
			ownerWorkspaces, err := s.IM.GetWorkspaces(txCtx, a.OwnerID)
			if err != nil {
				markFailed = true
				return err
			}
			selected := domain.Intersect(a.WorkspaceIDs, ownerWorkspaces)
			if a.AppliedMembershipRevision == a.MembershipRevision {
				// Actual membership wins after a moderator removal. Updating a profile or
				// restarting the control plane cannot recreate that revoked grant.
				actual, err := s.IM.GetWorkspaces(txCtx, a.ID)
				if err != nil {
					markFailed = true
					return err
				}
				selected = domain.Intersect(selected, actual)
			}
			if !domain.Same(selected, a.WorkspaceIDs) {
				if err := s.Agents.UpdateWorkspaces(txCtx, id, selected); err != nil {
					markFailed = true
					return err
				}
				retry = true
				return nil
			}
			if a.AppliedRevision == a.Revision {
				return nil
			}
			deleted := a.DeletedAt != nil
			if err := s.IM.ProvisionPrincipal(txCtx, a, 2*a.Revision, false, deleted); err != nil {
				markFailed = true
				return err
			}
			actual, err := s.IM.SyncMemberships(txCtx, a, selected)
			if err != nil {
				markFailed = true
				return err
			}
			if !domain.Same(actual, selected) {
				if err := s.Agents.UpdateWorkspaces(txCtx, id, domain.Intersect(selected, actual)); err != nil {
					markFailed = true
					return err
				}
				retry = true
				return nil
			}
			enabled := a.Enabled && policy && !deleted
			if err := s.IM.ProvisionPrincipal(txCtx, a, 2*a.Revision+1, enabled, deleted); err != nil {
				markFailed = true
				return err
			}
			if err := s.Agents.MarkReconciled(txCtx, id, enabled); err != nil {
				markFailed = true
				return err
			}
			changed = true
			return nil
		})
		if err != nil {
			if markFailed {
				_ = s.Agents.MarkReconcileFailed(ctx, id)
			}
			return err
		}
		if !retry {
			return nil
		}
	}
	return domain.ErrControlUnavailable("Agent permissions are still changing")
}

// Run periodically reconciles all Agents until ctx is canceled.
func (s *Service) Run(ctx context.Context) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			agents, err := s.Agents.ListAll(ctx)
			if err != nil {
				continue
			}
			for _, a := range agents {
				if ctx.Err() != nil {
					return
				}
				call, cancel := context.WithTimeout(ctx, 20*time.Second)
				_ = s.Reconcile(call, a.ID)
				cancel()
			}
		}
	}
}

// ListAll returns all Agents (including disabled/deleted) for internal runtime consumption.
func (s *Service) ListAll(ctx context.Context) ([]domain.Agent, error) {
	return s.Agents.ListAll(ctx)
}

// GetByID returns a single Agent by ID for internal runtime consumption.
func (s *Service) GetByID(ctx context.Context, id string) (domain.Agent, bool, error) {
	return s.Agents.GetByID(ctx, id)
}

// ReportRuntime records a runtime health/activity report after validating state and session ID.
func (s *Service) ReportRuntime(ctx context.Context, r domain.RuntimeStatus) (bool, error) {
	if !domain.ValidRuntimeReport(r.AgentID, r.RuntimeState, r.SessionID) {
		return false, nil
	}
	return s.Agents.ReportRuntime(ctx, r.AgentID, r.RuntimeState, r.LastActiveAt, domain.SafeRuntimeError(r.LastError))
}

// GlobalEnabled reports whether the global agents.enabled feature flag is active.
func (s *Service) GlobalEnabled() bool {
	return s.flagEnabled("agents.enabled")
}

// Ping checks database readiness.
func (s *Service) Ping(ctx context.Context) error {
	return s.Tx.Ping(ctx)
}

// Ready checks both database and IM dependency readiness.
func (s *Service) Ready(ctx context.Context) error {
	if err := s.Tx.Ping(ctx); err != nil {
		return err
	}
	return s.IM.Ping(ctx)
}
