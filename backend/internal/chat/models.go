package chat

import "time"

type Workspace struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	CreatedBy string    `json:"createdBy"`
	CreatedAt time.Time `json:"createdAt"`
}
type Conversation struct {
	ID               string    `json:"id"`
	WorkspaceID      string    `json:"workspaceId"`
	Type             string    `json:"type"`
	Name             string    `json:"name"`
	MemberIDs        []string  `json:"memberIds"`
	LastSeq          int64     `json:"lastSeq"`
	ReadSeq          int64     `json:"readSeq"`
	UpdatedAt        time.Time `json:"updatedAt"`
	CreatedBy        string    `json:"createdBy,omitempty"`
	CanManageMembers bool      `json:"canManageMembers"`
}
type Message struct {
	ID              string       `json:"id"`
	ConversationID  string       `json:"conversationId"`
	SenderID        string       `json:"senderId"`
	SenderName      string       `json:"senderName"`
	SenderKind      string       `json:"senderKind"`
	ClientMessageID string       `json:"clientMessageId"`
	Seq             int64        `json:"seq"`
	Body            string       `json:"body"`
	ReplyToID       *string      `json:"replyToId,omitempty"`
	CreatedAt       time.Time    `json:"createdAt"`
	EditedAt        *time.Time   `json:"editedAt,omitempty"`
	RetractedAt     *time.Time   `json:"retractedAt,omitempty"`
	Attachments     []Attachment `json:"attachments,omitempty"`
}

type Attachment struct {
	ID        string `json:"id"`
	Filename  string `json:"filename"`
	MediaType string `json:"mediaType"`
	SizeBytes int    `json:"sizeBytes"`
	SHA256    string `json:"sha256"`
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
