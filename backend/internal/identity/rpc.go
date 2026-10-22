package identity

import (
	"context"

	rpcadapter "teamie/backend/internal/identity/adapter/rpc"
	"teamie/backend/kitex_gen/im"
)

// RPC delegates Kitex IdentityService RPC calls to the inbound RPC adapter.
type RPC struct {
	Store *Store
}

func (h *RPC) server() *rpcadapter.Server {
	return &rpcadapter.Server{App: h.Store.App()}
}

func (h *RPC) ValidateSession(ctx context.Context, r *im.SessionRequest) (*im.SessionResponse, error) {
	return h.server().ValidateSession(ctx, r)
}

func (h *RPC) GetUsers(ctx context.Context, r *im.UsersRequest) (*im.UsersResponse, error) {
	return h.server().GetUsers(ctx, r)
}

func (h *RPC) Ping(ctx context.Context, r *im.PingRequest) (*im.PingResponse, error) {
	return h.server().Ping(ctx, r)
}

func (h *RPC) ProvisionPrincipal(ctx context.Context, r *im.PrincipalControlRequest) (*im.PrincipalResponse, error) {
	return h.server().ProvisionPrincipal(ctx, r)
}

func (h *RPC) IssuePrincipalToken(ctx context.Context, r *im.PrincipalTokenRequest) (*im.PrincipalTokenResponse, error) {
	return h.server().IssuePrincipalToken(ctx, r)
}

func (h *RPC) ValidatePrincipalToken(ctx context.Context, r *im.PrincipalAuthRequest) (*im.SessionResponse, error) {
	return h.server().ValidatePrincipalToken(ctx, r)
}
