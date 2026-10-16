package chat

import (
	"context"
	"errors"
	"teamie/backend/internal/api"
	"teamie/backend/kitex_gen/im"
)

type RPC struct {
	Store        *Store
	Identity     api.Identity
	ControlToken string
}

func (h *RPC) CheckAccess(ctx context.Context, r *im.AccessRequest) (*im.AccessResponse, error) {
	c, e := conversationAccess(ctx, h.Store.DB, r.ConversationId, r.UserId)
	if e != nil {
		var ae *api.Error
		if errors.As(e, &ae) && ae.Status == 404 {
			return &im.AccessResponse{Allowed: false, MemberIds: []string{}, TypingEnabled: false}, nil
		}
		return nil, e
	}
	return &im.AccessResponse{Allowed: true, MemberIds: c.MemberIDs, TypingEnabled: h.Store.Flags.Load().Flags["im.typing.publish"]}, nil
}
func (h *RPC) Ping(ctx context.Context, r *im.PingRequest) (*im.PingResponse, error) {
	return &im.PingResponse{Ready: h.Store.Ping(ctx) == nil}, nil
}
func code(e error) string {
	var ae *api.Error
	if errors.As(e, &ae) {
		return ae.Code
	}
	return "internal_error"
}
func (h *RPC) GetParticipantWorkspaces(ctx context.Context, r *im.ParticipantWorkspacesRequest) (*im.ParticipantWorkspacesResponse, error) {
	if !api.ValidServiceKey(h.ControlToken, r.ServiceToken) {
		return &im.ParticipantWorkspacesResponse{Ok: false, ErrorCode: "forbidden", WorkspaceIds: []string{}}, nil
	}
	ids, e := h.Store.ParticipantWorkspaces(ctx, r.UserId)
	if e != nil {
		return &im.ParticipantWorkspacesResponse{Ok: false, ErrorCode: code(e), WorkspaceIds: []string{}}, nil
	}
	return &im.ParticipantWorkspacesResponse{Ok: true, WorkspaceIds: ids}, nil
}
func (h *RPC) GetWorkspaceParticipants(ctx context.Context, r *im.WorkspaceParticipantsRequest) (*im.WorkspaceParticipantsResponse, error) {
	if !api.ValidServiceKey(h.ControlToken, r.ServiceToken) {
		return &im.WorkspaceParticipantsResponse{Ok: false, ErrorCode: "forbidden", MemberIds: []string{}}, nil
	}
	ids, e := h.Store.WorkspaceParticipants(ctx, r.WorkspaceId, r.RequesterId)
	if e != nil {
		return &im.WorkspaceParticipantsResponse{Ok: false, ErrorCode: code(e), MemberIds: []string{}}, nil
	}
	return &im.WorkspaceParticipantsResponse{Ok: true, MemberIds: ids}, nil
}
func (h *RPC) SetPrincipalWorkspaces(ctx context.Context, r *im.PrincipalWorkspacesRequest) (*im.ParticipantWorkspacesResponse, error) {
	if !api.ValidServiceKey(h.ControlToken, r.ServiceToken) {
		return &im.ParticipantWorkspacesResponse{Ok: false, ErrorCode: "forbidden", WorkspaceIds: []string{}}, nil
	}
	users, e := h.Identity.Users(ctx, []string{r.PrincipalId, r.GrantorUserId})
	if e != nil {
		return nil, e
	}
	var principal, grantor api.User
	for _, u := range users {
		if u.ID == r.PrincipalId {
			principal = u
		}
		if u.ID == r.GrantorUserId {
			grantor = u
		}
	}
	ids, e := h.Store.SetPrincipalWorkspaces(ctx, principal, grantor, r.WorkspaceIds, r.Revision)
	if e != nil {
		return &im.ParticipantWorkspacesResponse{Ok: false, ErrorCode: code(e), WorkspaceIds: []string{}}, nil
	}
	return &im.ParticipantWorkspacesResponse{Ok: true, WorkspaceIds: ids}, nil
}
