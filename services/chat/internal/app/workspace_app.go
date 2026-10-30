package app

import (
	"context"
	"slices"
	"strings"
	"time"

	"teamie/services/chat/internal/domain"
)

// ListWorkspaces lists workspaces where user is a current member.
func (s *Service) ListWorkspaces(ctx context.Context, userID string) ([]domain.Workspace, error) {
	return s.Workspaces.ListWorkspaces(ctx, userID)
}

// CreateWorkspace creates a workspace, owner membership, and default general channel atomically.
func (s *Service) CreateWorkspace(ctx context.Context, userID, name string) (domain.Workspace, domain.Conversation, error) {
	var ws domain.Workspace
	var c domain.Conversation
	name = strings.TrimSpace(name)
	if !domain.BoundedString(name, 1, 80) {
		return ws, c, domain.ErrInvalidWorkspaceName()
	}
	err := s.Tx.WithinTx(ctx, func(txCtx context.Context) error {
		ws = domain.Workspace{ID: domain.NewID(), Name: name, CreatedBy: userID}
		createdAt, err := s.Workspaces.InsertWorkspace(txCtx, ws)
		if err != nil {
			return err
		}
		ws.CreatedAt = createdAt
		if _, err = s.Workspaces.InsertWorkspaceMember(txCtx, ws.ID, userID, "owner", nil, false); err != nil {
			return err
		}
		c = domain.Conversation{ID: domain.NewID(), WorkspaceID: ws.ID, Type: "channel", Name: "general", CreatedBy: userID, MemberIDs: []string{userID}, NotificationLevel: "all"}
		updatedAt, err := s.Conversations.InsertConversation(txCtx, c, "")
		if err != nil {
			return err
		}
		c.UpdatedAt = updatedAt
		return s.Conversations.Fanout(txCtx, []string{userID}, domain.Event{
			Type:           "conversation.created",
			WorkspaceID:    ws.ID,
			ConversationID: c.ID,
			Conversation:   &c,
		})
	})
	return ws, c, err
}

// Members lists stored members of a workspace for an authorized member.
func (s *Service) Members(ctx context.Context, userID, workspaceID string) ([]domain.Member, error) {
	var out []domain.Member
	err := s.Tx.WithinTx(ctx, func(txCtx context.Context) error {
		if err := s.Workspaces.WorkspaceLock(txCtx, workspaceID, userID, false, false); err != nil {
			return err
		}
		var listErr error
		out, listErr = s.Workspaces.ListWorkspaceMembers(txCtx, workspaceID)
		return listErr
	})
	return out, err
}

// ResolvedMembers lists workspace members and resolves their display names and kinds via IdentityDirectory.
func (s *Service) ResolvedMembers(ctx context.Context, userID, workspaceID string) ([]domain.Member, error) {
	members, err := s.Members(ctx, userID, workspaceID)
	if err != nil {
		return nil, err
	}
	if s.Identity == nil {
		return members, nil
	}
	ids := make([]string, 0, len(members))
	for _, m := range members {
		ids = append(ids, m.UserID)
	}
	users, err := s.Identity.Users(ctx, ids)
	if err != nil {
		return nil, err
	}
	names := make(map[string]domain.User, len(users))
	for _, u := range users {
		names[u.ID] = u
	}
	for i := range members {
		if u, ok := names[members[i].UserID]; ok {
			members[i].DisplayName = u.DisplayName
			members[i].Kind = u.Kind
		}
	}
	return members, nil
}

// CreateInvite creates a workspace invitation for a workspace owner.
func (s *Service) CreateInvite(ctx context.Context, userID, workspaceID string) (string, time.Time, error) {
	var code string
	var expiry time.Time
	err := s.Tx.WithinTx(ctx, func(txCtx context.Context) error {
		if err := s.Workspaces.WorkspaceLock(txCtx, workspaceID, userID, true, false); err != nil {
			return err
		}
		code = domain.RandomHex(24)
		expiry = time.Now().UTC().Add(7 * 24 * time.Hour)
		return s.Workspaces.InsertWorkspaceInvite(txCtx, domain.HashString(code), workspaceID, userID, expiry)
	})
	return code, expiry, err
}

// Invite inspects a workspace invitation code.
func (s *Service) Invite(ctx context.Context, code string) (domain.Workspace, time.Time, error) {
	if len(code) != 48 {
		return domain.Workspace{}, time.Time{}, domain.ErrInviteUnavailable()
	}
	return s.Workspaces.FindValidInvite(ctx, domain.HashString(code))
}

// AcceptInvite admits a user into the invited workspace idempotently.
func (s *Service) AcceptInvite(ctx context.Context, userID, code string) (domain.Workspace, []domain.Conversation, error) {
	var ws domain.Workspace
	err := s.Tx.WithinTx(ctx, func(txCtx context.Context) error {
		var lockErr error
		ws, lockErr = s.Workspaces.LockValidInviteWorkspace(txCtx, domain.HashString(code))
		if lockErr != nil {
			return lockErr
		}
		ids, memberErr := s.Workspaces.WorkspaceMemberIDs(txCtx, ws.ID)
		if memberErr != nil {
			return memberErr
		}
		if !slices.Contains(ids, userID) && len(ids) >= domain.MaxWorkspaceMembers {
			return domain.ErrWorkspaceMemberLimit()
		}
		inserted, insertErr := s.Workspaces.InsertWorkspaceMember(txCtx, ws.ID, userID, "member", nil, true)
		if insertErr != nil {
			return insertErr
		}
		if inserted {
			ids, memberErr = s.Workspaces.WorkspaceMemberIDs(txCtx, ws.ID)
			if memberErr != nil {
				return memberErr
			}
			if fanoutErr := s.Conversations.Fanout(txCtx, ids, domain.Event{Type: "membership.changed", WorkspaceID: ws.ID}); fanoutErr != nil {
				return fanoutErr
			}
		}
		return nil
	})
	if err != nil {
		return ws, nil, err
	}
	convs, err := s.ListConversations(ctx, userID, ws.ID)
	return ws, convs, err
}

// RemoveMember removes a non-owner member (and any principals they granted) from a workspace.
func (s *Service) RemoveMember(ctx context.Context, userID, workspaceID, targetUserID string) error {
	return s.Tx.WithinTx(ctx, func(txCtx context.Context) error {
		if err := s.Workspaces.WorkspaceLock(txCtx, workspaceID, userID, true, true); err != nil {
			return err
		}
		role, found, err := s.Workspaces.GetWorkspaceMemberRole(txCtx, workspaceID, targetUserID)
		if err != nil {
			return err
		}
		if !found {
			return nil
		}
		if role == "owner" {
			return domain.ErrOwnerCannotBeRemoved()
		}
		ids, err := s.Workspaces.WorkspaceMemberIDs(txCtx, workspaceID)
		if err != nil {
			return err
		}
		removed, err := s.Workspaces.FindMemberAndDelegatedIDs(txCtx, workspaceID, targetUserID)
		if err != nil {
			return err
		}
		if err = s.Workspaces.DeleteConversationMembersInWorkspace(txCtx, workspaceID, removed); err != nil {
			return err
		}
		if err = s.Workspaces.DeleteWorkspaceMembers(txCtx, workspaceID, removed); err != nil {
			return err
		}
		return s.Conversations.Fanout(txCtx, ids, domain.Event{Type: "membership.changed", WorkspaceID: workspaceID})
	})
}

// ParticipantWorkspaces lists workspace IDs where user is a member.
func (s *Service) ParticipantWorkspaces(ctx context.Context, userID string) ([]string, error) {
	return s.Memberships.ParticipantWorkspaces(ctx, userID)
}

// WorkspaceParticipants lists member IDs of a workspace for an authorized requester.
func (s *Service) WorkspaceParticipants(ctx context.Context, workspaceID, requesterID string) ([]string, error) {
	var out []string
	err := s.Tx.WithinTx(ctx, func(txCtx context.Context) error {
		if err := s.Workspaces.WorkspaceLock(txCtx, workspaceID, requesterID, false, false); err != nil {
			return err
		}
		var memberErr error
		out, memberErr = s.Workspaces.WorkspaceMemberIDs(txCtx, workspaceID)
		return memberErr
	})
	return out, err
}

// SharedWorkspaceMembers lists distinct member IDs sharing any workspace with userID.
func (s *Service) SharedWorkspaceMembers(ctx context.Context, userID string) ([]string, error) {
	return s.Workspaces.SharedWorkspaceMemberIDs(ctx, userID)
}

// WorkspaceRole returns the workspace owner ID and the caller's role if userID is a current member.
func (s *Service) WorkspaceRole(ctx context.Context, workspaceID, userID string) (string, string, bool, error) {
	return s.Workspaces.GetWorkspaceRoleAndOwner(ctx, workspaceID, userID)
}

// SetPrincipalWorkspaces reconciles delegated workspace memberships for an agent principal.
func (s *Service) SetPrincipalWorkspaces(ctx context.Context, principal, grantor domain.User, requested []string, revision int64) ([]string, error) {
	if principal.Kind != "agent" || grantor.Kind != "human" || principal.ID == grantor.ID || revision < 1 || len(requested) > 100 {
		return nil, domain.ErrInvalidDelegatedMembership()
	}
	wanted := domain.NormalizedIDs(requested)
	var result []string
	err := s.Tx.WithinTx(ctx, func(txCtx context.Context) error {
		if err := s.Tx.LockPrincipalMembership(txCtx, principal.ID); err != nil {
			return err
		}
		oldRevision, err := s.Memberships.EnsureAndLockPrincipalRevision(txCtx, principal.ID)
		if err != nil {
			return err
		}
		if revision <= oldRevision {
			result, err = s.Memberships.ParticipantWorkspaces(txCtx, principal.ID)
			return err
		}
		old, err := s.Memberships.ParticipantWorkspaces(txCtx, principal.ID)
		if err != nil {
			return err
		}
		union := domain.NormalizedIDs(append(append([]string{}, old...), wanted...))
		selected := map[string]bool{}
		for _, id := range wanted {
			selected[id] = true
		}
		for _, id := range union {
			if err = s.Workspaces.LockWorkspaceByIDForUpdate(txCtx, id); err != nil {
				return err
			}
		}
		for _, id := range wanted {
			yes, existsErr := s.Workspaces.WorkspaceMemberExists(txCtx, id, grantor.ID)
			if existsErr != nil {
				return existsErr
			}
			if !yes {
				return domain.ErrGrantorMembershipRequired()
			}
		}
		oldSet := map[string]bool{}
		for _, id := range old {
			oldSet[id] = true
		}
		allHeads := []string{principal.ID}
		for _, id := range union {
			audience, memberErr := s.Workspaces.WorkspaceMemberIDs(txCtx, id)
			if memberErr != nil {
				return memberErr
			}
			if selected[id] && !oldSet[id] && len(audience) >= domain.MaxWorkspaceMembers {
				return domain.ErrWorkspaceMemberLimit()
			}
			allHeads = append(allHeads, audience...)
		}
		if err = s.Conversations.EnsureAndLockSyncHeads(txCtx, domain.NormalizedIDs(allHeads)); err != nil {
			return err
		}
		for _, id := range union {
			if selected[id] == oldSet[id] {
				continue
			}
			ids, memberErr := s.Workspaces.WorkspaceMemberIDs(txCtx, id)
			if memberErr != nil {
				return memberErr
			}
			if selected[id] {
				grantorID := grantor.ID
				_, err = s.Workspaces.InsertWorkspaceMember(txCtx, id, principal.ID, "member", &grantorID, false)
				ids = append(ids, principal.ID)
			} else {
				if err = s.Workspaces.DeleteConversationMembersInWorkspace(txCtx, id, []string{principal.ID}); err != nil {
					return err
				}
				err = s.Workspaces.DeleteWorkspaceMembers(txCtx, id, []string{principal.ID})
			}
			if err != nil {
				return err
			}
			if err = s.Conversations.Fanout(txCtx, ids, domain.Event{Type: "membership.changed", WorkspaceID: id}); err != nil {
				return err
			}
		}
		if err = s.Memberships.UpdatePrincipalRevision(txCtx, principal.ID, revision); err != nil {
			return err
		}
		result = wanted
		return nil
	})
	return result, err
}
