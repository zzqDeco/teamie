package app

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"strings"

	"teamie/services/chat/internal/domain"
)

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
			ID:                domain.NewID(),
			WorkspaceID:       workspaceID,
			Type:              kind,
			Name:              name,
			MemberIDs:         ids,
			CreatedBy:         userID,
			CanManageMembers:  kind == "group",
			NotificationLevel: "all",
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

// GroupMember adds or removes a member from a group conversation.
func (s *Service) GroupMember(ctx context.Context, actorID, conversationID, targetUserID string, add bool) (domain.Conversation, error) {
	return s.groupMember(ctx, actorID, conversationID, targetUserID, add, false)
}
func (s *Service) groupMember(ctx context.Context, actorID, conversationID, targetUserID string, add, replay bool) (domain.Conversation, error) {
	var c domain.Conversation
	if add && !replay {
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
		if replay {
			c.MemberIDs, c.CanManageMembers = ids, true
			return nil
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

// SetNotificationLevel updates the caller's notification preference for a conversation.
func (s *Service) SetNotificationLevel(ctx context.Context, userID, conversationID, level string) (string, error) {
	if err := s.requireFlag("im.notifications.configure"); err != nil {
		return "", err
	}
	if err := domain.ValidateNotificationLevel(level); err != nil {
		return "", err
	}
	err := s.Tx.WithinTx(ctx, func(txCtx context.Context) error {
		c, err := s.Conversations.LockConversation(txCtx, conversationID, userID)
		if err != nil {
			return err
		}
		changed, err := s.Conversations.SetNotificationLevel(txCtx, conversationID, userID, level)
		if err != nil || !changed {
			return err
		}
		return s.Conversations.Fanout(txCtx, []string{userID}, domain.Event{
			Type:              "preference.updated",
			WorkspaceID:       c.WorkspaceID,
			ConversationID:    conversationID,
			NotificationLevel: &level,
		})
	})
	if err != nil {
		return "", err
	}
	return level, nil
}
