package rpcutil

import (
	"context"
	"fmt"
	"github.com/cloudwego/kitex/client"
	"teamie/backend/internal/api"
	"teamie/backend/kitex_gen/im"
	"teamie/backend/kitex_gen/im/chatservice"
	"teamie/backend/kitex_gen/im/identityservice"
	"time"
)

func ToUser(u api.User) *im.User {
	return &im.User{Id: u.ID, Email: u.Email, DisplayName: u.DisplayName, Kind: u.Kind, CreatedAt: u.CreatedAt.Format(time.RFC3339Nano)}
}
func FromUser(u *im.User) api.User {
	if u == nil {
		return api.User{}
	}
	at, _ := time.Parse(time.RFC3339Nano, u.CreatedAt)
	return api.User{ID: u.Id, Email: u.Email, DisplayName: u.DisplayName, Kind: u.Kind, CreatedAt: at}
}

type Identity struct{ Client identityservice.Client }

func NewIdentity(addr string) (*Identity, error) {
	c, e := identityservice.NewClient("teamie.identity", client.WithHostPorts(addr), client.WithRPCTimeout(3*time.Second), client.WithConnectTimeout(time.Second))
	return &Identity{c}, e
}
func (c *Identity) Validate(ctx context.Context, token string) (api.Session, error) {
	if len(token) != 64 {
		return api.Session{}, api.Err(401, "unauthenticated", "Sign in to continue")
	}
	r, e := c.Client.ValidateSession(ctx, &im.SessionRequest{Token: token})
	if e != nil {
		return api.Session{}, api.Err(503, "identity_unavailable", "Identity service is temporarily unavailable")
	}
	if !r.Valid || r.User == nil {
		return api.Session{}, api.Err(401, "unauthenticated", "Session expired or revoked")
	}
	return api.Session{User: FromUser(r.User), CSRFToken: r.GetCsrfToken()}, nil
}
func (c *Identity) Users(ctx context.Context, ids []string) ([]api.User, error) {
	r, e := c.Client.GetUsers(ctx, &im.UsersRequest{Ids: ids})
	if e != nil {
		return nil, api.Err(503, "identity_unavailable", "Identity service is temporarily unavailable")
	}
	out := []api.User{}
	for _, u := range r.Users {
		out = append(out, FromUser(u))
	}
	return out, nil
}
func (c *Identity) Ping(ctx context.Context) error {
	r, e := c.Client.Ping(ctx, &im.PingRequest{})
	if e != nil {
		return e
	}
	if !r.Ready {
		return fmt.Errorf("identity not ready")
	}
	return nil
}

type Chat struct{ Client chatservice.Client }

func NewChat(addr string) (*Chat, error) {
	c, e := chatservice.NewClient("teamie.chat", client.WithHostPorts(addr), client.WithRPCTimeout(3*time.Second), client.WithConnectTimeout(time.Second))
	return &Chat{c}, e
}
func (c *Chat) CheckAccess(ctx context.Context, user, conversation string) (*im.AccessResponse, error) {
	return c.Client.CheckAccess(ctx, &im.AccessRequest{UserId: user, ConversationId: conversation})
}
func (c *Chat) Ping(ctx context.Context) error {
	r, e := c.Client.Ping(ctx, &im.PingRequest{})
	if e != nil {
		return e
	}
	if !r.Ready {
		return fmt.Errorf("chat not ready")
	}
	return nil
}
