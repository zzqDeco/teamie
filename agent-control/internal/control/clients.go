package control

import (
	"context"

	"teamie/agent-control/internal/control/domain"
	"teamie/agent-control/internal/control/infra/rpcclient"
	"teamie/backend/kitex_gen/im"
	"teamie/backend/kitex_gen/im/chatservice"
	"teamie/backend/kitex_gen/im/identityservice"
)

// Authority is the IM authority facade interface preserved for composition and tests.
type Authority interface {
	Validate(context.Context, string) (*im.User, string, error)
	Users(context.Context, []string) (map[string]*im.User, error)
	Workspaces(context.Context, string) ([]string, error)
	Participants(context.Context, string, string) ([]string, error)
	Provision(context.Context, Agent, int64, bool, bool) error
	Memberships(context.Context, Agent, []string) ([]string, error)
	Ping(context.Context) error
}

// authorityBridge adapts any Authority implementation (including test fakes) to domain.IMAuthority.
type authorityBridge struct {
	inner Authority
}

var _ domain.IMAuthority = authorityBridge{}

func (b authorityBridge) ValidateSession(ctx context.Context, token string) (*domain.UserProfile, string, error) {
	u, csrf, err := b.inner.Validate(ctx, token)
	if err != nil {
		return nil, "", err
	}
	if u == nil {
		return nil, csrf, nil
	}
	return &domain.UserProfile{
		ID:           u.Id,
		DisplayName:  u.DisplayName,
		Kind:         u.Kind,
		PlatformRole: u.PlatformRole,
	}, csrf, nil
}

func (b authorityBridge) GetUsers(ctx context.Context, ids []string) (map[string]domain.UserProfile, error) {
	raw, err := b.inner.Users(ctx, ids)
	if err != nil {
		return nil, err
	}
	out := make(map[string]domain.UserProfile, len(raw))
	for k, u := range raw {
		if u != nil {
			out[k] = domain.UserProfile{
				ID:           u.Id,
				DisplayName:  u.DisplayName,
				Kind:         u.Kind,
				PlatformRole: u.PlatformRole,
			}
		}
	}
	return out, nil
}

func (b authorityBridge) GetWorkspaces(ctx context.Context, userID string) ([]string, error) {
	return b.inner.Workspaces(ctx, userID)
}

func (b authorityBridge) GetParticipants(ctx context.Context, workspaceID, requesterID string) ([]string, error) {
	return b.inner.Participants(ctx, workspaceID, requesterID)
}

func (b authorityBridge) ProvisionPrincipal(ctx context.Context, a domain.Agent, revision int64, enabled, deleted bool) error {
	return b.inner.Provision(ctx, a, revision, enabled, deleted)
}

func (b authorityBridge) SyncMemberships(ctx context.Context, a domain.Agent, ids []string) ([]string, error) {
	return b.inner.Memberships(ctx, a, ids)
}

func (b authorityBridge) Ping(ctx context.Context) error {
	return b.inner.Ping(ctx)
}

// IM wraps the Kitex RPC client adapter for Identity and Chat services.
type IM struct {
	Identity     identityservice.Client
	Chat         chatservice.Client
	ControlToken string
}

func NewIM(identityAddr, chatAddr, token string) (*IM, error) {
	c, err := rpcclient.NewIMClient(identityAddr, chatAddr, token)
	if err != nil {
		return nil, err
	}
	return &IM{Identity: c.Identity, Chat: c.Chat, ControlToken: c.ControlToken}, nil
}

func (i *IM) client() *rpcclient.IMClient {
	return &rpcclient.IMClient{
		Identity:     i.Identity,
		Chat:         i.Chat,
		ControlToken: i.ControlToken,
	}
}

func (i *IM) Validate(ctx context.Context, token string) (*im.User, string, error) {
	return i.client().ValidateRaw(ctx, token)
}

func (i *IM) Users(ctx context.Context, ids []string) (map[string]*im.User, error) {
	return i.client().UsersRaw(ctx, ids)
}

func (i *IM) Workspaces(ctx context.Context, user string) ([]string, error) {
	return i.client().GetWorkspaces(ctx, user)
}

func (i *IM) Participants(ctx context.Context, workspace, user string) ([]string, error) {
	return i.client().GetParticipants(ctx, workspace, user)
}

func (i *IM) Provision(ctx context.Context, a Agent, revision int64, enabled, deleted bool) error {
	return i.client().ProvisionPrincipal(ctx, a, revision, enabled, deleted)
}

func (i *IM) Memberships(ctx context.Context, a Agent, ids []string) ([]string, error) {
	return i.client().SyncMemberships(ctx, a, ids)
}

func (i *IM) Ping(ctx context.Context) error {
	return i.client().Ping(ctx)
}
