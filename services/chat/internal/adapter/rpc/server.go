// Package rpc implements the inbound Kitex ChatService RPC adapter.
package rpc

import (
	"context"
	"errors"

	"teamie/services/chat/internal/app"
	"teamie/services/chat/internal/domain"
	"teamie/contracts/gen/im/kitex_gen/im"
)

// Server adapts the Chat application service to the generated Kitex ChatService interface.
type Server struct {
	App *app.Service
}

var _ im.ChatService = (*Server)(nil)

func errorCode(err error) string {
	var de *domain.Error
	if errors.As(err, &de) {
		return de.Code
	}
	var coded interface{ ErrorCode() string }
	if errors.As(err, &coded) {
		return coded.ErrorCode()
	}
	return "internal_error"
}

func (s *Server) CheckAccess(ctx context.Context, r *im.AccessRequest) (*im.AccessResponse, error) {
	c, err := s.App.ConversationAccess(ctx, r.ConversationId, r.UserId)
	if err != nil {
		var de *domain.Error
		if errors.As(err, &de) && de.Status == 404 {
			return &im.AccessResponse{Allowed: false, MemberIds: []string{}, TypingEnabled: false}, nil
		}
		return nil, err
	}
	typingEnabled := false
	if s.App.Flags != nil {
		typingEnabled = s.App.Flags.Enabled("im.typing.publish")
	}
	return &im.AccessResponse{Allowed: true, MemberIds: c.MemberIDs, TypingEnabled: typingEnabled}, nil
}

func (s *Server) Ping(ctx context.Context, _ *im.PingRequest) (*im.PingResponse, error) {
	return &im.PingResponse{Ready: s.App.Ping(ctx) == nil}, nil
}

func (s *Server) GetParticipantWorkspaces(ctx context.Context, r *im.ParticipantWorkspacesRequest) (*im.ParticipantWorkspacesResponse, error) {
	if !domain.ValidServiceKey(s.App.ControlToken, r.ServiceToken) {
		return &im.ParticipantWorkspacesResponse{Ok: false, ErrorCode: "forbidden", WorkspaceIds: []string{}}, nil
	}
	ids, err := s.App.ParticipantWorkspaces(ctx, r.UserId)
	if err != nil {
		return &im.ParticipantWorkspacesResponse{Ok: false, ErrorCode: errorCode(err), WorkspaceIds: []string{}}, nil
	}
	return &im.ParticipantWorkspacesResponse{Ok: true, WorkspaceIds: ids}, nil
}

func (s *Server) GetWorkspaceParticipants(ctx context.Context, r *im.WorkspaceParticipantsRequest) (*im.WorkspaceParticipantsResponse, error) {
	if !domain.ValidServiceKey(s.App.ControlToken, r.ServiceToken) {
		return &im.WorkspaceParticipantsResponse{Ok: false, ErrorCode: "forbidden", MemberIds: []string{}}, nil
	}
	ids, err := s.App.WorkspaceParticipants(ctx, r.WorkspaceId, r.RequesterId)
	if err != nil {
		return &im.WorkspaceParticipantsResponse{Ok: false, ErrorCode: errorCode(err), MemberIds: []string{}}, nil
	}
	return &im.WorkspaceParticipantsResponse{Ok: true, MemberIds: ids}, nil
}

func (s *Server) SetPrincipalWorkspaces(ctx context.Context, r *im.PrincipalWorkspacesRequest) (*im.ParticipantWorkspacesResponse, error) {
	if !domain.ValidServiceKey(s.App.ControlToken, r.ServiceToken) {
		return &im.ParticipantWorkspacesResponse{Ok: false, ErrorCode: "forbidden", WorkspaceIds: []string{}}, nil
	}
	if s.App.Identity == nil {
		return &im.ParticipantWorkspacesResponse{Ok: false, ErrorCode: "internal_error", WorkspaceIds: []string{}}, nil
	}
	users, err := s.App.Identity.Users(ctx, []string{r.PrincipalId, r.GrantorUserId})
	if err != nil {
		return nil, err
	}
	var principal, grantor domain.User
	for _, u := range users {
		if u.ID == r.PrincipalId {
			principal = u
		}
		if u.ID == r.GrantorUserId {
			grantor = u
		}
	}
	ids, err := s.App.SetPrincipalWorkspaces(ctx, principal, grantor, r.WorkspaceIds, r.Revision)
	if err != nil {
		return &im.ParticipantWorkspacesResponse{Ok: false, ErrorCode: errorCode(err), WorkspaceIds: []string{}}, nil
	}
	return &im.ParticipantWorkspacesResponse{Ok: true, WorkspaceIds: ids}, nil
}
