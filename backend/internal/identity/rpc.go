package identity

import (
	"context"
	"errors"
	"teamie/backend/internal/api"
	"teamie/backend/internal/rpcutil"
	"teamie/backend/kitex_gen/im"
	"time"
)

type RPC struct{ Store *Store }

func (h *RPC) ValidateSession(ctx context.Context, r *im.SessionRequest) (*im.SessionResponse, error) {
	ss, e := h.Store.Validate(ctx, r.Token)
	if e != nil {
		var ae *api.Error
		if errors.As(e, &ae) && ae.Status == 401 {
			return &im.SessionResponse{Valid: false}, nil
		}
		return nil, e
	}
	return &im.SessionResponse{Valid: true, User: rpcutil.ToUser(ss.User), CsrfToken: &ss.CSRFToken}, nil
}
func (h *RPC) GetUsers(ctx context.Context, r *im.UsersRequest) (*im.UsersResponse, error) {
	us, e := h.Store.Users(ctx, r.Ids)
	if e != nil {
		return nil, e
	}
	out := &im.UsersResponse{Users: []*im.User{}}
	for _, u := range us {
		out.Users = append(out.Users, rpcutil.ToUser(u))
	}
	return out, nil
}
func (h *RPC) Ping(ctx context.Context, r *im.PingRequest) (*im.PingResponse, error) {
	return &im.PingResponse{Ready: h.Store.Ping(ctx) == nil}, nil
}
func (h *RPC) ProvisionPrincipal(ctx context.Context, r *im.PrincipalControlRequest) (*im.PrincipalResponse, error) {
	if !api.ValidServiceKey(h.Store.ControlToken, r.ServiceToken) {
		return &im.PrincipalResponse{Ok: false, ErrorCode: "forbidden"}, nil
	}
	u, e := h.Store.ProvisionPrincipal(ctx, r.PrincipalId, r.DisplayName, r.Kind, r.Enabled, r.Deleted, r.Revision)
	if e != nil {
		var ae *api.Error
		if errors.As(e, &ae) {
			return &im.PrincipalResponse{Ok: false, ErrorCode: ae.Code}, nil
		}
		return nil, e
	}
	return &im.PrincipalResponse{Ok: true, User: rpcutil.ToUser(u)}, nil
}
func (h *RPC) IssuePrincipalToken(ctx context.Context, r *im.PrincipalTokenRequest) (*im.PrincipalTokenResponse, error) {
	if !api.ValidServiceKey(h.Store.RuntimeToken, r.ServiceToken) {
		return &im.PrincipalTokenResponse{Ok: false, ErrorCode: "forbidden"}, nil
	}
	token, expiry, e := h.Store.IssuePrincipalToken(ctx, r.PrincipalId)
	if e != nil {
		var ae *api.Error
		if errors.As(e, &ae) {
			return &im.PrincipalTokenResponse{Ok: false, ErrorCode: ae.Code}, nil
		}
		return nil, e
	}
	return &im.PrincipalTokenResponse{Ok: true, Token: token, ExpiresAt: expiry.Format(time.RFC3339Nano)}, nil
}
func (h *RPC) ValidatePrincipalToken(ctx context.Context, r *im.PrincipalAuthRequest) (*im.SessionResponse, error) {
	ss, e := h.Store.ValidatePrincipal(ctx, r.Token, r.ServiceToken)
	if e != nil {
		var ae *api.Error
		if errors.As(e, &ae) {
			return &im.SessionResponse{Valid: false}, nil
		}
		return nil, e
	}
	return &im.SessionResponse{Valid: true, User: rpcutil.ToUser(ss.User)}, nil
}
