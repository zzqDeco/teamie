package domain

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	MaxSearchTerms      = 8
	MaxSearchQueryRunes = 200
)

type GlobalSearchItem struct {
	Message       Message               `json:"message"`
	Conversation  AttentionConversation `json:"conversation"`
	WorkspaceName string                `json:"workspaceName"`
}

type GlobalSearchPage struct {
	Items  []GlobalSearchItem `json:"items"`
	Cursor string             `json:"cursor,omitempty"`
}

type GlobalSearchQuery struct {
	Terms          []string
	WorkspaceID    string
	ConversationID string
	SenderID       string
	HasFiles       bool
	After          *time.Time
	Before         *time.Time
	Cursor         AttentionCursor
	HasCursor      bool
	Limit          int
}

type AttachmentGalleryCursor struct {
	Seq int64  `json:"seq"`
	ID  string `json:"id"`
}

type AttachmentGalleryQuery struct {
	Kind      string
	Query     string
	Cursor    AttachmentGalleryCursor
	HasCursor bool
	Limit     int
}

type ConversationAttachmentItem struct {
	Attachment  Attachment `json:"attachment"`
	MessageID   string     `json:"messageId"`
	MessageSeq  int64      `json:"messageSeq"`
	SenderID    string     `json:"senderId"`
	SenderName  string     `json:"senderName"`
	SenderKind  string     `json:"senderKind"`
	CreatedAt   time.Time  `json:"createdAt"`
	IsForwarded bool       `json:"isForwarded"`
}

type ConversationAttachmentPage struct {
	Items  []ConversationAttachmentItem `json:"items"`
	Cursor string                       `json:"cursor,omitempty"`
}

// ParseSearchTerms splits a user search string into deduplicated literal terms
// (supporting balanced double-quoted phrases) with strict length and UTF-8 checks.
func ParseSearchTerms(raw string) ([]string, error) {
	if !utf8.ValidString(raw) || strings.ContainsRune(raw, 0) {
		return nil, ErrInvalidSearchQuery()
	}
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil, nil
	}
	if utf8.RuneCountInString(trimmed) > MaxSearchQueryRunes {
		return nil, ErrInvalidSearchQuery()
	}
	runes := []rune(trimmed)
	tokens := []string{}
	i := 0
	for i < len(runes) {
		for i < len(runes) && unicode.IsSpace(runes[i]) {
			i++
		}
		if i >= len(runes) {
			break
		}
		if runes[i] == '"' {
			closeIdx := -1
			for j := i + 1; j < len(runes); j++ {
				if runes[j] == '"' {
					closeIdx = j
					break
				}
			}
			if closeIdx >= i+1 {
				phrase := strings.TrimSpace(string(runes[i+1 : closeIdx]))
				if phrase != "" {
					tokens = append(tokens, phrase)
				}
				i = closeIdx + 1
				continue
			}
		}
		start := i
		for i < len(runes) && !unicode.IsSpace(runes[i]) {
			i++
		}
		tok := string(runes[start:i])
		if tok != "" {
			tokens = append(tokens, tok)
		}
	}
	seen := make(map[string]struct{}, len(tokens))
	dedup := make([]string, 0, len(tokens))
	for _, tok := range tokens {
		key := strings.ToLower(tok)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		dedup = append(dedup, tok)
		if len(dedup) > MaxSearchTerms {
			return nil, ErrInvalidSearchQuery()
		}
	}
	return dedup, nil
}

func parseOptionalFilterID(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", nil
	}
	if len(trimmed) > 128 || !utf8.ValidString(trimmed) || strings.ContainsRune(trimmed, 0) {
		return "", ErrInvalidSearchQuery()
	}
	return trimmed, nil
}

func parseOptionalTimestamp(raw string) (*time.Time, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil, nil
	}
	if len(trimmed) > 64 || !utf8.ValidString(trimmed) || strings.ContainsRune(trimmed, 0) {
		return nil, ErrInvalidSearchQuery()
	}
	parsed, err := time.Parse(time.RFC3339Nano, trimmed)
	if err != nil {
		return nil, ErrInvalidSearchQuery()
	}
	utc := parsed.UTC()
	return &utc, nil
}

// ValidateGlobalSearchQuery parses and validates all parameters for GET /api/v1/messages/search.
func ValidateGlobalSearchQuery(rawQ, rawWorkspaceID, rawConversationID, rawSenderID, rawHasFiles, rawAfter, rawBefore, rawCursor string, limit int) (GlobalSearchQuery, error) {
	var out GlobalSearchQuery
	if limit < 1 || limit > 50 {
		return out, ErrInvalidSearchQuery()
	}
	terms, err := ParseSearchTerms(rawQ)
	if err != nil {
		return out, err
	}
	wsID, err := parseOptionalFilterID(rawWorkspaceID)
	if err != nil {
		return out, err
	}
	convID, err := parseOptionalFilterID(rawConversationID)
	if err != nil {
		return out, err
	}
	senderID, err := parseOptionalFilterID(rawSenderID)
	if err != nil {
		return out, err
	}
	hasFiles := false
	switch strings.TrimSpace(rawHasFiles) {
	case "", "false":
		hasFiles = false
	case "true":
		hasFiles = true
	default:
		return out, ErrInvalidSearchQuery()
	}
	after, err := parseOptionalTimestamp(rawAfter)
	if err != nil {
		return out, err
	}
	before, err := parseOptionalTimestamp(rawBefore)
	if err != nil {
		return out, err
	}
	if after != nil && before != nil && after.After(*before) {
		return out, ErrInvalidSearchQuery()
	}
	if len(terms) == 0 && senderID == "" && !hasFiles && after == nil && before == nil {
		return out, ErrInvalidSearchQuery()
	}
	cursor, err := ParseWorkspaceSearchCursor(rawCursor)
	if err != nil {
		return out, err
	}
	return GlobalSearchQuery{
		Terms:          terms,
		WorkspaceID:    wsID,
		ConversationID: convID,
		SenderID:       senderID,
		HasFiles:       hasFiles,
		After:          after,
		Before:         before,
		Cursor:         cursor,
		HasCursor:      rawCursor != "",
		Limit:          limit,
	}, nil
}

func ParseAttachmentGalleryCursor(raw string) (AttachmentGalleryCursor, error) {
	var cursor AttachmentGalleryCursor
	if raw == "" {
		return cursor, nil
	}
	if len(raw) > 512 {
		return cursor, ErrInvalidSearchCursor()
	}
	b, err := base64.RawURLEncoding.Strict().DecodeString(raw)
	if err != nil || json.Unmarshal(b, &cursor) != nil || cursor.Seq < 1 || cursor.ID == "" || len(cursor.ID) > 128 || strings.ContainsRune(cursor.ID, 0) {
		return cursor, ErrInvalidSearchCursor()
	}
	return cursor, nil
}

func EncodeAttachmentGalleryCursor(seq int64, id string) (string, error) {
	raw, err := json.Marshal(AttachmentGalleryCursor{Seq: seq, ID: id})
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

// ValidateAttachmentGalleryQuery parses and validates GET /api/v1/conversations/:id/attachments parameters.
func ValidateAttachmentGalleryQuery(rawKind, rawQ, rawCursor string, limit int) (AttachmentGalleryQuery, error) {
	var out AttachmentGalleryQuery
	if limit < 1 || limit > 60 {
		return out, ErrInvalidSearchQuery()
	}
	kind := strings.TrimSpace(rawKind)
	if kind == "" {
		kind = "all"
	}
	if kind != "all" && kind != "image" && kind != "file" {
		return out, ErrInvalidSearchQuery()
	}
	if !utf8.ValidString(rawQ) || strings.ContainsRune(rawQ, 0) {
		return out, ErrInvalidSearchQuery()
	}
	q := strings.TrimSpace(rawQ)
	if utf8.RuneCountInString(q) > MaxSearchQueryRunes {
		return out, ErrInvalidSearchQuery()
	}
	cursor, err := ParseAttachmentGalleryCursor(rawCursor)
	if err != nil {
		return out, err
	}
	return AttachmentGalleryQuery{
		Kind:      kind,
		Query:     q,
		Cursor:    cursor,
		HasCursor: rawCursor != "",
		Limit:     limit,
	}, nil
}
