package control

import (
	"context"
	"fmt"
	"github.com/cloudwego/kitex/client"
	"teamie/backend/kitex_gen/im"
	"teamie/backend/kitex_gen/im/chatservice"
	"teamie/backend/kitex_gen/im/identityservice"
	"teamie/backend/pkg/observability"
	"time"
)

type IM struct {
	Identity     identityservice.Client
	Chat         chatservice.Client
	ControlToken string
}

func NewIM(identityAddr, chatAddr, token string) (*IM, error) {
	opts := func(addr string) []client.Option {
		return []client.Option{observability.RPCClient(), client.WithHostPorts(addr), client.WithRPCTimeout(3 * time.Second), client.WithConnectTimeout(time.Second)}
	}
	identity, e := identityservice.NewClient("teamie.identity", opts(identityAddr)...)
	if e != nil {
		return nil, e
	}
	chat, e := chatservice.NewClient("teamie.chat", opts(chatAddr)...)
	return &IM{identity, chat, token}, e
}

type Authority interface {
	Validate(context.Context, string) (*im.User, string, error)
	Users(context.Context, []string) (map[string]*im.User, error)
	Workspaces(context.Context, string) ([]string, error)
	Participants(context.Context, string, string) ([]string, error)
	Provision(context.Context, Agent, int64, bool, bool) error
	Memberships(context.Context, Agent, []string) ([]string, error)
	Ping(context.Context) error
}

func (i *IM) Validate(ctx context.Context, token string) (*im.User, string, error) {
	if len(token) != 64 {
		return nil, "", errAPI(401, "unauthenticated", "Sign in to continue")
	}
	r, e := i.Identity.ValidateSession(ctx, &im.SessionRequest{Token: token})
	if e != nil {
		return nil, "", e
	}
	if !r.Valid || r.User == nil || r.User.Kind != "human" {
		return nil, "", errAPI(401, "unauthenticated", "A current human session is required")
	}
	return r.User, r.GetCsrfToken(), nil
}
func (i *IM) Users(ctx context.Context, ids []string) (map[string]*im.User, error) {
	out := map[string]*im.User{}
	ids = normalized(ids)
	for len(ids) > 0 {
		n := len(ids)
		if n > 500 {
			n = 500
		}
		r, e := i.Identity.GetUsers(ctx, &im.UsersRequest{Ids: ids[:n]})
		if e != nil {
			return nil, e
		}
		for _, u := range r.Users {
			out[u.Id] = u
		}
		ids = ids[n:]
	}
	return out, nil
}
func (i *IM) Workspaces(ctx context.Context, user string) ([]string, error) {
	r, e := i.Chat.GetParticipantWorkspaces(ctx, &im.ParticipantWorkspacesRequest{ServiceToken: i.ControlToken, UserId: user})
	if e != nil {
		return nil, e
	}
	if !r.Ok {
		return nil, errAPI(403, r.ErrorCode, "Workspace membership is unavailable")
	}
	return r.WorkspaceIds, nil
}
func (i *IM) Participants(ctx context.Context, workspace, user string) ([]string, error) {
	r, e := i.Chat.GetWorkspaceParticipants(ctx, &im.WorkspaceParticipantsRequest{ServiceToken: i.ControlToken, WorkspaceId: workspace, RequesterId: user})
	if e != nil {
		return nil, e
	}
	if !r.Ok {
		return nil, errAPI(404, r.ErrorCode, "Workspace is unavailable")
	}
	return r.MemberIds, nil
}
func (i *IM) Provision(ctx context.Context, a Agent, revision int64, enabled, deleted bool) error {
	r, e := i.Identity.ProvisionPrincipal(ctx, &im.PrincipalControlRequest{ServiceToken: i.ControlToken, PrincipalId: a.ID, DisplayName: a.Name, Kind: "agent", Enabled: enabled, Deleted: deleted, Revision: revision})
	if e != nil {
		return e
	}
	if !r.Ok {
		return fmt.Errorf("principal lifecycle unavailable: %s", r.ErrorCode)
	}
	return nil
}
func (i *IM) Memberships(ctx context.Context, a Agent, ids []string) ([]string, error) {
	r, e := i.Chat.SetPrincipalWorkspaces(ctx, &im.PrincipalWorkspacesRequest{ServiceToken: i.ControlToken, PrincipalId: a.ID, GrantorUserId: a.OwnerID, WorkspaceIds: ids, Revision: a.MembershipRevision})
	if e != nil {
		return nil, e
	}
	if !r.Ok {
		return nil, fmt.Errorf("participant membership unavailable: %s", r.ErrorCode)
	}
	return r.WorkspaceIds, nil
}
func (i *IM) Ping(ctx context.Context) error {
	a, e := i.Identity.Ping(ctx, &im.PingRequest{})
	if e != nil {
		return e
	}
	b, e := i.Chat.Ping(ctx, &im.PingRequest{})
	if e != nil {
		return e
	}
	if !a.Ready || !b.Ready {
		return fmt.Errorf("IM dependencies not ready")
	}
	return nil
}
