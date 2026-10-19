package chat

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"strings"
	"unicode/utf8"

	"teamie/backend/internal/api"
)

type WorkspaceSearchItem struct {
	Message      Message               `json:"message"`
	Conversation AttentionConversation `json:"conversation"`
}

type WorkspaceSearchPage struct {
	Items  []WorkspaceSearchItem `json:"items"`
	Cursor string                `json:"cursor,omitempty"`
}

func parseWorkspaceSearchCursor(raw string) (attentionCursor, error) {
	cursor, err := parseAttentionCursor(raw)
	if err != nil {
		return cursor, api.Err(400, "invalid_query", "Invalid search cursor")
	}
	return cursor, nil
}

func (s *Store) searchWorkspaceCandidateIDs(ctx context.Context, user, workspaceID, query string, before any, cursorID string, limit int) ([]string, error) {
	rows, err := s.DB.Query(ctx, `
SELECT m.id
FROM messages m
JOIN conversations c ON c.id=m.conversation_id
JOIN workspace_members wm ON wm.workspace_id=c.workspace_id AND wm.user_id=$1
WHERE c.workspace_id=$2 AND m.retracted_at IS NULL
  AND (c.type='channel' OR EXISTS(SELECT 1 FROM conversation_members cm WHERE cm.conversation_id=c.id AND cm.user_id=$1))
  AND strpos(lower(m.body),lower($3))>0
  AND ($4::timestamptz IS NULL OR (m.created_at,m.id)<($4,$5::text))
ORDER BY m.created_at DESC,m.id DESC LIMIT $6
`, user, workspaceID, query, before, cursorID, limit)
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

// SearchWorkspaceMessages searches unretracted messages across channels and
// authorized direct/group conversations in the target workspace. Candidate
// message IDs are scanned without holding workspace or conversation locks, and
// only the matched rows are verified by primary key under sorted share locks.
func (s *Store) SearchWorkspaceMessages(ctx context.Context, user, workspaceID, query, rawCursor string, limit int) (WorkspaceSearchPage, error) {
	out := WorkspaceSearchPage{Items: []WorkspaceSearchItem{}}
	query = strings.TrimSpace(query)
	if limit < 1 || limit > 50 || len([]rune(query)) < 1 || len([]rune(query)) > 200 || !utf8.ValidString(query) || strings.ContainsRune(query, 0) {
		return out, api.Err(400, "invalid_query", "Invalid search query")
	}
	cursor, err := parseWorkspaceSearchCursor(rawCursor)
	if err != nil {
		return out, err
	}
	var before any
	if rawCursor != "" {
		before = cursor.At
	}
	for attempt := 0; attempt < 2; attempt++ {
		candidateIDs, err := s.searchWorkspaceCandidateIDs(ctx, user, workspaceID, query, before, cursor.ID, limit+1)
		if err != nil {
			return out, err
		}
		tx, err := s.DB.Begin(ctx)
		if err != nil {
			return out, err
		}
		if err = workspaceLock(ctx, tx, workspaceID, user, false, false); err != nil {
			tx.Rollback(ctx)
			return out, err
		}
		var member bool
		if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM workspace_members WHERE workspace_id=$1 AND user_id=$2)`, workspaceID, user).Scan(&member); err != nil {
			tx.Rollback(ctx)
			return out, err
		}
		if !member {
			tx.Rollback(ctx)
			return out, api.Err(404, "not_found", "This resource is unavailable")
		}
		if len(candidateIDs) == 0 {
			tx.Rollback(ctx)
			return out, nil
		}
		conversations, err := attentionLockIDs(ctx, tx, `SELECT c.id FROM conversations c JOIN workspace_members wm ON wm.workspace_id=c.workspace_id AND wm.user_id=$1 WHERE c.workspace_id=$2 AND c.id IN (SELECT m.conversation_id FROM messages m WHERE m.id=ANY($3::text[])) AND (c.type='channel' OR EXISTS(SELECT 1 FROM conversation_members cm WHERE cm.conversation_id=c.id AND cm.user_id=$1)) ORDER BY c.id FOR SHARE OF c`, user, workspaceID, candidateIDs)
		if err != nil {
			tx.Rollback(ctx)
			return out, err
		}
		if len(conversations) == 0 {
			tx.Rollback(ctx)
			if len(candidateIDs) == limit+1 && attempt == 0 {
				continue
			}
			return out, nil
		}
		var rawItems []byte
		err = tx.QueryRow(ctx, `
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
`, user, query, before, cursor.ID, limit+1, candidateIDs, conversations).Scan(&rawItems)
		tx.Rollback(ctx)
		if err != nil {
			return out, err
		}
		items := []WorkspaceSearchItem{}
		if err = json.Unmarshal(rawItems, &items); err != nil {
			return out, err
		}
		if len(items) < len(candidateIDs) && len(candidateIDs) == limit+1 && attempt == 0 {
			continue
		}
		out.Items = items
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
	return out, nil
}
