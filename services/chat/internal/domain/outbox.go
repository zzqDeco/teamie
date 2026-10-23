package domain

import "time"

const HintChannel = "teamie:im:hints:v1"

// Event (also aliased as OutboxEvent) represents a sequenced per-recipient sync event.
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

// OutboxEvent is an alias for Event.
type OutboxEvent = Event
