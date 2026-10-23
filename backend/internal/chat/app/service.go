// Package app orchestrates Chat use cases and transaction boundaries against
// pure domain models and outbound repository ports.
package app

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"strings"
	"time"

	"teamie/backend/internal/chat/domain"
)

// Service coordinates all Chat application use cases.
type Service struct {
	Tx             domain.TxManager
	Workspaces     domain.WorkspaceRepository
	Conversations  domain.ConversationRepository
	Memberships    domain.MembershipRepository
	Messages       domain.MessageRepository
	Attachments    domain.AttachmentRepository
	Outbox         domain.OutboxRepository
	Identity       domain.IdentityDirectory
	Flags          domain.FeatureFlags
	SniffMediaType func([]byte) string
	ControlToken   string
}

func (s *Service) requireFlag(key string) error {
	if s.Flags == nil || !s.Flags.Enabled(key) {
		return domain.ErrFeatureDisabled()
	}
	return nil
}

// Ping checks whether the Chat store is ready.
func (s *Service) Ping(ctx context.Context) error {
	return s.Tx.Ping(ctx)
}

// CheckDependencies checks both the Chat store and Identity RPC dependency.
func (s *Service) CheckDependencies(ctx context.Context) error {
	if err := s.Tx.Ping(ctx); err != nil {
		return err
	}
	if s.Identity != nil {
		return s.Identity.Ping(ctx)
	}
	return nil
}

// ListWorkspaces lists workspaces where user is a current member.
func (s *Service) ListWorkspaces(ctx context.Context, userID string) ([]domain.Workspace, error) {
	return s.Workspaces.ListWorkspaces(ctx, userID)
}

// ListConversations lists authorized conversations for user (optionally scoped to a workspace).
func (s *Service) ListConversations(ctx context.Context, userID, workspaceID string) ([]domain.Conversation, error) {
	if workspaceID != "" {
		yes, err := s.Workspaces.WorkspaceMemberExists(ctx, workspaceID, userID)
		if err != nil {
			return nil, err
		}
		if !yes {
			return nil, domain.ErrWorkspaceUnavailable()
		}
	}
	ids, err := s.Conversations.ListAuthorizedConversationIDs(ctx, userID, workspaceID)
	if err != nil {
		return nil, err
	}
	out := []domain.Conversation{}
	for _, id := range ids {
		c, accessErr := s.Conversations.ConversationAccess(ctx, id, userID)
		if accessErr != nil {
			var de *domain.Error
			if errors.As(accessErr, &de) && de.Status == 404 {
				continue
			}
			return nil, accessErr
		}
		out = append(out, c)
	}
	return out, nil
}

// ConversationAccess checks and returns user's access to a conversation.
func (s *Service) ConversationAccess(ctx context.Context, conversationID, userID string) (domain.Conversation, error) {
	return s.Conversations.ConversationAccess(ctx, conversationID, userID)
}

// Head returns the user's current sync stream head cursor.
func (s *Service) Head(ctx context.Context, userID string) (string, error) {
	n, err := s.Conversations.SyncHead(ctx, userID)
	return strconv.FormatInt(n, 10), err
}

// Sync returns a bounded page of durable sync events for user after cursor.
func (s *Service) Sync(ctx context.Context, userID, cursor string, limit int) ([]domain.Event, string, bool, error) {
	if cursor == "" {
		cursor = "0"
	}
	n, err := strconv.ParseInt(cursor, 10, 64)
	if err != nil || n < 0 {
		return nil, "", false, domain.ErrInvalidSyncCursor()
	}
	h, err := s.Conversations.SyncHead(ctx, userID)
	if err != nil {
		return nil, "", false, err
	}
	if n > h {
		return nil, "", false, domain.ErrFutureSyncCursor()
	}
	events, next, err := s.Conversations.QuerySyncEvents(ctx, userID, n, h, limit)
	if err != nil {
		return nil, "", false, err
	}
	return events, strconv.FormatInt(next, 10), next < h, nil
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
		c = domain.Conversation{ID: domain.NewID(), WorkspaceID: ws.ID, Type: "channel", Name: "general", CreatedBy: userID, MemberIDs: []string{userID}}
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
		inserted, insertErr := s.Workspaces.InsertWorkspaceMember(txCtx, ws.ID, userID, "member", nil, true)
		if insertErr != nil {
			return insertErr
		}
		if inserted {
			ids, memberErr := s.Workspaces.WorkspaceMemberIDs(txCtx, ws.ID)
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

// CreateConversation creates or reopens a channel, direct chat, or group conversation.
func (s *Service) CreateConversation(ctx context.Context, userID, workspaceID, kind, name string, members []string) (domain.Conversation, error) {
	var c domain.Conversation
	flag := "im." + kind + ".create"
	if kind == "direct" {
		flag = "im.dm.create"
	}
	if kind != "channel" && kind != "direct" && kind != "group" {
		return c, domain.ErrInvalidConversationType()
	}
	if err := s.requireFlag(flag); err != nil {
		return c, err
	}
	name = strings.TrimSpace(name)
	if len([]rune(name)) > 80 || (kind == "channel" && name == "") {
		return c, domain.ErrInvalidConversationName()
	}
	err := s.Tx.WithinTx(ctx, func(txCtx context.Context) error {
		if err := s.Workspaces.WorkspaceLock(txCtx, workspaceID, userID, kind == "channel", true); err != nil {
			return err
		}
		ids := []string{}
		pair := ""
		if kind != "channel" {
			unique := map[string]bool{userID: true}
			for _, id := range members {
				if id != "" {
					unique[id] = true
				}
			}
			for id := range unique {
				ids = append(ids, id)
			}
			if (kind == "direct" && len(ids) != 2) || (kind == "group" && (len(ids) < 2 || len(ids) > 50)) {
				return domain.ErrInvalidMembersCount()
			}
			valid, err := s.Workspaces.WorkspaceMemberIDs(txCtx, workspaceID)
			if err != nil {
				return err
			}
			set := map[string]bool{}
			for _, id := range valid {
				set[id] = true
			}
			for _, id := range ids {
				if !set[id] {
					return domain.ErrInvalidWorkspaceMembers()
				}
			}
			if kind == "direct" {
				if ids[0] > ids[1] {
					ids[0], ids[1] = ids[1], ids[0]
				}
				pair = ids[0] + ":" + ids[1]
				existing, found, err := s.Conversations.FindDirectConversationByPairKey(txCtx, workspaceID, pair)
				if err != nil {
					return err
				}
				if found {
					restored := false
					for _, id := range ids {
						changed, insertErr := s.Conversations.InsertConversationMember(txCtx, existing, id, true)
						if insertErr != nil {
							return insertErr
						}
						restored = restored || changed
					}
					c, err = s.Conversations.ConversationAccess(txCtx, existing, userID)
					if err != nil {
						return err
					}
					if restored {
						if err = s.Conversations.Fanout(txCtx, c.MemberIDs, domain.Event{
							Type:           "conversation.created",
							WorkspaceID:    workspaceID,
							ConversationID: c.ID,
							Conversation:   &c,
						}); err != nil {
							return err
						}
					}
					return nil
				}
			}
		}
		c = domain.Conversation{
			ID:               domain.NewID(),
			WorkspaceID:      workspaceID,
			Type:             kind,
			Name:             name,
			MemberIDs:        ids,
			CreatedBy:        userID,
			CanManageMembers: kind == "group",
		}
		updatedAt, err := s.Conversations.InsertConversation(txCtx, c, pair)
		if err != nil {
			return err
		}
		c.UpdatedAt = updatedAt
		for _, id := range ids {
			if _, err = s.Conversations.InsertConversationMember(txCtx, c.ID, id, false); err != nil {
				return err
			}
		}
		if kind == "channel" {
			ids, err = s.Workspaces.WorkspaceMemberIDs(txCtx, workspaceID)
			if err != nil {
				return err
			}
			c.MemberIDs = ids
		}
		return s.Conversations.Fanout(txCtx, ids, domain.Event{
			Type:           "conversation.created",
			WorkspaceID:    workspaceID,
			ConversationID: c.ID,
			Conversation:   &c,
		})
	})
	return c, err
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

// GroupMember adds or removes a member from a group conversation.
func (s *Service) GroupMember(ctx context.Context, actorID, conversationID, targetUserID string, add bool) (domain.Conversation, error) {
	var c domain.Conversation
	if add {
		if err := s.requireFlag("im.group.create"); err != nil {
			return c, err
		}
	}
	err := s.Tx.WithinTx(ctx, func(txCtx context.Context) error {
		ws, err := s.Conversations.GetConversationWorkspaceID(txCtx, conversationID)
		if err != nil {
			return err
		}
		if err = s.Workspaces.WorkspaceLock(txCtx, ws, actorID, false, false); err != nil {
			return err
		}
		var role string
		c, role, err = s.Conversations.LockConversationWithActorRole(txCtx, conversationID, actorID)
		if err != nil {
			return err
		}
		if c.Type != "group" {
			return domain.ErrOnlyGroupMembershipChanged()
		}
		if role != "owner" && c.CreatedBy != actorID {
			return domain.ErrGroupManageForbidden()
		}
		ids, err := s.Conversations.ConversationRecipients(txCtx, c)
		if err != nil {
			return err
		}
		if role != "owner" && !slices.Contains(ids, actorID) {
			return domain.ErrGroupCurrentMembershipRequired()
		}
		if add {
			yes, existsErr := s.Workspaces.WorkspaceMemberExists(txCtx, ws, targetUserID)
			if existsErr != nil {
				return existsErr
			}
			if !yes {
				return domain.ErrInvalidGroupParticipant()
			}
			members, present, countErr := s.Conversations.CountConversationMembersAndCheckTarget(txCtx, conversationID, targetUserID)
			if countErr != nil {
				return countErr
			}
			if present {
				c.MemberIDs, c.CanManageMembers = ids, true
				return nil
			}
			if members >= 50 {
				return domain.ErrGroupMemberLimit()
			}
			_, err = s.Conversations.InsertConversationMember(txCtx, conversationID, targetUserID, true)
			ids = append(ids, targetUserID)
		} else {
			err = s.Conversations.DeleteConversationMember(txCtx, conversationID, targetUserID)
		}
		if err != nil {
			return err
		}
		c.MemberIDs, err = s.Conversations.ConversationRecipients(txCtx, c)
		if err != nil {
			return err
		}
		c.CanManageMembers = role == "owner" || c.CreatedBy == actorID
		return s.Conversations.Fanout(txCtx, ids, domain.Event{
			Type:           "membership.changed",
			WorkspaceID:    ws,
			ConversationID: conversationID,
		})
	})
	return c, err
}

// Message returns a single message in an authorized conversation.
func (s *Service) Message(ctx context.Context, userID, conversationID, messageID string) (domain.Message, error) {
	var m domain.Message
	err := s.Tx.WithinTx(ctx, func(txCtx context.Context) error {
		ws, err := s.Conversations.GetConversationWorkspaceID(txCtx, conversationID)
		if err != nil {
			return err
		}
		if err = s.Workspaces.WorkspaceLock(txCtx, ws, userID, false, false); err != nil {
			return err
		}
		if _, err = s.Conversations.ConversationAccess(txCtx, conversationID, userID); err != nil {
			return err
		}
		m, err = s.Messages.GetMessage(txCtx, conversationID, messageID)
		return err
	})
	return m, err
}

// MessageHistory searches or pages conversation messages.
func (s *Service) MessageHistory(ctx context.Context, userID, conversationID string, before, around int64, query string, limit int) ([]domain.Message, bool, error) {
	return s.messageHistory(ctx, userID, conversationID, before, around, query, limit, false)
}

// FileHistory lists only published files in a conversation, matched by literal filename.
func (s *Service) FileHistory(ctx context.Context, userID, conversationID string, before int64, query string, limit int) ([]domain.Message, bool, error) {
	return s.messageHistory(ctx, userID, conversationID, before, 0, query, limit, true)
}

func (s *Service) messageHistory(ctx context.Context, userID, conversationID string, before, around int64, query string, limit int, filesOnly bool) ([]domain.Message, bool, error) {
	trimmed, err := domain.ValidateHistoryQuery(before, around, query, limit)
	if err != nil {
		return nil, false, err
	}
	var out []domain.Message
	var more bool
	err = s.Tx.WithinTx(ctx, func(txCtx context.Context) error {
		ws, err := s.Conversations.GetConversationWorkspaceID(txCtx, conversationID)
		if err != nil {
			return err
		}
		if err = s.Workspaces.WorkspaceLock(txCtx, ws, userID, false, false); err != nil {
			return err
		}
		if _, err = s.Conversations.ConversationAccess(txCtx, conversationID, userID); err != nil {
			return err
		}
		out, more, err = s.Messages.QueryMessageHistory(txCtx, conversationID, before, around, trimmed, limit, filesOnly)
		return err
	})
	return out, more, err
}

// SendAnnotated sends or idempotently replays a message with optional attachments and mentions.
func (s *Service) SendAnnotated(ctx context.Context, user domain.User, conversationID, clientID, body string, reply *string, ids []string, mentions []domain.Mention) (domain.Message, bool, error) {
	var m domain.Message
	if err := domain.ValidateMentions(body, mentions); err != nil {
		return m, false, err
	}
	if mentions == nil {
		mentions = []domain.Mention{}
	}
	if err := domain.ValidateAttachmentIDs(ids); err != nil {
		return m, false, err
	}
	if ids == nil {
		ids = []string{}
	}
	if !domain.BoundedString(clientID, 1, 128) {
		return m, false, domain.ErrInvalidClientMessageID()
	}
	if reply != nil && *reply == "" {
		reply = nil
	}
	created := false
	err := s.Tx.WithinTx(ctx, func(txCtx context.Context) error {
		if err := s.Tx.LockSenderClientMessage(txCtx, user.ID, clientID); err != nil {
			return err
		}
		c, err := s.Conversations.LockConversation(txCtx, conversationID, user.ID)
		if err != nil {
			return err
		}
		existing, hash, originalIDs, originalMentions, found, err := s.Messages.FindMessageBySenderClientID(txCtx, user.ID, clientID)
		if err != nil {
			return err
		}
		if found {
			m = existing
			sameReply := (reply == nil && m.ReplyToID == nil) || (reply != nil && m.ReplyToID != nil && *reply == *m.ReplyToID)
			if m.ConversationID != conversationID || hash != domain.HashString(body) || !sameReply || !slices.Equal(ids, originalIDs) || !slices.Equal(mentions, originalMentions) {
				return domain.ErrIdempotencyConflict()
			}
			return nil
		}
		if err = domain.ValidateMessage(body, len(ids)); err != nil {
			return err
		}
		if len(mentions) > 0 {
			if err = s.requireFlag("im.message.mentions"); err != nil {
				return err
			}
			if err = domain.ValidateMentionMembers(c.MemberIDs, mentions); err != nil {
				return err
			}
		}
		if len(ids) > 0 {
			if err = s.requireFlag("im.attachments.upload"); err != nil {
				return err
			}
		}
		if reply != nil {
			exists, existsErr := s.Messages.MessageExistsInConversation(txCtx, *reply, conversationID)
			if existsErr != nil {
				return existsErr
			}
			if !exists {
				return domain.ErrInvalidReplyTarget()
			}
		}
		m = domain.Message{
			ID:              domain.NewID(),
			ConversationID:  conversationID,
			SenderID:        user.ID,
			SenderName:      user.DisplayName,
			SenderKind:      user.Kind,
			ClientMessageID: clientID,
			Body:            body,
			ReplyToID:       reply,
		}
		seq, err := s.Conversations.IncrementConversationSeq(txCtx, conversationID)
		if err != nil {
			return err
		}
		m.Seq = seq
		createdAt, err := s.Messages.InsertMessage(txCtx, m, domain.HashString(body))
		if err != nil {
			return err
		}
		m.CreatedAt = createdAt
		if len(mentions) > 0 {
			m.Mentions = mentions
			if err = s.Messages.SetMessageInitialMentions(txCtx, m.ID, mentions); err != nil {
				return err
			}
		}
		if len(ids) > 0 {
			m.Attachments, err = s.Attachments.AttachFilesToMessage(txCtx, user.ID, conversationID, m.ID, ids)
			if err != nil {
				return err
			}
			if err = s.Messages.SetMessageInitialAttachments(txCtx, m.ID, m.Attachments, ids); err != nil {
				return err
			}
		}
		if err = s.Conversations.Fanout(txCtx, c.MemberIDs, domain.Event{
			Type:           "message.created",
			WorkspaceID:    c.WorkspaceID,
			ConversationID: conversationID,
			Message:        &m,
		}); err != nil {
			return err
		}
		created = true
		return nil
	})
	return m, created, err
}

// ChangeAnnotatedMessage edits or retracts an existing message.
func (s *Service) ChangeAnnotatedMessage(ctx context.Context, userID, conversationID, messageID, body string, retract bool, mentions []domain.Mention) (domain.Message, error) {
	var m domain.Message
	flag := "im.message.edit"
	if retract {
		flag = "im.message.retract"
	}
	if err := s.requireFlag(flag); err != nil {
		return m, err
	}
	err := s.Tx.WithinTx(ctx, func(txCtx context.Context) error {
		c, err := s.Conversations.LockConversation(txCtx, conversationID, userID)
		if err != nil {
			return err
		}
		m, err = s.Messages.GetMessage(txCtx, conversationID, messageID)
		if err != nil {
			return err
		}
		if m.SenderID != userID {
			return domain.ErrAuthorOnlyMessageChange()
		}
		if !retract {
			if err = domain.ValidateMessage(body, len(m.Attachments)); err != nil {
				return err
			}
			if err = domain.ValidateMentions(body, mentions); err != nil {
				return err
			}
			if len(mentions) > 0 {
				if !slices.Equal(mentions, m.Mentions) {
					if err = s.requireFlag("im.message.mentions"); err != nil {
						return err
					}
				}
				if err = domain.ValidateMentionMembers(c.MemberIDs, mentions); err != nil {
					return err
				}
			}
		}
		event := "message.updated"
		now := time.Now().UTC()
		if retract {
			if m.RetractedAt != nil {
				return nil
			}
			m.RetractedAt = &now
			m.Body = ""
			m.Attachments = nil
			m.Mentions = nil
			event = "message.retracted"
			if err = s.Messages.RetractMessageAndDeleteAttachments(txCtx, messageID, now); err != nil {
				return err
			}
		} else {
			if m.RetractedAt != nil {
				return domain.ErrMessageRetracted()
			}
			m.Body = body
			m.Mentions = mentions
			if mentions == nil {
				mentions = []domain.Mention{}
			}
			m.EditedAt = &now
			if err = s.Messages.UpdateMessageBodyAndMentions(txCtx, messageID, body, now, mentions); err != nil {
				return err
			}
		}
		return s.Conversations.Fanout(txCtx, c.MemberIDs, domain.Event{
			Type:           event,
			WorkspaceID:    c.WorkspaceID,
			ConversationID: conversationID,
			Message:        &m,
		})
	})
	return m, err
}

// Read advances the caller's read cursor monotonically.
func (s *Service) Read(ctx context.Context, userID, conversationID string, seq int64) (int64, error) {
	if seq < 0 {
		return 0, domain.ErrInvalidReadSeq()
	}
	var result int64
	err := s.Tx.WithinTx(ctx, func(txCtx context.Context) error {
		c, err := s.Conversations.LockConversation(txCtx, conversationID, userID)
		if err != nil {
			return err
		}
		if seq > c.LastSeq {
			seq = c.LastSeq
		}
		if seq <= c.ReadSeq {
			result = c.ReadSeq
			return nil
		}
		if err = s.Conversations.UpsertReadCursor(txCtx, conversationID, userID, seq); err != nil {
			return err
		}
		if err = s.Conversations.Fanout(txCtx, []string{userID}, domain.Event{
			Type:           "read.updated",
			WorkspaceID:    c.WorkspaceID,
			ConversationID: conversationID,
			ReadSeq:        &seq,
		}); err != nil {
			return err
		}
		result = seq
		return nil
	})
	return result, err
}

// UploadAttachmentWithKey uploads or idempotently replays a file attachment.
func (s *Service) UploadAttachmentWithKey(ctx context.Context, userID, conversationID, filename string, content []byte, key string) (domain.Attachment, bool, error) {
	var a domain.Attachment
	if key != "" && !domain.ValidUploadKey(key) {
		return a, false, domain.ErrInvalidUploadKeyFormat()
	}
	if err := domain.ValidateFilename(filename); err != nil {
		return a, false, err
	}
	if len(content) == 0 || len(content) > domain.AttachmentBytes {
		return a, false, domain.ErrAttachmentTooLarge()
	}
	first := false
	err := s.Tx.WithinTx(ctx, func(txCtx context.Context) error {
		if err := s.Tx.LockAttachmentSender(txCtx, userID); err != nil {
			return err
		}
		if _, err := s.Conversations.LockConversation(txCtx, conversationID, userID); err != nil {
			return err
		}
		contentHash := domain.SHA256Hex(content)
		fingerprint := domain.UploadFingerprint(conversationID, filename, contentHash)
		if key != "" {
			old, found, lookupErr := s.Attachments.FindAttachmentUpload(txCtx, userID, key)
			if lookupErr != nil {
				return lookupErr
			}
			if found {
				if old.Conversation != conversationID || old.Fingerprint != fingerprint {
					return domain.ErrUploadConflict()
				}
				if !old.Available {
					return domain.ErrAttachmentUploadUnavailable(true)
				}
				a = old.Attachment
				return nil
			}
		}
		if err := s.requireFlag("im.attachments.upload"); err != nil {
			return err
		}
		if err := s.Attachments.DeleteExpiredPendingForSender(txCtx, userID); err != nil {
			return err
		}
		pending, err := s.Attachments.CountPendingForSender(txCtx, userID)
		if err != nil {
			return err
		}
		if pending >= domain.PendingAttachmentsPerSender {
			return domain.ErrAttachmentCapacity()
		}
		mediaType := "application/octet-stream"
		if s.SniffMediaType != nil {
			mediaType = s.SniffMediaType(content)
		}
		a = domain.Attachment{
			ID:        domain.NewID(),
			Filename:  filename,
			MediaType: mediaType,
			SizeBytes: len(content),
			SHA256:    contentHash,
		}
		if err = s.Attachments.InsertAttachment(txCtx, a, conversationID, userID, content); err != nil {
			return err
		}
		if key != "" {
			if err = s.Attachments.InsertAttachmentUpload(txCtx, userID, key, conversationID, fingerprint, a.ID); err != nil {
				return err
			}
		}
		first = true
		return nil
	})
	return a, first, err
}

// LookupAttachmentUpload recovers a caller's own successful upload descriptor by key.
func (s *Service) LookupAttachmentUpload(ctx context.Context, userID, conversationID, key string) (domain.Attachment, error) {
	if !domain.ValidUploadKey(key) {
		return domain.Attachment{}, domain.ErrInvalidUploadKey()
	}
	var out domain.Attachment
	err := s.Tx.WithinTx(ctx, func(txCtx context.Context) error {
		if _, err := s.Conversations.LockConversation(txCtx, conversationID, userID); err != nil {
			return err
		}
		old, found, err := s.Attachments.FindAttachmentUpload(txCtx, userID, key)
		if err != nil {
			return err
		}
		if !found || old.Conversation != conversationID {
			return domain.ErrAttachmentUploadNotFound()
		}
		if !old.Available {
			return domain.ErrAttachmentUploadUnavailable(false)
		}
		out = old.Attachment
		return nil
	})
	return out, err
}

// DeletePendingAttachment removes the caller's unlinked pending attachment.
func (s *Service) DeletePendingAttachment(ctx context.Context, userID, conversationID, attachmentID string) error {
	return s.Tx.WithinTx(ctx, func(txCtx context.Context) error {
		if _, err := s.Conversations.LockConversation(txCtx, conversationID, userID); err != nil {
			return err
		}
		owner, message, found, err := s.Attachments.LockAttachmentForUpdate(txCtx, attachmentID, conversationID)
		if err != nil {
			return err
		}
		if !found {
			return nil
		}
		if owner != userID || message != nil {
			return domain.ErrPendingFileUnavailable()
		}
		return s.Attachments.DeleteAttachmentByID(txCtx, attachmentID)
	})
}

// DownloadAttachment returns authorized attachment metadata and bytes.
func (s *Service) DownloadAttachment(ctx context.Context, userID, conversationID, attachmentID string) (domain.Attachment, []byte, error) {
	var a domain.Attachment
	var content []byte
	err := s.Tx.WithinTx(ctx, func(txCtx context.Context) error {
		if _, err := s.Conversations.LockConversation(txCtx, conversationID, userID); err != nil {
			return err
		}
		var getErr error
		a, content, getErr = s.Attachments.GetAuthorizedAttachmentContent(txCtx, attachmentID, conversationID, userID)
		return getErr
	})
	return a, content, err
}

// DeleteAllExpiredPendingAttachments removes expired unlinked attachments.
func (s *Service) DeleteAllExpiredPendingAttachments(ctx context.Context) error {
	return s.Attachments.DeleteAllExpiredPending(ctx)
}

// SearchWorkspaceMessages searches unretracted messages across authorized conversations in a workspace.
func (s *Service) SearchWorkspaceMessages(ctx context.Context, userID, workspaceID, query, rawCursor string, limit int) (domain.WorkspaceSearchPage, error) {
	out := domain.WorkspaceSearchPage{Items: []domain.WorkspaceSearchItem{}}
	trimmed, cursor, err := domain.ValidateWorkspaceSearchQuery(query, rawCursor, limit)
	if err != nil {
		return out, err
	}
	var before any
	if rawCursor != "" {
		before = cursor.At
	}
	for attempt := 0; attempt < 2; attempt++ {
		candidateIDs, err := s.Messages.SearchWorkspaceCandidateIDs(ctx, userID, workspaceID, trimmed, before, cursor.ID, limit+1)
		if err != nil {
			return out, err
		}
		retry := false
		err = s.Tx.WithinTx(ctx, func(txCtx context.Context) error {
			if err := s.Workspaces.WorkspaceLock(txCtx, workspaceID, userID, false, false); err != nil {
				return err
			}
			member, err := s.Workspaces.WorkspaceMemberExists(txCtx, workspaceID, userID)
			if err != nil {
				return err
			}
			if !member {
				return domain.ErrNotFound()
			}
			if len(candidateIDs) == 0 {
				return nil
			}
			conversations, err := s.Messages.LockSearchConversationsForShare(txCtx, userID, workspaceID, candidateIDs)
			if err != nil {
				return err
			}
			if len(conversations) == 0 {
				if len(candidateIDs) == limit+1 && attempt == 0 {
					retry = true
				}
				return nil
			}
			items, err := s.Messages.QueryVerifiedWorkspaceSearchItems(txCtx, userID, trimmed, before, cursor.ID, limit+1, candidateIDs, conversations)
			if err != nil {
				return err
			}
			if len(items) < len(candidateIDs) && len(candidateIDs) == limit+1 && attempt == 0 {
				retry = true
				return nil
			}
			out.Items = items
			if len(out.Items) > limit {
				out.Items = out.Items[:limit]
				last := out.Items[len(out.Items)-1].Message
				encoded, err := domain.EncodeCursor(last.CreatedAt, last.ID)
				if err != nil {
					return err
				}
				out.Cursor = encoded
			}
			return nil
		})
		if err != nil {
			return out, err
		}
		if retry {
			continue
		}
		return out, nil
	}
	return out, nil
}

// Attention computes the caller's mention/reply attention inbox projection.
func (s *Service) Attention(ctx context.Context, userID, rawCursor string, limit int, unreadOnly bool) (domain.AttentionPage, error) {
	out := domain.AttentionPage{Items: []domain.AttentionItem{}}
	if limit < 1 || limit > 100 {
		return out, domain.ErrInvalidAttentionPageSize()
	}
	cursor, err := domain.ParseAttentionCursor(rawCursor)
	if err != nil {
		return out, err
	}
	err = s.Tx.WithinTx(ctx, func(txCtx context.Context) error {
		workspaces, err := s.Messages.LockUserWorkspacesForShare(txCtx, userID)
		if err != nil {
			return err
		}
		conversations, err := s.Messages.LockUserConversationsForShare(txCtx, userID, workspaces)
		if err != nil {
			return err
		}
		if len(conversations) == 0 {
			return nil
		}
		var before any
		if rawCursor != "" {
			before = cursor.At
		}
		unread, channelUnread, items, err := s.Messages.QueryAttentionProjection(txCtx, userID, before, cursor.ID, unreadOnly, limit+1, conversations)
		if err != nil {
			return err
		}
		out.UnreadCount = unread
		out.ChannelUnreadCount = channelUnread
		out.Items = items
		if len(out.Items) > limit {
			out.Items = out.Items[:limit]
			last := out.Items[len(out.Items)-1].Message
			encoded, err := domain.EncodeCursor(last.CreatedAt, last.ID)
			if err != nil {
				return err
			}
			out.Cursor = encoded
		}
		return nil
	})
	return out, err
}

// PrunePublishedOutbox deletes a bounded batch of legacy published outbox rows.
func (s *Service) PrunePublishedOutbox(ctx context.Context) (int64, error) {
	return s.Outbox.PrunePublishedOutbox(ctx)
}

// PublishOneOutbox claims, publishes, and deletes one pending outbox hint atomically.
func (s *Service) PublishOneOutbox(ctx context.Context, pub domain.OutboxPublisher) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	sent := false
	err := s.Tx.WithinTx(ctx, func(txCtx context.Context) error {
		id, payload, found, err := s.Outbox.ClaimNextUnpublishedOutbox(txCtx)
		if err != nil || !found {
			return err
		}
		if err = pub.PublishHint(txCtx, domain.HintChannel, payload); err != nil {
			return err
		}
		if err = s.Outbox.DeleteOutboxByID(txCtx, id); err != nil {
			return err
		}
		sent = true
		return nil
	})
	if err != nil {
		return false, err
	}
	return sent, nil
}
