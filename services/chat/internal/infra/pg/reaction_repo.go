package pg

import (
	"context"
	"time"

	"teamie/services/chat/internal/domain"
)

// Read the message revision and its full list in one statement snapshot. Starting
// from messages also returns the revision after the final reaction is deleted.
func (r *Repository) loadReactionStatesByMessageIDs(ctx context.Context, messageIDs []string) (map[string]domain.ReactionState, error) {
	if len(messageIDs) == 0 {
		return nil, nil
	}
	rows, err := r.DB.Conn(ctx).Query(ctx, `
SELECT m.id,m.reactions_version,r.emoji,r.user_id
FROM messages m
LEFT JOIN message_reactions r ON r.message_id=m.id AND m.retracted_at IS NULL
WHERE m.id=ANY($1::text[])
ORDER BY m.id,r.seq`, messageIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make(map[string]domain.ReactionState)
	indexes := make(map[string]map[string]int)
	for rows.Next() {
		var id string
		var version int64
		var emoji, userID *string
		if err = rows.Scan(&id, &version, &emoji, &userID); err != nil {
			return nil, err
		}
		state, ok := result[id]
		if !ok {
			state = domain.ReactionState{ReactionsVersion: version, Reactions: []domain.Reaction{}}
			indexes[id] = make(map[string]int)
		}
		if emoji != nil && userID != nil {
			if index, ok := indexes[id][*emoji]; ok {
				state.Reactions[index].UserIDs = append(state.Reactions[index].UserIDs, *userID)
				state.Reactions[index].Count = int64(len(state.Reactions[index].UserIDs))
			} else {
				indexes[id][*emoji] = len(state.Reactions)
				state.Reactions = append(state.Reactions, domain.Reaction{Emoji: *emoji, Count: 1, UserIDs: []string{*userID}})
			}
		}
		result[id] = state
	}
	return result, rows.Err()
}

func (r *Repository) populateMessageReactions(ctx context.Context, messages []domain.Message) error {
	ids := make([]string, 0, len(messages))
	for _, message := range messages {
		ids = append(ids, message.ID)
	}
	states, err := r.loadReactionStatesByMessageIDs(ctx, ids)
	if err != nil {
		return err
	}
	for index := range messages {
		state := states[messages[index].ID]
		messages[index].Reactions = state.Reactions
		messages[index].ReactionsVersion = state.ReactionsVersion
	}
	return nil
}

func (r *Repository) bumpReactionVersion(ctx context.Context, messageID string) error {
	_, err := r.DB.Conn(ctx).Exec(ctx, "UPDATE messages SET reactions_version=reactions_version+1 WHERE id=$1", messageID)
	return err
}

func (r *Repository) reactionResult(ctx context.Context, messageID string, changed bool) (domain.ReactionState, bool, error) {
	if changed {
		if err := r.bumpReactionVersion(ctx, messageID); err != nil {
			return domain.ReactionState{}, false, err
		}
	}
	states, err := r.loadReactionStatesByMessageIDs(ctx, []string{messageID})
	return states[messageID], changed, err
}

func (r *Repository) AddReaction(ctx context.Context, conversationID, messageID, userID, emoji string) (domain.ReactionState, bool, error) {
	conn := r.DB.Conn(ctx)
	var retractedAt *time.Time
	if err := conn.QueryRow(ctx, "SELECT retracted_at FROM messages WHERE id=$1 AND conversation_id=$2 FOR UPDATE", messageID, conversationID).Scan(&retractedAt); err != nil {
		return domain.ReactionState{}, false, notFound(err)
	}
	if retractedAt != nil {
		return domain.ReactionState{}, false, domain.ErrMessageRetracted()
	}
	var alreadyHasEmoji bool
	if err := conn.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM message_reactions WHERE message_id=$1 AND emoji=$2)", messageID, emoji).Scan(&alreadyHasEmoji); err != nil {
		return domain.ReactionState{}, false, err
	}
	if !alreadyHasEmoji {
		var distinctCount int
		if err := conn.QueryRow(ctx, "SELECT count(DISTINCT emoji) FROM message_reactions WHERE message_id=$1", messageID).Scan(&distinctCount); err != nil {
			return domain.ReactionState{}, false, err
		}
		if distinctCount >= 20 {
			return domain.ReactionState{}, false, domain.ErrReactionLimit()
		}
	}
	tag, err := conn.Exec(ctx, "INSERT INTO message_reactions(conversation_id,message_id,emoji,user_id) VALUES($1,$2,$3,$4) ON CONFLICT DO NOTHING", conversationID, messageID, emoji, userID)
	if err != nil {
		return domain.ReactionState{}, false, err
	}
	return r.reactionResult(ctx, messageID, tag.RowsAffected() > 0)
}

func (r *Repository) RemoveReaction(ctx context.Context, conversationID, messageID, userID, emoji string) (domain.ReactionState, bool, error) {
	conn := r.DB.Conn(ctx)
	var retractedAt *time.Time
	if err := conn.QueryRow(ctx, "SELECT retracted_at FROM messages WHERE id=$1 AND conversation_id=$2 FOR UPDATE", messageID, conversationID).Scan(&retractedAt); err != nil {
		return domain.ReactionState{}, false, notFound(err)
	}
	if retractedAt != nil {
		return r.reactionResult(ctx, messageID, false)
	}
	tag, err := conn.Exec(ctx, "DELETE FROM message_reactions WHERE message_id=$1 AND emoji=$2 AND user_id=$3", messageID, emoji, userID)
	if err != nil {
		return domain.ReactionState{}, false, err
	}
	return r.reactionResult(ctx, messageID, tag.RowsAffected() > 0)
}
