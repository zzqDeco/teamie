package domain

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	HintChannel                 = "teamie:im:hints:v1"
	AttachmentBytes             = 4 * 1024 * 1024
	AttachmentsPerMessage       = 8
	PendingAttachmentsPerSender = 32
	AttachmentRequestBytes      = AttachmentBytes + 64*1024
)

type User struct {
	ID           string    `json:"id"`
	Email        string    `json:"email"`
	DisplayName  string    `json:"displayName"`
	Kind         string    `json:"kind"`
	PlatformRole string    `json:"platformRole"`
	CreatedAt    time.Time `json:"createdAt"`
}

type Workspace struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	CreatedBy string    `json:"createdBy"`
	CreatedAt time.Time `json:"createdAt"`
}

type Member struct {
	UserID      string `json:"userId"`
	DisplayName string `json:"displayName"`
	Role        string `json:"role"`
	Kind        string `json:"kind"`
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

type Mention struct {
	UserID string `json:"userId"`
	Start  int    `json:"start"`
	End    int    `json:"end"`
}

type Attachment struct {
	ID        string `json:"id"`
	Filename  string `json:"filename"`
	MediaType string `json:"mediaType"`
	SizeBytes int    `json:"sizeBytes"`
	SHA256    string `json:"sha256"`
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
	Items              []AttentionItem `json:"items"`
	Cursor             string          `json:"cursor,omitempty"`
	UnreadCount        int64           `json:"unreadCount"`
	ChannelUnreadCount int64           `json:"channelUnreadCount"`
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

type AttachmentUpload struct {
	Conversation string
	Fingerprint  string
	Attachment   Attachment
	Available    bool
}

func NewID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[:4], b[4:6], b[6:8], b[8:10], b[10:])
}

func RandomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

func HashString(v string) string {
	h := sha256.Sum256([]byte(v))
	return hex.EncodeToString(h[:])
}

func SHA256Hex(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func UploadFingerprint(conversation, filename, contentHash string) string {
	return HashString(conversation + "\x00" + filename + "\x00" + contentHash)
}

func BoundedString(v string, min, max int) bool {
	n := len([]rune(strings.TrimSpace(v)))
	return n >= min && n <= max
}

func ValidServiceKey(expected, provided string) bool {
	return expected != "" && provided != "" && subtle.ConstantTimeCompare([]byte(expected), []byte(provided)) == 1
}

func NormalizedIDs(ids []string) []string {
	set := map[string]bool{}
	for _, id := range ids {
		if id != "" {
			set[id] = true
		}
	}
	out := []string{}
	for id := range set {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

func ValidUploadKey(key string) bool {
	if len(key) < 1 || len(key) > 128 {
		return false
	}
	for _, c := range key {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '.' || c == '_' || c == ':' || c == '-') {
			return false
		}
	}
	return true
}

func ValidFilename(name string) bool {
	if name == "" || name == "." || name == ".." || !utf8.ValidString(name) || len([]rune(name)) > 180 || len(name) > 512 || strings.TrimSpace(name) != name || strings.ContainsAny(name, "/\\") {
		return false
	}
	for _, r := range name {
		if unicode.IsControl(r) || unicode.In(r, unicode.Cf) {
			return false
		}
	}
	return true
}

func ValidateFilename(name string) error {
	if !ValidFilename(name) {
		return ErrInvalidFilename()
	}
	return nil
}

func ValidateAttachmentIDs(ids []string) error {
	if len(ids) > AttachmentsPerMessage {
		return ErrTooManyAttachments()
	}
	seen := map[string]bool{}
	for _, id := range ids {
		if !BoundedString(id, 1, 128) || seen[id] {
			return ErrDuplicateAttachmentID()
		}
		seen[id] = true
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

func ValidateHistoryQuery(before, around int64, query string, limit int) (string, error) {
	query = strings.TrimSpace(query)
	if before < 0 || around < 0 || before > 0 && around > 0 || limit < 1 || limit > 100 || len([]rune(query)) > 200 || !utf8.ValidString(query) || strings.ContainsRune(query, 0) {
		return "", ErrInvalidHistoryQuery()
	}
	return query, nil
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
