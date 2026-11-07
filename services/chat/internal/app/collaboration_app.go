package app

import (
	"context"

	"teamie/services/chat/internal/domain"
)

// --- Pins ---

// PinMessage pins an unretracted message for every current member. Re-pinning
// returns the existing pin unchanged with the current version.
func (s *Service) PinMessage(ctx context.Context, user domain.User, conversationID, messageID string) (domain.Pin, int64, error) {
	if err := s.requireFlag("im.messages.pin"); err != nil {
		return domain.Pin{}, 0, err
	}
	var pin domain.Pin
	var version int64
	err := s.Tx.WithinTx(ctx, func(txCtx context.Context) error {
		c, err := s.Conversations.LockConversation(txCtx, conversationID, user.ID)
		if err != nil {
			return err
		}
		m, err := s.Messages.GetMessage(txCtx, conversationID, messageID)
		if err != nil {
			return err
		}
		if m.RetractedAt != nil {
			return domain.ErrRetractedMessageUnavailable()
		}
		existing, found, err := s.Messages.FindPin(txCtx, conversationID, messageID)
		if err != nil {
			return err
		}
		if found {
			pin = existing
			version, err = s.Messages.PinsVersion(txCtx, conversationID)
			return err
		}
		count, err := s.Messages.CountPins(txCtx, conversationID)
		if err != nil {
			return err
		}
		if count >= domain.PinsPerConversation {
			return domain.ErrPinLimit()
		}
		pin, version, err = s.Messages.InsertPin(txCtx, conversationID, messageID, user.ID, user.DisplayName)
		if err != nil {
			return err
		}
		pinned := true
		pinnedAt := pin.PinnedAt
		return s.Conversations.Fanout(txCtx, c.MemberIDs, domain.Event{
			Type:           "pin.updated",
			WorkspaceID:    c.WorkspaceID,
			ConversationID: conversationID,
			MessageID:      messageID,
			Pinned:         &pinned,
			PinnedBy:       pin.PinnedBy,
			PinnedAt:       &pinnedAt,
			PinsVersion:    version,
		})
	})
	return pin, version, err
}

// UnpinMessage stays available when new pins are disabled. Unpinning an absent
// pin (including one already removed by retraction) returns the current version.
func (s *Service) UnpinMessage(ctx context.Context, userID, conversationID, messageID string) (int64, error) {
	var version int64
	err := s.Tx.WithinTx(ctx, func(txCtx context.Context) error {
		c, err := s.Conversations.LockConversation(txCtx, conversationID, userID)
		if err != nil {
			return err
		}
		exists, err := s.Messages.MessageExistsInConversation(txCtx, messageID, conversationID)
		if err != nil {
			return err
		}
		if !exists {
			return domain.ErrNotFound()
		}
		if err = s.unpinInTx(txCtx, c, messageID); err != nil {
			return err
		}
		version, err = s.Messages.PinsVersion(txCtx, conversationID)
		return err
	})
	return version, err
}

// unpinInTx removes a pin under the caller's conversation row lock and commits
// the counter increment and durable pin.updated event with it.
func (s *Service) unpinInTx(ctx context.Context, c domain.Conversation, messageID string) error {
	version, deleted, err := s.Messages.DeletePin(ctx, c.ID, messageID)
	if err != nil || !deleted {
		return err
	}
	pinned := false
	return s.Conversations.Fanout(ctx, c.MemberIDs, domain.Event{
		Type:           "pin.updated",
		WorkspaceID:    c.WorkspaceID,
		ConversationID: c.ID,
		MessageID:      messageID,
		Pinned:         &pinned,
		PinsVersion:    version,
	})
}

// ListPins returns every pin newest first with complete current messages.
func (s *Service) ListPins(ctx context.Context, userID, conversationID string) ([]domain.Pin, int64, error) {
	var pins []domain.Pin
	var version int64
	err := s.Tx.WithinTx(ctx, func(txCtx context.Context) error {
		if err := s.lockConversationRead(txCtx, userID, conversationID); err != nil {
			return err
		}
		var err error
		if pins, err = s.Messages.ListPins(txCtx, conversationID); err != nil {
			return err
		}
		version, err = s.Messages.PinsVersion(txCtx, conversationID)
		return err
	})
	return pins, version, err
}

// lockConversationRead acquires the ordinary workspace and conversation share
// locks, then rechecks current access after any wait.
func (s *Service) lockConversationRead(ctx context.Context, userID, conversationID string) error {
	ws, err := s.Conversations.GetConversationWorkspaceID(ctx, conversationID)
	if err != nil {
		return err
	}
	if err = s.Workspaces.WorkspaceLock(ctx, ws, userID, false, false); err != nil {
		return err
	}
	if err = s.Conversations.LockConversationForShare(ctx, conversationID); err != nil {
		return err
	}
	_, err = s.Conversations.ConversationAccess(ctx, conversationID, userID)
	return err
}

// --- Saved items ---

// SaveMessage privately saves an unretracted message for the caller.
// Re-saving keeps the original savedAt and emits no event.
func (s *Service) SaveMessage(ctx context.Context, userID, conversationID, messageID string) (domain.SavedMarker, error) {
	if err := s.requireFlag("im.messages.save"); err != nil {
		return domain.SavedMarker{}, err
	}
	var saved domain.SavedMarker
	err := s.Tx.WithinTx(ctx, func(txCtx context.Context) error {
		if err := s.lockConversationRead(txCtx, userID, conversationID); err != nil {
			return err
		}
		if err := s.Tx.LockSavedItems(txCtx, userID); err != nil {
			return err
		}
		m, err := s.Messages.GetMessage(txCtx, conversationID, messageID)
		if err != nil {
			return err
		}
		if m.RetractedAt != nil {
			return domain.ErrRetractedMessageUnavailable()
		}
		existing, found, err := s.Messages.FindSaved(txCtx, userID, messageID)
		if err != nil {
			return err
		}
		if found {
			saved = existing
			return nil
		}
		count, err := s.Messages.CountSaved(txCtx, userID)
		if err != nil {
			return err
		}
		if count >= domain.SavedItemsPerUser {
			return domain.ErrSavedLimit()
		}
		if saved, err = s.Messages.InsertSaved(txCtx, userID, conversationID, messageID); err != nil {
			return err
		}
		return s.fanoutSaved(txCtx, userID, conversationID, messageID, &saved)
	})
	return saved, err
}

// UnsaveMessage stays available when new saves are disabled.
func (s *Service) UnsaveMessage(ctx context.Context, userID, conversationID, messageID string) error {
	return s.Tx.WithinTx(ctx, func(txCtx context.Context) error {
		if err := s.lockConversationRead(txCtx, userID, conversationID); err != nil {
			return err
		}
		if err := s.Tx.LockSavedItems(txCtx, userID); err != nil {
			return err
		}
		exists, err := s.Messages.MessageExistsInConversation(txCtx, messageID, conversationID)
		if err != nil {
			return err
		}
		if !exists {
			return domain.ErrNotFound()
		}
		deleted, err := s.Messages.DeleteSaved(txCtx, userID, conversationID, messageID)
		if err != nil || !deleted {
			return err
		}
		return s.fanoutSaved(txCtx, userID, conversationID, messageID, nil)
	})
}

func (s *Service) fanoutSaved(ctx context.Context, userID, conversationID, messageID string, saved *domain.SavedMarker) error {
	ws, err := s.Conversations.GetConversationWorkspaceID(ctx, conversationID)
	if err != nil {
		return err
	}
	isSaved := saved != nil
	event := domain.Event{
		Type:           "saved.updated",
		WorkspaceID:    ws,
		ConversationID: conversationID,
		MessageID:      messageID,
		Saved:          &isSaved,
	}
	if saved != nil {
		savedAt := saved.SavedAt
		event.SavedAt = &savedAt
	}
	return s.Conversations.Fanout(ctx, []string{userID}, event)
}

// SavedMessageIDs lists the caller's saved message IDs in one conversation.
func (s *Service) SavedMessageIDs(ctx context.Context, userID, conversationID string) ([]string, error) {
	var ids []string
	err := s.Tx.WithinTx(ctx, func(txCtx context.Context) error {
		if err := s.lockConversationRead(txCtx, userID, conversationID); err != nil {
			return err
		}
		var err error
		ids, err = s.Messages.SavedMessageIDs(txCtx, userID, conversationID)
		return err
	})
	return ids, err
}

// SavedItems pages the caller's saved list newest first. Every page share-locks
// the caller's workspaces and conversations in sorted order and omits items the
// caller can no longer read.
func (s *Service) SavedItems(ctx context.Context, userID, rawCursor string, limit int) (domain.SavedPage, error) {
	out := domain.SavedPage{Items: []domain.SavedItem{}}
	cursor, err := domain.ValidateSavedQuery(rawCursor, limit)
	if err != nil {
		return out, err
	}
	err = s.Tx.WithinTx(ctx, func(txCtx context.Context) error {
		workspaces, err := s.Messages.LockUserWorkspacesForShare(txCtx, userID)
		if err != nil {
			return err
		}
		conversations, err := s.Messages.LockUserConversationsForShare(txCtx, userID, workspaces)
		if err != nil || len(conversations) == 0 {
			return err
		}
		var before any
		if rawCursor != "" {
			before = cursor.At
		}
		items, err := s.Messages.QuerySavedPage(txCtx, userID, before, cursor.ID, limit+1, conversations)
		if err != nil {
			return err
		}
		out.Items = items
		if len(out.Items) > limit {
			out.Items = out.Items[:limit]
			last := out.Items[len(out.Items)-1]
			encoded, err := domain.EncodeCursor(last.SavedAt, last.Message.ID)
			if err != nil {
				return err
			}
			out.Cursor = encoded
		}
		return nil
	})
	return out, err
}

// --- Manual unread ---

// MarkUnread sets the caller's personal marker without moving the read cursor.
func (s *Service) MarkUnread(ctx context.Context, userID, conversationID string, seq int64) (int64, error) {
	if err := s.requireFlag("im.unread.mark"); err != nil {
		return 0, err
	}
	err := s.Tx.WithinTx(ctx, func(txCtx context.Context) error {
		c, err := s.Conversations.LockConversation(txCtx, conversationID, userID)
		if err != nil {
			return err
		}
		if err = domain.ValidateManualUnreadSeq(seq, c.LastSeq); err != nil {
			return err
		}
		if c.ManualUnreadSeq == seq {
			return nil
		}
		if err = s.Conversations.SetManualUnread(txCtx, conversationID, userID, seq); err != nil {
			return err
		}
		return s.fanoutReadState(txCtx, c, userID, domain.ReadState{ReadSeq: c.ReadSeq, ManualUnreadSeq: seq})
	})
	if err != nil {
		return 0, err
	}
	return seq, nil
}

// ClearUnread removes the caller's marker idempotently; it stays available when
// new markers are disabled.
func (s *Service) ClearUnread(ctx context.Context, userID, conversationID string) error {
	return s.Tx.WithinTx(ctx, func(txCtx context.Context) error {
		c, err := s.Conversations.LockConversation(txCtx, conversationID, userID)
		if err != nil {
			return err
		}
		if c.ManualUnreadSeq == 0 {
			return nil
		}
		cleared, err := s.Conversations.ClearManualUnread(txCtx, conversationID, userID)
		if err != nil || !cleared {
			return err
		}
		return s.fanoutReadState(txCtx, c, userID, domain.ReadState{ReadSeq: c.ReadSeq})
	})
}
