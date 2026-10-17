package engine

import (
	"encoding/json"
	"errors"

	"github.com/cloudwego/eino/adk"
)

const contextProfileLegacy = "teamie-context-v037"
const contextProfileV1 = "teamie-context-v038"

func contextProfile(run *Run) string {
	if run == nil || run.ContextProfile == "" {
		return contextProfileLegacy
	}
	return run.ContextProfile
}

func modelRetryObservation(err error) bool {
	var retry *adk.WillRetryError
	return errors.As(err, &retry)
}

func canonicalContextPolicy(profile string, raw json.RawMessage) (string, json.RawMessage, error) {
	if profile == "" {
		profile = contextProfileLegacy
	}
	if profile != contextProfileLegacy && profile != contextProfileV1 {
		return "", nil, errors.New("unknown context policy")
	}
	if len(raw) == 0 {
		raw = json.RawMessage(`{}`)
	}
	if len(raw) > 4096 {
		return "", nil, errors.New("context policy exceeds limit")
	}
	var value map[string]any
	if json.Unmarshal(raw, &value) != nil || value == nil {
		return "", nil, errors.New("invalid context policy")
	}
	if profile == contextProfileLegacy && len(value) != 0 {
		return "", nil, errors.New("legacy context policy has no budget snapshot")
	}
	if profile == contextProfileV1 {
		if _, err := ParseContextBudgetSnapshot(raw); err != nil {
			return "", nil, err
		}
	}
	b, err := json.Marshal(value)
	return profile, b, err
}

// InputSource is a reference, never an inferred destination or an ACL grant.
// Source bodies remain in the ordinary IM history and private Session events.
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

func inputSources(inputs []Input) ([]InputSource, bool) {
	const maximum = 128
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
		if len(refs) == maximum {
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
		if in.Kind != "task.notification" {
			ref := InputSource{EventID: in.EventID, WorkspaceID: in.WorkspaceID, ConversationID: in.ConversationID, MessageID: in.MessageID, ReplyToID: in.ReplyToID, SenderID: in.SenderID, SenderKind: in.SenderKind}
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
