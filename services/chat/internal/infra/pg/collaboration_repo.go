package pg

import (
	"context"
	"encoding/json"
	"strings"

	"teamie/pkg/postgres"
	"teamie/services/chat/internal/domain"
)

// --- Pins ---

func (r *Repository) FindPin(ctx context.Context, conversationID, messageID string) (domain.Pin, bool, error) {
	var p domain.Pin
	err := r.DB.Conn(ctx).QueryRow(ctx, "SELECT pinned_by,pinned_by_name,pinned_at FROM conversation_pins WHERE conversation_id=$1 AND message_id=$2", conversationID, messageID).Scan(&p.PinnedBy, &p.PinnedByName, &p.PinnedAt)
	if postgres.IsNoRows(err) {
		return domain.Pin{}, false, nil
	}
	if err != nil {
		return domain.Pin{}, false, err
	}
	p.Message, err = r.GetMessage(ctx, conversationID, messageID)
	return p, err == nil, err
}

func (r *Repository) CountPins(ctx context.Context, conversationID string) (int, error) {
	var n int
	err := r.DB.Conn(ctx).QueryRow(ctx, "SELECT count(*) FROM conversation_pins WHERE conversation_id=$1", conversationID).Scan(&n)
	return n, err
}

// InsertPin stores the pin and advances the per-conversation counter in the
// caller's transaction. The caller holds the conversation row lock.
func (r *Repository) InsertPin(ctx context.Context, conversationID, messageID, pinnedBy, pinnedByName string) (domain.Pin, int64, error) {
	conn := r.DB.Conn(ctx)
	p := domain.Pin{PinnedBy: pinnedBy, PinnedByName: pinnedByName}
	if err := conn.QueryRow(ctx, "INSERT INTO conversation_pins(conversation_id,message_id,pinned_by,pinned_by_name) VALUES($1,$2,$3,$4) RETURNING pinned_at", conversationID, messageID, pinnedBy, pinnedByName).Scan(&p.PinnedAt); err != nil {
		return domain.Pin{}, 0, err
	}
	version, err := r.bumpPinsVersion(ctx, conversationID)
	if err != nil {
		return domain.Pin{}, 0, err
	}
	p.Message, err = r.GetMessage(ctx, conversationID, messageID)
	return p, version, err
}

func (r *Repository) DeletePin(ctx context.Context, conversationID, messageID string) (int64, bool, error) {
	tag, err := r.DB.Conn(ctx).Exec(ctx, "DELETE FROM conversation_pins WHERE conversation_id=$1 AND message_id=$2", conversationID, messageID)
	if err != nil {
		return 0, false, err
	}
	if tag.RowsAffected() == 0 {
		version, err := r.PinsVersion(ctx, conversationID)
		return version, false, err
	}
	version, err := r.bumpPinsVersion(ctx, conversationID)
	return version, true, err
}

func (r *Repository) bumpPinsVersion(ctx context.Context, conversationID string) (int64, error) {
	var version int64
	err := r.DB.Conn(ctx).QueryRow(ctx, "UPDATE conversations SET pins_version=pins_version+1 WHERE id=$1 RETURNING pins_version", conversationID).Scan(&version)
	return version, err
}

func (r *Repository) PinsVersion(ctx context.Context, conversationID string) (int64, error) {
	var version int64
	err := r.DB.Conn(ctx).QueryRow(ctx, "SELECT pins_version FROM conversations WHERE id=$1", conversationID).Scan(&version)
	return version, notFound(err)
}

func (r *Repository) ListPins(ctx context.Context, conversationID string) ([]domain.Pin, error) {
	rows, err := r.DB.Conn(ctx).Query(ctx, `SELECT `+prefixedMessageColumns("m")+`,p.pinned_by,p.pinned_by_name,p.pinned_at
FROM conversation_pins p JOIN messages m ON m.id=p.message_id AND m.conversation_id=p.conversation_id
WHERE p.conversation_id=$1 AND m.retracted_at IS NULL
ORDER BY p.pinned_at DESC,p.message_id DESC LIMIT $2`, conversationID, domain.PinsPerConversation)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.Pin{}
	messages := []domain.Message{}
	for rows.Next() {
		var p domain.Pin
		m, scanErr := scanMessageWith(rows, &p.PinnedBy, &p.PinnedByName, &p.PinnedAt)
		if scanErr != nil {
			return nil, scanErr
		}
		out = append(out, p)
		messages = append(messages, m)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	if err = r.populateMessageReactions(ctx, messages); err != nil {
		return nil, err
	}
	for i := range out {
		out[i].Message = messages[i]
	}
	return out, nil
}

// --- Saved items ---

func (r *Repository) FindSaved(ctx context.Context, userID, messageID string) (domain.SavedMarker, bool, error) {
	var s domain.SavedMarker
	err := r.DB.Conn(ctx).QueryRow(ctx, "SELECT conversation_id,message_id,saved_at FROM saved_messages WHERE user_id=$1 AND message_id=$2", userID, messageID).Scan(&s.ConversationID, &s.MessageID, &s.SavedAt)
	if postgres.IsNoRows(err) {
		return domain.SavedMarker{}, false, nil
	}
	return s, err == nil, err
}

func (r *Repository) CountSaved(ctx context.Context, userID string) (int, error) {
	var n int
	err := r.DB.Conn(ctx).QueryRow(ctx, "SELECT count(*) FROM saved_messages WHERE user_id=$1", userID).Scan(&n)
	return n, err
}

func (r *Repository) InsertSaved(ctx context.Context, userID, conversationID, messageID string) (domain.SavedMarker, error) {
	s := domain.SavedMarker{ConversationID: conversationID, MessageID: messageID}
	err := r.DB.Conn(ctx).QueryRow(ctx, "INSERT INTO saved_messages(user_id,conversation_id,message_id) VALUES($1,$2,$3) RETURNING saved_at", userID, conversationID, messageID).Scan(&s.SavedAt)
	return s, err
}

func (r *Repository) DeleteSaved(ctx context.Context, userID, conversationID, messageID string) (bool, error) {
	tag, err := r.DB.Conn(ctx).Exec(ctx, "DELETE FROM saved_messages WHERE user_id=$1 AND conversation_id=$2 AND message_id=$3", userID, conversationID, messageID)
	return tag.RowsAffected() > 0, err
}

func (r *Repository) DeleteSavedForMessage(ctx context.Context, messageID string) error {
	_, err := r.DB.Conn(ctx).Exec(ctx, "DELETE FROM saved_messages WHERE message_id=$1", messageID)
	return err
}

func (r *Repository) SavedMessageIDs(ctx context.Context, userID, conversationID string) ([]string, error) {
	return r.lockIDs(ctx, `SELECT s.message_id FROM saved_messages s JOIN messages m ON m.id=s.message_id AND m.conversation_id=s.conversation_id
WHERE s.user_id=$1 AND s.conversation_id=$2 AND m.retracted_at IS NULL ORDER BY s.saved_at DESC,s.message_id DESC`, userID, conversationID)
}

// QuerySavedPage returns only items in conversationIDs, which the caller has
// already share-locked and rechecked, so inaccessible items are omitted.
func (r *Repository) QuerySavedPage(ctx context.Context, userID string, before any, cursorID string, limit int, conversationIDs []string) ([]domain.SavedItem, error) {
	var raw []byte
	err := r.DB.Conn(ctx).QueryRow(ctx, `
WITH page AS (
 SELECT s.message_id,s.saved_at
 FROM saved_messages s
 JOIN messages m ON m.id=s.message_id AND m.conversation_id=s.conversation_id
 JOIN conversations c ON c.id=s.conversation_id
 JOIN workspace_members wm ON wm.workspace_id=c.workspace_id AND wm.user_id=$1
 WHERE s.user_id=$1 AND s.conversation_id=ANY($5::text[]) AND m.retracted_at IS NULL
   AND (c.type='channel' OR EXISTS(SELECT 1 FROM conversation_members cm WHERE cm.conversation_id=c.id AND cm.user_id=$1))
   AND ($2::timestamptz IS NULL OR (s.saved_at,s.message_id)<($2,$3::text))
 ORDER BY s.saved_at DESC,s.message_id DESC LIMIT $4
)
SELECT COALESCE((SELECT jsonb_agg(jsonb_build_object(
  'message',jsonb_build_object('id',m.id,'conversationId',m.conversation_id,'senderId',m.sender_id,'senderName',m.sender_name,'senderKind',m.sender_kind,'clientMessageId',m.client_message_id,'seq',m.seq,'body',m.body,'replyToId',m.reply_to_id,'createdAt',m.created_at,'editedAt',m.edited_at,'retractedAt',m.retracted_at,'attachments',m.attachments,'mentions',m.mentions,'forwarded',m.forwarded),
  'conversation',jsonb_build_object('id',c.id,'workspaceId',c.workspace_id,'name',c.name,'type',c.type),
  'workspaceName',w.name,'savedAt',p.saved_at
) ORDER BY p.saved_at DESC,p.message_id DESC)
FROM page p JOIN messages m ON m.id=p.message_id JOIN conversations c ON c.id=m.conversation_id JOIN workspaces w ON w.id=c.workspace_id),'[]'::jsonb)
`, userID, before, cursorID, limit, conversationIDs).Scan(&raw)
	if err != nil {
		return nil, err
	}
	items := []domain.SavedItem{}
	if err = json.Unmarshal(raw, &items); err != nil {
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
		items[i].SavedAt = items[i].SavedAt.UTC()
	}
	return items, nil
}

func prefixedMessageColumns(alias string) string {
	columns := strings.Split(messageColumns, ",")
	for i := range columns {
		columns[i] = alias + "." + columns[i]
	}
	return strings.Join(columns, ",")
}

// scanMessageWith scans message columns followed by extra destinations.
func scanMessageWith(row rowScanner, extra ...any) (domain.Message, error) {
	var m domain.Message
	dest := []any{&m.ID, &m.ConversationID, &m.SenderID, &m.SenderName, &m.SenderKind, &m.ClientMessageID, &m.Seq, &m.Body, &m.ReplyToID, &m.CreatedAt, &m.EditedAt, &m.RetractedAt, &m.Attachments, &m.Mentions, &m.Forwarded}
	err := row.Scan(append(dest, extra...)...)
	if m.RetractedAt != nil {
		m.Body = ""
		m.Attachments = nil
		m.Mentions = nil
		m.Forwarded = nil
	}
	return m, err
}
