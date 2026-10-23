package chat

import (
	"context"
	"time"
)

func (s *Store) CreateWorkspace(ctx context.Context, user, name string) (Workspace, Conversation, error) {
	ws, c, err := s.App(nil, "").CreateWorkspace(ctx, user, name)
	return ws, c, compatErr(err)
}

func (s *Store) Members(ctx context.Context, user, ws string) ([]Member, error) {
	ms, err := s.App(nil, "").Members(ctx, user, ws)
	return ms, compatErr(err)
}

func (s *Store) CreateInvite(ctx context.Context, user, ws string) (string, time.Time, error) {
	code, exp, err := s.App(nil, "").CreateInvite(ctx, user, ws)
	return code, exp, compatErr(err)
}

func (s *Store) Invite(ctx context.Context, code string) (Workspace, time.Time, error) {
	ws, exp, err := s.App(nil, "").Invite(ctx, code)
	return ws, exp, compatErr(err)
}

func (s *Store) AcceptInvite(ctx context.Context, user, code string) (Workspace, []Conversation, error) {
	ws, cs, err := s.App(nil, "").AcceptInvite(ctx, user, code)
	return ws, cs, compatErr(err)
}

func (s *Store) RemoveMember(ctx context.Context, user, ws, target string) error {
	return compatErr(s.App(nil, "").RemoveMember(ctx, user, ws, target))
}

func (s *Store) CreateConversation(ctx context.Context, user, ws, kind, name string, members []string) (Conversation, error) {
	c, err := s.App(nil, "").CreateConversation(ctx, user, ws, kind, name, members)
	return c, compatErr(err)
}
