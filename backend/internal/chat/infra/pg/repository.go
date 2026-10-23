// Package pg implements the Chat outbound repository ports backed by PostgreSQL
// using the shared teamie/backend/pkg/postgres mechanism.
package pg

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"time"

	"teamie/backend/internal/chat/domain"
	"teamie/backend/pkg/postgres"
)

const (
	senderClientMessageSeed int64 = 704103
	attachmentSenderSeed    int64 = 704104
	principalMembershipSeed int64 = 704106
	messageColumns                = "id,conversation_id,sender_id,sender_name,sender_kind,client_message_id,seq,body,reply_to_id,created_at,edited_at,retracted_at,attachments,mentions"
)

// Repository implements the Chat domain repository and TxManager interfaces on
// top of the shared context-aware postgres.DB handle.
type Repository struct {
	DB *postgres.DB
}

// New creates a PostgreSQL-backed Chat repository.
func New(db *postgres.DB) *Repository {
	return &Repository{DB: db}
}

func notFound(err error) error {
	if postgres.IsNoRows(err) {
		return domain.ErrNotFound()
	}
	return err
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanMessage(row rowScanner) (domain.Message, error) {
	var m domain.Message
	err := row.Scan(&m.ID, &m.ConversationID, &m.SenderID, &m.SenderName, &m.SenderKind, &m.ClientMessageID, &m.Seq, &m.Body, &m.ReplyToID, &m.CreatedAt, &m.EditedAt, &m.RetractedAt, &m.Attachments, &m.Mentions)
	if m.RetractedAt != nil {
		m.Body = ""
		m.Attachments = nil
		m.Mentions = nil
	}
	return m, err
}

// --- TxManager ---

func (r *Repository) WithinTx(ctx context.Context, fn func(ctx context.Context) error) error {
	return r.DB.WithinTx(ctx, fn)
}

func (r *Repository) LockSenderClientMessage(ctx context.Context, senderID, clientMessageID string) error {
	return r.DB.AdvisoryXactLockKey(ctx, senderID+":"+clientMessageID, senderClientMessageSeed)
}

func (r *Repository) LockAttachmentSender(ctx context.Context, userID string) error {
	return r.DB.AdvisoryXactLockKey(ctx, userID, attachmentSenderSeed)
}

func (r *Repository) LockPrincipalMembership(ctx context.Context, principalID string) error {
	return r.DB.AdvisoryXactLockKey(ctx, principalID, principalMembershipSeed)
}

func (r *Repository) Ping(ctx context.Context) error {
	return r.DB.Ping(ctx)
}

// --- WorkspaceRepository ---

func (r *Repository) ListWorkspaces(ctx context.Context, userID string) ([]domain.Workspace, error) {
	rows, err := r.DB.Conn(ctx).Query(ctx, `SELECT w.id,w.name,w.created_by,w.created_at FROM workspaces w JOIN workspace_members m ON m.workspace_id=w.id WHERE m.user_id=$1 ORDER BY w.created_at,w.id`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.Workspace{}
	for rows.Next() {
		var w domain.Workspace
		if err = rows.Scan(&w.ID, &w.Name, &w.CreatedBy, &w.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

func (r *Repository) WorkspaceLock(ctx context.Context, workspaceID, userID string, owner, exclusive bool) error {
	clause := " FOR SHARE OF w"
	if exclusive {
		clause = " FOR UPDATE OF w"
	}
	var role string
	err := r.DB.Conn(ctx).QueryRow(ctx, `SELECT m.role FROM workspaces w JOIN workspace_members m ON m.workspace_id=w.id WHERE w.id=$1 AND m.user_id=$2`+clause, workspaceID, userID).Scan(&role)
	if err != nil {
		return notFound(err)
	}
	if owner && role != "owner" {
		return domain.ErrWorkspaceOwnerRequired()
	}
	return nil
}

func (r *Repository) WorkspaceMemberExists(ctx context.Context, workspaceID, userID string) (bool, error) {
	var yes bool
	err := r.DB.Conn(ctx).QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM workspace_members WHERE workspace_id=$1 AND user_id=$2)", workspaceID, userID).Scan(&yes)
	return yes, err
}

func (r *Repository) LockWorkspaceByIDForUpdate(ctx context.Context, workspaceID string) error {
	_, err := r.DB.Conn(ctx).Exec(ctx, "SELECT id FROM workspaces WHERE id=$1 FOR UPDATE", workspaceID)
	return err
}

func (r *Repository) InsertWorkspace(ctx context.Context, ws domain.Workspace) (time.Time, error) {
	var createdAt time.Time
	err := r.DB.Conn(ctx).QueryRow(ctx, "INSERT INTO workspaces(id,name,created_by) VALUES($1,$2,$3) RETURNING created_at", ws.ID, ws.Name, ws.CreatedBy).Scan(&createdAt)
	return createdAt, err
}

func (r *Repository) InsertWorkspaceMember(ctx context.Context, workspaceID, userID, role string, grantedBy *string, onConflictDoNothing bool) (bool, error) {
	conn := r.DB.Conn(ctx)
	if grantedBy != nil {
		tag, err := conn.Exec(ctx, "INSERT INTO workspace_members(workspace_id,user_id,role,granted_by) VALUES($1,$2,$3,$4)", workspaceID, userID, role, *grantedBy)
		return tag.RowsAffected() > 0, err
	}
	query := "INSERT INTO workspace_members(workspace_id,user_id,role) VALUES($1,$2,$3)"
	if onConflictDoNothing {
		query += " ON CONFLICT DO NOTHING"
	}
	tag, err := conn.Exec(ctx, query, workspaceID, userID, role)
	return tag.RowsAffected() > 0, err
}

func (r *Repository) ListWorkspaceMembers(ctx context.Context, workspaceID string) ([]domain.Member, error) {
	rows, err := r.DB.Conn(ctx).Query(ctx, "SELECT user_id,role FROM workspace_members WHERE workspace_id=$1 ORDER BY role,user_id", workspaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.Member{}
	for rows.Next() {
		var m domain.Member
		if err = rows.Scan(&m.UserID, &m.Role); err != nil {
			return nil, err
		}
		m.Kind = "human"
		m.DisplayName = m.UserID
		out = append(out, m)
	}
	return out, rows.Err()
}

func (r *Repository) WorkspaceMemberIDs(ctx context.Context, workspaceID string) ([]string, error) {
	return r.ConversationRecipients(ctx, domain.Conversation{WorkspaceID: workspaceID, Type: "channel"})
}

func (r *Repository) InsertWorkspaceInvite(ctx context.Context, codeHash, workspaceID, createdBy string, expiresAt time.Time) error {
	_, err := r.DB.Conn(ctx).Exec(ctx, "INSERT INTO workspace_invites(code_hash,workspace_id,created_by,expires_at) VALUES($1,$2,$3,$4)", codeHash, workspaceID, createdBy, expiresAt)
	return err
}

func (r *Repository) FindValidInvite(ctx context.Context, codeHash string) (domain.Workspace, time.Time, error) {
	var ws domain.Workspace
	var expiry time.Time
	err := r.DB.Conn(ctx).QueryRow(ctx, `SELECT w.id,w.name,i.expires_at FROM workspace_invites i JOIN workspaces w ON w.id=i.workspace_id WHERE i.code_hash=$1 AND i.expires_at>now()`, codeHash).Scan(&ws.ID, &ws.Name, &expiry)
	return ws, expiry, notFound(err)
}

func (r *Repository) LockValidInviteWorkspace(ctx context.Context, codeHash string) (domain.Workspace, error) {
	var ws domain.Workspace
	err := r.DB.Conn(ctx).QueryRow(ctx, `SELECT w.id,w.name,w.created_by,w.created_at FROM workspace_invites i JOIN workspaces w ON w.id=i.workspace_id WHERE i.code_hash=$1 AND i.expires_at>now() FOR UPDATE OF w`, codeHash).Scan(&ws.ID, &ws.Name, &ws.CreatedBy, &ws.CreatedAt)
	return ws, notFound(err)
}

func (r *Repository) GetWorkspaceMemberRole(ctx context.Context, workspaceID, userID string) (string, bool, error) {
	var role string
	err := r.DB.Conn(ctx).QueryRow(ctx, "SELECT role FROM workspace_members WHERE workspace_id=$1 AND user_id=$2", workspaceID, userID).Scan(&role)
	if postgres.IsNoRows(err) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return role, true, nil
}

func (r *Repository) FindMemberAndDelegatedIDs(ctx context.Context, workspaceID, targetUserID string) ([]string, error) {
	rows, err := r.DB.Conn(ctx).Query(ctx, "SELECT user_id FROM workspace_members WHERE workspace_id=$1 AND(user_id=$2 OR granted_by=$2)", workspaceID, targetUserID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	removed := []string{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			return nil, err
		}
		removed = append(removed, id)
	}
	return removed, rows.Err()
}

func (r *Repository) DeleteConversationMembersInWorkspace(ctx context.Context, workspaceID string, userIDs []string) error {
	_, err := r.DB.Conn(ctx).Exec(ctx, "DELETE FROM conversation_members cm USING conversations c WHERE cm.conversation_id=c.id AND c.workspace_id=$1 AND cm.user_id=ANY($2)", workspaceID, userIDs)
	return err
}

func (r *Repository) DeleteWorkspaceMembers(ctx context.Context, workspaceID string, userIDs []string) error {
	_, err := r.DB.Conn(ctx).Exec(ctx, "DELETE FROM workspace_members WHERE workspace_id=$1 AND user_id=ANY($2)", workspaceID, userIDs)
	return err
}

// --- ConversationRepository ---

func (r *Repository) ListAuthorizedConversationIDs(ctx context.Context, userID, workspaceID string) ([]string, error) {
	rows, err := r.DB.Conn(ctx).Query(ctx, `SELECT c.id FROM conversations c JOIN workspace_members wm ON wm.workspace_id=c.workspace_id WHERE wm.user_id=$1 AND ($2='' OR c.workspace_id=$2) AND (c.type='channel' OR EXISTS(SELECT 1 FROM conversation_members cm WHERE cm.conversation_id=c.id AND cm.user_id=$1)) ORDER BY c.updated_at,c.id`, userID, workspaceID)
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

func (r *Repository) ConversationAccess(ctx context.Context, conversationID, userID string) (domain.Conversation, error) {
	var c domain.Conversation
	var role string
	conn := r.DB.Conn(ctx)
	err := conn.QueryRow(ctx, `SELECT c.id,c.workspace_id,c.type,c.name,c.last_seq,COALESCE(r.seq,0),c.updated_at,COALESCE(c.created_by,''),wm.role FROM conversations c JOIN workspace_members wm ON wm.workspace_id=c.workspace_id AND wm.user_id=$2 LEFT JOIN read_cursors r ON r.conversation_id=c.id AND r.user_id=$2 WHERE c.id=$1 AND (c.type='channel' OR EXISTS(SELECT 1 FROM conversation_members cm WHERE cm.conversation_id=c.id AND cm.user_id=$2))`, conversationID, userID).Scan(&c.ID, &c.WorkspaceID, &c.Type, &c.Name, &c.LastSeq, &c.ReadSeq, &c.UpdatedAt, &c.CreatedBy, &role)
	if err != nil {
		return c, notFound(err)
	}
	c.CanManageMembers = c.Type == "group" && (c.CreatedBy == userID || role == "owner")
	c.MemberIDs, err = r.ConversationRecipients(ctx, c)
	return c, err
}

func (r *Repository) ConversationRecipients(ctx context.Context, c domain.Conversation) ([]string, error) {
	rows, err := r.DB.Conn(ctx).Query(ctx, `SELECT wm.user_id FROM workspace_members wm WHERE wm.workspace_id=$1 AND ($2='channel' OR EXISTS(SELECT 1 FROM conversation_members cm WHERE cm.conversation_id=$3 AND cm.user_id=wm.user_id)) ORDER BY wm.user_id`, c.WorkspaceID, c.Type, c.ID)
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

func (r *Repository) GetConversationWorkspaceID(ctx context.Context, conversationID string) (string, error) {
	var ws string
	err := r.DB.Conn(ctx).QueryRow(ctx, "SELECT workspace_id FROM conversations WHERE id=$1", conversationID).Scan(&ws)
	return ws, notFound(err)
}

func (r *Repository) LockConversation(ctx context.Context, conversationID, userID string) (domain.Conversation, error) {
	ws, err := r.GetConversationWorkspaceID(ctx, conversationID)
	if err != nil {
		return domain.Conversation{}, err
	}
	if err = r.WorkspaceLock(ctx, ws, userID, false, false); err != nil {
		return domain.Conversation{}, err
	}
	if _, err = r.DB.Conn(ctx).Exec(ctx, "SELECT id FROM conversations WHERE id=$1 FOR UPDATE", conversationID); err != nil {
		return domain.Conversation{}, err
	}
	return r.ConversationAccess(ctx, conversationID, userID)
}

func (r *Repository) FindDirectConversationByPairKey(ctx context.Context, workspaceID, pairKey string) (string, bool, error) {
	var existing string
	err := r.DB.Conn(ctx).QueryRow(ctx, "SELECT id FROM conversations WHERE workspace_id=$1 AND pair_key=$2", workspaceID, pairKey).Scan(&existing)
	if postgres.IsNoRows(err) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return existing, true, nil
}

func (r *Repository) InsertConversation(ctx context.Context, c domain.Conversation, pairKey string) (time.Time, error) {
	var updatedAt time.Time
	err := r.DB.Conn(ctx).QueryRow(ctx, "INSERT INTO conversations(id,workspace_id,type,name,pair_key,created_by) VALUES($1,$2,$3,$4,NULLIF($5,''),$6) RETURNING updated_at", c.ID, c.WorkspaceID, c.Type, c.Name, pairKey, c.CreatedBy).Scan(&updatedAt)
	return updatedAt, err
}

func (r *Repository) InsertConversationMember(ctx context.Context, conversationID, userID string, onConflictDoNothing bool) (bool, error) {
	query := "INSERT INTO conversation_members(conversation_id,user_id) VALUES($1,$2)"
	if onConflictDoNothing {
		query += " ON CONFLICT DO NOTHING"
	}
	tag, err := r.DB.Conn(ctx).Exec(ctx, query, conversationID, userID)
	return tag.RowsAffected() > 0, err
}

func (r *Repository) DeleteConversationMember(ctx context.Context, conversationID, userID string) error {
	_, err := r.DB.Conn(ctx).Exec(ctx, "DELETE FROM conversation_members WHERE conversation_id=$1 AND user_id=$2", conversationID, userID)
	return err
}

func (r *Repository) LockConversationWithActorRole(ctx context.Context, conversationID, actorID string) (domain.Conversation, string, error) {
	var c domain.Conversation
	var role string
	err := r.DB.Conn(ctx).QueryRow(ctx, "SELECT c.id,c.workspace_id,c.type,c.name,COALESCE(c.created_by,''),c.last_seq,c.updated_at,wm.role FROM conversations c JOIN workspace_members wm ON wm.workspace_id=c.workspace_id AND wm.user_id=$2 WHERE c.id=$1 FOR UPDATE OF c", conversationID, actorID).Scan(&c.ID, &c.WorkspaceID, &c.Type, &c.Name, &c.CreatedBy, &c.LastSeq, &c.UpdatedAt, &role)
	return c, role, notFound(err)
}

func (r *Repository) CountConversationMembersAndCheckTarget(ctx context.Context, conversationID, targetUserID string) (int, bool, error) {
	var members int
	var present bool
	err := r.DB.Conn(ctx).QueryRow(ctx, "SELECT count(*),COALESCE(bool_or(user_id=$2),false) FROM conversation_members WHERE conversation_id=$1", conversationID, targetUserID).Scan(&members, &present)
	return members, present, err
}

func (r *Repository) IncrementConversationSeq(ctx context.Context, conversationID string) (int64, error) {
	var seq int64
	err := r.DB.Conn(ctx).QueryRow(ctx, "UPDATE conversations SET last_seq=last_seq+1,updated_at=now() WHERE id=$1 RETURNING last_seq", conversationID).Scan(&seq)
	return seq, err
}

func (r *Repository) UpsertReadCursor(ctx context.Context, conversationID, userID string, seq int64) error {
	_, err := r.DB.Conn(ctx).Exec(ctx, "INSERT INTO read_cursors(conversation_id,user_id,seq) VALUES($1,$2,$3) ON CONFLICT(conversation_id,user_id) DO UPDATE SET seq=GREATEST(read_cursors.seq,excluded.seq)", conversationID, userID, seq)
	return err
}

func (r *Repository) SyncHead(ctx context.Context, userID string) (int64, error) {
	var n int64
	err := r.DB.Conn(ctx).QueryRow(ctx, "SELECT COALESCE((SELECT last_id FROM sync_heads WHERE user_id=$1),0)", userID).Scan(&n)
	return n, err
}

func (r *Repository) EnsureAndLockSyncHeads(ctx context.Context, userIDs []string) error {
	conn := r.DB.Conn(ctx)
	for _, id := range userIDs {
		if _, err := conn.Exec(ctx, "INSERT INTO sync_heads(user_id) VALUES($1) ON CONFLICT DO NOTHING", id); err != nil {
			return err
		}
		if _, err := conn.Exec(ctx, "SELECT user_id FROM sync_heads WHERE user_id=$1 FOR UPDATE", id); err != nil {
			return err
		}
	}
	return nil
}

// Fanout acquires sorted sync_head updates so fanout is atomic and deadlock-free across overlapping audiences.
func (r *Repository) Fanout(ctx context.Context, ids []string, event domain.Event) error {
	conn := r.DB.Conn(ctx)
	ids = append([]string(nil), ids...)
	sort.Strings(ids)
	event.CreatedAt = time.Now().UTC()
	last := ""
	for _, id := range ids {
		if id == last {
			continue
		}
		last = id
		if _, err := conn.Exec(ctx, "INSERT INTO sync_heads(user_id) VALUES($1) ON CONFLICT DO NOTHING", id); err != nil {
			return err
		}
		var cursor int64
		if err := conn.QueryRow(ctx, "UPDATE sync_heads SET last_id=last_id+1 WHERE user_id=$1 RETURNING last_id", id).Scan(&cursor); err != nil {
			return err
		}
		if event.Conversation != nil {
			c, err := r.ConversationAccess(ctx, event.Conversation.ID, id)
			if err != nil {
				return err
			}
			event.Conversation = &c
		}
		event.ID = strconv.FormatInt(cursor, 10)
		b, err := json.Marshal(event)
		if err != nil {
			return err
		}
		if _, err = conn.Exec(ctx, "INSERT INTO sync_events(user_id,id,workspace_id,conversation_id,payload) VALUES($1,$2,NULLIF($3,''),NULLIF($4,''),$5)", id, cursor, event.WorkspaceID, event.ConversationID, b); err != nil {
			return err
		}
	}
	b, err := json.Marshal(map[string]any{"type": "sync.required", "userIds": ids})
	if err != nil {
		return err
	}
	_, err = conn.Exec(ctx, "INSERT INTO outbox(payload) VALUES($1)", b)
	return err
}

func (r *Repository) QuerySyncEvents(ctx context.Context, userID string, afterID, upToID int64, limit int) ([]domain.Event, int64, error) {
	rows, err := r.DB.Conn(ctx).Query(ctx, `SELECT e.id,e.payload,COALESCE(m.retracted_at IS NOT NULL,false),(e.payload->>'type'='membership.changed' OR (EXISTS(SELECT 1 FROM workspace_members wm WHERE wm.workspace_id=e.workspace_id AND wm.user_id=$1) AND (e.conversation_id IS NULL OR EXISTS(SELECT 1 FROM conversations c WHERE c.id=e.conversation_id AND (c.type='channel' OR EXISTS(SELECT 1 FROM conversation_members cm WHERE cm.conversation_id=c.id AND cm.user_id=$1)))))) AS allowed FROM sync_events e LEFT JOIN messages m ON m.id=e.payload#>>'{message,id}' WHERE e.user_id=$1 AND e.id>$2 AND e.id<=$3 ORDER BY e.id LIMIT $4`, userID, afterID, upToID, limit)
	if err != nil {
		return nil, afterID, err
	}
	defer rows.Close()
	out := []domain.Event{}
	n := afterID
	for rows.Next() {
		var id int64
		var b []byte
		var allowed, deleted bool
		if err = rows.Scan(&id, &b, &deleted, &allowed); err != nil {
			return nil, afterID, err
		}
		n = id
		if !allowed {
			continue
		}
		var ev domain.Event
		if err = json.Unmarshal(b, &ev); err != nil {
			return nil, afterID, fmt.Errorf("decode persisted event: %w", err)
		}
		if ev.Message != nil && deleted {
			ev.Message.Body = ""
			ev.Message.Attachments = nil
			ev.Message.Mentions = nil
		}
		out = append(out, ev)
	}
	return out, n, rows.Err()
}

// --- MembershipRepository ---

func (r *Repository) ParticipantWorkspaces(ctx context.Context, userID string) ([]string, error) {
	rows, err := r.DB.Conn(ctx).Query(ctx, "SELECT workspace_id FROM workspace_members WHERE user_id=$1 ORDER BY workspace_id", userID)
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

func (r *Repository) EnsureAndLockPrincipalRevision(ctx context.Context, principalID string) (int64, error) {
	conn := r.DB.Conn(ctx)
	if _, err := conn.Exec(ctx, "INSERT INTO principal_membership_revisions(principal_id,revision) VALUES($1,0) ON CONFLICT DO NOTHING", principalID); err != nil {
		return 0, err
	}
	var oldRevision int64
	err := conn.QueryRow(ctx, "SELECT revision FROM principal_membership_revisions WHERE principal_id=$1 FOR UPDATE", principalID).Scan(&oldRevision)
	return oldRevision, err
}

func (r *Repository) UpdatePrincipalRevision(ctx context.Context, principalID string, revision int64) error {
	_, err := r.DB.Conn(ctx).Exec(ctx, "UPDATE principal_membership_revisions SET revision=$2 WHERE principal_id=$1", principalID, revision)
	return err
}

// --- MessageRepository ---

func (r *Repository) GetMessage(ctx context.Context, conversationID, messageID string) (domain.Message, error) {
	m, err := scanMessage(r.DB.Conn(ctx).QueryRow(ctx, "SELECT "+messageColumns+" FROM messages WHERE id=$1 AND conversation_id=$2", messageID, conversationID))
	return m, notFound(err)
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
	_, err := conn.Exec(ctx, "DELETE FROM attachments WHERE message_id=$1", messageID)
	return err
}

func (r *Repository) UpdateMessageBodyAndMentions(ctx context.Context, messageID, body string, editedAt time.Time, mentions []domain.Mention) error {
	_, err := r.DB.Conn(ctx).Exec(ctx, "UPDATE messages SET body=$2,edited_at=$3,mentions=$4 WHERE id=$1", messageID, body, editedAt, mentions)
	return err
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
	return unreadCount, channelUnreadCount, items, nil
}

// --- AttachmentRepository ---

func (r *Repository) FindAttachmentUpload(ctx context.Context, userID, key string) (domain.AttachmentUpload, bool, error) {
	var out domain.AttachmentUpload
	err := r.DB.Conn(ctx).QueryRow(ctx, `SELECT u.conversation_id,u.fingerprint,COALESCE(a.id,''),COALESCE(a.filename,''),COALESCE(a.media_type,''),COALESCE(a.size_bytes,0),COALESCE(a.sha256,''),
 a.id IS NOT NULL AND ((a.message_id IS NULL AND a.expires_at>now()) OR (m.id IS NOT NULL AND m.retracted_at IS NULL))
 FROM attachment_uploads u LEFT JOIN attachments a ON a.id=u.attachment_id LEFT JOIN messages m ON m.id=a.message_id
 WHERE u.uploader_id=$1 AND u.upload_key=$2`, userID, key).Scan(&out.Conversation, &out.Fingerprint, &out.Attachment.ID, &out.Attachment.Filename, &out.Attachment.MediaType, &out.Attachment.SizeBytes, &out.Attachment.SHA256, &out.Available)
	if postgres.IsNoRows(err) {
		return domain.AttachmentUpload{}, false, nil
	}
	if err != nil {
		return domain.AttachmentUpload{}, false, err
	}
	return out, true, nil
}

func (r *Repository) DeleteExpiredPendingForSender(ctx context.Context, userID string) error {
	_, err := r.DB.Conn(ctx).Exec(ctx, "DELETE FROM attachments WHERE uploader_id=$1 AND message_id IS NULL AND expires_at<=now()", userID)
	return err
}

func (r *Repository) CountPendingForSender(ctx context.Context, userID string) (int, error) {
	var pending int
	err := r.DB.Conn(ctx).QueryRow(ctx, "SELECT count(*) FROM attachments WHERE uploader_id=$1 AND message_id IS NULL", userID).Scan(&pending)
	return pending, err
}

func (r *Repository) InsertAttachment(ctx context.Context, a domain.Attachment, conversationID, uploaderID string, content []byte) error {
	_, err := r.DB.Conn(ctx).Exec(ctx, "INSERT INTO attachments(id,conversation_id,uploader_id,filename,media_type,size_bytes,sha256,content) VALUES($1,$2,$3,$4,$5,$6,$7,$8)", a.ID, conversationID, uploaderID, a.Filename, a.MediaType, a.SizeBytes, a.SHA256, content)
	return err
}

func (r *Repository) InsertAttachmentUpload(ctx context.Context, uploaderID, key, conversationID, fingerprint, attachmentID string) error {
	_, err := r.DB.Conn(ctx).Exec(ctx, "INSERT INTO attachment_uploads(uploader_id,upload_key,conversation_id,fingerprint,attachment_id) VALUES($1,$2,$3,$4,$5)", uploaderID, key, conversationID, fingerprint, attachmentID)
	return err
}

func (r *Repository) DeleteAllExpiredPending(ctx context.Context) error {
	_, err := r.DB.Conn(ctx).Exec(ctx, "DELETE FROM attachments WHERE message_id IS NULL AND expires_at<=now()")
	return err
}

func (r *Repository) LockAttachmentForUpdate(ctx context.Context, attachmentID, conversationID string) (string, *string, bool, error) {
	var owner string
	var message *string
	err := r.DB.Conn(ctx).QueryRow(ctx, "SELECT uploader_id,message_id FROM attachments WHERE id=$1 AND conversation_id=$2 FOR UPDATE", attachmentID, conversationID).Scan(&owner, &message)
	if postgres.IsNoRows(err) {
		return "", nil, false, nil
	}
	if err != nil {
		return "", nil, false, err
	}
	return owner, message, true, nil
}

func (r *Repository) DeleteAttachmentByID(ctx context.Context, attachmentID string) error {
	_, err := r.DB.Conn(ctx).Exec(ctx, "DELETE FROM attachments WHERE id=$1", attachmentID)
	return err
}

func (r *Repository) AttachFilesToMessage(ctx context.Context, userID, conversationID, messageID string, attachmentIDs []string) ([]domain.Attachment, error) {
	conn := r.DB.Conn(ctx)
	out := []domain.Attachment{}
	for _, id := range attachmentIDs {
		var a domain.Attachment
		err := conn.QueryRow(ctx, `UPDATE attachments SET message_id=$1 WHERE id=$2 AND conversation_id=$3 AND uploader_id=$4 AND message_id IS NULL AND expires_at>now() RETURNING id,filename,media_type,size_bytes,sha256`, messageID, id, conversationID, userID).Scan(&a.ID, &a.Filename, &a.MediaType, &a.SizeBytes, &a.SHA256)
		if postgres.IsNoRows(err) {
			return nil, domain.ErrInvalidAttachmentUnavailable()
		}
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, nil
}

func (r *Repository) GetAuthorizedAttachmentContent(ctx context.Context, attachmentID, conversationID, userID string) (domain.Attachment, []byte, error) {
	var a domain.Attachment
	var content []byte
	err := r.DB.Conn(ctx).QueryRow(ctx, `SELECT a.id,a.filename,a.media_type,a.size_bytes,a.sha256,a.content FROM attachments a LEFT JOIN messages m ON m.id=a.message_id WHERE a.id=$1 AND a.conversation_id=$2 AND ((a.message_id IS NULL AND a.uploader_id=$3 AND a.expires_at>now()) OR (a.message_id IS NOT NULL AND m.retracted_at IS NULL))`, attachmentID, conversationID, userID).Scan(&a.ID, &a.Filename, &a.MediaType, &a.SizeBytes, &a.SHA256, &content)
	return a, content, notFound(err)
}

// --- OutboxRepository ---

func (r *Repository) PrunePublishedOutbox(ctx context.Context) (int64, error) {
	tag, err := r.DB.Conn(ctx).Exec(ctx, `WITH published AS (
		SELECT id FROM outbox WHERE published_at IS NOT NULL ORDER BY id
		FOR UPDATE SKIP LOCKED LIMIT 1000
	) DELETE FROM outbox o USING published p WHERE o.id=p.id AND o.published_at IS NOT NULL`)
	return tag.RowsAffected(), err
}

func (r *Repository) ClaimNextUnpublishedOutbox(ctx context.Context) (int64, []byte, bool, error) {
	var id int64
	var payload []byte
	err := r.DB.Conn(ctx).QueryRow(ctx, "SELECT id,payload FROM outbox WHERE published_at IS NULL ORDER BY id FOR UPDATE SKIP LOCKED LIMIT 1").Scan(&id, &payload)
	if postgres.IsNoRows(err) {
		return 0, nil, false, nil
	}
	if err != nil {
		return 0, nil, false, err
	}
	return id, payload, true, nil
}

func (r *Repository) DeleteOutboxByID(ctx context.Context, id int64) error {
	_, err := r.DB.Conn(ctx).Exec(ctx, "DELETE FROM outbox WHERE id=$1", id)
	return err
}
