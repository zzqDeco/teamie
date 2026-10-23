package chat

import (
	"context"

	"teamie/backend/internal/api"
	rpcadapter "teamie/backend/internal/chat/adapter/rpc"
	"teamie/backend/kitex_gen/im"
)

// RPC delegates Kitex ChatService RPC calls to the inbound RPC adapter.
type RPC struct {
	Store        *Store
	Identity     api.Identity
	ControlToken string
}

func (h *RPC) server() *rpcadapter.Server {
	return &rpcadapter.Server{
		App: h.Store.App(h.Identity, h.ControlToken),
	}
}

func (h *RPC) CheckAccess(ctx context.Context, r *im.AccessRequest) (*im.AccessResponse, error) {
	return h.server().CheckAccess(ctx, r)
}

func (h *RPC) Ping(ctx context.Context, r *im.PingRequest) (*im.PingResponse, error) {
	return h.server().Ping(ctx, r)
}

func (h *RPC) GetParticipantWorkspaces(ctx context.Context, r *im.ParticipantWorkspacesRequest) (*im.ParticipantWorkspacesResponse, error) {
	return h.server().GetParticipantWorkspaces(ctx, r)
}

func (h *RPC) GetWorkspaceParticipants(ctx context.Context, r *im.WorkspaceParticipantsRequest) (*im.WorkspaceParticipantsResponse, error) {
	return h.server().GetWorkspaceParticipants(ctx, r)
}

func (h *RPC) SetPrincipalWorkspaces(ctx context.Context, r *im.PrincipalWorkspacesRequest) (*im.ParticipantWorkspacesResponse, error) {
	return h.server().SetPrincipalWorkspaces(ctx, r)
}
