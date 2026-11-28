package app

import (
	"context"
	"slices"
	"strings"
	"time"
)

const maxPushCandidateBatch = 500

// PushRecipientEvaluation holds opaque wake targets authorized to receive a background push notification.
type PushRecipientEvaluation struct {
	WorkspaceID      string
	ConversationID   string
	MessageID        string
	RecipientUserIDs []string
}

// EvaluatePushRecipients filters candidate subscribers by current conversation ACL,
// notification preferences, unretracted/unread message state, and Realtime presence
// (suppressing users who are actively "online"). No message bodies or titles are returned.
func (s *Service) EvaluatePushRecipients(ctx context.Context, conversationID, messageID string, candidateUserIDs []string) (PushRecipientEvaluation, error) {
	out := PushRecipientEvaluation{
		ConversationID:   strings.TrimSpace(conversationID),
		MessageID:        strings.TrimSpace(messageID),
		RecipientUserIDs: []string{},
	}
	if s.Flags == nil || !s.Flags.Enabled("im.push.web") {
		return out, nil
	}
	if out.ConversationID == "" || out.MessageID == "" || len(candidateUserIDs) == 0 {
		return out, nil
	}
	candidates := normalizePushCandidates(candidateUserIDs, maxPushCandidateBatch)
	if len(candidates) == 0 {
		return out, nil
	}

	workspaceID, eligible, err := s.Messages.EvaluatePushRecipients(ctx, out.ConversationID, out.MessageID, candidates)
	if err != nil {
		return out, err
	}
	out.WorkspaceID = workspaceID
	if len(eligible) == 0 {
		return out, nil
	}

	if s.Presence != nil {
		lookupCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		states, presenceErr := s.Presence.Presence(lookupCtx, eligible)
		cancel()
		if presenceErr == nil {
			filtered := make([]string, 0, len(eligible))
			for _, uid := range eligible {
				if states[uid] == "online" {
					continue
				}
				filtered = append(filtered, uid)
			}
			eligible = filtered
		}
	}

	out.RecipientUserIDs = eligible
	return out, nil
}

func normalizePushCandidates(ids []string, limit int) []string {
	seen := make(map[string]bool, len(ids))
	out := make([]string, 0, min(len(ids), limit))
	for _, raw := range ids {
		id := strings.TrimSpace(raw)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
		if len(out) >= limit {
			break
		}
	}
	slices.Sort(out)
	return out
}
