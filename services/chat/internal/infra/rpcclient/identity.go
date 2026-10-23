// Package rpcclient adapts the shared Identity RPC client to the Chat domain IdentityDirectory port.
package rpcclient

import (
	"context"

	"teamie/pkg/httpx"
	"teamie/services/chat/internal/domain"
)

// IdentityAdapter implements domain.IdentityDirectory over httpx.Identity.
type IdentityAdapter struct {
	Identity httpx.Identity
}

func NewIdentity(id httpx.Identity) IdentityAdapter {
	return IdentityAdapter{Identity: id}
}

func (a IdentityAdapter) Users(ctx context.Context, ids []string) ([]domain.User, error) {
	if a.Identity == nil {
		return nil, nil
	}
	us, err := a.Identity.Users(ctx, ids)
	if err != nil {
		return nil, err
	}
	out := make([]domain.User, 0, len(us))
	for _, u := range us {
		out = append(out, domain.User{
			ID:           u.ID,
			Email:        u.Email,
			DisplayName:  u.DisplayName,
			Kind:         u.Kind,
			PlatformRole: u.PlatformRole,
			CreatedAt:    u.CreatedAt,
		})
	}
	return out, nil
}

func (a IdentityAdapter) Ping(ctx context.Context) error {
	if a.Identity == nil {
		return nil
	}
	return a.Identity.Ping(ctx)
}
