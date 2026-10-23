// Package rpcclient implements the outbound Kitex RPC adapters for IM authority
// and reconciliation observability in the Agent Control Plane bounded context.
package rpcclient

import (
	"context"
	"fmt"
	"time"

	"github.com/cloudwego/kitex/client"
	"go.uber.org/zap"
	"teamie/agent-control/internal/control/domain"
	"teamie/backend/kitex_gen/im"
	"teamie/backend/kitex_gen/im/chatservice"
	"teamie/backend/kitex_gen/im/identityservice"
	"teamie/backend/pkg/observability"
)

// IMClient communicates with the independent Identity and Chat RPC services.
type IMClient struct {
	Identity     identityservice.Client
	Chat         chatservice.Client
	ControlToken string
}

var _ domain.IMAuthority = (*IMClient)(nil)

// NewIMClient creates Kitex clients for Identity and Chat RPC endpoints.
func NewIMClient(identityAddr, chatAddr, token string) (*IMClient, error) {
	opts := func(addr string) []client.Option {
		return []client.Option{
			observability.RPCClient(),
			client.WithHostPorts(addr),
			client.WithRPCTimeout(3 * time.Second),
			client.WithConnectTimeout(time.Second),
		}
	}
	identity, err := identityservice.NewClient("teamie.identity", opts(identityAddr)...)
	if err != nil {
		return nil, err
	}
	chat, err := chatservice.NewClient("teamie.chat", opts(chatAddr)...)
	if err != nil {
		return nil, err
	}
	return &IMClient{Identity: identity, Chat: chat, ControlToken: token}, nil
}

func toDomainUserProfile(u *im.User) domain.UserProfile {
	if u == nil {
		return domain.UserProfile{}
	}
	return domain.UserProfile{
		ID:           u.Id,
		DisplayName:  u.DisplayName,
		Kind:         u.Kind,
		PlatformRole: u.PlatformRole,
	}
}

func (i *IMClient) ValidateRaw(ctx context.Context, token string) (*im.User, string, error) {
	if len(token) != 64 {
		return nil, "", domain.ErrUnauthenticated("Sign in to continue")
	}
	r, err := i.Identity.ValidateSession(ctx, &im.SessionRequest{Token: token})
	if err != nil {
		return nil, "", err
	}
	if !r.Valid || r.User == nil || r.User.Kind != "human" {
		return nil, "", domain.ErrUnauthenticated("A current human session is required")
	}
	return r.User, r.GetCsrfToken(), nil
}

func (i *IMClient) ValidateSession(ctx context.Context, token string) (*domain.UserProfile, string, error) {
	u, csrf, err := i.ValidateRaw(ctx, token)
	if err != nil {
		return nil, "", err
	}
	p := toDomainUserProfile(u)
	return &p, csrf, nil
}

func (i *IMClient) UsersRaw(ctx context.Context, ids []string) (map[string]*im.User, error) {
	out := map[string]*im.User{}
	ids = domain.Normalized(ids)
	for len(ids) > 0 {
		n := len(ids)
		if n > 500 {
			n = 500
		}
		r, err := i.Identity.GetUsers(ctx, &im.UsersRequest{Ids: ids[:n]})
		if err != nil {
			return nil, err
		}
		for _, u := range r.Users {
			out[u.Id] = u
		}
		ids = ids[n:]
	}
	return out, nil
}

func (i *IMClient) GetUsers(ctx context.Context, ids []string) (map[string]domain.UserProfile, error) {
	raw, err := i.UsersRaw(ctx, ids)
	if err != nil {
		return nil, err
	}
	out := make(map[string]domain.UserProfile, len(raw))
	for k, u := range raw {
		out[k] = toDomainUserProfile(u)
	}
	return out, nil
}

func (i *IMClient) GetWorkspaces(ctx context.Context, userID string) ([]string, error) {
	r, err := i.Chat.GetParticipantWorkspaces(ctx, &im.ParticipantWorkspacesRequest{
		ServiceToken: i.ControlToken,
		UserId:       userID,
	})
	if err != nil {
		return nil, err
	}
	if !r.Ok {
		return nil, domain.Err(403, r.ErrorCode, "Workspace membership is unavailable")
	}
	return r.WorkspaceIds, nil
}

func (i *IMClient) GetParticipants(ctx context.Context, workspaceID, requesterID string) ([]string, error) {
	r, err := i.Chat.GetWorkspaceParticipants(ctx, &im.WorkspaceParticipantsRequest{
		ServiceToken: i.ControlToken,
		WorkspaceId:  workspaceID,
		RequesterId:  requesterID,
	})
	if err != nil {
		return nil, err
	}
	if !r.Ok {
		return nil, domain.Err(404, r.ErrorCode, "Workspace is unavailable")
	}
	return r.MemberIds, nil
}

func (i *IMClient) ProvisionPrincipal(ctx context.Context, a domain.Agent, revision int64, enabled, deleted bool) error {
	r, err := i.Identity.ProvisionPrincipal(ctx, &im.PrincipalControlRequest{
		ServiceToken: i.ControlToken,
		PrincipalId:  a.ID,
		DisplayName:  a.Name,
		Kind:         "agent",
		Enabled:      enabled,
		Deleted:      deleted,
		Revision:     revision,
	})
	if err != nil {
		return err
	}
	if !r.Ok {
		return fmt.Errorf("principal lifecycle unavailable: %s", r.ErrorCode)
	}
	return nil
}

func (i *IMClient) SyncMemberships(ctx context.Context, a domain.Agent, ids []string) ([]string, error) {
	r, err := i.Chat.SetPrincipalWorkspaces(ctx, &im.PrincipalWorkspacesRequest{
		ServiceToken:  i.ControlToken,
		PrincipalId:   a.ID,
		GrantorUserId: a.OwnerID,
		WorkspaceIds:  ids,
		Revision:      a.MembershipRevision,
	})
	if err != nil {
		return nil, err
	}
	if !r.Ok {
		return nil, fmt.Errorf("participant membership unavailable: %s", r.ErrorCode)
	}
	return r.WorkspaceIds, nil
}

func (i *IMClient) Ping(ctx context.Context) error {
	a, err := i.Identity.Ping(ctx, &im.PingRequest{})
	if err != nil {
		return err
	}
	b, err := i.Chat.Ping(ctx, &im.PingRequest{})
	if err != nil {
		return err
	}
	if !a.Ready || !b.Ready {
		return fmt.Errorf("IM dependencies not ready")
	}
	return nil
}

// ObservabilityNotifier records structured reconciliation telemetry via backend/pkg/observability.
type ObservabilityNotifier struct{}

var _ domain.RuntimeNotifier = ObservabilityNotifier{}

func (ObservabilityNotifier) BeginReconcile(ctx context.Context, agentID string) (context.Context, func(changed bool, attempts int, err error)) {
	ctx, operation := observability.Begin(ctx, "agent-reconciliation", zap.String("custom.agent.id", agentID))
	return ctx, func(changed bool, attempts int, failure error) {
		fields := []zap.Field{
			zap.Bool("custom.agent.membership_changed", changed),
			zap.Int("custom.retry.attempts", attempts),
		}
		if changed {
			operation.End(failure, fields...)
		} else {
			operation.EndRecoverable(failure, fields...)
		}
	}
}
