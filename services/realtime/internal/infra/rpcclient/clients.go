// Package rpcclient implements outbound Identity and Chat RPC client adapters for the Realtime service.
package rpcclient

import (
	"context"
	"fmt"
	"time"

	"teamie/contracts/gen/im/kitex_gen/im"
	"teamie/contracts/gen/im/kitex_gen/im/chatservice"
	"teamie/contracts/gen/im/kitex_gen/im/identityservice"
	"teamie/pkg/httpx"
	"teamie/pkg/rpcx"
	"teamie/services/realtime/internal/domain"
)

// FromUser converts a Kitex im.User wire struct into an httpx.User.
func FromUser(u *im.User) httpx.User {
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

// Identity wraps the Kitex IdentityService client for session validation and readiness checks.
type Identity struct {
	Client identityservice.Client
}

// NewIdentity dials a new Identity RPC client at the given address.
func NewIdentity(addr string) (*Identity, error) {
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
	return httpx.Session{User: FromUser(r.User), CSRFToken: r.GetCsrfToken()}, nil
}

func (c *Identity) Users(ctx context.Context, ids []string) ([]httpx.User, error) {
	unique := make([]string, 0, len(ids))
	seen := make(map[string]bool, len(ids))
	for _, id := range ids {
		if !seen[id] {
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
			out = append(out, FromUser(u))
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

func (c *Identity) ValidatePrincipal(ctx context.Context, token, key string) (httpx.Session, error) {
	r, err := c.Client.ValidatePrincipalToken(ctx, &im.PrincipalAuthRequest{Token: token, ServiceToken: key})
	if err != nil {
		return httpx.Session{}, httpx.Err(503, "identity_unavailable", "Identity service is temporarily unavailable")
	}
	if !r.Valid || r.User == nil {
		return httpx.Session{}, httpx.Err(401, "unauthenticated", "Participant credential expired or revoked")
	}
	return httpx.Session{User: FromUser(r.User), ServicePrincipal: true}, nil
}

// Chat wraps the Kitex ChatService client.
type Chat struct {
	Client chatservice.Client
}

// NewChat dials a new Chat RPC client at the given address.
func NewChat(addr string) (*Chat, error) {
	c, err := chatservice.NewClient("teamie.chat", rpcx.ClientOptions(addr, 3*time.Second, time.Second)...)
	return &Chat{Client: c}, err
}

func (c *Chat) CheckAccess(ctx context.Context, user, conversation string) (*im.AccessResponse, error) {
	return c.Client.CheckAccess(ctx, &im.AccessRequest{UserId: user, ConversationId: conversation})
}

func (c *Chat) Ping(ctx context.Context) error {
	r, err := c.Client.Ping(ctx, &im.PingRequest{})
	if err != nil {
		return err
	}
	if !r.Ready {
		return fmt.Errorf("chat not ready")
	}
	return nil
}

// ChatAccess matches the Chat RPC access checker interface.
type ChatAccess interface {
	CheckAccess(context.Context, string, string) (*im.AccessResponse, error)
	Ping(context.Context) error
}

// IdentityValidator adapts httpx.Identity to domain.SessionValidator.
type IdentityValidator struct {
	Identity httpx.Identity
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
