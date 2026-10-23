// Package infra implements outbound authority, session validation, feature flag,
// and Redis pub/sub ports for the Realtime bounded context.
package infra

import (
	"context"

	"github.com/redis/go-redis/v9"
	"teamie/backend/internal/api"
	"teamie/backend/internal/config"
	"teamie/backend/internal/realtime/domain"
	"teamie/backend/kitex_gen/im"
)

// ChatAccess matches the Chat RPC access checker interface.
type ChatAccess interface {
	CheckAccess(context.Context, string, string) (*im.AccessResponse, error)
	Ping(context.Context) error
}

// IdentityValidator adapts api.Identity to domain.SessionValidator.
type IdentityValidator struct {
	Identity api.Identity
}

func (v IdentityValidator) Validate(ctx context.Context, token string) (domain.Session, error) {
	s, err := v.Identity.Validate(ctx, token)
	if err != nil {
		return domain.Session{}, err
	}
	return domain.Session{
		User: domain.User{
			ID:           s.User.ID,
			Email:        s.User.Email,
			DisplayName:  s.User.DisplayName,
			Kind:         s.User.Kind,
			PlatformRole: s.User.PlatformRole,
			CreatedAt:    s.User.CreatedAt,
		},
		CSRFToken:        s.CSRFToken,
		ServicePrincipal: s.ServicePrincipal,
	}, nil
}

func (v IdentityValidator) Ping(ctx context.Context) error {
	return v.Identity.Ping(ctx)
}

// ChatAuthority adapts ChatAccess to domain.AccessAuthority.
type ChatAuthority struct {
	Chat ChatAccess
}

func (a ChatAuthority) CheckAccess(ctx context.Context, userID, conversationID string) (domain.AccessResult, error) {
	res, err := a.Chat.CheckAccess(ctx, userID, conversationID)
	if err != nil || res == nil {
		return domain.AccessResult{}, err
	}
	return domain.AccessResult{
		Allowed:       res.Allowed,
		MemberIDs:     res.MemberIds,
		TypingEnabled: res.TypingEnabled,
	}, nil
}

func (a ChatAuthority) Ping(ctx context.Context) error {
	return a.Chat.Ping(ctx)
}

// FlagsProvider adapts config.Flags to domain.FeatureFlags.
type FlagsProvider struct {
	Flags *config.Flags
}

func (f FlagsProvider) TypingPublishEnabled() bool {
	if f.Flags == nil {
		return true
	}
	return f.Flags.Load().Flags["im.typing.publish"]
}

// RedisBus adapts *redis.Client to domain.HintBus.
type RedisBus struct {
	Client *redis.Client
}

func (b RedisBus) Publish(ctx context.Context, channel string, payload []byte) error {
	return b.Client.Publish(ctx, channel, payload).Err()
}
