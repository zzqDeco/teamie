package identity

import (
	"context"
	"errors"
	"teamie/backend/internal/api"
	"teamie/backend/internal/rpcutil"
	"teamie/backend/kitex_gen/im"
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
