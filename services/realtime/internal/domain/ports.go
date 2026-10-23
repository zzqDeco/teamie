package domain

import "context"

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
