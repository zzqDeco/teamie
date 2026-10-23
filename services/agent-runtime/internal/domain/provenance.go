package domain

import (
	"slices"
	"sort"
)

// MaxExtractedInputSources caps the inline provenance source list on a single input batch.
const MaxExtractedInputSources = 128

// InputSource is a provenance reference, never an inferred destination or an ACL grant.
// Source bodies remain in ordinary IM history and private Session events.
type InputSource struct {
	EventID        string   `json:"eventId"`
	WorkspaceID    string   `json:"workspaceId"`
	ConversationID string   `json:"conversationId"`
	MessageID      string   `json:"messageId"`
	ReplyToID      string   `json:"replyToId,omitempty"`
	SenderID       string   `json:"senderId,omitempty"`
	SenderKind     string   `json:"senderKind,omitempty"`
	AttachmentIDs  []string `json:"attachmentIds,omitempty"`
}

// SourceMessageSnapshot holds the live IM message properties verified during ACL re-authorization.
type SourceMessageSnapshot struct {
	ID             string
	ConversationID string
	SenderID       string
	Retracted      bool
	AttachmentIDs  []string
}

// ExtractInputSources extracts deduplicated InputSource references from a turn's Inputs,
// reporting whether the source list overflowed MaxExtractedInputSources.
func ExtractInputSources(inputs []Input) ([]InputSource, bool) {
	refs := []InputSource{}
	seen := map[string]bool{}
	overflow := false
	appendRef := func(ref InputSource) {
		if ref.ConversationID == "" || ref.MessageID == "" {
			return
		}
		key := ref.WorkspaceID + "/" + ref.ConversationID + "/" + ref.MessageID + "/" + ref.EventID
		if seen[key] {
			return
		}
		seen[key] = true
		if len(refs) == MaxExtractedInputSources {
			overflow = true
			return
		}
		ref.AttachmentIDs = append([]string(nil), ref.AttachmentIDs...)
		refs = append(refs, ref)
	}
	for _, in := range inputs {
		if in.SourceOverflow {
			overflow = true
		}
		if in.Kind != "task.notification" && in.Kind != "schedule.wakeup" {
			ref := InputSource{
				EventID:        in.EventID,
				WorkspaceID:    in.WorkspaceID,
				ConversationID: in.ConversationID,
				MessageID:      in.MessageID,
				ReplyToID:      in.ReplyToID,
				SenderID:       in.SenderID,
				SenderKind:     in.SenderKind,
			}
			for _, attachment := range in.Attachments {
				ref.AttachmentIDs = append(ref.AttachmentIDs, attachment.ID)
			}
			appendRef(ref)
		}
		for _, ref := range in.SourceRefs {
			appendRef(ref)
		}
	}
	return refs, overflow
}

// CanonicalContextSources validates, deduplicates, and merges InputSource references.
// Conflicting non-empty identities for the same source key return ErrReplayConflict.
func CanonicalContextSources(refs []InputSource) ([]InputSource, error) {
	byKey := make(map[string]InputSource, len(refs))
	for _, ref := range refs {
		if !ValidUUID(ref.ConversationID) || !ValidUUID(ref.MessageID) || (ref.WorkspaceID != "" && !ValidUUID(ref.WorkspaceID)) || (ref.SenderID != "" && !ValidUUID(ref.SenderID)) {
			// A conversation-only source represents metadata from a committed
			// im_list_conversations response, never a fabricated message ID.
			if !ValidUUID(ref.ConversationID) || ref.MessageID != "" || ref.EventID != "" || ref.ReplyToID != "" || ref.SenderID != "" || ref.SenderKind != "" || len(ref.AttachmentIDs) != 0 || (ref.WorkspaceID != "" && !ValidUUID(ref.WorkspaceID)) {
				return nil, ErrContextUnavailable
			}
		}
		ref.AttachmentIDs = append([]string(nil), ref.AttachmentIDs...)
		for _, id := range ref.AttachmentIDs {
			if !ValidUUID(id) {
				return nil, ErrContextUnavailable
			}
		}
		sort.Strings(ref.AttachmentIDs)
		ref.AttachmentIDs = slices.Compact(ref.AttachmentIDs)
		key := ref.EventID + "/" + ref.ConversationID + "/" + ref.MessageID
		if old, exists := byKey[key]; exists {
			for _, pair := range []struct {
				old *string
				new string
			}{
				{&old.WorkspaceID, ref.WorkspaceID},
				{&old.SenderID, ref.SenderID},
				{&old.SenderKind, ref.SenderKind},
				{&old.ReplyToID, ref.ReplyToID},
			} {
				if pair.new != "" {
					if *pair.old != "" && *pair.old != pair.new {
						return nil, ErrReplayConflict
					}
					*pair.old = pair.new
				}
			}
			old.AttachmentIDs = append(old.AttachmentIDs, ref.AttachmentIDs...)
			sort.Strings(old.AttachmentIDs)
			old.AttachmentIDs = slices.Compact(old.AttachmentIDs)
			ref = old
		}
		byKey[key] = ref
	}
	keys := make([]string, 0, len(byKey))
	for key := range byKey {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := make([]InputSource, 0, len(keys))
	for _, key := range keys {
		out = append(out, byKey[key])
	}
	return out, nil
}

// VerifyConversationSourceAccess checks whether a conversation-only InputSource is still
// accessible given the Agent's current conversation-to-workspace membership map.
func VerifyConversationSourceAccess(ref InputSource, conversationWorkspaces map[string]string) error {
	wsID, exists := conversationWorkspaces[ref.ConversationID]
	if !exists || (ref.WorkspaceID != "" && wsID != ref.WorkspaceID) {
		return ErrContextUnavailable
	}
	return nil
}

// VerifyMessageSourceAccess checks whether a message-scoped InputSource is still
// accessible and untampered given the live IM message snapshot.
func VerifyMessageSourceAccess(ref InputSource, msg *SourceMessageSnapshot) error {
	if msg == nil || msg.ID != ref.MessageID || msg.ConversationID != ref.ConversationID || msg.Retracted || (ref.SenderID != "" && msg.SenderID != ref.SenderID) {
		return ErrContextUnavailable
	}
	for _, id := range ref.AttachmentIDs {
		if !slices.Contains(msg.AttachmentIDs, id) {
			return ErrContextUnavailable
		}
	}
	return nil
}
