package control

import (
	"context"
	"time"
)

func intersect(selected []string, sets ...[]string) []string {
	out := normalized(selected)
	for _, set := range sets {
		have := map[string]bool{}
		for _, id := range set {
			have[id] = true
		}
		next := []string{}
		for _, id := range out {
			if have[id] {
				next = append(next, id)
			}
		}
		out = next
	}
	return out
}

// Reconcile records the desired revision before making idempotent RPC changes.
// It disables the identity first, applies ordinary grants, then enables only the
// fully applied revision. A process crash resumes the same fenced operation.
func (s *Store) Reconcile(ctx context.Context, id string) error {
	for attempt := 0; attempt < 4; attempt++ {
		tx, e := s.DB.Begin(ctx)
		if e != nil {
			return e
		}
		a, e := scan(tx.QueryRow(ctx, "SELECT "+columns+" FROM agents WHERE id=$1 FOR UPDATE", id))
		if e != nil {
			_ = tx.Rollback(ctx)
			return e
		}
		failed := func(err error) error {
			_ = tx.Rollback(ctx)
			_, _ = s.DB.Exec(ctx, "UPDATE agents SET runtime_state=CASE WHEN enabled AND policy_enabled AND deleted_at IS NULL THEN 'error' ELSE 'disabled' END,last_error='im_unavailable' WHERE id=$1", id)
			return err
		}
		policy := s.Flags.Enabled("agents.enabled")
		if a.PolicyEnabled != policy {
			_, e = tx.Exec(ctx, "UPDATE agents SET policy_enabled=$2,revision=revision+1,updated_at=now() WHERE id=$1", id, policy)
			if e != nil {
				return failed(e)
			}
			if e = tx.Commit(ctx); e != nil {
				return e
			}
			continue
		}
		ownerWorkspaces, e := s.IM.Workspaces(ctx, a.OwnerID)
		if e != nil {
			return failed(e)
		}
		selected := intersect(a.WorkspaceIDs, ownerWorkspaces)
		if a.AppliedMembershipRevision == a.MembershipRevision {
			// Actual membership wins after a moderator removal. Updating a profile or
			// restarting the control plane cannot recreate that revoked grant.
			actual, e := s.IM.Workspaces(ctx, a.ID)
			if e != nil {
				return failed(e)
			}
			selected = intersect(selected, actual)
		}
		if !same(selected, a.WorkspaceIDs) {
			_, e = tx.Exec(ctx, "UPDATE agents SET workspace_ids=$2,revision=revision+1,membership_revision=membership_revision+1,updated_at=now() WHERE id=$1", id, selected)
			if e != nil {
				return failed(e)
			}
			if e = tx.Commit(ctx); e != nil {
				return e
			}
			continue
		}
		if a.AppliedRevision == a.Revision {
			e = tx.Commit(ctx)
			return e
		}
		deleted := a.DeletedAt != nil
		if e = s.IM.Provision(ctx, a, 2*a.Revision, false, deleted); e != nil {
			return failed(e)
		}
		actual, e := s.IM.Memberships(ctx, a, selected)
		if e != nil {
			return failed(e)
		}
		if !same(actual, selected) {
			_, e = tx.Exec(ctx, "UPDATE agents SET workspace_ids=$2,revision=revision+1,membership_revision=membership_revision+1,updated_at=now() WHERE id=$1", id, intersect(selected, actual))
			if e != nil {
				return failed(e)
			}
			if e = tx.Commit(ctx); e != nil {
				return e
			}
			continue
		}
		enabled := a.Enabled && policy && !deleted
		if e = s.IM.Provision(ctx, a, 2*a.Revision+1, enabled, deleted); e != nil {
			return failed(e)
		}
		_, e = tx.Exec(ctx, "UPDATE agents SET applied_revision=revision,applied_membership_revision=membership_revision,runtime_state=CASE WHEN $2 THEN CASE WHEN runtime_state IN ('provisioning','disabled','error') THEN 'idle' ELSE runtime_state END ELSE 'disabled' END,last_error='' WHERE id=$1", id, enabled)
		if e != nil {
			return failed(e)
		}
		return tx.Commit(ctx)
	}
	return errAPI(503, "control_unavailable", "Agent permissions are still changing")
}
func (s *Store) Run(ctx context.Context) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			agents, e := s.list(ctx, "")
			if e != nil {
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
