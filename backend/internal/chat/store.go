package chat

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"sort"
	"strconv"
	"teamie/backend/internal/api"
	"teamie/backend/internal/config"
	"teamie/backend/internal/database"
	"time"
)

//go:embed schema.sql
var schema string

//go:embed migrations/002_membership_management.sql
var schemaV2 string

//go:embed migrations/003_attachments.sql
var schemaV3 string

//go:embed migrations/004_attachment_uploads.sql
var schemaV4 string

type Store struct {
	DB    *pgxpool.Pool
	Flags config.Flags
}
type querier interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

func Open(ctx context.Context, url string, flags config.Flags) (*Store, error) {
	db, e := database.Open(ctx, url)
	if e != nil {
		return nil, e
	}
	s := &Store{db, flags}
	if e = s.Migrate(ctx); e != nil {
		db.Close()
		return nil, e
	}
	return s, nil
}
func (s *Store) Migrate(ctx context.Context) error {
	return database.Migrate(ctx, s.DB, 704102, "chat_schema_migrations", []database.Migration{{Version: 1, SQL: schema}, {Version: 2, SQL: schemaV2}, {Version: 3, SQL: schemaV3}, {Version: 4, SQL: schemaV4}})
}
func (s *Store) Ping(ctx context.Context) error { return s.DB.Ping(ctx) }
func notFound(e error) error {
	if errors.Is(e, pgx.ErrNoRows) {
		return api.Err(404, "not_found", "This resource is unavailable")
	}
	return e
}
func (s *Store) requireFlag(key string) error {
	if !s.Flags.Load().Flags[key] {
		return api.Err(403, "feature_disabled", "This action is currently disabled")
	}
	return nil
}
func workspaceLock(ctx context.Context, tx pgx.Tx, id, user string, owner, exclusive bool) error {
	clause := " FOR SHARE OF w"
	if exclusive {
		clause = " FOR UPDATE OF w"
	}
	var role string
	e := tx.QueryRow(ctx, `SELECT m.role FROM workspaces w JOIN workspace_members m ON m.workspace_id=w.id WHERE w.id=$1 AND m.user_id=$2`+clause, id, user).Scan(&role)
	if e != nil {
		return notFound(e)
	}
	if owner && role != "owner" {
		return api.Err(403, "forbidden", "Workspace owner permission is required")
	}
	return nil
}
func conversationAccess(ctx context.Context, q querier, id, user string) (Conversation, error) {
	var c Conversation
	var role string
	e := q.QueryRow(ctx, `SELECT c.id,c.workspace_id,c.type,c.name,c.last_seq,COALESCE(r.seq,0),c.updated_at,COALESCE(c.created_by,''),wm.role FROM conversations c JOIN workspace_members wm ON wm.workspace_id=c.workspace_id AND wm.user_id=$2 LEFT JOIN read_cursors r ON r.conversation_id=c.id AND r.user_id=$2 WHERE c.id=$1 AND (c.type='channel' OR EXISTS(SELECT 1 FROM conversation_members cm WHERE cm.conversation_id=c.id AND cm.user_id=$2))`, id, user).Scan(&c.ID, &c.WorkspaceID, &c.Type, &c.Name, &c.LastSeq, &c.ReadSeq, &c.UpdatedAt, &c.CreatedBy, &role)
	if e != nil {
		return c, notFound(e)
	}
	c.CanManageMembers = c.Type == "group" && (c.CreatedBy == user || role == "owner")
	c.MemberIDs, e = recipients(ctx, q, c)
	return c, e
}
func recipients(ctx context.Context, q querier, c Conversation) ([]string, error) {
	rows, e := q.Query(ctx, `SELECT wm.user_id FROM workspace_members wm WHERE wm.workspace_id=$1 AND ($2='channel' OR EXISTS(SELECT 1 FROM conversation_members cm WHERE cm.conversation_id=$3 AND cm.user_id=wm.user_id)) ORDER BY wm.user_id`, c.WorkspaceID, c.Type, c.ID)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	ids := []string{}
	for rows.Next() {
		var id string
		if e = rows.Scan(&id); e != nil {
			return nil, e
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
func memberIDs(ctx context.Context, q querier, ws string) ([]string, error) {
	return recipients(ctx, q, Conversation{WorkspaceID: ws, Type: "channel"})
}
func lockConversation(ctx context.Context, tx pgx.Tx, id, user string) (Conversation, error) {
	var ws string
	if e := tx.QueryRow(ctx, "SELECT workspace_id FROM conversations WHERE id=$1", id).Scan(&ws); e != nil {
		return Conversation{}, notFound(e)
	}
	if e := workspaceLock(ctx, tx, ws, user, false, false); e != nil {
		return Conversation{}, e
	}
	if _, e := tx.Exec(ctx, "SELECT id FROM conversations WHERE id=$1 FOR UPDATE", id); e != nil {
		return Conversation{}, e
	}
	return conversationAccess(ctx, tx, id, user)
}

// Sorted locks make fanout atomic and deadlock-free across overlapping audiences.
func fanout(ctx context.Context, tx pgx.Tx, ids []string, event Event) error {
	ids = append([]string(nil), ids...)
	sort.Strings(ids)
	event.CreatedAt = time.Now().UTC()
	last := ""
	for _, id := range ids {
		if id == last {
			continue
		}
		last = id
		if _, e := tx.Exec(ctx, "INSERT INTO sync_heads(user_id) VALUES($1) ON CONFLICT DO NOTHING", id); e != nil {
			return e
		}
		var cursor int64
		if e := tx.QueryRow(ctx, "UPDATE sync_heads SET last_id=last_id+1 WHERE user_id=$1 RETURNING last_id", id).Scan(&cursor); e != nil {
			return e
		}
		if event.Conversation != nil {
			c, e := conversationAccess(ctx, tx, event.Conversation.ID, id)
			if e != nil {
				return e
			}
			event.Conversation = &c
		}
		event.ID = strconv.FormatInt(cursor, 10)
		b, e := json.Marshal(event)
		if e != nil {
			return e
		}
		if _, e = tx.Exec(ctx, "INSERT INTO sync_events(user_id,id,workspace_id,conversation_id,payload) VALUES($1,$2,NULLIF($3,''),NULLIF($4,''),$5)", id, cursor, event.WorkspaceID, event.ConversationID, b); e != nil {
			return e
		}
	}
	b, e := json.Marshal(map[string]any{"type": "sync.required", "userIds": ids})
	if e != nil {
		return e
	}
	_, e = tx.Exec(ctx, "INSERT INTO outbox(payload) VALUES($1)", b)
	return e
}
func (s *Store) Workspaces(ctx context.Context, user string) ([]Workspace, error) {
	rows, e := s.DB.Query(ctx, `SELECT w.id,w.name,w.created_by,w.created_at FROM workspaces w JOIN workspace_members m ON m.workspace_id=w.id WHERE m.user_id=$1 ORDER BY w.created_at,w.id`, user)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []Workspace{}
	for rows.Next() {
		var w Workspace
		if e = rows.Scan(&w.ID, &w.Name, &w.CreatedBy, &w.CreatedAt); e != nil {
			return nil, e
		}
		out = append(out, w)
	}
	return out, rows.Err()
}
func (s *Store) Conversations(ctx context.Context, user, ws string) ([]Conversation, error) {
	if ws != "" {
		var yes bool
		if e := s.DB.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM workspace_members WHERE workspace_id=$1 AND user_id=$2)", ws, user).Scan(&yes); e != nil {
			return nil, e
		}
		if !yes {
			return nil, api.Err(404, "not_found", "Workspace is unavailable")
		}
	}
	rows, e := s.DB.Query(ctx, `SELECT c.id FROM conversations c JOIN workspace_members wm ON wm.workspace_id=c.workspace_id WHERE wm.user_id=$1 AND ($2='' OR c.workspace_id=$2) AND (c.type='channel' OR EXISTS(SELECT 1 FROM conversation_members cm WHERE cm.conversation_id=c.id AND cm.user_id=$1)) ORDER BY c.updated_at,c.id`, user, ws)
	if e != nil {
		return nil, e
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if e = rows.Scan(&id); e != nil {
			rows.Close()
			return nil, e
		}
		ids = append(ids, id)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return nil, e
	}
	out := []Conversation{}
	for _, id := range ids {
		c, e := conversationAccess(ctx, s.DB, id, user)
		if e != nil {
			var ae *api.Error
			if errors.As(e, &ae) && ae.Status == 404 {
				continue
			}
			return nil, e
		}
		out = append(out, c)
	}
	return out, nil
}
func (s *Store) Head(ctx context.Context, user string) (string, error) {
	var n int64
	e := s.DB.QueryRow(ctx, "SELECT COALESCE((SELECT last_id FROM sync_heads WHERE user_id=$1),0)", user).Scan(&n)
	return strconv.FormatInt(n, 10), e
}
func (s *Store) Sync(ctx context.Context, user, cursor string, limit int) ([]Event, string, bool, error) {
	if cursor == "" {
		cursor = "0"
	}
	n, e := strconv.ParseInt(cursor, 10, 64)
	if e != nil || n < 0 {
		return nil, "", false, api.Err(400, "invalid_cursor", "Cursor must be a non-negative decimal")
	}
	head, e := s.Head(ctx, user)
	if e != nil {
		return nil, "", false, e
	}
	h, _ := strconv.ParseInt(head, 10, 64)
	if n > h {
		return nil, "", false, api.Err(400, "invalid_cursor", "Cursor is ahead of this user's stream")
	}
	// Select an unfiltered page and advance over revoked payloads as well.
	rows, e := s.DB.Query(ctx, `SELECT e.id,e.payload,COALESCE(m.retracted_at IS NOT NULL,false),(e.payload->>'type'='membership.changed' OR (EXISTS(SELECT 1 FROM workspace_members wm WHERE wm.workspace_id=e.workspace_id AND wm.user_id=$1) AND (e.conversation_id IS NULL OR EXISTS(SELECT 1 FROM conversations c WHERE c.id=e.conversation_id AND (c.type='channel' OR EXISTS(SELECT 1 FROM conversation_members cm WHERE cm.conversation_id=c.id AND cm.user_id=$1)))))) AS allowed FROM sync_events e LEFT JOIN messages m ON m.id=e.payload#>>'{message,id}' WHERE e.user_id=$1 AND e.id>$2 AND e.id<=$3 ORDER BY e.id LIMIT $4`, user, n, h, limit)
	if e != nil {
		return nil, "", false, e
	}
	defer rows.Close()
	out := []Event{}
	for rows.Next() {
		var id int64
		var b []byte
		var allowed, deleted bool
		if e = rows.Scan(&id, &b, &deleted, &allowed); e != nil {
			return nil, "", false, e
		}
		n = id
		if !allowed {
			continue
		}
		var ev Event
		if e = json.Unmarshal(b, &ev); e != nil {
			return nil, "", false, fmt.Errorf("decode persisted event: %w", e)
		}
		// Erase body from historical sync events after a later retraction, too.
		if ev.Message != nil && deleted {
			ev.Message.Body = ""
			ev.Message.Attachments = nil
		}
		out = append(out, ev)
	}
	return out, strconv.FormatInt(n, 10), n < h, rows.Err()
}
