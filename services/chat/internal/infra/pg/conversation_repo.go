package pg

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"time"

	"teamie/pkg/postgres"
	"teamie/services/chat/internal/domain"
)

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
	err := conn.QueryRow(ctx, `SELECT c.id,c.workspace_id,c.type,c.name,c.last_seq,COALESCE(r.seq,0),c.updated_at,COALESCE(c.created_by,''),wm.role,COALESCE(nl.level,'all') FROM conversations c JOIN workspace_members wm ON wm.workspace_id=c.workspace_id AND wm.user_id=$2 LEFT JOIN read_cursors r ON r.conversation_id=c.id AND r.user_id=$2 LEFT JOIN conversation_notification_levels nl ON nl.conversation_id=c.id AND nl.user_id=$2 WHERE c.id=$1 AND (c.type='channel' OR EXISTS(SELECT 1 FROM conversation_members cm WHERE cm.conversation_id=c.id AND cm.user_id=$2))`, conversationID, userID).Scan(&c.ID, &c.WorkspaceID, &c.Type, &c.Name, &c.LastSeq, &c.ReadSeq, &c.UpdatedAt, &c.CreatedBy, &role, &c.NotificationLevel)
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
	conn := r.DB.Conn(ctx)
	if _, err := conn.Exec(ctx, "DELETE FROM conversation_notification_levels WHERE conversation_id=$1 AND user_id=$2", conversationID, userID); err != nil {
		return err
	}
	_, err := conn.Exec(ctx, "DELETE FROM conversation_members WHERE conversation_id=$1 AND user_id=$2", conversationID, userID)
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

func (r *Repository) SetNotificationLevel(ctx context.Context, conversationID, userID, level string) (bool, error) {
	conn := r.DB.Conn(ctx)
	var current string
	err := conn.QueryRow(ctx, "SELECT COALESCE((SELECT level FROM conversation_notification_levels WHERE conversation_id=$1 AND user_id=$2),'all')", conversationID, userID).Scan(&current)
	if err != nil {
		return false, err
	}
	if current == level {
		return false, nil
	}
	if level == "all" {
		_, err = conn.Exec(ctx, "DELETE FROM conversation_notification_levels WHERE conversation_id=$1 AND user_id=$2", conversationID, userID)
	} else {
		_, err = conn.Exec(ctx, "INSERT INTO conversation_notification_levels(conversation_id,user_id,level,updated_at) VALUES($1,$2,$3,now()) ON CONFLICT(conversation_id,user_id) DO UPDATE SET level=excluded.level,updated_at=now()", conversationID, userID, level)
	}
	return err == nil, err
}

func (r *Repository) QueryReadReceipts(ctx context.Context, c domain.Conversation, callerUserID string) (domain.ReadReceipts, error) {
	eligible := make([]string, 0, len(c.MemberIDs))
	for _, id := range c.MemberIDs {
		if id != callerUserID {
			eligible = append(eligible, id)
		}
	}
	cursors := make(map[string]int64, len(eligible))
	if len(eligible) > 0 {
		rows, err := r.DB.Conn(ctx).Query(ctx, "SELECT user_id,seq FROM read_cursors WHERE conversation_id=$1 AND user_id=ANY($2::text[])", c.ID, eligible)
		if err != nil {
			return domain.ReadReceipts{}, err
		}
		defer rows.Close()
		for rows.Next() {
			var uid string
			var seq int64
			if err = rows.Scan(&uid, &seq); err != nil {
				return domain.ReadReceipts{}, err
			}
			if seq > c.LastSeq {
				seq = c.LastSeq
			}
			if seq < 0 {
				seq = 0
			}
			cursors[uid] = seq
		}
		if err = rows.Err(); err != nil {
			return domain.ReadReceipts{}, err
		}
	}
	countBySeq := make(map[int64]int64)
	var members []domain.MemberReadReceipt
	if c.Type == "direct" || c.Type == "group" {
		members = make([]domain.MemberReadReceipt, 0, len(eligible))
	}
	for _, uid := range eligible {
		seq := cursors[uid]
		countBySeq[seq]++
		if c.Type == "direct" || c.Type == "group" {
			members = append(members, domain.MemberReadReceipt{UserID: uid, ReadSeq: seq})
		}
	}
	seqCounts := make([]domain.ReadSeqCount, 0, len(countBySeq))
	for seq, count := range countBySeq {
		seqCounts = append(seqCounts, domain.ReadSeqCount{ReadSeq: seq, Count: count})
	}
	sort.Slice(seqCounts, func(i, j int) bool {
		return seqCounts[i].ReadSeq > seqCounts[j].ReadSeq
	})
	return domain.ReadReceipts{
		ConversationID: c.ID,
		EligibleCount:  int64(len(eligible)),
		ReadSeqCounts:  seqCounts,
		Members:        members,
	}, nil
}

func (r *Repository) EnqueueOutboxHint(ctx context.Context, payload any) error {
	b, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	_, err = r.DB.Conn(ctx).Exec(ctx, "INSERT INTO outbox(payload) VALUES($1)", b)
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
	rows, err := r.DB.Conn(ctx).Query(ctx, `SELECT e.id,e.payload,m.retracted_at,COALESCE(m.reactions_version,0),(e.payload->>'type'='membership.changed' OR (EXISTS(SELECT 1 FROM workspace_members wm WHERE wm.workspace_id=e.workspace_id AND wm.user_id=$1) AND (e.conversation_id IS NULL OR EXISTS(SELECT 1 FROM conversations c WHERE c.id=e.conversation_id AND (c.type='channel' OR EXISTS(SELECT 1 FROM conversation_members cm WHERE cm.conversation_id=c.id AND cm.user_id=$1)))))) AS allowed FROM sync_events e LEFT JOIN messages m ON m.id=COALESCE(e.payload#>>'{message,id}',e.payload->>'messageId') WHERE e.user_id=$1 AND e.id>$2 AND e.id<=$3 ORDER BY e.id LIMIT $4`, userID, afterID, upToID, limit)
	if err != nil {
		return nil, afterID, err
	}
	defer rows.Close()
	out := []domain.Event{}
	n := afterID
	for rows.Next() {
		var id int64
		var b []byte
		var allowed bool
		var retractedAt *time.Time
		var reactionsVersion int64
		if err = rows.Scan(&id, &b, &retractedAt, &reactionsVersion, &allowed); err != nil {
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
		if retractedAt != nil {
			if ev.Message != nil {
				ev.Message.Body = ""
				ev.Message.Attachments = nil
				ev.Message.Mentions = nil
				ev.Message.Reactions = nil
				ev.Message.RetractedAt = retractedAt
				ev.Message.ReactionsVersion = reactionsVersion
			}
			if ev.Type == "reaction.updated" {
				ev.Reactions = nil
				ev.ReactionsVersion = reactionsVersion
			}
		}
		out = append(out, ev)
	}
	return out, n, rows.Err()
}
