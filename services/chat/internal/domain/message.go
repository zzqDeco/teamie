package domain

import (
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

type Mention struct {
	UserID string `json:"userId"`
	Start  int    `json:"start"`
	End    int    `json:"end"`
}

type Reaction struct {
	Emoji   string   `json:"emoji"`
	Count   int64    `json:"count"`
	UserIDs []string `json:"userIds"`
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
	Mentions        []Mention    `json:"mentions,omitempty"`
	Reactions       []Reaction   `json:"reactions,omitempty"`
}

func isDefaultEmojiPresentation(r rune) bool {
	switch {
	case r == 0x231A || r == 0x231B || (r >= 0x23E9 && r <= 0x23EC) || r == 0x23F0 || r == 0x23F3:
		return true
	case r == 0x25FD || r == 0x25FE || r == 0x2614 || r == 0x2615 || (r >= 0x2648 && r <= 0x2653) || r == 0x267F || r == 0x2693 || r == 0x26A1 || r == 0x26AA || r == 0x26AB || r == 0x26BD || r == 0x26BE || r == 0x26C4 || r == 0x26C5 || r == 0x26CE || r == 0x26D4 || r == 0x26EA || r == 0x26F2 || r == 0x26F3 || r == 0x26F5 || r == 0x26FA || r == 0x26FD:
		return true
	case r == 0x2705 || r == 0x270A || r == 0x270B || r == 0x2728 || r == 0x274C || r == 0x274E || (r >= 0x2753 && r <= 0x2755) || r == 0x2757 || (r >= 0x2795 && r <= 0x2797) || r == 0x27B0 || r == 0x27BF || r == 0x2B1B || r == 0x2B1C || r == 0x2B50 || r == 0x2B55:
		return true
	case r == 0x1F004 || r == 0x1F0CF || r == 0x1F18E || (r >= 0x1F191 && r <= 0x1F19A) || (r >= 0x1F201 && r <= 0x1F202) || (r >= 0x1F21A && r <= 0x1F22F) || (r >= 0x1F232 && r <= 0x1F23A) || (r >= 0x1F250 && r <= 0x1F251):
		return true
	case (r >= 0x1F300 && r <= 0x1F6FF) || (r >= 0x1F900 && r <= 0x1FAFF):
		// Exclude skin tone modifiers themselves from being base emojis.
		return !(r >= 0x1F3FB && r <= 0x1F3FF)
	default:
		return false
	}
}

func isTextPresentationEmojiBase(r rune) bool {
	switch {
	case r == 0x00A9 || r == 0x00AE || r == 0x203C || r == 0x2049 || r == 0x2122 || r == 0x2139 || (r >= 0x2194 && r <= 0x2199) || r == 0x21A9 || r == 0x21AA:
		return true
	case r == 0x2328 || r == 0x23CF || (r >= 0x23ED && r <= 0x23EF) || r == 0x23F1 || r == 0x23F2 || (r >= 0x23F8 && r <= 0x23FA) || r == 0x24C2 || r == 0x25AA || r == 0x25AB || r == 0x25B6 || r == 0x25C0 || (r >= 0x25FB && r <= 0x25FC):
		return true
	case (r >= 0x2600 && r <= 0x26FF) && !isDefaultEmojiPresentation(r):
		return true
	case (r >= 0x2700 && r <= 0x27BF) && !isDefaultEmojiPresentation(r):
		return true
	case r == 0x2934 || r == 0x2935 || (r >= 0x2B05 && r <= 0x2B07) || r == 0x3030 || r == 0x303D || r == 0x3297 || r == 0x3299 || r == 0x1F170 || r == 0x1F171 || r == 0x1F17E || r == 0x1F17F || r == 0x1F202 || r == 0x1F237:
		return true
	default:
		return false
	}
}

func isSkinToneModifier(r rune) bool {
	return r >= 0x1F3FB && r <= 0x1F3FF
}

// ValidateReactionEmoji enforces that emoji is a single fully-qualified Unicode
// emoji sequence from the supported Unicode Emoji set, at most 64 UTF-8 bytes.
func ValidateReactionEmoji(emoji string) error {
	if emoji == "" || len(emoji) > 64 || !utf8.ValidString(emoji) {
		return ErrInvalidReaction()
	}
	runes := []rune(emoji)
	if len(runes) == 0 {
		return ErrInvalidReaction()
	}
	// 1. Keycap sequence: [0-9#*] U+FE0F U+20E3
	if len(runes) == 3 && ((runes[0] >= '0' && runes[0] <= '9') || runes[0] == '#' || runes[0] == '*') && runes[1] == 0xFE0F && runes[2] == 0x20E3 {
		return nil
	}
	// 2. Regional indicator flag pair: two runes in U+1F1E6..U+1F1FF
	if len(runes) == 2 && runes[0] >= 0x1F1E6 && runes[0] <= 0x1F1FF && runes[1] >= 0x1F1E6 && runes[1] <= 0x1F1FF {
		return nil
	}
	// 3. Subdivision flag tag sequence: U+1F3F4 + tag chars (U+E0020..U+E007E) + U+E007F
	if len(runes) >= 4 && runes[0] == 0x1F3F4 && runes[len(runes)-1] == 0xE007F {
		validTag := true
		for _, r := range runes[1 : len(runes)-1] {
			if r < 0xE0020 || r > 0xE007E {
				validTag = false
				break
			}
		}
		if validTag {
			return nil
		}
	}
	// 4. Single emoji or ZWJ emoji sequence: Base (U+FE0F | SkinTone)? (U+200D Base (U+FE0F | SkinTone)?)*
	i := 0
	segments := 0
	for i < len(runes) {
		segments++
		if segments > 5 {
			return ErrInvalidReaction()
		}
		base := runes[i]
		defaultPres := isDefaultEmojiPresentation(base)
		textPres := isTextPresentationEmojiBase(base)
		if !defaultPres && !textPres {
			return ErrInvalidReaction()
		}
		i++
		hasVS16 := false
		hasSkin := false
		if i < len(runes) && runes[i] == 0xFE0F {
			hasVS16 = true
			i++
		} else if i < len(runes) && isSkinToneModifier(runes[i]) {
			hasSkin = true
			i++
		}
		// Text-presentation bases (e.g. U+2764 heavy black heart) must carry
		// U+FE0F or a skin-tone modifier to be fully-qualified.
		if textPres && !hasVS16 && !hasSkin {
			return ErrInvalidReaction()
		}
		if i == len(runes) {
			return nil
		}
		// Only U+200D (ZWJ) may join another emoji segment; two adjacent emojis without ZWJ are rejected.
		if runes[i] != 0x200D || i+1 >= len(runes) {
			return ErrInvalidReaction()
		}
		i++
	}
	return ErrInvalidReaction()
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
	for _, m := range mentions {
		startByte, startOK := boundaries[m.Start]
		endByte, endOK := boundaries[m.End]
		if m.UserID == "" || len(m.UserID) > 128 || m.Start < end || m.End <= m.Start || !startOK || !endOK {
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
		if !slices.Contains(members, m.UserID) {
			return ErrInvalidMentionRecipients()
		}
	}
	return nil
}

func ValidateHistoryQuery(before, around int64, query string, limit int) (string, error) {
	query = strings.TrimSpace(query)
	if before < 0 || around < 0 || before > 0 && around > 0 || limit < 1 || limit > 100 || len([]rune(query)) > 200 || !utf8.ValidString(query) || strings.ContainsRune(query, 0) {
		return "", ErrInvalidHistoryQuery()
	}
	return query, nil
}
