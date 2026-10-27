package app

import (
	"context"

	"teamie/services/chat/internal/domain"
)

// AddReaction adds the caller's Unicode emoji reaction idempotently.
func (s *Service) AddReaction(ctx context.Context, userID, conversationID, messageID, emoji string) (domain.ReactionState, error) {
	if err := s.requireFlag("im.reactions.create"); err != nil {
		return domain.ReactionState{}, err
	}
	return s.changeReaction(ctx, userID, conversationID, messageID, emoji, true)
}

// RemoveReaction remains available when creating reactions is disabled.
func (s *Service) RemoveReaction(ctx context.Context, userID, conversationID, messageID, emoji string) (domain.ReactionState, error) {
	return s.changeReaction(ctx, userID, conversationID, messageID, emoji, false)
}

func (s *Service) changeReaction(ctx context.Context, userID, conversationID, messageID, emoji string, add bool) (domain.ReactionState, error) {
	if err := domain.ValidateReactionEmoji(emoji); err != nil {
		return domain.ReactionState{}, err
	}
	var state domain.ReactionState
	err := s.Tx.WithinTx(ctx, func(txCtx context.Context) error {
		conversation, err := s.Conversations.LockConversation(txCtx, conversationID, userID)
		if err != nil {
			return err
		}
		var changed bool
		if add {
			state, changed, err = s.Messages.AddReaction(txCtx, conversationID, messageID, userID, emoji)
		} else {
			state, changed, err = s.Messages.RemoveReaction(txCtx, conversationID, messageID, userID, emoji)
		}
		if err != nil || !changed {
			return err
		}
		return s.Conversations.Fanout(txCtx, conversation.MemberIDs, domain.Event{
			Type:             "reaction.updated",
			WorkspaceID:      conversation.WorkspaceID,
			ConversationID:   conversationID,
			MessageID:        messageID,
			Reactions:        state.Reactions,
			ReactionsVersion: state.ReactionsVersion,
		})
	})
	return state, err
}
