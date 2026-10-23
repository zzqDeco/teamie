package identity

import (
	"context"
	"teamie/backend/internal/identity/domain"
)

const DefaultBootstrapInviteCode = domain.DefaultBootstrapInviteCode

type RegistrationInvite = domain.RegistrationInvite

func (s *Store) BootstrapRequired(ctx context.Context) (bool, error) {
	req, err := s.App().BootstrapRequired(ctx)
	return req, compatErr(err)
}

func (s *Store) requireAdmin(ctx context.Context, user string) error {
	return compatErr(s.App().RequireAdmin(ctx, user))
}

func (s *Store) CreateRegistrationInvite(ctx context.Context, user string) (RegistrationInvite, string, error) {
	inv, code, err := s.App().CreateRegistrationInvite(ctx, user)
	return inv, code, compatErr(err)
}

func (s *Store) RegistrationInvites(ctx context.Context, user string) ([]RegistrationInvite, error) {
	invs, err := s.App().RegistrationInvites(ctx, user)
	return invs, compatErr(err)
}

func (s *Store) RevokeRegistrationInvite(ctx context.Context, user, id string) error {
	return compatErr(s.App().RevokeRegistrationInvite(ctx, user, id))
}
