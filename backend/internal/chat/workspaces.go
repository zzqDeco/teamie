package chat

import (
	"context"
	"github.com/jackc/pgx/v5"
	"strings"
	"teamie/backend/internal/api"
	"time"
)

func (s *Store) CreateWorkspace(ctx context.Context, user, name string) (Workspace, Conversation, error) {
	var ws Workspace
	var c Conversation
	name = strings.TrimSpace(name)
	if !api.BoundedString(name, 1, 80) {
		return ws, c, api.Err(400, "invalid_name", "Workspace name must contain 1 to 80 characters")
	}
	tx, e := s.DB.Begin(ctx)
	if e != nil {
		return ws, c, e
	}
	defer tx.Rollback(ctx)
	ws = Workspace{ID: api.ID(), Name: name, CreatedBy: user}
	e = tx.QueryRow(ctx, "INSERT INTO workspaces(id,name,created_by) VALUES($1,$2,$3) RETURNING created_at", ws.ID, ws.Name, user).Scan(&ws.CreatedAt)
	if e != nil {
		return ws, c, e
	}
	if _, e = tx.Exec(ctx, "INSERT INTO workspace_members(workspace_id,user_id,role) VALUES($1,$2,'owner')", ws.ID, user); e != nil {
		return ws, c, e
	}
	c = Conversation{ID: api.ID(), WorkspaceID: ws.ID, Type: "channel", Name: "general", CreatedBy: user, MemberIDs: []string{user}}
	if e = tx.QueryRow(ctx, "INSERT INTO conversations(id,workspace_id,type,name,created_by) VALUES($1,$2,$3,$4,$5) RETURNING updated_at", c.ID, ws.ID, c.Type, c.Name, user).Scan(&c.UpdatedAt); e != nil {
		return ws, c, e
	}
	if e = fanout(ctx, tx, []string{user}, Event{Type: "conversation.created", WorkspaceID: ws.ID, ConversationID: c.ID, Conversation: &c}); e != nil {
		return ws, c, e
	}
	return ws, c, tx.Commit(ctx)
}

type Member struct {
	UserID      string `json:"userId"`
	DisplayName string `json:"displayName"`
	Role        string `json:"role"`
	Kind        string `json:"kind"`
}

func (s *Store) Members(ctx context.Context, user, ws string) ([]Member, error) {
	tx, e := s.DB.Begin(ctx)
	if e != nil {
		return nil, e
	}
	defer tx.Rollback(ctx)
	if e = workspaceLock(ctx, tx, ws, user, false, false); e != nil {
		return nil, e
	}
	rows, e := tx.Query(ctx, "SELECT user_id,role FROM workspace_members WHERE workspace_id=$1 ORDER BY role,user_id", ws)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []Member{}
	for rows.Next() {
		var m Member
		if e = rows.Scan(&m.UserID, &m.Role); e != nil {
			return nil, e
		}
		m.Kind = "human"
		m.DisplayName = m.UserID
		out = append(out, m)
	}
	return out, rows.Err()
}
func (s *Store) CreateInvite(ctx context.Context, user, ws string) (string, time.Time, error) {
	tx, e := s.DB.Begin(ctx)
	if e != nil {
		return "", time.Time{}, e
	}
	defer tx.Rollback(ctx)
	if e = workspaceLock(ctx, tx, ws, user, true, false); e != nil {
		return "", time.Time{}, e
	}
	code, expiry := api.Random(24), time.Now().UTC().Add(7*24*time.Hour)
	if _, e = tx.Exec(ctx, "INSERT INTO workspace_invites(code_hash,workspace_id,created_by,expires_at) VALUES($1,$2,$3,$4)", api.Hash(code), ws, user, expiry); e != nil {
		return "", expiry, e
	}
	return code, expiry, tx.Commit(ctx)
}
func (s *Store) Invite(ctx context.Context, code string) (Workspace, time.Time, error) {
	var ws Workspace
	var expiry time.Time
	if len(code) != 48 {
		return ws, expiry, api.Err(404, "not_found", "Invitation is unavailable or expired")
	}
	e := s.DB.QueryRow(ctx, `SELECT w.id,w.name,i.expires_at FROM workspace_invites i JOIN workspaces w ON w.id=i.workspace_id WHERE i.code_hash=$1 AND i.expires_at>now()`, api.Hash(code)).Scan(&ws.ID, &ws.Name, &expiry)
	return ws, expiry, notFound(e)
}
func (s *Store) AcceptInvite(ctx context.Context, user, code string) (Workspace, []Conversation, error) {
	var ws Workspace
	tx, e := s.DB.Begin(ctx)
	if e != nil {
		return ws, nil, e
	}
	defer tx.Rollback(ctx)
	e = tx.QueryRow(ctx, `SELECT w.id,w.name,w.created_by,w.created_at FROM workspace_invites i JOIN workspaces w ON w.id=i.workspace_id WHERE i.code_hash=$1 AND i.expires_at>now() FOR UPDATE OF w`, api.Hash(code)).Scan(&ws.ID, &ws.Name, &ws.CreatedBy, &ws.CreatedAt)
	if e != nil {
		return ws, nil, notFound(e)
	}
	tag, e := tx.Exec(ctx, "INSERT INTO workspace_members(workspace_id,user_id,role) VALUES($1,$2,'member') ON CONFLICT DO NOTHING", ws.ID, user)
	if e != nil {
		return ws, nil, e
	}
	if tag.RowsAffected() > 0 {
		ids, e := memberIDs(ctx, tx, ws.ID)
		if e != nil {
			return ws, nil, e
		}
		if e = fanout(ctx, tx, ids, Event{Type: "membership.changed", WorkspaceID: ws.ID}); e != nil {
			return ws, nil, e
		}
	}
	if e = tx.Commit(ctx); e != nil {
		return ws, nil, e
	}
	convs, e := s.Conversations(ctx, user, ws.ID)
	return ws, convs, e
}
func (s *Store) RemoveMember(ctx context.Context, user, ws, target string) error {
	tx, e := s.DB.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	if e = workspaceLock(ctx, tx, ws, user, true, true); e != nil {
		return e
	}
	var role string
	e = tx.QueryRow(ctx, "SELECT role FROM workspace_members WHERE workspace_id=$1 AND user_id=$2", ws, target).Scan(&role)
	if e == pgx.ErrNoRows {
		return tx.Commit(ctx)
	}
	if e != nil {
		return e
	}
	if role == "owner" {
		return api.Err(403, "forbidden", "The workspace owner cannot be removed")
	}
	ids, e := memberIDs(ctx, tx, ws)
	if e != nil {
		return e
	}
	rows, e := tx.Query(ctx, "SELECT user_id FROM workspace_members WHERE workspace_id=$1 AND(user_id=$2 OR granted_by=$2)", ws, target)
	if e != nil {
		return e
	}
	removed := []string{}
	for rows.Next() {
		var id string
		if e = rows.Scan(&id); e != nil {
			rows.Close()
			return e
		}
		removed = append(removed, id)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return e
	}
	if _, e = tx.Exec(ctx, "DELETE FROM conversation_members cm USING conversations c WHERE cm.conversation_id=c.id AND c.workspace_id=$1 AND cm.user_id=ANY($2)", ws, removed); e != nil {
		return e
	}
	if _, e = tx.Exec(ctx, "DELETE FROM workspace_members WHERE workspace_id=$1 AND user_id=ANY($2)", ws, removed); e != nil {
		return e
	}
	if e = fanout(ctx, tx, ids, Event{Type: "membership.changed", WorkspaceID: ws}); e != nil {
		return e
	}
	return tx.Commit(ctx)
}
func (s *Store) CreateConversation(ctx context.Context, user, ws, kind, name string, members []string) (Conversation, error) {
	var c Conversation
	flag := "im." + kind + ".create"
	if kind == "direct" {
		flag = "im.dm.create"
	}
	if kind != "channel" && kind != "direct" && kind != "group" {
		return c, api.Err(400, "invalid_type", "Conversation type must be channel, direct or group")
	}
	if e := s.requireFlag(flag); e != nil {
		return c, e
	}
	name = strings.TrimSpace(name)
	if len([]rune(name)) > 80 || (kind == "channel" && name == "") {
		return c, api.Err(400, "invalid_name", "Conversation name must contain 1 to 80 characters")
	}
	tx, e := s.DB.Begin(ctx)
	if e != nil {
		return c, e
	}
	defer tx.Rollback(ctx)
	if e = workspaceLock(ctx, tx, ws, user, kind == "channel", true); e != nil {
		return c, e
	}
	ids := []string{}
	pair := ""
	if kind != "channel" {
		unique := map[string]bool{user: true}
		for _, id := range members {
			if id != "" {
				unique[id] = true
			}
		}
		for id := range unique {
			ids = append(ids, id)
		}
		if (kind == "direct" && len(ids) != 2) || (kind == "group" && (len(ids) < 2 || len(ids) > 50)) {
			return c, api.Err(400, "invalid_members", "Choose one participant for a direct chat or 2 to 50 group participants")
		}
		valid, e := memberIDs(ctx, tx, ws)
		if e != nil {
			return c, e
		}
		set := map[string]bool{}
		for _, id := range valid {
			set[id] = true
		}
		for _, id := range ids {
			if !set[id] {
				return c, api.Err(400, "invalid_members", "All participants must be current workspace members")
			}
		}
		if kind == "direct" {
			if ids[0] > ids[1] {
				ids[0], ids[1] = ids[1], ids[0]
			}
			pair = ids[0] + ":" + ids[1]
			var existing string
			e = tx.QueryRow(ctx, "SELECT id FROM conversations WHERE workspace_id=$1 AND pair_key=$2", ws, pair).Scan(&existing)
			if e == nil {
				for _, id := range ids {
					if _, e = tx.Exec(ctx, "INSERT INTO conversation_members(conversation_id,user_id) VALUES($1,$2) ON CONFLICT DO NOTHING", existing, id); e != nil {
						return c, e
					}
				}
				c, e = conversationAccess(ctx, tx, existing, user)
				if e != nil {
					return c, e
				}
				return c, tx.Commit(ctx)
			}
			if e != pgx.ErrNoRows {
				return c, e
			}
		}
	}
	c = Conversation{ID: api.ID(), WorkspaceID: ws, Type: kind, Name: name, MemberIDs: ids, CreatedBy: user, CanManageMembers: kind == "group"}
	e = tx.QueryRow(ctx, "INSERT INTO conversations(id,workspace_id,type,name,pair_key,created_by) VALUES($1,$2,$3,$4,NULLIF($5,''),$6) RETURNING updated_at", c.ID, ws, kind, name, pair, user).Scan(&c.UpdatedAt)
	if e != nil {
		return c, e
	}
	for _, id := range ids {
		if _, e = tx.Exec(ctx, "INSERT INTO conversation_members(conversation_id,user_id) VALUES($1,$2)", c.ID, id); e != nil {
			return c, e
		}
	}
	if kind == "channel" {
		ids, e = memberIDs(ctx, tx, ws)
		if e != nil {
			return c, e
		}
		c.MemberIDs = ids
	}
	if e = fanout(ctx, tx, ids, Event{Type: "conversation.created", WorkspaceID: ws, ConversationID: c.ID, Conversation: &c}); e != nil {
		return c, e
	}
	return c, tx.Commit(ctx)
}
