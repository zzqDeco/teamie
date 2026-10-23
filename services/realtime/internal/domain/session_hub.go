// Package domain contains pure Go entities, value objects, typing policy
// invariants, domain errors, and outbound port interfaces for the Realtime
// bounded context. It has zero dependencies on HTTP, WebSocket, RPC, or Redis
// frameworks.
package domain

import "time"

const (
	HintChannel     = "teamie:im:hints:v1"
	MaxPeersPerUser = 8
	MaxTotalPeers   = 10000
)

// Error represents a domain-level business rule violation with a canonical
// status classification and machine-readable error code.
type Error struct {
	Status  int
	Code    string
	Message string
}

func (e *Error) Error() string             { return e.Message }
func (e *Error) StatusCode() int           { return e.Status }
func (e *Error) ErrorCode() string         { return e.Code }
func (e *Error) DiagnosticMessage() string { return e.Code + ": " + e.Message }

// Err creates a new domain Error.
func Err(status int, code, message string) *Error {
	return &Error{Status: status, Code: code, Message: message}
}

func ErrUntrustedOrigin() error {
	return Err(403, "untrusted_origin", "WebSocket origin must match this application")
}

func ErrTooManyConnections() error {
	return Err(429, "too_many_connections", "Close an existing connection and try again")
}

func ErrInvalidRequest(msg string) error {
	return Err(400, "invalid_request", msg)
}

func ErrFeatureDisabled() error {
	return Err(403, "feature_disabled", "Typing is disabled")
}

func ErrChatUnavailable() error {
	return Err(503, "chat_unavailable", "Conversation access is temporarily unavailable")
}

func ErrForbidden() error {
	return Err(403, "forbidden", "Conversation access is required")
}

func ErrRealtimeUnavailable() error {
	return Err(503, "realtime_unavailable", "Typing is temporarily unavailable")
}

// User represents an authenticated IM participant.
type User struct {
	ID           string    `json:"id"`
	Email        string    `json:"email"`
	DisplayName  string    `json:"displayName"`
	Kind         string    `json:"kind"`
	PlatformRole string    `json:"platformRole"`
	CreatedAt    time.Time `json:"createdAt"`
}

// Session represents an authenticated participant session.
type Session struct {
	User             User   `json:"user"`
	CSRFToken        string `json:"csrfToken"`
	ServicePrincipal bool   `json:"-"`
}

// AccessResult represents the conversation ACL check result from Chat.
type AccessResult struct {
	Allowed       bool
	MemberIDs     []string
	TypingEnabled bool
}

// Event (also aliased as Hint) represents a realtime wakeup or ephemeral typing signal.
type Event struct {
	Type           string   `json:"type"`
	UserIDs        []string `json:"userIds,omitempty"`
	ConversationID string   `json:"conversationId,omitempty"`
	UserID         string   `json:"userId,omitempty"`
	DisplayName    string   `json:"displayName,omitempty"`
	IsTyping       bool     `json:"isTyping"`
}

// Hint is an alias for Event for compatibility with existing callers.
type Hint = Event

// Client represents a connected WebSocket subscriber in the SessionHub.
type Client interface {
	UserID() string
	Token() string
	Send(ev Event)
	Close()
}
