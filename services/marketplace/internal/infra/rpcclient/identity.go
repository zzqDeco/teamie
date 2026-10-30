// Package rpcclient adapts Identity and Chat Kitex RPC clients to the
// Marketplace domain IdentityDirectory and ChatScope ports.
package rpcclient

import (
	"context"
	"fmt"
	"time"

	"teamie/contracts/gen/im/kitex_gen/im"
	"teamie/contracts/gen/im/kitex_gen/im/identityservice"
	"teamie/pkg/httpx"
	"teamie/pkg/rpcx"
	"teamie/services/marketplace/internal/domain"
)

func fromUser(u *im.User) httpx.User {
	if u == nil {
		return httpx.User{}
	}
	at, _ := time.Parse(time.RFC3339Nano, u.CreatedAt)
	return httpx.User{
		ID:           u.Id,
		Email:        u.Email,
		DisplayName:  u.DisplayName,
		Kind:         u.Kind,
		PlatformRole: u.PlatformRole,
		CreatedAt:    at,
	}
}

// Identity wraps the Kitex IdentityService client for session/principal authentication and user lookup.
type Identity struct {
	Client identityservice.Client
}

// NewIdentityClient dials the Identity RPC service at addr.
func NewIdentityClient(addr string) (*Identity, error) {
	c, err := identityservice.NewClient("teamie.identity", rpcx.ClientOptions(addr, 3*time.Second, time.Second)...)
	return &Identity{Client: c}, err
}

func (c *Identity) Validate(ctx context.Context, token string) (httpx.Session, error) {
	if len(token) != 64 {
		return httpx.Session{}, httpx.Err(401, "unauthenticated", "Sign in to continue")
	}
	r, err := c.Client.ValidateSession(ctx, &im.SessionRequest{Token: token})
	if err != nil {
		return httpx.Session{}, httpx.Err(503, "identity_unavailable", "Identity service is temporarily unavailable")
	}
	if !r.Valid || r.User == nil {
		return httpx.Session{}, httpx.Err(401, "unauthenticated", "Session expired or revoked")
	}
	return httpx.Session{User: fromUser(r.User), CSRFToken: r.GetCsrfToken()}, nil
}

func (c *Identity) ValidatePrincipal(ctx context.Context, token, key string) (httpx.Session, error) {
	r, err := c.Client.ValidatePrincipalToken(ctx, &im.PrincipalAuthRequest{Token: token, ServiceToken: key})
	if err != nil {
		return httpx.Session{}, httpx.Err(503, "identity_unavailable", "Identity service is temporarily unavailable")
	}
	if !r.Valid || r.User == nil {
		return httpx.Session{}, httpx.Err(401, "unauthenticated", "Participant credential expired or revoked")
	}
	return httpx.Session{User: fromUser(r.User), ServicePrincipal: true}, nil
}

func (c *Identity) Users(ctx context.Context, ids []string) ([]httpx.User, error) {
	unique := make([]string, 0, len(ids))
	seen := make(map[string]bool, len(ids))
	for _, id := range ids {
		if id != "" && !seen[id] {
			seen[id] = true
			unique = append(unique, id)
		}
	}
	out := []httpx.User{}
	for start := 0; start < len(unique); start += 1000 {
		end := min(start+1000, len(unique))
		r, err := c.Client.GetUsers(ctx, &im.UsersRequest{Ids: unique[start:end]})
		if err != nil || r == nil {
			return nil, httpx.Err(503, "identity_unavailable", "Identity service is temporarily unavailable")
		}
		for _, u := range r.Users {
			out = append(out, fromUser(u))
		}
	}
	return out, nil
}

func (c *Identity) Ping(ctx context.Context) error {
	r, err := c.Client.Ping(ctx, &im.PingRequest{})
	if err != nil {
		return err
	}
	if !r.Ready {
		return fmt.Errorf("identity not ready")
	}
	return nil
}

// IdentityAdapter adapts httpx.Identity to domain.IdentityDirectory.
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
