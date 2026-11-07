package domain

import (
	"encoding/json"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

type Mention struct {
	UserID         string   `json:"userId,omitempty"`
	Kind           string   `json:"kind,omitempty"`
	Start          int      `json:"start"`
	End            int      `json:"end"`
	RecipientIDs   []string `json:"recipientIds,omitempty"`
	RecipientCount *int     `json:"recipientCount,omitempty"`
}

func (m Mention) MarshalJSON() ([]byte, error) {
	if m.RecipientIDs != nil && len(m.RecipientIDs) == 0 {
		type raw struct {
			UserID         string   `json:"userId,omitempty"`
			Kind           string   `json:"kind,omitempty"`
			Start          int      `json:"start"`
			End            int      `json:"end"`
			RecipientIDs   []string `json:"recipientIds"`
			RecipientCount *int     `json:"recipientCount,omitempty"`
		}
		return json.Marshal(raw{
			UserID:         m.UserID,
			Kind:           m.Kind,
			Start:          m.Start,
			End:            m.End,
			RecipientIDs:   []string{},
			RecipientCount: m.RecipientCount,
		})
	}
	type alias Mention
	return json.Marshal(alias(m))
}

type Reaction struct {
	Emoji   string   `json:"emoji"`
	Count   int64    `json:"count"`
	UserIDs []string `json:"userIds"`
}

// ReactionState is a complete list at one message-owned, durable revision.
type ReactionState struct {
	Reactions        []Reaction
	ReactionsVersion int64
}

type Message struct {
	ID               string       `json:"id"`
	ConversationID   string       `json:"conversationId"`
	SenderID         string       `json:"senderId"`
	SenderName       string       `json:"senderName"`
	SenderKind       string       `json:"senderKind"`
	ClientMessageID  string       `json:"clientMessageId"`
	Seq              int64        `json:"seq"`
	Body             string       `json:"body"`
	ReplyToID        *string      `json:"replyToId,omitempty"`
	CreatedAt        time.Time    `json:"createdAt"`
	EditedAt         *time.Time   `json:"editedAt,omitempty"`
	RetractedAt      *time.Time   `json:"retractedAt,omitempty"`
	Attachments      []Attachment `json:"attachments,omitempty"`
	Mentions         []Mention    `json:"mentions,omitempty"`
	Reactions        []Reaction   `json:"reactions,omitempty"`
	ReactionsVersion int64        `json:"reactionsVersion,string"`
	// Forwarded is the immutable snapshot captured when this message forwarded
	// another one. It is removed from every read after retraction.
	Forwarded *ForwardedSnapshot `json:"forwarded,omitempty"`
}

// ValidateReactionEmoji accepts exactly one fully-qualified Unicode Emoji 17.0
// sequence. Generation, validation and tests use only the pinned offline data.
func ValidateReactionEmoji(emoji string) error {
	if emoji == "" || len(emoji) > 64 || !utf8.ValidString(emoji) {
		return ErrInvalidReaction()
	}
	if _, ok := slices.BinarySearch(reactionEmojiSequences, emoji); !ok {
		return ErrInvalidReaction()
	}
	return nil
}

func ValidateBody(body string) error {
	if strings.TrimSpace(body) == "" || len([]rune(body)) > 8000 {
		return ErrInvalidBodyRange()
	}
	return nil
}

func ValidateMessage(body string, attachmentCount int) error {
	if attachmentCount == 0 {
		return ValidateBody(body)
	}
	if len([]rune(body)) > 8000 {
		return ErrInvalidBodyMax()
	}
	return nil
}

func ValidateMentions(body string, mentions []Mention) error {
	if len(mentions) > 32 || !utf8.ValidString(body) {
		return ErrInvalidMentionsRange()
	}
	boundaries := map[int]int{0: 0}
	units := 0
	for at, r := range body {
		boundaries[units] = at
		units++
		if r > 0xffff {
			units++
		}
	}
	boundaries[units] = len(body)
	end := 0
	broadcastCount := 0
	for _, m := range mentions {
		startByte, startOK := boundaries[m.Start]
		endByte, endOK := boundaries[m.End]
		if m.Start < end || m.End <= m.Start || !startOK || !endOK {
			return ErrInvalidMentionsRange()
		}
		if m.Kind != "" {
			if m.UserID != "" || (m.Kind != "all" && m.Kind != "here") {
				return ErrInvalidMentionsRange()
			}
			broadcastCount++
			if broadcastCount > 1 {
				return ErrInvalidMentionsRange()
			}
		} else if m.UserID == "" || len(m.UserID) > 128 {
			return ErrInvalidMentionsRange()
		}
		text := body[startByte:endByte]
		if !strings.HasPrefix(text, "@") || utf8.RuneCountInString(text) < 2 || utf8.RuneCountInString(text) > 181 || strings.ContainsAny(text, "\r\n\t") {
			return ErrInvalidMentionsRange()
		}
		end = m.End
	}
	return nil
}

func ValidateMentionMembers(members []string, mentions []Mention) error {
	for _, m := range mentions {
		if m.Kind != "" {
			continue
		}
		if !slices.Contains(members, m.UserID) {
			return ErrInvalidMentionRecipients()
		}
	}
	return nil
}

// BroadcastMentionKind returns the single broadcast mention kind ("all" or "here"), or "" if absent.
func BroadcastMentionKind(mentions []Mention) string {
	for _, m := range mentions {
		if m.Kind != "" {
			return m.Kind
		}
	}
	return ""
}

// CanonicalMentions strips resolved broadcast snapshots, retaining only the
// immutable request/display fields (UserID, Kind, Start, End).
func CanonicalMentions(mentions []Mention) []Mention {
	out := make([]Mention, len(mentions))
	for i, m := range mentions {
		out[i] = Mention{
			UserID: m.UserID,
			Kind:   m.Kind,
			Start:  m.Start,
			End:    m.End,
		}
	}
	return out
}

// SameMentions compares canonical mention identity (ignoring resolved recipient snapshots).
func SameMentions(a, b []Mention) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].UserID != b[i].UserID || a[i].Kind != b[i].Kind || a[i].Start != b[i].Start || a[i].End != b[i].End {
			return false
		}
	}
	return true
}

func ValidateHistoryQuery(before, around int64, query string, limit int) (string, error) {
	query = strings.TrimSpace(query)
	if before < 0 || around < 0 || before > 0 && around > 0 || limit < 1 || limit > 100 || len([]rune(query)) > 200 || !utf8.ValidString(query) || strings.ContainsRune(query, 0) {
		return "", ErrInvalidHistoryQuery()
	}
	return query, nil
}
