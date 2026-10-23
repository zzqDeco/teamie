package chat

import (
	"context"
	"teamie/backend/internal/api"
)

func (s *Store) ParticipantWorkspaces(ctx context.Context, user string) ([]string, error) {
	ids, err := s.App(nil, "").ParticipantWorkspaces(ctx, user)
	return ids, compatErr(err)
}

func (s *Store) WorkspaceParticipants(ctx context.Context, workspace, requester string) ([]string, error) {
	ids, err := s.App(nil, "").WorkspaceParticipants(ctx, workspace, requester)
	return ids, compatErr(err)
}

func (s *Store) SetPrincipalWorkspaces(ctx context.Context, principal, grantor api.User, requested []string, revision int64) ([]string, error) {
	ids, err := s.App(nil, "").SetPrincipalWorkspaces(ctx, toDomainUser(principal), toDomainUser(grantor), requested, revision)
	return ids, compatErr(err)
}

func (s *Store) GroupMember(ctx context.Context, actor, conversation, target string, add bool) (Conversation, error) {
	c, err := s.App(nil, "").GroupMember(ctx, actor, conversation, target, add)
	return c, compatErr(err)
}
