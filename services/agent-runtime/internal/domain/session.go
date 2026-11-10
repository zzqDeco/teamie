// Package domain contains pure Go entities, value objects, domain invariants,
// domain errors, and outbound port interfaces for the Agent Runtime engine.
// It has zero external, framework, database, or RPC dependencies.
package domain

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"
)

// Core runtime domain sentinel errors.
var (
	ErrLeaseLost               = errors.New("runtime lease lost")
	ErrReplayConflict          = errors.New("durable execution replay conflict")
	ErrEmptyRun                = errors.New("empty run")
	ErrMissingRunContext       = errors.New("missing run context")
	ErrSessionScopeMismatch    = errors.New("session scope mismatch")
	ErrCheckpointScopeMismatch = errors.New("checkpoint scope mismatch")
	ErrContextUnavailable      = errors.New("context_unavailable")
	ErrContextArguments        = errors.New("invalid_context_arguments")
	ErrMixedInputRunOwnership  = errors.New("mixed input run ownership")
)

// Error represents a coded domain error with an optional HTTP/RPC status, diagnostic stage/reason, internal cause, and retry classification.
type Error struct {
	Status  int
	Code    string
	Stage   string
	Reason  string
	Message string
	Cause   error
	Retry   bool
}

func (e *Error) Error() string {
	if e == nil {
		return ""
	}
	if e.Message != "" {
		return e.Message
	}
	return e.Code
}

// Err constructs a coded domain Error.
func Err(status int, code string, retry bool) *Error {
	return &Error{Status: status, Code: code, Message: code, Retry: retry}
}

// Runtime version and tool profile constants.
const (
	NativeRuntimeVersion = "eino-v0.10.0-alpha.35/teamie-v4-agentic"
	CurrentToolProfile   = "teamie-tools-v035"
	V034ToolProfile      = "teamie-tools-v034"
)

// Lease represents a fenced single-writer worker lease for an Agent Session.
type Lease struct {
	AgentID, Owner string
	Fence          int64
}

// ReceivedReadPosition is an IM message already committed to the Runtime inbox.
// It deliberately carries no message body or execution state.
type ReceivedReadPosition struct {
	EventID        string
	ConversationID string
	Seq            int64
}

// ReadReceiptInputPage advances through a fixed committed IM cursor, including
// inbox rows that have since been processed, failed, or skipped.
type ReadReceiptInputPage struct {
	Items       []ReceivedReadPosition
	NextEventID string
	HasMore     bool
}

// Valid reports whether the lease has a valid Agent UUID, non-empty owner, and positive fence token.
func (l Lease) Valid() bool {
	return ValidUUID(l.AgentID) && strings.TrimSpace(l.Owner) != "" && l.Fence > 0
}

// SessionID returns the canonical global Session ID for the leased Agent.
func (l Lease) SessionID() string {
	return "agent:" + l.AgentID
}

// ValidateSessionScope verifies that sessionID matches this lease's single global Agent Session.
func (l Lease) ValidateSessionScope(sessionID string) error {
	if sessionID != l.SessionID() {
		return ErrSessionScopeMismatch
	}
	return nil
}

// ValidateCheckpointScope verifies that checkpointID is scoped to this lease's Agent.
func (l Lease) ValidateCheckpointScope(checkpointID string) error {
	if l.AgentID == "" || !strings.Contains(checkpointID, l.AgentID) {
		return ErrCheckpointScopeMismatch
	}
	return nil
}

// AttachmentImage carries optional server-verified raster dimensions on an IM attachment descriptor.
type AttachmentImage struct {
	Width  int `json:"width"`
	Height int `json:"height"`
}

// Attachment describes an immutable IM attachment metadata descriptor.
type Attachment struct {
	ID        string           `json:"id"`
	Filename  string           `json:"filename"`
	MediaType string           `json:"mediaType"`
	SizeBytes int              `json:"sizeBytes"`
	SHA256    string           `json:"sha256"`
	Image     *AttachmentImage `json:"image,omitempty"`
}

// AttachmentEqual compares two Attachment descriptors by value, including optional Image dimensions.
func AttachmentEqual(a, b Attachment) bool {
	if a.ID != b.ID || a.Filename != b.Filename || a.MediaType != b.MediaType || a.SizeBytes != b.SizeBytes || a.SHA256 != b.SHA256 {
		return false
	}
	if (a.Image == nil) != (b.Image == nil) {
		return false
	}
	return a.Image == nil || *a.Image == *b.Image
}

// AttachmentsEqual compares two Attachment slices by value.
func AttachmentsEqual(a, b []Attachment) bool {
	return slices.EqualFunc(a, b, AttachmentEqual)
}

// MentionSpan represents a structured UTF-16 @mention or broadcast range in an IM message.
// When Kind is empty, it marshals identically to an individual {userId,start,end} mention.
// When Kind is non-empty ("all" or "here"), it preserves broadcast fields losslessly.
type MentionSpan struct {
	Kind           string
	UserID         string
	Start          int
	End            int
	RecipientIDs   []string
	RecipientCount *int
}

func (m MentionSpan) MarshalJSON() ([]byte, error) {
	if m.Kind == "" {
		return json.Marshal(struct {
			UserID string `json:"userId"`
			Start  int    `json:"start"`
			End    int    `json:"end"`
		}{
			UserID: m.UserID,
			Start:  m.Start,
			End:    m.End,
		})
	}
	var recipientIDs *[]string
	if m.RecipientIDs != nil {
		recipientIDs = &m.RecipientIDs
	}
	return json.Marshal(struct {
		Kind           string    `json:"kind"`
		UserID         string    `json:"userId,omitempty"`
		Start          int       `json:"start"`
		End            int       `json:"end"`
		RecipientIDs   *[]string `json:"recipientIds,omitempty"`
		RecipientCount *int      `json:"recipientCount,omitempty"`
	}{
		Kind:           m.Kind,
		UserID:         m.UserID,
		Start:          m.Start,
		End:            m.End,
		RecipientIDs:   recipientIDs,
		RecipientCount: m.RecipientCount,
	})
}

func (m *MentionSpan) UnmarshalJSON(data []byte) error {
	var raw struct {
		Kind           string    `json:"kind"`
		UserID         string    `json:"userId"`
		Start          int       `json:"start"`
		End            int       `json:"end"`
		RecipientIDs   *[]string `json:"recipientIds"`
		RecipientCount *int      `json:"recipientCount"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	out := MentionSpan{
		Kind:   raw.Kind,
		UserID: raw.UserID,
		Start:  raw.Start,
		End:    raw.End,
	}
	if raw.RecipientIDs != nil {
		ids := make([]string, len(*raw.RecipientIDs))
		copy(ids, *raw.RecipientIDs)
		out.RecipientIDs = ids
	}
	if raw.RecipientCount != nil {
		c := *raw.RecipientCount
		out.RecipientCount = &c
	}
	*m = out
	return nil
}

// MentionSpanEqual compares two MentionSpan values losslessly.
func MentionSpanEqual(a, b MentionSpan) bool {
	if a.Kind != b.Kind || a.UserID != b.UserID || a.Start != b.Start || a.End != b.End {
		return false
	}
	if (a.RecipientIDs == nil) != (b.RecipientIDs == nil) || !slices.Equal(a.RecipientIDs, b.RecipientIDs) {
		return false
	}
	if (a.RecipientCount == nil) != (b.RecipientCount == nil) {
		return false
	}
	return a.RecipientCount == nil || *a.RecipientCount == *b.RecipientCount
}

// MentionsEqual compares two MentionSpan slices losslessly.
func MentionsEqual(a, b []MentionSpan) bool {
	return slices.EqualFunc(a, b, MentionSpanEqual)
}

// HasBroadcastMention reports whether mentions contains any broadcast entry ("all" or "here").
func HasBroadcastMention(mentions []MentionSpan) bool {
	for _, m := range mentions {
		if m.Kind == "all" || m.Kind == "here" {
			return true
		}
	}
	return false
}

// HasAllBroadcastTo reports whether mentions contains an "all" broadcast entry whose recipientIds contains agentID.
func HasAllBroadcastTo(mentions []MentionSpan, agentID string) bool {
	if agentID == "" {
		return false
	}
	for _, m := range mentions {
		if m.Kind == "all" && slices.Contains(m.RecipientIDs, agentID) {
			return true
		}
	}
	return false
}

// HasHereBroadcast reports whether mentions contains a "here" broadcast entry.
func HasHereBroadcast(mentions []MentionSpan) bool {
	for _, m := range mentions {
		if m.Kind == "here" {
			return true
		}
	}
	return false
}

// MentionsAddressAgent reports whether top-level mentions or derived broadcast resolution address agentID.
// Display-only forwarded snapshot mentions must never be passed here.
func MentionsAddressAgent(mentions []MentionSpan, agentID string, broadcastAddressed bool) bool {
	if broadcastAddressed {
		return true
	}
	if agentID == "" {
		return false
	}
	for _, m := range mentions {
		if m.Kind == "" && m.UserID == agentID {
			return true
		}
		if m.Kind == "all" && slices.Contains(m.RecipientIDs, agentID) {
			return true
		}
	}
	return false
}

// ForwardedMessage holds the untrusted display snapshot of a forwarded IM message.
// Snapshot sourceConversationId/sourceMessageId/senderId are plain references, never provenance grants.
// Copied snapshot attachments are target-conversation attachment rows owned by the forward message.
type ForwardedMessage struct {
	SourceConversationID string        `json:"sourceConversationId"`
	SourceMessageID      string        `json:"sourceMessageId"`
	SenderID             string        `json:"senderId"`
	SenderName           string        `json:"senderName"`
	SenderKind           string        `json:"senderKind"`
	CreatedAt            string        `json:"createdAt"`
	Body                 string        `json:"body"`
	Mentions             []MentionSpan `json:"mentions,omitempty"`
	Attachments          []Attachment  `json:"attachments,omitempty"`
}

// ForwardedEqual compares two ForwardedMessage snapshots by value.
func ForwardedEqual(a, b *ForwardedMessage) bool {
	if (a == nil) != (b == nil) {
		return false
	}
	if a == nil {
		return true
	}
	return a.SourceConversationID == b.SourceConversationID &&
		a.SourceMessageID == b.SourceMessageID &&
		a.SenderID == b.SenderID &&
		a.SenderName == b.SenderName &&
		a.SenderKind == b.SenderKind &&
		a.CreatedAt == b.CreatedAt &&
		a.Body == b.Body &&
		MentionsEqual(a.Mentions, b.Mentions) &&
		AttachmentsEqual(a.Attachments, b.Attachments)
}

// MessageAttachmentIDs returns the target-conversation attachment IDs owned by a message,
// including copied forward attachments when forwarded is non-nil.
func MessageAttachmentIDs(attachments []Attachment, forwarded *ForwardedMessage) []string {
	var ids []string
	for _, a := range attachments {
		ids = append(ids, a.ID)
	}
	if forwarded != nil {
		for _, a := range forwarded.Attachments {
			ids = append(ids, a.ID)
		}
	}
	return ids
}

// Input represents a durable inbox event delivered to an Agent's serial TurnLoop.
type Input struct {
	EventID            string            `json:"eventId"`
	WorkspaceID        string            `json:"workspaceId"`
	ConversationID     string            `json:"conversationId"`
	MessageID          string            `json:"messageId"`
	SenderID           string            `json:"senderId"`
	SenderName         string            `json:"senderName"`
	SenderKind         string            `json:"senderKind"`
	Body               string            `json:"body"`
	Attachments        []Attachment      `json:"attachments,omitempty"`
	Mentions           []MentionSpan     `json:"mentions,omitempty"`
	ReplyToID          string            `json:"replyToId,omitempty"`
	Forwarded          *ForwardedMessage `json:"forwarded,omitempty"`
	BroadcastAddressed bool              `json:"broadcastAddressed,omitempty"`
	Seq                int64             `json:"seq"`
	CreatedAt          string            `json:"createdAt"`
	Kind               string            `json:"kind,omitempty"`
	TaskID             string            `json:"taskId,omitempty"`
	ScheduleID         string            `json:"scheduleId,omitempty"`
	NotificationID     string            `json:"notificationId,omitempty"`
	Status             string            `json:"status,omitempty"`
	ParentRunID        string            `json:"parentRunId,omitempty"`
	SourceRefs         []InputSource     `json:"sourceRefs,omitempty"`
	SourceOverflow     bool              `json:"sourceOverflow,omitempty"`
}

// AddressedTo reports whether this input's top-level mentions or resolved broadcast addressed agentID.
// Display-only mentions inside Forwarded never address the Agent.
func (in Input) AddressedTo(agentID string) bool {
	return MentionsAddressAgent(in.Mentions, agentID, in.BroadcastAddressed)
}

// SessionState summarizes the runtime state of an Agent's single global Session.
type SessionState struct {
	AgentID       string
	SessionID     string
	State         string
	ActiveRunID   string
	PendingInputs int
	RunningTasks  int
	LastError     string
	LastActiveAt  *time.Time
}

// CheckpointMetadata tracks the runtime version, revision, and context policy of a TurnLoop checkpoint.
type CheckpointMetadata struct {
	Presence          *CheckpointPresence `json:"presence,omitempty"`
	ToolProfile       string
	ContextProfile    string `json:",omitempty"`
	ContextBudgetHash string `json:",omitempty"`
	Version           string
	Revision          int64
	RunID, Attempt    string
	ModelStep         int64
	Targets           []string
}

// CheckpointPresence is the conversation scope at the native execution boundary.
// A nil value denotes a legacy checkpoint; an enabled, empty scope means "left".
type CheckpointPresence struct {
	Version             int    `json:"version"`
	Enabled             bool   `json:"enabled"`
	MarketplaceDisabled bool   `json:"marketplaceDisabled,omitempty"`
	WorkspaceID         string `json:"workspaceId,omitempty"`
	ConversationID      string `json:"conversationId,omitempty"`
	ConvType            string `json:"convType,omitempty"`
}

// ShouldPreemptWorker reports whether an active worker must be canceled due to
// agent deletion, disablement, or control-plane revision advancement.
func ShouldPreemptWorker(enabled, deleted bool, currentRevision, workerRevision int64) bool {
	return !enabled || deleted || currentRevision != workerRevision
}

// CanCancelRun reports whether a run in the given state can be canceled.
func CanCancelRun(state string) bool {
	return state != "completed" && state != "failed" && state != "canceled"
}

// CanRetryRun reports whether a run in the given state can be retried.
func CanRetryRun(state string) bool {
	switch state {
	case "failed", "interrupted", "retry", "waiting_configuration", "canceled", "waiting_input":
		return true
	default:
		return false
	}
}

// ResumeCompatible reports whether a persisted TurnLoop checkpoint is compatible
// with the current agent revision and run ID.
func ResumeCompatible(m *CheckpointMetadata, revision int64, runID string) bool {
	return m != nil &&
		m.Version == NativeRuntimeVersion &&
		m.Revision == revision &&
		m.RunID == runID &&
		m.Attempt != "" &&
		len(m.Targets) > 0
}

// RelevantPreemptInput reports whether a newly arrived input should preempt an in-flight
// turn after tool calls complete. Presence, when enabled, is authoritative even
// after the Agent switches away from the conversation that started this turn.
// Legacy turns use their consumed inputs' conversation provenance instead.
func RelevantPreemptInput(in Input, consumed []Input, active *InputSource) bool {
	if in.Kind == "task.notification" || in.Kind == "schedule.wakeup" || in.Kind == "platform.announcement" || in.SourceOverflow {
		return false
	}
	machine := in.Kind == "app.notification" || in.Kind == "im.unread_digest"
	if !machine && in.SenderKind != "human" {
		return false
	}
	refs := []InputSource{{WorkspaceID: in.WorkspaceID, ConversationID: in.ConversationID}}
	if machine {
		refs = append(refs, in.SourceRefs...)
	}
	var targets []InputSource
	if active != nil {
		targets = []InputSource{*active}
	} else {
		for _, old := range consumed {
			targets = append(targets, InputSource{WorkspaceID: old.WorkspaceID, ConversationID: old.ConversationID})
			targets = append(targets, old.SourceRefs...)
		}
	}
	for _, ref := range refs {
		for _, target := range targets {
			if ref.ConversationID != "" && ref.ConversationID == target.ConversationID &&
				(ref.WorkspaceID == "" || target.WorkspaceID == "" || ref.WorkspaceID == target.WorkspaceID) {
				return true
			}
		}
	}
	return false
}

// BoundedInputBatch partitions eligible inputs into a bounded turn batch (at most 32 items / 48 KiB)
// and any remaining overflow items.
func BoundedInputBatch(inputs []Input) ([]Input, []Input) {
	n, total := 0, 0
	for _, in := range inputs {
		b, _ := json.Marshal(in)
		if n > 0 && (total+len(b) > 48<<10 || n >= 32) {
			break
		}
		total += len(b)
		n++
	}
	return inputs[:n], inputs[n:]
}

// PublicErrorCode maps internal error codes to the bounded set of safe public error codes.
func PublicErrorCode(code string) string {
	switch code {
	case "model_unconfigured", "model_disabled", "gateway_unavailable", "gateway_error",
		"upstream_timeout", "upstream_error", "invalid_request", "request_too_large",
		"upstream_response_limit", "invalid_upstream_response", "run_cancelled", "cancelled",
		"canceled", "deadline_exceeded", "execution_failed", "lease_lost", "replay_conflict",
		"im_unavailable", "source_unavailable", "new_input", "principal_disabled",
		"control_unavailable", "runtime_disabled", "task_execution_disabled",
		"sandbox_unavailable", "sandbox_busy", "sandbox_command_failed",
		"sandbox_execution_unknown", "gateway_stream_interrupted",
		"superseded_orphan_run":
		return code
	}
	return "execution_failed"
}

// PublicErrorCodeOptional returns "" when code is empty, or PublicErrorCode(code) otherwise.
func PublicErrorCodeOptional(code string) string {
	if code == "" {
		return ""
	}
	return PublicErrorCode(code)
}

// ValidServiceKey performs a constant-time comparison of a service authentication key.
func ValidServiceKey(expected, actual string) bool {
	return expected != "" && subtle.ConstantTimeCompare([]byte(actual), []byte(expected)) == 1
}

// Digest returns the lowercase hex-encoded SHA-256 digest of b.
func Digest(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func isHexByte(c byte) bool {
	return (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
}

// ValidUUID reports whether s is a valid UUID string (matching RFC 4122 / google/uuid formats).
func ValidUUID(s string) bool {
	switch len(s) {
	case 36:
		// xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx
	case 32:
		for i := 0; i < 32; i++ {
			if !isHexByte(s[i]) {
				return false
			}
		}
		return true
	case 38:
		if s[0] != '{' || s[37] != '}' {
			return false
		}
		s = s[1:37]
	case 45:
		if !strings.EqualFold(s[:9], "urn:uuid:") {
			return false
		}
		s = s[9:]
	default:
		return false
	}
	for i := 0; i < 36; i++ {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if s[i] != '-' {
				return false
			}
			continue
		}
		if !isHexByte(s[i]) {
			return false
		}
	}
	return true
}
