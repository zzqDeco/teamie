// Package rpc implements the inbound Kitex IdentityService RPC adapter.
package rpc

import (
	"context"
	"errors"
	"time"

	"teamie/services/identity/internal/app"
	"teamie/services/identity/internal/domain"
	"teamie/contracts/gen/im/kitex_gen/im"
)

// Server adapts the Identity application service to the generated Kitex IdentityService interface.
type Server struct {
	App *app.Service
}

var _ im.IdentityService = (*Server)(nil)

func toWireUser(u domain.User) *im.User {
	return &im.User{
		Id:           u.ID,
		Email:        u.Email,
		DisplayName:  u.DisplayName,
		Kind:         u.Kind,
		CreatedAt:    u.CreatedAt.UTC().Format(time.RFC3339Nano),
		PlatformRole: u.PlatformRole,
	}
}

func (s *Server) ValidateSession(ctx context.Context, r *im.SessionRequest) (*im.SessionResponse, error) {
	ss, err := s.App.Validate(ctx, r.Token)
	if err != nil {
		var de *domain.Error
		if errors.As(err, &de) && de.Status == 401 {
			return &im.SessionResponse{Valid: false}, nil
		}
		return nil, err
	}
	return &im.SessionResponse{Valid: true, User: toWireUser(ss.User), CsrfToken: &ss.CSRFToken}, nil
}

func (s *Server) GetUsers(ctx context.Context, r *im.UsersRequest) (*im.UsersResponse, error) {
	us, err := s.App.GetUsers(ctx, r.Ids)
	if err != nil {
		return nil, err
	}
	out := &im.UsersResponse{Users: make([]*im.User, 0, len(us))}
	for _, u := range us {
		out.Users = append(out.Users, toWireUser(u))
	}
	return out, nil
}

func (s *Server) Ping(ctx context.Context, _ *im.PingRequest) (*im.PingResponse, error) {
	return &im.PingResponse{Ready: s.App.Ping(ctx) == nil}, nil
}

func (s *Server) ProvisionPrincipal(ctx context.Context, r *im.PrincipalControlRequest) (*im.PrincipalResponse, error) {
	if !domain.ValidServiceKey(s.App.ControlToken, r.ServiceToken) {
		return &im.PrincipalResponse{Ok: false, ErrorCode: "forbidden"}, nil
	}
	u, err := s.App.ProvisionPrincipal(ctx, r.PrincipalId, r.DisplayName, r.Kind, r.Enabled, r.Deleted, r.Revision)
	if err != nil {
		var de *domain.Error
		if errors.As(err, &de) {
			return &im.PrincipalResponse{Ok: false, ErrorCode: de.Code}, nil
		}
		return nil, err
	}
	return &im.PrincipalResponse{Ok: true, User: toWireUser(u)}, nil
}

func (s *Server) IssuePrincipalToken(ctx context.Context, r *im.PrincipalTokenRequest) (*im.PrincipalTokenResponse, error) {
	if !domain.ValidServiceKey(s.App.RuntimeToken, r.ServiceToken) {
		return &im.PrincipalTokenResponse{Ok: false, ErrorCode: "forbidden"}, nil
	}
	token, expiry, err := s.App.IssuePrincipalToken(ctx, r.PrincipalId)
	if err != nil {
		var de *domain.Error
		if errors.As(err, &de) {
			return &im.PrincipalTokenResponse{Ok: false, ErrorCode: de.Code}, nil
		}
		return nil, err
	}
	return &im.PrincipalTokenResponse{Ok: true, Token: token, ExpiresAt: expiry.Format(time.RFC3339Nano)}, nil
}

func (s *Server) ValidatePrincipalToken(ctx context.Context, r *im.PrincipalAuthRequest) (*im.SessionResponse, error) {
	ss, err := s.App.ValidatePrincipal(ctx, r.Token, r.ServiceToken)
	if err != nil {
		var de *domain.Error
		if errors.As(err, &de) {
			return &im.SessionResponse{Valid: false}, nil
		}
		return nil, err
	}
	return &im.SessionResponse{Valid: true, User: toWireUser(ss.User)}, nil
}
