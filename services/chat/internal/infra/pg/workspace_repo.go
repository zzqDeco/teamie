package pg

import (
	"context"
	"time"

	"teamie/pkg/postgres"
	"teamie/services/chat/internal/domain"
)

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
	conn := r.DB.Conn(ctx)
	if _, err := conn.Exec(ctx, "DELETE FROM conversation_notification_levels nl USING conversations c WHERE nl.conversation_id=c.id AND c.workspace_id=$1 AND nl.user_id=ANY($2)", workspaceID, userIDs); err != nil {
		return err
	}
	_, err := conn.Exec(ctx, "DELETE FROM conversation_members cm USING conversations c WHERE cm.conversation_id=c.id AND c.workspace_id=$1 AND cm.user_id=ANY($2)", workspaceID, userIDs)
	return err
}

func (r *Repository) DeleteWorkspaceMembers(ctx context.Context, workspaceID string, userIDs []string) error {
	_, err := r.DB.Conn(ctx).Exec(ctx, "DELETE FROM workspace_members WHERE workspace_id=$1 AND user_id=ANY($2)", workspaceID, userIDs)
	return err
}

func (r *Repository) SharedWorkspaceMemberIDs(ctx context.Context, userID string) ([]string, error) {
	rows, err := r.DB.Conn(ctx).Query(ctx, "SELECT DISTINCT wm2.user_id FROM workspace_members wm1 JOIN workspace_members wm2 ON wm1.workspace_id=wm2.workspace_id WHERE wm1.user_id=$1 ORDER BY wm2.user_id", userID)
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
