package chat

import (
	"context"
	"errors"
	"github.com/jackc/pgx/v5"
	"strings"
	"teamie/backend/internal/api"
	"time"
)

const messageColumns = "id,conversation_id,sender_id,sender_name,sender_kind,client_message_id,seq,body,reply_to_id,created_at,edited_at,retracted_at"

type scanner interface{ Scan(...any) error }

func scanMessage(row scanner) (Message, error) {
	var m Message
	e := row.Scan(&m.ID, &m.ConversationID, &m.SenderID, &m.SenderName, &m.SenderKind, &m.ClientMessageID, &m.Seq, &m.Body, &m.ReplyToID, &m.CreatedAt, &m.EditedAt, &m.RetractedAt)
	if m.RetractedAt != nil {
		m.Body = ""
	}
	return m, e
}
func validateBody(body string) error {
	if strings.TrimSpace(body) == "" || len([]rune(body)) > 8000 {
		return api.Err(400, "invalid_body", "Message text must contain 1 to 8000 characters")
	}
	return nil
}
func (s *Store) Message(ctx context.Context, user, conversation, id string) (Message, error) {
	var m Message
	tx, e := s.DB.Begin(ctx)
	if e != nil {
		return m, e
	}
	defer tx.Rollback(ctx)
	var ws string
	if e = tx.QueryRow(ctx, "SELECT workspace_id FROM conversations WHERE id=$1", conversation).Scan(&ws); e != nil {
		return m, notFound(e)
	}
	if e = workspaceLock(ctx, tx, ws, user, false, false); e != nil {
		return m, e
	}
	if _, e = conversationAccess(ctx, tx, conversation, user); e != nil {
		return m, e
	}
	m, e = scanMessage(tx.QueryRow(ctx, "SELECT "+messageColumns+" FROM messages WHERE id=$1 AND conversation_id=$2", id, conversation))
	return m, notFound(e)
}
func (s *Store) Messages(ctx context.Context, user, conversation string, before int64, limit int) ([]Message, bool, error) {
	tx, e := s.DB.Begin(ctx)
	if e != nil {
		return nil, false, e
	}
	defer tx.Rollback(ctx)
	var ws string
	if e = tx.QueryRow(ctx, "SELECT workspace_id FROM conversations WHERE id=$1", conversation).Scan(&ws); e != nil {
		return nil, false, notFound(e)
	}
	if e = workspaceLock(ctx, tx, ws, user, false, false); e != nil {
		return nil, false, e
	}
	if _, e = conversationAccess(ctx, tx, conversation, user); e != nil {
		return nil, false, e
	}
	rows, e := tx.Query(ctx, "SELECT "+messageColumns+" FROM messages WHERE conversation_id=$1 AND ($2::bigint=0 OR seq<$2) ORDER BY seq DESC LIMIT $3", conversation, before, limit+1)
	if e != nil {
		return nil, false, e
	}
	defer rows.Close()
	out := []Message{}
	for rows.Next() {
		m, e := scanMessage(rows)
		if e != nil {
			return nil, false, e
		}
		out = append(out, m)
	}
	if e = rows.Err(); e != nil {
		return nil, false, e
	}
	more := len(out) > limit
	if more {
		out = out[:limit]
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, more, nil
}
func (s *Store) Send(ctx context.Context, user api.User, conversation, clientID, body string, reply *string) (Message, bool, error) {
	var m Message
	if e := validateBody(body); e != nil {
		return m, false, e
	}
	if !api.BoundedString(clientID, 1, 128) {
		return m, false, api.Err(400, "invalid_client_message_id", "clientMessageId is required and must be at most 128 characters")
	}
	if reply != nil && *reply == "" {
		reply = nil
	}
	tx, e := s.DB.Begin(ctx)
	if e != nil {
		return m, false, e
	}
	defer tx.Rollback(ctx)
	// Serialize a sender's idempotency key even when requests name different conversations.
	if _, e = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1,704103))", user.ID+":"+clientID); e != nil {
		return m, false, e
	}
	c, e := lockConversation(ctx, tx, conversation, user.ID)
	if e != nil {
		return m, false, e
	}
	m, e = scanMessage(tx.QueryRow(ctx, "SELECT "+messageColumns+" FROM messages WHERE sender_id=$1 AND client_message_id=$2", user.ID, clientID))
	if e == nil {
		var hash string
		if e = tx.QueryRow(ctx, "SELECT original_body_hash FROM messages WHERE id=$1", m.ID).Scan(&hash); e != nil {
			return m, false, e
		}
		sameReply := (reply == nil && m.ReplyToID == nil) || (reply != nil && m.ReplyToID != nil && *reply == *m.ReplyToID)
		if m.ConversationID != conversation || hash != api.Hash(body) || !sameReply {
			return m, false, api.Err(409, "idempotency_conflict", "This clientMessageId was used for a different message")
		}
		return m, false, tx.Commit(ctx)
	}
	if !errors.Is(e, pgx.ErrNoRows) {
		return m, false, e
	}
	if reply != nil {
		var exists bool
		e = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM messages WHERE id=$1 AND conversation_id=$2)", *reply, conversation).Scan(&exists)
		if e != nil {
			return m, false, e
		}
		if !exists {
			return m, false, api.Err(400, "invalid_reply", "Reply target must belong to this conversation")
		}
	}
	m = Message{ID: api.ID(), ConversationID: conversation, SenderID: user.ID, SenderName: user.DisplayName, SenderKind: user.Kind, ClientMessageID: clientID, Body: body, ReplyToID: reply}
	e = tx.QueryRow(ctx, "UPDATE conversations SET last_seq=last_seq+1,updated_at=now() WHERE id=$1 RETURNING last_seq", conversation).Scan(&m.Seq)
	if e != nil {
		return m, false, e
	}
	e = tx.QueryRow(ctx, "INSERT INTO messages(id,conversation_id,sender_id,sender_name,sender_kind,client_message_id,seq,body,original_body_hash,reply_to_id) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10) RETURNING created_at", m.ID, conversation, user.ID, user.DisplayName, user.Kind, clientID, m.Seq, body, api.Hash(body), reply).Scan(&m.CreatedAt)
	if e != nil {
		return m, false, e
	}
	if e = fanout(ctx, tx, c.MemberIDs, Event{Type: "message.created", WorkspaceID: c.WorkspaceID, ConversationID: conversation, Message: &m}); e != nil {
		return m, false, e
	}
	return m, true, tx.Commit(ctx)
}
func (s *Store) ChangeMessage(ctx context.Context, user, conversation, id, body string, retract bool) (Message, error) {
	var m Message
	flag := "im.message.edit"
	if retract {
		flag = "im.message.retract"
	}
	if e := s.requireFlag(flag); e != nil {
		return m, e
	}
	if !retract {
		if e := validateBody(body); e != nil {
			return m, e
		}
	}
	tx, e := s.DB.Begin(ctx)
	if e != nil {
		return m, e
	}
	defer tx.Rollback(ctx)
	c, e := lockConversation(ctx, tx, conversation, user)
	if e != nil {
		return m, e
	}
	m, e = scanMessage(tx.QueryRow(ctx, "SELECT "+messageColumns+" FROM messages WHERE id=$1 AND conversation_id=$2", id, conversation))
	if e != nil {
		return m, notFound(e)
	}
	if m.SenderID != user {
		return m, api.Err(403, "forbidden", "Only the author can change a message")
	}
	event := "message.updated"
	now := time.Now().UTC()
	if retract {
		if m.RetractedAt != nil {
			return m, tx.Commit(ctx)
		}
		m.RetractedAt = &now
		m.Body = ""
		event = "message.retracted"
		_, e = tx.Exec(ctx, "UPDATE messages SET body='',retracted_at=$2 WHERE id=$1", id, now)
	} else {
		if m.RetractedAt != nil {
			return m, api.Err(409, "message_retracted", "A retracted message cannot be edited")
		}
		m.Body = body
		m.EditedAt = &now
		_, e = tx.Exec(ctx, "UPDATE messages SET body=$2,edited_at=$3 WHERE id=$1", id, body, now)
	}
	if e != nil {
		return m, e
	}
	if e = fanout(ctx, tx, c.MemberIDs, Event{Type: event, WorkspaceID: c.WorkspaceID, ConversationID: conversation, Message: &m}); e != nil {
		return m, e
	}
	return m, tx.Commit(ctx)
}
func (s *Store) Read(ctx context.Context, user, conversation string, seq int64) (int64, error) {
	if seq < 0 {
		return 0, api.Err(400, "invalid_seq", "Read sequence cannot be negative")
	}
	tx, e := s.DB.Begin(ctx)
	if e != nil {
		return 0, e
	}
	defer tx.Rollback(ctx)
	c, e := lockConversation(ctx, tx, conversation, user)
	if e != nil {
		return 0, e
	}
	if seq > c.LastSeq {
		seq = c.LastSeq
	}
	if seq <= c.ReadSeq {
		return c.ReadSeq, tx.Commit(ctx)
	}
	_, e = tx.Exec(ctx, "INSERT INTO read_cursors(conversation_id,user_id,seq) VALUES($1,$2,$3) ON CONFLICT(conversation_id,user_id) DO UPDATE SET seq=GREATEST(read_cursors.seq,excluded.seq)", conversation, user, seq)
	if e != nil {
		return 0, e
	}
	if e = fanout(ctx, tx, []string{user}, Event{Type: "read.updated", WorkspaceID: c.WorkspaceID, ConversationID: conversation, ReadSeq: &seq}); e != nil {
		return 0, e
	}
	return seq, tx.Commit(ctx)
}
