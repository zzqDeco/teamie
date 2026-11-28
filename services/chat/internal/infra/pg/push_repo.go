package pg

import (
	"context"
)

// EvaluatePushRecipients computes which candidate user IDs are authorized and
// eligible (under current workspace/conversation ACL, notification levels, unread
// state, and mention/reply rules) to receive a background wake notification for
// an unretracted message. Zero message content or titles are returned.
func (r *Repository) EvaluatePushRecipients(ctx context.Context, conversationID, messageID string, candidateUserIDs []string) (string, []string, error) {
	if len(candidateUserIDs) == 0 {
		return "", []string{}, nil
	}
	rows, err := r.DB.Conn(ctx).Query(ctx, `
WITH target AS (
  SELECT m.id,
         m.conversation_id,
         m.sender_id,
         m.seq,
         m.mentions,
         m.broadcast_recipient_ids,
         c.workspace_id,
         c.type AS conversation_type,
         parent.sender_id AS parent_sender_id,
         parent.retracted_at AS parent_retracted_at
  FROM messages m
  JOIN conversations c ON c.id = m.conversation_id
  LEFT JOIN messages parent ON parent.id = m.reply_to_id AND parent.conversation_id = c.id
  WHERE m.id = $2 AND m.conversation_id = $1 AND m.retracted_at IS NULL
)
SELECT t.workspace_id, u.user_id
FROM target t
JOIN unnest($3::text[]) AS u(user_id) ON u.user_id <> t.sender_id
JOIN workspace_members wm ON wm.workspace_id = t.workspace_id AND wm.user_id = u.user_id
LEFT JOIN conversation_notification_levels nl ON nl.conversation_id = t.conversation_id AND nl.user_id = u.user_id
LEFT JOIN read_cursors rc ON rc.conversation_id = t.conversation_id AND rc.user_id = u.user_id
WHERE (t.conversation_type = 'channel' OR EXISTS(
        SELECT 1 FROM conversation_members cm WHERE cm.conversation_id = t.conversation_id AND cm.user_id = u.user_id
      ))
  AND COALESCE(rc.seq, 0) < t.seq
  AND COALESCE(nl.level, 'all') <> 'none'
  AND (
    t.conversation_type <> 'channel'
    OR t.mentions @> jsonb_build_array(jsonb_build_object('userId', u.user_id))
    OR u.user_id = ANY(t.broadcast_recipient_ids)
    OR (t.parent_sender_id = u.user_id AND t.parent_retracted_at IS NULL)
  )
ORDER BY u.user_id`, conversationID, messageID, candidateUserIDs)
	if err != nil {
		return "", nil, err
	}
	defer rows.Close()

	var workspaceID string
	eligible := []string{}
	for rows.Next() {
		var wsID, uid string
		if err = rows.Scan(&wsID, &uid); err != nil {
			return "", nil, err
		}
		if workspaceID == "" {
			workspaceID = wsID
		}
		eligible = append(eligible, uid)
	}
	return workspaceID, eligible, rows.Err()
}
