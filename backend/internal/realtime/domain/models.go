// Package domain contains pure Go entities, value objects, typing policy
// invariants, domain errors, and outbound port interfaces for the Realtime
// bounded context. It has zero dependencies on HTTP, WebSocket, RPC, or Redis
// frameworks.
package domain

import (
	"context"
	"sync"
	"time"
)

const (
	HintChannel           = "teamie:im:hints:v1"
	TypingMinimumInterval = 300 * time.Millisecond
	TypingTTL             = 5 * time.Second
	MaxPeersPerUser       = 8
	MaxTotalPeers         = 10000
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

// Client represents a connected WebSocket subscriber.
type Client interface {
	UserID() string
	Token() string
	Send(ev Event)
	Close()
}

// SessionValidator validates participant session tokens.
type SessionValidator interface {
	Validate(ctx context.Context, token string) (Session, error)
	Ping(ctx context.Context) error
}

// AccessAuthority checks conversation membership and typing permissions.
type AccessAuthority interface {
	CheckAccess(ctx context.Context, userID, conversationID string) (AccessResult, error)
	Ping(ctx context.Context) error
}

// FeatureFlags exposes realtime feature toggles.
type FeatureFlags interface {
	TypingPublishEnabled() bool
}

// HintBus publishes realtime hints across instances.
type HintBus interface {
	Publish(ctx context.Context, channel string, payload []byte) error
}

// TypingKey uniquely identifies a participant's typing stream in a conversation.
type TypingKey struct {
	UserID         string
	ConversationID string
}

func (k TypingKey) String() string {
	return k.UserID + "/" + k.ConversationID
}

type typingGate struct {
	active       chan struct{}
	refs         int
	lastPositive time.Time
}

// TypingPolicy enforces per-(user, conversation) signal serialization, positive
// start coalescing, immediate unthrottled stop, and idle gate expiration.
type TypingPolicy struct {
	Interval time.Duration
	mu       sync.Mutex
	gates    map[string]*typingGate
}

func (p *TypingPolicy) interval() time.Duration {
	if p.Interval > 0 {
		return p.Interval
	}
	return TypingMinimumInterval
}

// Lock acquires the per-(user, conversation) serialization gate.
func (p *TypingPolicy) Lock(ctx context.Context, key TypingKey) (func(), error) {
	k := key.String()
	minInterval := p.interval()
	p.mu.Lock()
	if p.gates == nil {
		p.gates = map[string]*typingGate{}
	}
	now := time.Now()
	for id, gate := range p.gates {
		if gate.refs == 0 && now.Sub(gate.lastPositive) >= minInterval {
			delete(p.gates, id)
		}
	}
	gate := p.gates[k]
	if gate == nil {
		gate = &typingGate{active: make(chan struct{}, 1)}
		p.gates[k] = gate
	}
	gate.refs++
	p.mu.Unlock()

	forget := func() {
		p.mu.Lock()
		gate.refs--
		if gate.refs == 0 && time.Since(gate.lastPositive) >= minInterval {
			delete(p.gates, k)
		}
		p.mu.Unlock()
	}

	if err := ctx.Err(); err != nil {
		forget()
		return nil, err
	}
	select {
	case gate.active <- struct{}{}:
		return func() { <-gate.active; forget() }, nil
	case <-ctx.Done():
		forget()
		return nil, ctx.Err()
	}
}

// AdmitPositive reports whether a positive typing hint should be published or coalesced.
func (p *TypingPolicy) AdmitPositive(key TypingKey) bool {
	k := key.String()
	minInterval := p.interval()
	p.mu.Lock()
	defer p.mu.Unlock()
	gate := p.gates[k]
	if gate == nil {
		gate = &typingGate{active: make(chan struct{}, 1)}
		p.gates[k] = gate
	}
	now := time.Now()
	if now.Sub(gate.lastPositive) < minInterval {
		return false
	}
	gate.lastPositive = now
	return true
}

// ResetPositive clears the positive-hint throttle after an explicit stop signal.
func (p *TypingPolicy) ResetPositive(key TypingKey) {
	k := key.String()
	p.mu.Lock()
	if gate := p.gates[k]; gate != nil {
		gate.lastPositive = time.Time{}
	}
	p.mu.Unlock()
}
