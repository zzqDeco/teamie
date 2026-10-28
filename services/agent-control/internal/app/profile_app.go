package app

import (
	"context"
	"slices"
	"strings"

	"teamie/services/agent-control/internal/domain"
)

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
	key := strings.TrimSpace(in.IdempotencyKey)
	if key == "" || len(key) > 128 {
		return domain.Agent{}, domain.ErrInvalidRequest("Idempotency-Key is required and must contain at most 128 bytes")
	}
	enabled := true
	if in.Enabled != nil {
		enabled = *in.Enabled
	}
	description := ""
	if in.Description != nil {
		description = *in.Description
	}
	modelID := ""
	if in.ModelID != nil {
		modelID = *in.ModelID
	}
	policyEnabled := s.flagEnabled("agents.enabled")

	intent := domain.Agent{
		ID: domain.DeterministicID(owner, key), OwnerID: owner,
		Name: *in.Name, Description: description, ModelID: modelID,
		WorkspaceIDs: *in.WorkspaceIDs, Enabled: enabled, PolicyEnabled: policyEnabled,
	}
	intent.CreationHash = domain.CreationHash(intent)
	var a domain.Agent
	if err := s.Tx.WithinTx(ctx, func(txCtx context.Context) error {
		var err error
		a, err = s.Agents.InsertOrGetCreation(txCtx, intent)
		if err != nil {
			return err
		}
		if a.OwnerID != owner || a.CreationHash != intent.CreationHash {
			return domain.ErrIdempotencyConflict()
		}
		if a.DeletedAt != nil {
			return domain.ErrNotFound("Agent is unavailable")
		}
		return nil
	}); err != nil {
		return domain.Agent{}, err
	}

	reconcileErr := s.Reconcile(ctx, a.ID)
	current, err := s.Owned(ctx, owner, a.ID)
	if err == nil {
		a = current
	}
	if reconcileErr != nil || a.AppliedRevision != a.Revision {
		return a, domain.ErrControlUnavailable("Creation is pending while IM authority is unavailable", reconcileErr)
	}
	if err != nil {
		return a, err
	}
	return a, nil
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
	if in.Name == nil && in.Description == nil && in.ModelID == nil && in.WorkspaceIDs == nil && in.Enabled == nil {
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
		changed := false
		if in.Name != nil && a.Name != *in.Name {
			a.Name = *in.Name
			changed = true
		}
		if in.Description != nil && a.Description != *in.Description {
			a.Description = *in.Description
			changed = true
		}
		if in.ModelID != nil && a.ModelID != *in.ModelID {
			a.ModelID = *in.ModelID
			changed = true
		}
		if in.WorkspaceIDs != nil && !slices.Equal(domain.Normalized(a.WorkspaceIDs), *in.WorkspaceIDs) {
			a.WorkspaceIDs = *in.WorkspaceIDs
			a.MembershipRevision++
			changed = true
		}
		if in.Enabled != nil && a.Enabled != *in.Enabled {
			a.Enabled = *in.Enabled
			changed = true
		}
		updated = a
		if !changed && a.AppliedRevision < a.Revision {
			return nil
		}
		return s.Agents.UpdateProfile(txCtx, a)
	}); err != nil {
		return updated, err
	}
	reconcileErr := s.Reconcile(ctx, id)
	current, err := s.Owned(ctx, owner, id)
	if err == nil {
		updated = current
	}
	if reconcileErr != nil || updated.AppliedRevision != updated.Revision {
		if in.Enabled != nil && !*in.Enabled {
			return updated, domain.ErrControlUnavailable("Disable is pending while IM authority is unavailable", reconcileErr)
		}
		return updated, domain.ErrControlUnavailable("Update is pending while IM authority is unavailable", reconcileErr)
	}
	if err != nil {
		return updated, err
	}
	return updated, nil
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
		return domain.ErrControlUnavailable("Deletion is pending while IM authority is unavailable", err)
	}
	return nil
}

// ListOwned returns all undeleted Agents owned by owner.
func (s *Service) ListOwned(ctx context.Context, owner string) ([]domain.Agent, error) {
	return s.Agents.ListOwned(ctx, owner)
}
