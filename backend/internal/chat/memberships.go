package chat

import (
	"context"
	"sort"
	"teamie/backend/internal/api"
)

func (s *Store) ParticipantWorkspaces(ctx context.Context, user string) ([]string, error) {
	rows, e := s.DB.Query(ctx, "SELECT workspace_id FROM workspace_members WHERE user_id=$1 ORDER BY workspace_id", user)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var id string
		if e = rows.Scan(&id); e != nil {
			return nil, e
		}
		out = append(out, id)
	}
	return out, rows.Err()
}
func (s *Store) WorkspaceParticipants(ctx context.Context, workspace, requester string) ([]string, error) {
	tx, e := s.DB.Begin(ctx)
	if e != nil {
		return nil, e
	}
	defer tx.Rollback(ctx)
	if e = workspaceLock(ctx, tx, workspace, requester, false, false); e != nil {
		return nil, e
	}
	return memberIDs(ctx, tx, workspace)
}
func normalizedIDs(ids []string) []string {
	set := map[string]bool{}
	for _, id := range ids {
		if id != "" {
			set[id] = true
		}
	}
	out := []string{}
	for id := range set {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}
func (s *Store) SetPrincipalWorkspaces(ctx context.Context, principal, grantor api.User, requested []string, revision int64) ([]string, error) {
	if principal.Kind != "agent" || grantor.Kind != "human" || principal.ID == grantor.ID || revision < 1 || len(requested) > 100 {
		return nil, api.Err(400, "invalid_membership", "Invalid delegated membership request")
	}
	wanted := normalizedIDs(requested)
	tx, e := s.DB.Begin(ctx)
	if e != nil {
		return nil, e
	}
	defer tx.Rollback(ctx)
	if _, e = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1,704106))", principal.ID); e != nil {
		return nil, e
	}
	if _, e = tx.Exec(ctx, "INSERT INTO principal_membership_revisions(principal_id,revision) VALUES($1,0) ON CONFLICT DO NOTHING", principal.ID); e != nil {
		return nil, e
	}
	var oldRevision int64
	if e = tx.QueryRow(ctx, "SELECT revision FROM principal_membership_revisions WHERE principal_id=$1 FOR UPDATE", principal.ID).Scan(&oldRevision); e != nil {
		return nil, e
	}
	// An applied revision is never replayed: moderators may have removed a
	// participant after it was applied, and background reconciliation cannot rejoin.
	if revision <= oldRevision {
		ids, e := participantWorkspaces(ctx, tx, principal.ID)
		if e != nil {
			return nil, e
		}
		return ids, tx.Commit(ctx)
	}
	old, e := participantWorkspaces(ctx, tx, principal.ID)
	if e != nil {
		return nil, e
	}
	union := normalizedIDs(append(append([]string{}, old...), wanted...))
	selected := map[string]bool{}
	for _, id := range wanted {
		selected[id] = true
	}
	for _, id := range union {
		if _, e = tx.Exec(ctx, "SELECT id FROM workspaces WHERE id=$1 FOR UPDATE", id); e != nil {
			return nil, e
		}
	}
	for _, id := range wanted {
		var yes bool
		if e = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM workspace_members WHERE workspace_id=$1 AND user_id=$2)", id, grantor.ID).Scan(&yes); e != nil {
			return nil, e
		}
		if !yes {
			return nil, api.Err(403, "forbidden", "Selected workspaces must be current memberships of the granting human")
		}
	}
	oldSet := map[string]bool{}
	for _, id := range old {
		oldSet[id] = true
	}
	allHeads := []string{principal.ID}
	for _, id := range union {
		audience, e := memberIDs(ctx, tx, id)
		if e != nil {
			return nil, e
		}
		allHeads = append(allHeads, audience...)
	}
	for _, id := range normalizedIDs(allHeads) {
		if _, e = tx.Exec(ctx, "INSERT INTO sync_heads(user_id) VALUES($1) ON CONFLICT DO NOTHING", id); e != nil {
			return nil, e
		}
		if _, e = tx.Exec(ctx, "SELECT user_id FROM sync_heads WHERE user_id=$1 FOR UPDATE", id); e != nil {
			return nil, e
		}
	}
	for _, id := range union {
		if selected[id] == oldSet[id] {
			continue
		}
		ids, e := memberIDs(ctx, tx, id)
		if e != nil {
			return nil, e
		}
		if selected[id] {
			_, e = tx.Exec(ctx, "INSERT INTO workspace_members(workspace_id,user_id,role,granted_by) VALUES($1,$2,'member',$3)", id, principal.ID, grantor.ID)
			ids = append(ids, principal.ID)
		} else {
			if _, e = tx.Exec(ctx, "DELETE FROM conversation_members cm USING conversations c WHERE cm.conversation_id=c.id AND c.workspace_id=$1 AND cm.user_id=$2", id, principal.ID); e != nil {
				return nil, e
			}
			_, e = tx.Exec(ctx, "DELETE FROM workspace_members WHERE workspace_id=$1 AND user_id=$2", id, principal.ID)
		}
		if e != nil {
			return nil, e
		}
		if e = fanout(ctx, tx, ids, Event{Type: "membership.changed", WorkspaceID: id}); e != nil {
			return nil, e
		}
	}
	if _, e = tx.Exec(ctx, "UPDATE principal_membership_revisions SET revision=$2 WHERE principal_id=$1", principal.ID, revision); e != nil {
		return nil, e
	}
	return wanted, tx.Commit(ctx)
}
func participantWorkspaces(ctx context.Context, q querier, user string) ([]string, error) {
	rows, e := q.Query(ctx, "SELECT workspace_id FROM workspace_members WHERE user_id=$1 ORDER BY workspace_id", user)
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
func (s *Store) GroupMember(ctx context.Context, actor, conversation, target string, add bool) (Conversation, error) {
	var c Conversation
	if add {
		if e := s.requireFlag("im.group.create"); e != nil {
			return c, e
		}
	}
	tx, e := s.DB.Begin(ctx)
	if e != nil {
		return c, e
	}
	defer tx.Rollback(ctx)
	var ws string
	if e = tx.QueryRow(ctx, "SELECT workspace_id FROM conversations WHERE id=$1", conversation).Scan(&ws); e != nil {
		return c, notFound(e)
	}
	if e = workspaceLock(ctx, tx, ws, actor, false, false); e != nil {
		return c, e
	}
	var role string
	e = tx.QueryRow(ctx, "SELECT c.id,c.workspace_id,c.type,c.name,COALESCE(c.created_by,''),c.last_seq,c.updated_at,wm.role FROM conversations c JOIN workspace_members wm ON wm.workspace_id=c.workspace_id AND wm.user_id=$2 WHERE c.id=$1 FOR UPDATE OF c", conversation, actor).Scan(&c.ID, &c.WorkspaceID, &c.Type, &c.Name, &c.CreatedBy, &c.LastSeq, &c.UpdatedAt, &role)
	if e != nil {
		return c, notFound(e)
	}
	if c.Type != "group" {
		return c, api.Err(400, "invalid_conversation", "Only group membership can be changed")
	}
	if role != "owner" && c.CreatedBy != actor {
		return c, api.Err(403, "forbidden", "Only group creator or workspace owner can manage group membership")
	}
	ids, e := recipients(ctx, tx, c)
	if e != nil {
		return c, e
	}
	if add {
		var yes bool
		if e = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM workspace_members WHERE workspace_id=$1 AND user_id=$2)", ws, target).Scan(&yes); e != nil {
			return c, e
		}
		if !yes {
			return c, api.Err(400, "invalid_members", "Participant must be a current workspace member")
		}
		_, e = tx.Exec(ctx, "INSERT INTO conversation_members(conversation_id,user_id) VALUES($1,$2) ON CONFLICT DO NOTHING", conversation, target)
		ids = append(ids, target)
	} else {
		_, e = tx.Exec(ctx, "DELETE FROM conversation_members WHERE conversation_id=$1 AND user_id=$2", conversation, target)
	}
	if e != nil {
		return c, e
	}
	c.MemberIDs, e = recipients(ctx, tx, c)
	if e != nil {
		return c, e
	}
	c.CanManageMembers = role == "owner" || c.CreatedBy == actor
	if e = fanout(ctx, tx, ids, Event{Type: "membership.changed", WorkspaceID: ws, ConversationID: conversation}); e != nil {
		return c, e
	}
	return c, tx.Commit(ctx)
}
