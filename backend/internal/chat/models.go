package chat

import (
	"teamie/backend/internal/chat/domain"
	"teamie/backend/pkg/imtext"
	"time"
)

type Workspace = domain.Workspace
type Conversation = domain.Conversation
type Attachment = domain.Attachment
type Member = domain.Member
type AttentionConversation = domain.AttentionConversation

type Message struct {
	ID              string           `json:"id"`
	ConversationID  string           `json:"conversationId"`
	SenderID        string           `json:"senderId"`
	SenderName      string           `json:"senderName"`
	SenderKind      string           `json:"senderKind"`
	ClientMessageID string           `json:"clientMessageId"`
	Seq             int64            `json:"seq"`
	Body            string           `json:"body"`
	ReplyToID       *string          `json:"replyToId,omitempty"`
	CreatedAt       time.Time        `json:"createdAt"`
	EditedAt        *time.Time       `json:"editedAt,omitempty"`
	RetractedAt     *time.Time       `json:"retractedAt,omitempty"`
	Attachments     []Attachment     `json:"attachments,omitempty"`
	Mentions        []imtext.Mention `json:"mentions,omitempty"`
}

type Event struct {
	ID             string        `json:"id"`
	Type           string        `json:"type"`
	ConversationID string        `json:"conversationId,omitempty"`
	WorkspaceID    string        `json:"workspaceId,omitempty"`
	Message        *Message      `json:"message,omitempty"`
	Conversation   *Conversation `json:"conversation,omitempty"`
	ReadSeq        *int64        `json:"readSeq,omitempty"`
	CreatedAt      time.Time     `json:"createdAt"`
}
