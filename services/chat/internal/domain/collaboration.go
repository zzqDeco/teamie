package domain

import (
	"errors"
	"strings"
	"time"
)

const (
	// PinsPerConversation bounds the conversation-scoped pin list.
	PinsPerConversation = 50
	// SavedItemsPerUser bounds each caller's private saved list.
	SavedItemsPerUser = 1000
)

// ForwardSource names the message a new message forwards.
type ForwardSource struct {
	ConversationID string `json:"conversationId"`
	MessageID      string `json:"messageId"`
}

// ForwardedSnapshot is the immutable copy of a forwarded message captured in the
// send transaction. Source IDs are references, never access grants. Mentions are
// display-only copies and Attachments describe copies owned by the target.
type ForwardedSnapshot struct {
	SourceConversationID string       `json:"sourceConversationId"`
	SourceMessageID      string       `json:"sourceMessageId"`
	SenderID             string       `json:"senderId"`
	SenderName           string       `json:"senderName"`
	SenderKind           string       `json:"senderKind"`
	CreatedAt            time.Time    `json:"createdAt"`
	Body                 string       `json:"body"`
	Mentions             []Mention    `json:"mentions,omitempty"`
	Attachments          []Attachment `json:"attachments,omitempty"`
}

// SendRequest is one ordinary message send, optionally forwarding a source.
type SendRequest struct {
	ConversationID  string
	ClientMessageID string
	Body            string
	ReplyToID       *string
	AttachmentIDs   []string
	Mentions        []Mention
	ForwardOf       *ForwardSource
}

// ValidateForwardRequest checks the request shape before any lock is taken.
func ValidateForwardRequest(req SendRequest) error {
	if req.ForwardOf == nil {
		return nil
	}
	if req.ReplyToID != nil || len(req.AttachmentIDs) > 0 {
		return ErrInvalidForward()
	}
	if !BoundedString(req.ForwardOf.ConversationID, 1, 128) || !BoundedString(req.ForwardOf.MessageID, 1, 128) ||
		strings.ContainsRune(req.ForwardOf.ConversationID, 0) || strings.ContainsRune(req.ForwardOf.MessageID, 0) {
		return ErrInvalidForward()
	}
	return nil
}

// SameForwardSource compares immutable forward identities for retries.
func SameForwardSource(a, b *ForwardSource) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

// NewForwardedSnapshot captures source's current (post-edit) state. Forwarding a
// forward copies the existing snapshot so nesting is bounded to one level; the
// intermediate forwarder's comment is dropped.
func NewForwardedSnapshot(source Message) ForwardedSnapshot {
	if source.Forwarded != nil {
		snapshot := *source.Forwarded
		snapshot.Mentions = CanonicalMentions(snapshot.Mentions)
		if len(snapshot.Mentions) == 0 {
			snapshot.Mentions = nil
		}
		snapshot.Attachments = append([]Attachment(nil), snapshot.Attachments...)
		return snapshot
	}
	mentions := CanonicalMentions(source.Mentions)
	if len(mentions) == 0 {
		mentions = nil
	}
	return ForwardedSnapshot{
		SourceConversationID: source.ConversationID,
		SourceMessageID:      source.ID,
		SenderID:             source.SenderID,
		SenderName:           source.SenderName,
		SenderKind:           source.SenderKind,
		CreatedAt:            source.CreatedAt,
		Body:                 source.Body,
		Mentions:             mentions,
		Attachments:          append([]Attachment(nil), source.Attachments...),
	}
}

// MessageIdentity is the immutable original send identity used for retries.
type MessageIdentity struct {
	BodyHash      string
	AttachmentIDs []string
	Mentions      []Mention
	ForwardOf     *ForwardSource
}

// Pin is one conversation-scoped pin with its complete current message.
type Pin struct {
	Message      Message   `json:"message"`
	PinnedBy     string    `json:"pinnedBy"`
	PinnedByName string    `json:"pinnedByName"`
	PinnedAt     time.Time `json:"pinnedAt"`
}

// SavedMarker is the caller's private saved relationship for one message.
type SavedMarker struct {
	ConversationID string    `json:"conversationId"`
	MessageID      string    `json:"messageId"`
	SavedAt        time.Time `json:"savedAt"`
}

// SavedItem is one authorized saved message in the caller's saved list.
type SavedItem struct {
	Message       Message               `json:"message"`
	Conversation  AttentionConversation `json:"conversation"`
	WorkspaceName string                `json:"workspaceName"`
	SavedAt       time.Time             `json:"savedAt"`
}

// SavedPage is one newest-first page of saved items.
type SavedPage struct {
	Items  []SavedItem `json:"items"`
	Cursor string      `json:"cursor,omitempty"`
}

// ReadState is the caller's read cursor plus optional manual unread marker.
type ReadState struct {
	ReadSeq         int64
	ManualUnreadSeq int64
}

// ValidateSavedQuery validates a saved-list page request.
func ValidateSavedQuery(rawCursor string, limit int) (AttentionCursor, error) {
	if limit < 1 || limit > 100 {
		return AttentionCursor{}, ErrInvalidSavedQuery()
	}
	cursor, err := ParseAttentionCursor(rawCursor)
	if err != nil {
		return AttentionCursor{}, ErrInvalidSavedQuery()
	}
	return cursor, nil
}

// ValidateManualUnreadSeq requires 1 <= seq <= lastSeq.
func ValidateManualUnreadSeq(seq, lastSeq int64) error {
	if seq < 1 || seq > lastSeq {
		return ErrInvalidManualUnreadSeq()
	}
	return nil
}

func ErrInvalidForward() error {
	return Err(400, "invalid_forward", "forwardOf cannot be combined with replyToId or attachmentIds")
}

func ErrForwardCrossWorkspace() error {
	return Err(400, "forward_cross_workspace", "Messages can only be forwarded within the same workspace")
}

func ErrPinLimit() error {
	return Err(409, "pin_limit", "A conversation supports at most 50 pinned messages")
}

func ErrSavedLimit() error {
	return Err(409, "saved_limit", "You can save at most 1000 messages")
}

func ErrInvalidSavedQuery() error {
	return Err(400, "invalid_query", "Invalid saved items query")
}

func ErrInvalidManualUnreadSeq() error {
	return Err(400, "invalid_seq", "Unread marker must be between 1 and the conversation's last sequence")
}

func ErrRetractedMessageUnavailable() error {
	return Err(409, "message_retracted", "This message was retracted")
}

func ErrBroadcastForbidden() error {
	return Err(403, "broadcast_forbidden", "Only the workspace owner can broadcast in channels")
}

func ErrPresenceUnavailable() error {
	return Err(503, "presence_unavailable", "Presence service is temporarily unavailable; please retry")
}

func ErrInvalidAttentionQuery() error {
	return Err(400, "invalid_query", "messageId cannot be combined with cursor or unreadOnly")
}

// IsNotFound reports whether err is the ordinary not-found domain error.
func IsNotFound(err error) bool {
	var e *Error
	return errors.As(err, &e) && e.Status == 404 && e.Code == "not_found"
}
