package pg

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"teamie/services/chat/internal/domain"
)

func escapeLikePattern(term string) string {
	var b strings.Builder
	b.Grow(len(term) + 2)
	b.WriteByte('%')
	for _, r := range term {
		switch r {
		case '\\', '%', '_':
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	b.WriteByte('%')
	return b.String()
}

// appendGlobalSearchFilters appends strictly parameterized WHERE predicates shared
// between the unlocked candidate scan and the locked verification query.
func appendGlobalSearchFilters(conds *[]string, args *[]any, q domain.GlobalSearchQuery) {
	nextArg := func(v any) string {
		*args = append(*args, v)
		return fmt.Sprintf("$%d", len(*args))
	}
	if q.WorkspaceID != "" {
		*conds = append(*conds, "c.workspace_id="+nextArg(q.WorkspaceID))
	}
	if q.ConversationID != "" {
		*conds = append(*conds, "m.conversation_id="+nextArg(q.ConversationID))
	}
	if q.SenderID != "" {
		*conds = append(*conds, "m.sender_id="+nextArg(q.SenderID))
	}
	if q.HasFiles {
		*conds = append(*conds, "jsonb_array_length(m.attachments||COALESCE(m.forwarded->'attachments','[]'::jsonb))>0")
	}
	if q.After != nil {
		*conds = append(*conds, "m.created_at>="+nextArg(*q.After))
	}
	if q.Before != nil {
		*conds = append(*conds, "m.created_at<="+nextArg(*q.Before))
	}
	if q.HasCursor {
		atArg := nextArg(q.Cursor.At)
		idArg := nextArg(q.Cursor.ID)
		*conds = append(*conds, fmt.Sprintf("(m.created_at,m.id)<(%s::timestamptz,%s::text)", atArg, idArg))
	}
	for _, term := range q.Terms {
		lowerTerm := strings.ToLower(term)
		rawArg := nextArg(lowerTerm)
		if q.HasFiles {
			*conds = append(*conds, fmt.Sprintf(
				"(strpos(lower(m.body),%s)>0 OR strpos(lower(COALESCE(m.forwarded->>'body','')),%s)>0 OR EXISTS(SELECT 1 FROM jsonb_array_elements(m.attachments||COALESCE(m.forwarded->'attachments','[]'::jsonb)) AS file WHERE strpos(lower(file->>'filename'),%s)>0))",
				rawArg, rawArg, rawArg,
			))
			continue
		}
		if utf8.RuneCountInString(lowerTerm) >= 3 {
			// Matches the GIN pg_trgm expression index on messages while also
			// verifying exact non-cross-boundary substring presence.
			likeArg := nextArg(escapeLikePattern(lowerTerm))
			*conds = append(*conds, fmt.Sprintf(
				"(lower(m.body || ' ' || COALESCE(m.forwarded->>'body', '')) LIKE %s ESCAPE '\\' AND (strpos(lower(m.body),%s)>0 OR strpos(lower(COALESCE(m.forwarded->>'body','')),%s)>0))",
				likeArg, rawArg, rawArg,
			))
		} else {
			*conds = append(*conds, fmt.Sprintf(
				"(strpos(lower(m.body),%s)>0 OR strpos(lower(COALESCE(m.forwarded->>'body','')),%s)>0)",
				rawArg, rawArg,
			))
		}
	}
}

func (r *Repository) SearchGlobalCandidateIDs(ctx context.Context, userID string, q domain.GlobalSearchQuery, limit int) ([]string, error) {
	args := []any{userID}
	conds := []string{
		"m.retracted_at IS NULL",
		"(c.type='channel' OR EXISTS(SELECT 1 FROM conversation_members cm WHERE cm.conversation_id=c.id AND cm.user_id=$1))",
	}
	appendGlobalSearchFilters(&conds, &args, q)
	args = append(args, limit)
	sql := fmt.Sprintf(`
SELECT m.id
FROM messages m
JOIN conversations c ON c.id=m.conversation_id
JOIN workspace_members wm ON wm.workspace_id=c.workspace_id AND wm.user_id=$1
WHERE %s
ORDER BY m.created_at DESC,m.id DESC
LIMIT $%d
`, strings.Join(conds, " AND "), len(args))
	rows, err := r.DB.Conn(ctx).Query(ctx, sql, args...)
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

func (r *Repository) LockGlobalSearchWorkspacesForShare(ctx context.Context, userID string, candidateMessageIDs []string) ([]string, error) {
	return r.lockIDs(ctx, `
SELECT w.id
FROM workspaces w
JOIN workspace_members wm ON wm.workspace_id=w.id AND wm.user_id=$1
WHERE w.id IN (
  SELECT c.workspace_id
  FROM messages m
  JOIN conversations c ON c.id=m.conversation_id
  WHERE m.id=ANY($2::text[])
)
ORDER BY w.id
FOR SHARE OF w
`, userID, candidateMessageIDs)
}

func (r *Repository) LockGlobalSearchConversationsForShare(ctx context.Context, userID string, workspaceIDs, candidateMessageIDs []string) ([]string, error) {
	return r.lockIDs(ctx, `
SELECT c.id
FROM conversations c
JOIN workspace_members wm ON wm.workspace_id=c.workspace_id AND wm.user_id=$1
WHERE c.workspace_id=ANY($2::text[])
  AND c.id IN (SELECT m.conversation_id FROM messages m WHERE m.id=ANY($3::text[]))
  AND (c.type='channel' OR EXISTS(SELECT 1 FROM conversation_members cm WHERE cm.conversation_id=c.id AND cm.user_id=$1))
ORDER BY c.id
FOR SHARE OF c
`, userID, workspaceIDs, candidateMessageIDs)
}

func (r *Repository) QueryVerifiedGlobalSearchItems(ctx context.Context, userID string, q domain.GlobalSearchQuery, limit int, candidateMessageIDs, conversationIDs []string) ([]domain.GlobalSearchItem, error) {
	args := []any{userID, candidateMessageIDs, conversationIDs}
	conds := []string{
		"m.id=ANY($2::text[])",
		"c.id=ANY($3::text[])",
		"m.retracted_at IS NULL",
		"(c.type='channel' OR EXISTS(SELECT 1 FROM conversation_members cm WHERE cm.conversation_id=c.id AND cm.user_id=$1))",
	}
	appendGlobalSearchFilters(&conds, &args, q)
	args = append(args, limit)
	sql := fmt.Sprintf(`
WITH page AS (
  SELECT m.id,m.created_at
  FROM messages m
  JOIN conversations c ON c.id=m.conversation_id
  JOIN workspace_members wm ON wm.workspace_id=c.workspace_id AND wm.user_id=$1
  WHERE %s
  ORDER BY m.created_at DESC,m.id DESC
  LIMIT $%d
)
SELECT COALESCE((SELECT jsonb_agg(jsonb_build_object(
  'message',jsonb_build_object('id',m.id,'conversationId',m.conversation_id,'senderId',m.sender_id,'senderName',m.sender_name,'senderKind',m.sender_kind,'clientMessageId',m.client_message_id,'seq',m.seq,'body',m.body,'replyToId',m.reply_to_id,'createdAt',m.created_at,'editedAt',m.edited_at,'retractedAt',m.retracted_at,'attachments',m.attachments,'mentions',m.mentions,'forwarded',m.forwarded),
  'conversation',jsonb_build_object('id',c.id,'workspaceId',c.workspace_id,'name',c.name,'type',c.type),
  'workspaceName',w.name
) ORDER BY p.created_at DESC,p.id DESC)
FROM page p
JOIN messages m ON m.id=p.id
JOIN conversations c ON c.id=m.conversation_id
JOIN workspaces w ON w.id=c.workspace_id),'[]'::jsonb)
`, strings.Join(conds, " AND "), len(args))
	var rawItems []byte
	if err := r.DB.Conn(ctx).QueryRow(ctx, sql, args...).Scan(&rawItems); err != nil {
		return nil, err
	}
	items := []domain.GlobalSearchItem{}
	if err := json.Unmarshal(rawItems, &items); err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(items))
	for _, it := range items {
		ids = append(ids, it.Message.ID)
	}
	byID, err := r.loadReactionStatesByMessageIDs(ctx, ids)
	if err != nil {
		return nil, err
	}
	for i := range items {
		items[i].Message.Reactions = byID[items[i].Message.ID].Reactions
		items[i].Message.ReactionsVersion = byID[items[i].Message.ID].ReactionsVersion
	}
	return items, nil
}

func (r *Repository) ListConversationAttachments(ctx context.Context, conversationID string, q domain.AttachmentGalleryQuery, limit int) ([]domain.ConversationAttachmentItem, error) {
	var cursorSeq int64
	var cursorID string
	if q.HasCursor {
		cursorSeq = q.Cursor.Seq
		cursorID = q.Cursor.ID
	}
	rows, err := r.DB.Conn(ctx).Query(ctx, `
SELECT
  a.id, a.filename, a.media_type, a.size_bytes, a.sha256, a.image_width, a.image_height,
  m.id, m.seq, m.sender_id, m.sender_name, m.sender_kind, m.created_at,
  (a.copied_from IS NOT NULL) AS is_forwarded
FROM attachments a
JOIN messages m ON m.id=a.message_id
WHERE a.conversation_id=$1
  AND a.message_id IS NOT NULL
  AND m.conversation_id=$1
  AND m.retracted_at IS NULL
  AND (
    $2::text='all'
    OR ($2::text='image' AND (a.image_width IS NOT NULL OR a.media_type IN ('image/png','image/jpeg','image/gif','image/webp')))
    OR ($2::text='file' AND a.image_width IS NULL AND a.media_type NOT IN ('image/png','image/jpeg','image/gif','image/webp'))
  )
  AND ($3::text='' OR strpos(lower(a.filename),lower($3))>0)
  AND ($4::bigint=0 OR (m.seq,a.id)<($4,$5::text))
ORDER BY m.seq DESC, a.id DESC
LIMIT $6
`, conversationID, q.Kind, q.Query, cursorSeq, cursorID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.ConversationAttachmentItem{}
	for rows.Next() {
		var item domain.ConversationAttachmentItem
		var width, height *int
		var createdAt time.Time
		if err = rows.Scan(
			&item.Attachment.ID,
			&item.Attachment.Filename,
			&item.Attachment.MediaType,
			&item.Attachment.SizeBytes,
			&item.Attachment.SHA256,
			&width,
			&height,
			&item.MessageID,
			&item.MessageSeq,
			&item.SenderID,
			&item.SenderName,
			&item.SenderKind,
			&createdAt,
			&item.IsForwarded,
		); err != nil {
			return nil, err
		}
		if width != nil && height != nil {
			item.Attachment.Image = &domain.AttachmentImage{Width: *width, Height: *height}
		}
		item.CreatedAt = createdAt
		out = append(out, item)
	}
	return out, rows.Err()
}
