package chat

import (
	"context"
	"teamie/backend/internal/api"
	"teamie/backend/pkg/imtext"
)

func (s *Store) Message(ctx context.Context, user, conversation, id string) (Message, error) {
	m, err := s.App(nil, "").Message(ctx, user, conversation, id)
	return fromDomainMessage(m), compatErr(err)
}

func (s *Store) Messages(ctx context.Context, user, conversation string, before int64, limit int) ([]Message, bool, error) {
	return s.MessageHistory(ctx, user, conversation, before, 0, "", limit)
}

func (s *Store) MessageHistory(ctx context.Context, user, conversation string, before, around int64, query string, limit int) ([]Message, bool, error) {
	ms, more, err := s.App(nil, "").MessageHistory(ctx, user, conversation, before, around, query, limit)
	if err != nil {
		return nil, false, compatErr(err)
	}
	return fromDomainMessages(ms), more, nil
}

func (s *Store) FileHistory(ctx context.Context, user, conversation string, before int64, query string, limit int) ([]Message, bool, error) {
	ms, more, err := s.App(nil, "").FileHistory(ctx, user, conversation, before, query, limit)
	if err != nil {
		return nil, false, compatErr(err)
	}
	return fromDomainMessages(ms), more, nil
}

func (s *Store) Send(ctx context.Context, user api.User, conversation, clientID, body string, reply *string) (Message, bool, error) {
	return s.SendWithAttachments(ctx, user, conversation, clientID, body, reply, nil)
}

func (s *Store) SendWithAttachments(ctx context.Context, user api.User, conversation, clientID, body string, reply *string, ids []string) (Message, bool, error) {
	return s.SendAnnotated(ctx, user, conversation, clientID, body, reply, ids, nil)
}

func (s *Store) SendAnnotated(ctx context.Context, user api.User, conversation, clientID, body string, reply *string, ids []string, mentions []imtext.Mention) (Message, bool, error) {
	m, created, err := s.App(nil, "").SendAnnotated(ctx, toDomainUser(user), conversation, clientID, body, reply, ids, toDomainMentions(mentions))
	return fromDomainMessage(m), created, compatErr(err)
}

func (s *Store) ChangeMessage(ctx context.Context, user, conversation, id, body string, retract bool) (Message, error) {
	return s.ChangeAnnotatedMessage(ctx, user, conversation, id, body, retract, nil)
}

func (s *Store) ChangeAnnotatedMessage(ctx context.Context, user, conversation, id, body string, retract bool, mentions []imtext.Mention) (Message, error) {
	m, err := s.App(nil, "").ChangeAnnotatedMessage(ctx, user, conversation, id, body, retract, toDomainMentions(mentions))
	return fromDomainMessage(m), compatErr(err)
}

func (s *Store) Read(ctx context.Context, user, conversation string, seq int64) (int64, error) {
	n, err := s.App(nil, "").Read(ctx, user, conversation, seq)
	return n, compatErr(err)
}
