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

// Attachment describes an immutable IM attachment metadata descriptor.
type Attachment struct {
	ID        string `json:"id"`
	Filename  string `json:"filename"`
	MediaType string `json:"mediaType"`
	SizeBytes int    `json:"sizeBytes"`
	SHA256    string `json:"sha256"`
}

// MentionSpan represents a structured UTF-16 @mention range in an IM message.
type MentionSpan struct {
	UserID string `json:"userId"`
	Start  int    `json:"start"`
	End    int    `json:"end"`
}

// Input represents a durable inbox event delivered to an Agent's serial TurnLoop.
type Input struct {
	EventID        string        `json:"eventId"`
	WorkspaceID    string        `json:"workspaceId"`
	ConversationID string        `json:"conversationId"`
	MessageID      string        `json:"messageId"`
	SenderID       string        `json:"senderId"`
	SenderName     string        `json:"senderName"`
	SenderKind     string        `json:"senderKind"`
	Body           string        `json:"body"`
	Attachments    []Attachment  `json:"attachments,omitempty"`
	Mentions       []MentionSpan `json:"mentions,omitempty"`
	ReplyToID      string        `json:"replyToId,omitempty"`
	Seq            int64         `json:"seq"`
	CreatedAt      string        `json:"createdAt"`
	Kind           string        `json:"kind,omitempty"`
	TaskID         string        `json:"taskId,omitempty"`
	ScheduleID     string        `json:"scheduleId,omitempty"`
	NotificationID string        `json:"notificationId,omitempty"`
	Status         string        `json:"status,omitempty"`
	ParentRunID    string        `json:"parentRunId,omitempty"`
	SourceRefs     []InputSource `json:"sourceRefs,omitempty"`
	SourceOverflow bool          `json:"sourceOverflow,omitempty"`
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
	ToolProfile       string
	ContextProfile    string `json:",omitempty"`
	ContextBudgetHash string `json:",omitempty"`
	Version           string
	Revision          int64
	RunID, Attempt    string
	ModelStep         int64
	Targets           []string
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
// turn after tool calls complete (only human messages in a conversation already active in the turn).
func RelevantPreemptInput(in Input, consumed []Input) bool {
	if in.Kind == "task.notification" || in.Kind == "schedule.wakeup" {
		return false
	}
	for _, old := range consumed {
		if in.ConversationID != "" && in.ConversationID == old.ConversationID && in.SenderKind == "human" {
			return true
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
		"sandbox_execution_unknown", "gateway_stream_interrupted":
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
