// Package rpcx provides shared CloudWeGo Kitex client and server option builders,
// IM Identity/Chat RPC clients, and administrator session authentication helpers.
package rpcx

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/cloudwego/kitex/client"
	"github.com/cloudwego/kitex/client/callopt"
	"github.com/cloudwego/kitex/pkg/remote/codec"
	"github.com/cloudwego/kitex/server"
	"teamie/contracts/gen/im/kitex_gen/im"
	"teamie/contracts/gen/im/kitex_gen/im/chatservice"
	"teamie/contracts/gen/im/kitex_gen/im/identityservice"
	"teamie/pkg/httpx"
	"teamie/pkg/observability"
)

// ClientOptions constructs the standard Kitex client options with ECS observability
// and bounded RPC/connection timeouts.
func ClientOptions(addr string, rpcTimeout, connectTimeout time.Duration) []client.Option {
	if rpcTimeout <= 0 {
		rpcTimeout = 3 * time.Second
	}
	if connectTimeout <= 0 {
		connectTimeout = time.Second
	}
	return []client.Option{
		observability.RPCClient(),
		client.WithHostPorts(addr),
		client.WithRPCTimeout(rpcTimeout),
		client.WithConnectTimeout(connectTimeout),
	}
}

// ServerOptions constructs the standard Kitex server options for the given TCP address
// with ECS observability and optional payload size limit.
func ServerOptions(addr net.Addr, maxPayloadBytes int, extra ...server.Option) []server.Option {
	opts := []server.Option{
		server.WithServiceAddr(addr),
		observability.RPCServer(),
	}
	if maxPayloadBytes > 0 {
		opts = append(opts, server.WithCodec(codec.NewDefaultCodecWithSizeLimit(maxPayloadBytes)))
	}
	return append(opts, extra...)
}

// ToUser converts an httpx.User into the Kitex im.User wire struct.
func ToUser(u httpx.User) *im.User {
	return &im.User{
		Id:           u.ID,
		Email:        u.Email,
		DisplayName:  u.DisplayName,
		Kind:         u.Kind,
		PlatformRole: u.PlatformRole,
		CreatedAt:    u.CreatedAt.Format(time.RFC3339Nano),
	}
}

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

// Identity wraps the Kitex IdentityService client with batch user resolution and session validation.
type Identity struct {
	Client identityservice.Client
}

func NewIdentity(addr string) (*Identity, error) {
	c, err := identityservice.NewClient("teamie.identity", ClientOptions(addr, 3*time.Second, time.Second)...)
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

func NewChat(addr string) (*Chat, error) {
	c, err := chatservice.NewClient("teamie.chat", ClientOptions(addr, 3*time.Second, time.Second)...)
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

// SessionValidator is the subset of IdentityService used by AdminAuth.
type SessionValidator interface {
	ValidateSession(context.Context, *im.SessionRequest, ...callopt.Option) (*im.SessionResponse, error)
}

// AdminAuth authenticates human platform administrators via Identity RPC with CSRF and Origin protection.
type AdminAuth struct {
	Identity SessionValidator
	Origin   string
}

func NewAdminAuth(addr, origin string) (AdminAuth, error) {
	c, err := identityservice.NewClient("teamie.identity", ClientOptions(addr, 3*time.Second, time.Second)...)
	return AdminAuth{Identity: c, Origin: origin}, err
}

func (a AdminAuth) Require(r *http.Request) (*im.User, error) {
	cookie, err := r.Cookie(httpx.CookieName)
	if err != nil || len(cookie.Value) != 64 {
		return nil, httpx.Err(401, "unauthorized", "Authentication required")
	}
	s, err := a.Identity.ValidateSession(r.Context(), &im.SessionRequest{Token: cookie.Value})
	if err != nil {
		return nil, httpx.Err(503, "identity_unavailable", "Authentication is temporarily unavailable")
	}
	if s == nil || !s.Valid || s.User == nil {
		return nil, httpx.Err(401, "unauthorized", "Authentication required")
	}
	if r.Method == "GET" || r.Method == "HEAD" {
		if provided := r.Header.Get("X-CSRF-Token"); provided != "" && (s.CsrfToken == nil || !httpx.EqualSecret(provided, *s.CsrfToken)) {
			return nil, httpx.Err(401, "session_changed", "Browser session changed")
		}
	}
	if s.User.Kind != "human" || s.User.PlatformRole != "admin" {
		return nil, httpx.Err(403, "forbidden", "Administrator access required")
	}
	if r.Method != "GET" && r.Method != "HEAD" {
		if r.Header.Get("Origin") != a.Origin {
			return nil, httpx.Err(403, "untrusted_origin", "Untrusted request origin")
		}
		if s.CsrfToken == nil || !httpx.EqualSecret(r.Header.Get("X-CSRF-Token"), *s.CsrfToken) {
			return nil, httpx.Err(403, "invalid_csrf", "Invalid CSRF token")
		}
	}
	return s.User, nil
}
