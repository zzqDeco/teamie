package pg

import (
	"context"
	"encoding/json"
	"time"

	"teamie/pkg/postgres"
	"teamie/services/chat/internal/domain"
)

func (r *Repository) loadReactionsByMessageIDs(ctx context.Context, messageIDs []string) (map[string][]domain.Reaction, error) {
	if len(messageIDs) == 0 {
		return nil, nil
	}
	rows, err := r.DB.Conn(ctx).Query(ctx, "SELECT message_id,emoji,user_id FROM message_reactions WHERE message_id=ANY($1::text[]) ORDER BY message_id,seq ASC", messageIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make(map[string][]domain.Reaction)
	indexByMsgEmoji := make(map[string]map[string]int)
	for rows.Next() {
		var mid, emoji, uid string
		if err = rows.Scan(&mid, &emoji, &uid); err != nil {
			return nil, err
		}
		emojiIdx, ok := indexByMsgEmoji[mid]
		if !ok {
			emojiIdx = make(map[string]int)
			indexByMsgEmoji[mid] = emojiIdx
		}
		if idx, exists := emojiIdx[emoji]; exists {
			result[mid][idx].UserIDs = append(result[mid][idx].UserIDs, uid)
			result[mid][idx].Count = int64(len(result[mid][idx].UserIDs))
		} else {
			emojiIdx[emoji] = len(result[mid])
			result[mid] = append(result[mid], domain.Reaction{
				Emoji:   emoji,
				Count:   1,
				UserIDs: []string{uid},
			})
		}
	}
	return result, rows.Err()
}

func (r *Repository) populateMessageReactions(ctx context.Context, msgs []domain.Message) error {
	ids := make([]string, 0, len(msgs))
	for _, m := range msgs {
		if m.RetractedAt == nil {
			ids = append(ids, m.ID)
		}
	}
	byID, err := r.loadReactionsByMessageIDs(ctx, ids)
	if err != nil {
		return err
	}
	for i := range msgs {
		if msgs[i].RetractedAt == nil {
			msgs[i].Reactions = byID[msgs[i].ID]
		}
	}
	return nil
}

func (r *Repository) GetMessage(ctx context.Context, conversationID, messageID string) (domain.Message, error) {
	m, err := scanMessage(r.DB.Conn(ctx).QueryRow(ctx, "SELECT "+messageColumns+" FROM messages WHERE id=$1 AND conversation_id=$2", messageID, conversationID))
	if err != nil {
		return m, notFound(err)
	}
	if m.RetractedAt == nil {
		byID, rErr := r.loadReactionsByMessageIDs(ctx, []string{m.ID})
		if rErr != nil {
			return m, rErr
		}
		m.Reactions = byID[m.ID]
	}
	return m, nil
}

func (r *Repository) FindMessageBySenderClientID(ctx context.Context, senderID, clientMessageID string) (domain.Message, string, []string, []domain.Mention, bool, error) {
	conn := r.DB.Conn(ctx)
	m, err := scanMessage(conn.QueryRow(ctx, "SELECT "+messageColumns+" FROM messages WHERE sender_id=$1 AND client_message_id=$2", senderID, clientMessageID))
	if postgres.IsNoRows(err) {
		return domain.Message{}, "", nil, nil, false, nil
	}
	if err != nil {
		return domain.Message{}, "", nil, nil, false, err
	}
	if m.RetractedAt == nil {
		byID, rErr := r.loadReactionsByMessageIDs(ctx, []string{m.ID})
		if rErr != nil {
			return domain.Message{}, "", nil, nil, false, rErr
		}
		m.Reactions = byID[m.ID]
	}
	var hash string
	var originalIDs []string
	var originalMentions []domain.Mention
	if err = conn.QueryRow(ctx, "SELECT original_body_hash,original_attachment_ids,original_mentions FROM messages WHERE id=$1", m.ID).Scan(&hash, &originalIDs, &originalMentions); err != nil {
		return domain.Message{}, "", nil, nil, false, err
	}
	return m, hash, originalIDs, originalMentions, true, nil
}

func (r *Repository) MessageExistsInConversation(ctx context.Context, messageID, conversationID string) (bool, error) {
	var exists bool
	err := r.DB.Conn(ctx).QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM messages WHERE id=$1 AND conversation_id=$2)", messageID, conversationID).Scan(&exists)
	return exists, err
}

func (r *Repository) InsertMessage(ctx context.Context, m domain.Message, originalBodyHash string) (time.Time, error) {
	var createdAt time.Time
	err := r.DB.Conn(ctx).QueryRow(ctx, "INSERT INTO messages(id,conversation_id,sender_id,sender_name,sender_kind,client_message_id,seq,body,original_body_hash,reply_to_id) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10) RETURNING created_at", m.ID, m.ConversationID, m.SenderID, m.SenderName, m.SenderKind, m.ClientMessageID, m.Seq, m.Body, originalBodyHash, m.ReplyToID).Scan(&createdAt)
	return createdAt, err
}

func (r *Repository) SetMessageInitialMentions(ctx context.Context, messageID string, mentions []domain.Mention) error {
	_, err := r.DB.Conn(ctx).Exec(ctx, "UPDATE messages SET mentions=$2,original_mentions=$2 WHERE id=$1", messageID, mentions)
	return err
}

func (r *Repository) SetMessageInitialAttachments(ctx context.Context, messageID string, attachments []domain.Attachment, originalIDs []string) error {
	_, err := r.DB.Conn(ctx).Exec(ctx, "UPDATE messages SET attachments=$2,original_attachment_ids=$3 WHERE id=$1", messageID, attachments, originalIDs)
	return err
}

func (r *Repository) RetractMessageAndDeleteAttachments(ctx context.Context, messageID string, retractedAt time.Time) error {
	conn := r.DB.Conn(ctx)
	if _, err := conn.Exec(ctx, "UPDATE messages SET body='',attachments='[]',mentions='[]',retracted_at=$2 WHERE id=$1", messageID, retractedAt); err != nil {
		return err
	}
	if _, err := conn.Exec(ctx, "DELETE FROM message_reactions WHERE message_id=$1", messageID); err != nil {
		return err
	}
	_, err := conn.Exec(ctx, "DELETE FROM attachments WHERE message_id=$1", messageID)
	return err
}

func (r *Repository) UpdateMessageBodyAndMentions(ctx context.Context, messageID, body string, editedAt time.Time, mentions []domain.Mention) error {
	_, err := r.DB.Conn(ctx).Exec(ctx, "UPDATE messages SET body=$2,edited_at=$3,mentions=$4 WHERE id=$1", messageID, body, editedAt, mentions)
	return err
}

func (r *Repository) AddReaction(ctx context.Context, conversationID, messageID, userID, emoji string) ([]domain.Reaction, bool, error) {
	conn := r.DB.Conn(ctx)
	var retractedAt *time.Time
	if err := conn.QueryRow(ctx, "SELECT retracted_at FROM messages WHERE id=$1 AND conversation_id=$2 FOR UPDATE", messageID, conversationID).Scan(&retractedAt); err != nil {
		return nil, false, notFound(err)
	}
	if retractedAt != nil {
		return nil, false, domain.ErrMessageRetracted()
	}
	var alreadyHasEmoji bool
	if err := conn.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM message_reactions WHERE message_id=$1 AND emoji=$2)", messageID, emoji).Scan(&alreadyHasEmoji); err != nil {
		return nil, false, err
	}
	if !alreadyHasEmoji {
		var distinctCount int
		if err := conn.QueryRow(ctx, "SELECT count(DISTINCT emoji) FROM message_reactions WHERE message_id=$1", messageID).Scan(&distinctCount); err != nil {
			return nil, false, err
		}
		if distinctCount >= 20 {
			return nil, false, domain.ErrReactionLimit()
		}
	}
	tag, err := conn.Exec(ctx, "INSERT INTO message_reactions(conversation_id,message_id,emoji,user_id) VALUES($1,$2,$3,$4) ON CONFLICT DO NOTHING", conversationID, messageID, emoji, userID)
	if err != nil {
		return nil, false, err
	}
	byID, err := r.loadReactionsByMessageIDs(ctx, []string{messageID})
	if err != nil {
		return nil, false, err
	}
	reactions := byID[messageID]
	if reactions == nil {
		reactions = []domain.Reaction{}
	}
	return reactions, tag.RowsAffected() > 0, nil
}

func (r *Repository) RemoveReaction(ctx context.Context, conversationID, messageID, userID, emoji string) ([]domain.Reaction, bool, error) {
	conn := r.DB.Conn(ctx)
	var retractedAt *time.Time
	if err := conn.QueryRow(ctx, "SELECT retracted_at FROM messages WHERE id=$1 AND conversation_id=$2 FOR UPDATE", messageID, conversationID).Scan(&retractedAt); err != nil {
		return nil, false, notFound(err)
	}
	if retractedAt != nil {
		return []domain.Reaction{}, false, nil
	}
	tag, err := conn.Exec(ctx, "DELETE FROM message_reactions WHERE message_id=$1 AND emoji=$2 AND user_id=$3", messageID, emoji, userID)
	if err != nil {
		return nil, false, err
	}
	byID, err := r.loadReactionsByMessageIDs(ctx, []string{messageID})
	if err != nil {
		return nil, false, err
	}
	reactions := byID[messageID]
	if reactions == nil {
		reactions = []domain.Reaction{}
	}
	return reactions, tag.RowsAffected() > 0, nil
}

func (r *Repository) QueryMessageHistory(ctx context.Context, conversationID string, before, around int64, query string, limit int, filesOnly bool) ([]domain.Message, bool, error) {
	filter := "($3::text='' OR (retracted_at IS NULL AND strpos(lower(body),lower($3))>0))"
	if filesOnly {
		filter = "(retracted_at IS NULL AND jsonb_array_length(attachments)>0 AND ($3::text='' OR EXISTS (SELECT 1 FROM jsonb_array_elements(attachments) AS file WHERE strpos(lower(file->>'filename'),lower($3))>0)))"
	}
	rows, err := r.DB.Conn(ctx).Query(ctx, "SELECT "+messageColumns+" FROM messages WHERE conversation_id=$1 AND ($2::bigint=0 OR seq<$2) AND "+filter+" AND ($4::bigint=0 OR seq BETWEEN GREATEST(1,$4-($5::bigint/2)) AND $4+(($5::bigint-1)/2)) ORDER BY seq DESC LIMIT $6", conversationID, before, query, around, limit, limit+1)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	out := []domain.Message{}
	for rows.Next() {
		m, scanErr := scanMessage(rows)
		if scanErr != nil {
			return nil, false, scanErr
		}
		out = append(out, m)
	}
	if err = rows.Err(); err != nil {
		return nil, false, err
	}
	more := len(out) > limit
	if more {
		out = out[:limit]
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	if err = r.populateMessageReactions(ctx, out); err != nil {
		return nil, false, err
	}
	return out, more, nil
}

func (r *Repository) SearchWorkspaceCandidateIDs(ctx context.Context, userID, workspaceID, query string, before any, cursorID string, limit int) ([]string, error) {
	rows, err := r.DB.Conn(ctx).Query(ctx, `
SELECT m.id
FROM messages m
JOIN conversations c ON c.id=m.conversation_id
JOIN workspace_members wm ON wm.workspace_id=c.workspace_id AND wm.user_id=$1
WHERE c.workspace_id=$2 AND m.retracted_at IS NULL
  AND (c.type='channel' OR EXISTS(SELECT 1 FROM conversation_members cm WHERE cm.conversation_id=c.id AND cm.user_id=$1))
  AND strpos(lower(m.body),lower($3))>0
  AND ($4::timestamptz IS NULL OR (m.created_at,m.id)<($4,$5::text))
ORDER BY m.created_at DESC,m.id DESC LIMIT $6
`, userID, workspaceID, query, before, cursorID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := []string{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (r *Repository) lockIDs(ctx context.Context, query string, args ...any) ([]string, error) {
	rows, err := r.DB.Conn(ctx).Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := []string{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (r *Repository) LockSearchConversationsForShare(ctx context.Context, userID, workspaceID string, candidateMessageIDs []string) ([]string, error) {
	return r.lockIDs(ctx, `SELECT c.id FROM conversations c JOIN workspace_members wm ON wm.workspace_id=c.workspace_id AND wm.user_id=$1 WHERE c.workspace_id=$2 AND c.id IN (SELECT m.conversation_id FROM messages m WHERE m.id=ANY($3::text[])) AND (c.type='channel' OR EXISTS(SELECT 1 FROM conversation_members cm WHERE cm.conversation_id=c.id AND cm.user_id=$1)) ORDER BY c.id FOR SHARE OF c`, userID, workspaceID, candidateMessageIDs)
}

func (r *Repository) QueryVerifiedWorkspaceSearchItems(ctx context.Context, userID, query string, before any, cursorID string, limit int, candidateMessageIDs, conversationIDs []string) ([]domain.WorkspaceSearchItem, error) {
	var rawItems []byte
	err := r.DB.Conn(ctx).QueryRow(ctx, `
WITH page AS (
 SELECT m.id,m.created_at
 FROM messages m
 JOIN conversations c ON c.id=m.conversation_id
 JOIN workspace_members wm ON wm.workspace_id=c.workspace_id AND wm.user_id=$1
 WHERE m.id=ANY($6::text[]) AND c.id=ANY($7::text[]) AND m.retracted_at IS NULL
   AND (c.type='channel' OR EXISTS(SELECT 1 FROM conversation_members cm WHERE cm.conversation_id=c.id AND cm.user_id=$1))
   AND strpos(lower(m.body),lower($2))>0
   AND ($3::timestamptz IS NULL OR (m.created_at,m.id)<($3,$4::text))
 ORDER BY m.created_at DESC,m.id DESC LIMIT $5
)
SELECT COALESCE((SELECT jsonb_agg(jsonb_build_object(
  'message',jsonb_build_object('id',m.id,'conversationId',m.conversation_id,'senderId',m.sender_id,'senderName',m.sender_name,'senderKind',m.sender_kind,'clientMessageId',m.client_message_id,'seq',m.seq,'body',m.body,'replyToId',m.reply_to_id,'createdAt',m.created_at,'editedAt',m.edited_at,'retractedAt',m.retracted_at,'attachments',m.attachments,'mentions',m.mentions),
  'conversation',jsonb_build_object('id',c.id,'workspaceId',c.workspace_id,'name',c.name,'type',c.type)
) ORDER BY p.created_at DESC,p.id DESC)
FROM page p JOIN messages m ON m.id=p.id JOIN conversations c ON c.id=m.conversation_id),'[]'::jsonb)
`, userID, query, before, cursorID, limit, candidateMessageIDs, conversationIDs).Scan(&rawItems)
	if err != nil {
		return nil, err
	}
	items := []domain.WorkspaceSearchItem{}
	if err = json.Unmarshal(rawItems, &items); err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(items))
	for _, it := range items {
		ids = append(ids, it.Message.ID)
	}
	byID, err := r.loadReactionsByMessageIDs(ctx, ids)
	if err != nil {
		return nil, err
	}
	for i := range items {
		items[i].Message.Reactions = byID[items[i].Message.ID]
	}
	return items, nil
}

func (r *Repository) LockUserWorkspacesForShare(ctx context.Context, userID string) ([]string, error) {
	return r.lockIDs(ctx, `SELECT w.id FROM workspaces w JOIN workspace_members wm ON wm.workspace_id=w.id AND wm.user_id=$1 ORDER BY w.id FOR SHARE OF w`, userID)
}

func (r *Repository) LockUserConversationsForShare(ctx context.Context, userID string, workspaceIDs []string) ([]string, error) {
	return r.lockIDs(ctx, `SELECT c.id FROM conversations c JOIN workspace_members wm ON wm.workspace_id=c.workspace_id AND wm.user_id=$1 WHERE c.workspace_id=ANY($2::text[]) AND (c.type='channel' OR EXISTS(SELECT 1 FROM conversation_members cm WHERE cm.conversation_id=c.id AND cm.user_id=$1)) ORDER BY c.id FOR SHARE OF c`, userID, workspaceIDs)
}

func (r *Repository) QueryAttentionProjection(ctx context.Context, userID string, before any, cursorID string, unreadOnly bool, limit int, conversationIDs []string) (int64, int64, []domain.AttentionItem, error) {
	var unreadCount, channelUnreadCount int64
	var rawItems []byte
	err := r.DB.Conn(ctx).QueryRow(ctx, `
WITH candidates AS MATERIALIZED (
 SELECT m.id,m.created_at,c.type AS conversation_type,m.seq>COALESCE(r.seq,0) AS unread,
        m.mentions @> jsonb_build_array(jsonb_build_object('userId',$1::text)) AS mentioned,
        (original.id IS NOT NULL AND original.sender_id=$1 AND original.retracted_at IS NULL) AS replied
 FROM messages m
 JOIN conversations c ON c.id=m.conversation_id
 JOIN workspace_members wm ON wm.workspace_id=c.workspace_id AND wm.user_id=$1
 LEFT JOIN read_cursors r ON r.conversation_id=c.id AND r.user_id=$1
 LEFT JOIN messages original ON original.id=m.reply_to_id AND original.conversation_id=c.id
 WHERE c.id=ANY($6::text[]) AND m.retracted_at IS NULL AND m.sender_id<>$1
   AND (c.type='channel' OR EXISTS(SELECT 1 FROM conversation_members cm WHERE cm.conversation_id=c.id AND cm.user_id=$1))
   AND (m.mentions @> jsonb_build_array(jsonb_build_object('userId',$1::text)) OR (original.sender_id=$1 AND original.retracted_at IS NULL))
), page AS (
 SELECT * FROM candidates
 WHERE ($2::timestamptz IS NULL OR (created_at,id)<($2,$3::text)) AND (NOT $4::boolean OR unread)
 ORDER BY created_at DESC,id DESC LIMIT $5
)
SELECT (SELECT count(*) FROM candidates WHERE unread),
 (SELECT count(*) FROM candidates WHERE unread AND conversation_type='channel'),
 COALESCE((SELECT jsonb_agg(jsonb_build_object(
   'message',jsonb_build_object('id',m.id,'conversationId',m.conversation_id,'senderId',m.sender_id,'senderName',m.sender_name,'senderKind',m.sender_kind,'clientMessageId',m.client_message_id,'seq',m.seq,'body',m.body,'replyToId',m.reply_to_id,'createdAt',m.created_at,'editedAt',m.edited_at,'retractedAt',m.retracted_at,'attachments',m.attachments,'mentions',m.mentions),
   'conversation',jsonb_build_object('id',c.id,'workspaceId',c.workspace_id,'name',c.name,'type',c.type),
   'workspaceName',w.name,'isRead',NOT p.unread,
   'reasons',to_jsonb(array_remove(ARRAY[CASE WHEN p.mentioned THEN 'mention' END,CASE WHEN p.replied THEN 'reply' END],NULL))
 ) ORDER BY p.created_at DESC,p.id DESC)
 FROM page p JOIN messages m ON m.id=p.id JOIN conversations c ON c.id=m.conversation_id JOIN workspaces w ON w.id=c.workspace_id),'[]'::jsonb)
`, userID, before, cursorID, unreadOnly, limit, conversationIDs).Scan(&unreadCount, &channelUnreadCount, &rawItems)
	if err != nil {
		return 0, 0, nil, err
	}
	items := []domain.AttentionItem{}
	if err = json.Unmarshal(rawItems, &items); err != nil {
		return 0, 0, nil, err
	}
	ids := make([]string, 0, len(items))
	for _, it := range items {
		ids = append(ids, it.Message.ID)
	}
	byID, err := r.loadReactionsByMessageIDs(ctx, ids)
	if err != nil {
		return 0, 0, nil, err
	}
	for i := range items {
		items[i].Message.Reactions = byID[items[i].Message.ID]
	}
	return unreadCount, channelUnreadCount, items, nil
}
