package app

import (
	"context"
	"slices"
	"time"

	"teamie/services/chat/internal/domain"
)

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
		if err = s.Conversations.LockConversationForShare(txCtx, conversationID); err != nil {
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
		if err = s.Conversations.LockConversationForShare(txCtx, conversationID); err != nil {
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
			// The response and durable event must use the cleared list and the
			// revision committed by the same retraction transaction.
			m, err = s.Messages.GetMessage(txCtx, conversationID, messageID)
			if err != nil {
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
		if s.Flags != nil && s.Flags.Enabled("im.read_receipts.view") {
			other := make([]string, 0, len(c.MemberIDs))
			for _, id := range c.MemberIDs {
				if id != userID {
					other = append(other, id)
				}
			}
			if len(other) > 0 {
				if err = s.Conversations.EnqueueOutboxHint(txCtx, map[string]any{
					"type":           "receipts.changed",
					"conversationId": conversationID,
					"userIds":        other,
				}); err != nil {
					return err
				}
			}
		}
		result = seq
		return nil
	})
	return result, err
}

// ReadReceipts returns conversation read receipts for an authorized member.
func (s *Service) ReadReceipts(ctx context.Context, userID, conversationID string) (domain.ReadReceipts, error) {
	if err := s.requireFlag("im.read_receipts.view"); err != nil {
		return domain.ReadReceipts{}, err
	}
	var out domain.ReadReceipts
	err := s.Tx.WithinTx(ctx, func(txCtx context.Context) error {
		ws, err := s.Conversations.GetConversationWorkspaceID(txCtx, conversationID)
		if err != nil {
			return err
		}
		if err = s.Workspaces.WorkspaceLock(txCtx, ws, userID, false, false); err != nil {
			return err
		}
		if err = s.Conversations.LockConversationForShare(txCtx, conversationID); err != nil {
			return err
		}
		c, err := s.Conversations.ConversationAccess(txCtx, conversationID, userID)
		if err != nil {
			return err
		}
		out, err = s.Conversations.QueryReadReceipts(txCtx, c, userID)
		return err
	})
	return out, err
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
	out := domain.AttentionPage{Items: []domain.AttentionItem{}, AttentionUnreadByConversation: []domain.AttentionUnread{}}
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
		unread, channelUnread, items, unreadByConversation, err := s.Messages.QueryAttentionProjection(txCtx, userID, before, cursor.ID, unreadOnly, limit+1, conversations)
		if err != nil {
			return err
		}
		out.UnreadCount = unread
		out.ChannelUnreadCount = channelUnread
		out.Items = items
		out.AttentionUnreadByConversation = unreadByConversation
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
