package app

import (
	"context"
	"time"

	"teamie/services/agent-control/internal/domain"
)

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

// ReportRuntime records a runtime health/activity report after validating state and session ID.
func (s *Service) ReportRuntime(ctx context.Context, r domain.RuntimeStatus) (bool, error) {
	if !domain.ValidRuntimeReport(r.AgentID, r.RuntimeState, r.SessionID) {
		return false, nil
	}
	return s.Agents.ReportRuntime(ctx, r.AgentID, r.RuntimeState, r.LastActiveAt, domain.SafeRuntimeError(r.LastError))
}
