package chat

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"teamie/backend/internal/api"
)

type AttentionConversation struct {
	ID          string `json:"id"`
	WorkspaceID string `json:"workspaceId"`
	Name        string `json:"name"`
	Type        string `json:"type"`
}

type AttentionItem struct {
	Message       Message               `json:"message"`
	Conversation  AttentionConversation `json:"conversation"`
	WorkspaceName string                `json:"workspaceName"`
	Reasons       []string              `json:"reasons"`
	IsRead        bool                  `json:"isRead"`
}

type AttentionPage struct {
	Items       []AttentionItem `json:"items"`
	Cursor      string          `json:"cursor,omitempty"`
	UnreadCount int64           `json:"unreadCount"`
}

type attentionCursor struct {
	At time.Time `json:"at"`
	ID string    `json:"id"`
}

func parseAttentionCursor(raw string) (attentionCursor, error) {
	var cursor attentionCursor
	if raw == "" {
		return cursor, nil
	}
	if len(raw) > 512 {
		return cursor, api.Err(400, "invalid_cursor", "Invalid attention cursor")
	}
	b, err := base64.RawURLEncoding.Strict().DecodeString(raw)
	if err != nil || json.Unmarshal(b, &cursor) != nil || cursor.At.IsZero() || cursor.ID == "" || len(cursor.ID) > 128 || strings.ContainsRune(cursor.ID, 0) {
		return cursor, api.Err(400, "invalid_cursor", "Invalid attention cursor")
	}
	return cursor, nil
}

func attentionLockIDs(ctx context.Context, tx pgx.Tx, query string, args ...any) ([]string, error) {
	rows, err := tx.Query(ctx, query, args...)
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

// Attention is a current, ACL-filtered projection of ordinary IM messages. No
// notification copies or Agent runtime resources are created. Sorted read locks
// order membership revocations with the projection using the normal lock order.
func (s *Store) Attention(ctx context.Context, user, rawCursor string, limit int, unreadOnly bool) (AttentionPage, error) {
	out := AttentionPage{Items: []AttentionItem{}}
	if limit < 1 || limit > 100 {
		return out, api.Err(400, "invalid_query", "Invalid attention page size")
	}
	cursor, err := parseAttentionCursor(rawCursor)
	if err != nil {
		return out, err
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return out, err
	}
	defer tx.Rollback(ctx)
	workspaces, err := attentionLockIDs(ctx, tx, `SELECT w.id FROM workspaces w JOIN workspace_members wm ON wm.workspace_id=w.id AND wm.user_id=$1 ORDER BY w.id FOR SHARE OF w`, user)
	if err != nil {
		return out, err
	}
	conversations, err := attentionLockIDs(ctx, tx, `SELECT c.id FROM conversations c JOIN workspace_members wm ON wm.workspace_id=c.workspace_id AND wm.user_id=$1 WHERE c.workspace_id=ANY($2::text[]) AND (c.type='channel' OR EXISTS(SELECT 1 FROM conversation_members cm WHERE cm.conversation_id=c.id AND cm.user_id=$1)) ORDER BY c.id FOR SHARE OF c`, user, workspaces)
	if err != nil {
		return out, err
	}
	if len(conversations) == 0 {
		return out, nil
	}
	var before any
	if rawCursor != "" {
		before = cursor.At
	}
	var rawItems []byte
	err = tx.QueryRow(ctx, `
WITH candidates AS MATERIALIZED (
 SELECT m.id,m.created_at,m.seq>COALESCE(r.seq,0) AS unread,
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
 COALESCE((SELECT jsonb_agg(jsonb_build_object(
   'message',jsonb_build_object('id',m.id,'conversationId',m.conversation_id,'senderId',m.sender_id,'senderName',m.sender_name,'senderKind',m.sender_kind,'clientMessageId',m.client_message_id,'seq',m.seq,'body',m.body,'replyToId',m.reply_to_id,'createdAt',m.created_at,'editedAt',m.edited_at,'retractedAt',m.retracted_at,'attachments',m.attachments,'mentions',m.mentions),
   'conversation',jsonb_build_object('id',c.id,'workspaceId',c.workspace_id,'name',c.name,'type',c.type),
   'workspaceName',w.name,'isRead',NOT p.unread,
   'reasons',to_jsonb(array_remove(ARRAY[CASE WHEN p.mentioned THEN 'mention' END,CASE WHEN p.replied THEN 'reply' END],NULL))
 ) ORDER BY p.created_at DESC,p.id DESC)
 FROM page p JOIN messages m ON m.id=p.id JOIN conversations c ON c.id=m.conversation_id JOIN workspaces w ON w.id=c.workspace_id),'[]'::jsonb)
`, user, before, cursor.ID, unreadOnly, limit+1, conversations).Scan(&out.UnreadCount, &rawItems)
	if err != nil {
		return out, err
	}
	if err = json.Unmarshal(rawItems, &out.Items); err != nil {
		return out, err
	}
	if len(out.Items) > limit {
		out.Items = out.Items[:limit]
		last := out.Items[len(out.Items)-1].Message
		raw, err := json.Marshal(attentionCursor{At: last.CreatedAt, ID: last.ID})
		if err != nil {
			return out, err
		}
		out.Cursor = base64.RawURLEncoding.EncodeToString(raw)
	}
	return out, nil
}
