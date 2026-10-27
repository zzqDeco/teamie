package domain

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"time"
	"unicode/utf8"
)

type Conversation struct {
	ID                string    `json:"id"`
	WorkspaceID       string    `json:"workspaceId"`
	Type              string    `json:"type"`
	Name              string    `json:"name"`
	MemberIDs         []string  `json:"memberIds"`
	LastSeq           int64     `json:"lastSeq"`
	ReadSeq           int64     `json:"readSeq"`
	UpdatedAt         time.Time `json:"updatedAt"`
	CreatedBy         string    `json:"createdBy,omitempty"`
	CanManageMembers  bool      `json:"canManageMembers"`
	NotificationLevel string    `json:"notificationLevel,omitempty"`
}

type ReadSeqCount struct {
	ReadSeq int64 `json:"readSeq"`
	Count   int64 `json:"count"`
}

type MemberReadReceipt struct {
	UserID  string `json:"userId"`
	ReadSeq int64  `json:"readSeq"`
}

type ReadReceipts struct {
	ConversationID string              `json:"conversationId"`
	EligibleCount  int64               `json:"eligibleCount"`
	ReadSeqCounts  []ReadSeqCount      `json:"readSeqCounts"`
	Members        []MemberReadReceipt `json:"members,omitempty"`
}

func ValidateNotificationLevel(level string) error {
	if level != "all" && level != "mentions" && level != "none" {
		return ErrInvalidNotificationLevel()
	}
	return nil
}

type AttentionConversation struct {
	ID          string `json:"id"`
	WorkspaceID string `json:"workspaceId"`
	Name        string `json:"name"`
	Type        string `json:"type"`
}

type AttentionItem struct {
	Message       Message               `json:"message"`
	Conversation  AttentionConversation `json:"conversation"`
	WorkspaceName string                `json:"workspaceName"`
	Reasons       []string              `json:"reasons"`
	IsRead        bool                  `json:"isRead"`
}

type AttentionPage struct {
	Items                         []AttentionItem   `json:"items"`
	Cursor                        string            `json:"cursor,omitempty"`
	UnreadCount                   int64             `json:"unreadCount"`
	ChannelUnreadCount            int64             `json:"channelUnreadCount"`
	AttentionUnreadByConversation []AttentionUnread `json:"attentionUnreadByConversation"`
}

type AttentionUnread struct {
	ConversationID string `json:"conversationId"`
	UnreadCount    int64  `json:"unreadCount"`
}

type WorkspaceSearchItem struct {
	Message      Message               `json:"message"`
	Conversation AttentionConversation `json:"conversation"`
}

type WorkspaceSearchPage struct {
	Items  []WorkspaceSearchItem `json:"items"`
	Cursor string                `json:"cursor,omitempty"`
}

type AttentionCursor struct {
	At time.Time `json:"at"`
	ID string    `json:"id"`
}

func ParseAttentionCursor(raw string) (AttentionCursor, error) {
	var cursor AttentionCursor
	if raw == "" {
		return cursor, nil
	}
	if len(raw) > 512 {
		return cursor, ErrInvalidAttentionCursor()
	}
	b, err := base64.RawURLEncoding.Strict().DecodeString(raw)
	if err != nil || json.Unmarshal(b, &cursor) != nil || cursor.At.IsZero() || cursor.ID == "" || len(cursor.ID) > 128 || strings.ContainsRune(cursor.ID, 0) {
		return cursor, ErrInvalidAttentionCursor()
	}
	return cursor, nil
}

func ParseWorkspaceSearchCursor(raw string) (AttentionCursor, error) {
	cursor, err := ParseAttentionCursor(raw)
	if err != nil {
		return cursor, ErrInvalidSearchCursor()
	}
	return cursor, nil
}

func EncodeCursor(at time.Time, id string) (string, error) {
	raw, err := json.Marshal(AttentionCursor{At: at, ID: id})
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

func ValidateWorkspaceSearchQuery(query, rawCursor string, limit int) (string, AttentionCursor, error) {
	query = strings.TrimSpace(query)
	if limit < 1 || limit > 50 || len([]rune(query)) < 1 || len([]rune(query)) > 200 || !utf8.ValidString(query) || strings.ContainsRune(query, 0) {
		return "", AttentionCursor{}, ErrInvalidSearchQuery()
	}
	cursor, err := ParseWorkspaceSearchCursor(rawCursor)
	if err != nil {
		return "", AttentionCursor{}, err
	}
	return query, cursor, nil
}
