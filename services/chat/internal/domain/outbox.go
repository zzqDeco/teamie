package domain

import "time"

const HintChannel = "teamie:im:hints:v1"

// Event (also aliased as OutboxEvent) represents a sequenced per-recipient sync event.
type Event struct {
	ID                string        `json:"id"`
	Type              string        `json:"type"`
	ConversationID    string        `json:"conversationId,omitempty"`
	WorkspaceID       string        `json:"workspaceId,omitempty"`
	Message           *Message      `json:"message,omitempty"`
	Conversation      *Conversation `json:"conversation,omitempty"`
	ReadSeq           *int64        `json:"readSeq,omitempty"`
	MessageID         string        `json:"messageId,omitempty"`
	Reactions         []Reaction    `json:"reactions,omitempty"`
	ReactionsVersion  int64         `json:"reactionsVersion,omitempty,string"`
	NotificationLevel *string       `json:"notificationLevel,omitempty"`
	// ManualUnreadSeq accompanies every read.updated event; 0 means no marker.
	ManualUnreadSeq *int64     `json:"manualUnreadSeq,omitempty"`
	Pinned          *bool      `json:"pinned,omitempty"`
	PinnedBy        string     `json:"pinnedBy,omitempty"`
	PinnedAt        *time.Time `json:"pinnedAt,omitempty"`
	PinsVersion     int64      `json:"pinsVersion,omitempty,string"`
	Saved           *bool      `json:"saved,omitempty"`
	SavedAt         *time.Time `json:"savedAt,omitempty"`
	CreatedAt       time.Time  `json:"createdAt"`
}

// OutboxEvent is an alias for Event.
type OutboxEvent = Event
