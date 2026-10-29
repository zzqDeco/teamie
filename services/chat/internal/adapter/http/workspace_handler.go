package http

import (
	"context"
	"time"

	chatgen "teamie/contracts/gen/im/hertz_gen/im/chat"
	"teamie/services/chat/internal/domain"
)

func (a *Adapter) CreateWorkspace(ctx context.Context, req *chatgen.CreateWorkspaceReq) (*chatgen.CreateWorkspaceResp, error) {
	user, ok := actorFromContext(ctx)
	if !ok {
		return nil, domain.Err(401, "unauthenticated", "Sign in to continue")
	}
	if user.Kind != "human" {
		return nil, domain.ErrHumanWorkspaceManagementRequired()
	}
	ws, c, err := a.App.CreateWorkspace(ctx, user.ID, req.Name)
	if err != nil {
		return nil, err
	}
	return &chatgen.CreateWorkspaceResp{
		Workspace:    toWorkspaceDTO(ws),
		Conversation: toConversationDTO(c),
	}, nil
}

func (a *Adapter) ListWorkspaceMembers(ctx context.Context, req *chatgen.ListWorkspaceMembersReq) (*chatgen.ListWorkspaceMembersResp, error) {
	user, ok := actorFromContext(ctx)
	if !ok {
		return nil, domain.Err(401, "unauthenticated", "Sign in to continue")
	}
	members, err := a.App.ResolvedMembers(ctx, user.ID, req.ID)
	if err != nil {
		return nil, err
	}
	out := make([]*chatgen.MemberDTO, 0, len(members))
	for _, m := range members {
		out = append(out, toMemberDTO(m))
	}
	return &chatgen.ListWorkspaceMembersResp{Members: out}, nil
}

func (a *Adapter) CreateWorkspaceInvite(ctx context.Context, req *chatgen.CreateWorkspaceInviteReq) (*chatgen.CreateWorkspaceInviteResp, error) {
	user, ok := actorFromContext(ctx)
	if !ok {
		return nil, domain.Err(401, "unauthenticated", "Sign in to continue")
	}
	code, expiry, err := a.App.CreateInvite(ctx, user.ID, req.ID)
	if err != nil {
		return nil, err
	}
	return &chatgen.CreateWorkspaceInviteResp{
		Code:      code,
		ExpiresAt: expiry.UTC().Format(time.RFC3339Nano),
		URL:       "/join/" + code,
	}, nil
}

func (a *Adapter) GetInvite(ctx context.Context, req *chatgen.GetInviteReq) (*chatgen.GetInviteResp, error) {
	ws, expiry, err := a.App.Invite(ctx, req.Code)
	if err != nil {
		return nil, err
	}
	return &chatgen.GetInviteResp{
		Workspace: &chatgen.InviteWorkspaceDTO{
			ID:   ws.ID,
			Name: ws.Name,
		},
		ExpiresAt: expiry.UTC().Format(time.RFC3339Nano),
	}, nil
}

func (a *Adapter) AcceptInvite(ctx context.Context, req *chatgen.AcceptInviteReq) (*chatgen.AcceptInviteResp, error) {
	user, ok := actorFromContext(ctx)
	if !ok {
		return nil, domain.Err(401, "unauthenticated", "Sign in to continue")
	}
	if user.Kind != "human" {
		return nil, domain.ErrHumanWorkspaceManagementRequired()
	}
	ws, cs, err := a.App.AcceptInvite(ctx, user.ID, req.Code)
	if err != nil {
		return nil, err
	}
	convs := make([]*chatgen.ConversationDTO, 0, len(cs))
	for _, c := range cs {
		convs = append(convs, toConversationDTO(c))
	}
	return &chatgen.AcceptInviteResp{
		Workspace:     toWorkspaceDTO(ws),
		Conversations: convs,
	}, nil
}

func (a *Adapter) RemoveWorkspaceMember(ctx context.Context, req *chatgen.RemoveWorkspaceMemberReq) (*chatgen.EmptyResp, error) {
	user, ok := actorFromContext(ctx)
	if !ok {
		return nil, domain.Err(401, "unauthenticated", "Sign in to continue")
	}
	if err := a.App.RemoveMember(ctx, user.ID, req.ID, req.UserId); err != nil {
		return nil, err
	}
	return &chatgen.EmptyResp{}, nil
}

func (a *Adapter) ListConversations(ctx context.Context, req *chatgen.ListConversationsReq) (*chatgen.ListConversationsResp, error) {
	user, ok := actorFromContext(ctx)
	if !ok {
		return nil, domain.Err(401, "unauthenticated", "Sign in to continue")
	}
	cs, err := a.App.ListConversations(ctx, user.ID, req.ID)
	if err != nil {
		return nil, err
	}
	convs := make([]*chatgen.ConversationDTO, 0, len(cs))
	for _, c := range cs {
		convs = append(convs, toConversationDTO(c))
	}
	return &chatgen.ListConversationsResp{Conversations: convs}, nil
}

func (a *Adapter) CreateConversation(ctx context.Context, req *chatgen.CreateConversationReq) (*chatgen.ConversationResp, error) {
	user, ok := actorFromContext(ctx)
	if !ok {
		return nil, domain.Err(401, "unauthenticated", "Sign in to continue")
	}
	c, err := a.App.CreateConversationWithKey(ctx, user.ID, req.ID, req.Type, req.GetName(), req.GetMemberIds(), req.GetIdempotencyKey())
	if err != nil {
		return nil, err
	}
	return &chatgen.ConversationResp{Conversation: toConversationDTO(c)}, nil
}

func (a *Adapter) AddConversationMember(ctx context.Context, req *chatgen.AddConversationMemberReq) (*chatgen.ConversationResp, error) {
	user, ok := actorFromContext(ctx)
	if !ok {
		return nil, domain.Err(401, "unauthenticated", "Sign in to continue")
	}
	c, err := a.App.AddConversationMemberWithKey(ctx, user.ID, req.ID, req.UserId, req.GetIdempotencyKey())
	if err != nil {
		return nil, err
	}
	return &chatgen.ConversationResp{Conversation: toConversationDTO(c)}, nil
}

func (a *Adapter) RemoveConversationMember(ctx context.Context, req *chatgen.RemoveConversationMemberReq) (*chatgen.ConversationResp, error) {
	user, ok := actorFromContext(ctx)
	if !ok {
		return nil, domain.Err(401, "unauthenticated", "Sign in to continue")
	}
	c, err := a.App.GroupMember(ctx, user.ID, req.ID, req.UserId, false)
	if err != nil {
		return nil, err
	}
	return &chatgen.ConversationResp{Conversation: toConversationDTO(c)}, nil
}

func (a *Adapter) MarkRead(ctx context.Context, req *chatgen.MarkReadReq) (*chatgen.MarkReadResp, error) {
	user, ok := actorFromContext(ctx)
	if !ok {
		return nil, domain.Err(401, "unauthenticated", "Sign in to continue")
	}
	seq, err := a.App.Read(ctx, user.ID, req.ID, req.Seq)
	if err != nil {
		return nil, err
	}
	return &chatgen.MarkReadResp{ReadSeq: seq}, nil
}

func (a *Adapter) SetNotificationLevel(ctx context.Context, req *chatgen.SetNotificationLevelReq) (*chatgen.NotificationLevelResp, error) {
	user, ok := actorFromContext(ctx)
	if !ok {
		return nil, domain.Err(401, "unauthenticated", "Sign in to continue")
	}
	level, err := a.App.SetNotificationLevel(ctx, user.ID, req.ID, req.Level)
	if err != nil {
		return nil, err
	}
	return &chatgen.NotificationLevelResp{
		ConversationId:    req.ID,
		NotificationLevel: level,
	}, nil
}
