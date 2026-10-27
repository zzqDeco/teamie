package domain

import (
	"context"
	"time"
)

// SessionValidator validates participant session tokens.
type SessionValidator interface {
	Validate(ctx context.Context, token string) (Session, error)
	Ping(ctx context.Context) error
}

// AccessAuthority checks conversation membership, workspace membership, and feature permissions.
type AccessAuthority interface {
	CheckAccess(ctx context.Context, userID, conversationID string) (AccessResult, error)
	CheckWorkspaceAccess(ctx context.Context, userID, workspaceID string) (WorkspaceAccessResult, error)
	SharedWorkspaceMembers(ctx context.Context, userID string) ([]string, bool, error)
	Ping(ctx context.Context) error
}

// FeatureFlags exposes realtime feature toggles.
type FeatureFlags interface {
	TypingPublishEnabled() bool
	PresencePublishEnabled() bool
	ReadReceiptsViewEnabled() bool
}

// HintBus publishes realtime hints across instances.
type HintBus interface {
	Publish(ctx context.Context, channel string, payload []byte) error
}

// PresenceStore persists disposable user presence state across Realtime instances.
type PresenceStore interface {
	SaveUserPresence(ctx context.Context, userID, state string, ttl time.Duration) error
	GetUsersPresence(ctx context.Context, userIDs []string) (map[string]string, error)
}
