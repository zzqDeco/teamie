package app

import (
	"context"
	"teamie/backend/internal/identity/domain"
)

// BootstrapRequired reports whether the identity database has zero accounts and
// awaits the initial bootstrap administrator registration.
func (s *Service) BootstrapRequired(ctx context.Context) (bool, error) {
	exists, err := s.Users.AnyUserExists(ctx)
	return !exists, err
}

// RegistrationAdmission evaluates whether code admits the first platform admin
// or matches an active ordinary invitation (locking the invite row when lock=true).
func (s *Service) RegistrationAdmission(ctx context.Context, code string, lock bool) (role, inviteID string, err error) {
	initialized, err := s.Users.AnyUserExists(ctx)
	if err != nil {
		return "", "", err
	}
	role, isBootstrap, err := domain.EvaluateBootstrapAdmission(initialized, code, s.BootstrapCode())
	if err != nil || isBootstrap {
		return role, "", err
	}
	inviteID, found, err := s.Invites.FindValidInviteID(ctx, s.Tokens.HashToken(code), lock)
	if err != nil {
		return "", "", err
	}
	if !found {
		return "", "", domain.ErrInvalidInvite()
	}
	return role, inviteID, nil
}

// RequireAdmin verifies that userID is an active human platform administrator.
func (s *Service) RequireAdmin(ctx context.Context, userID string) error {
	role, found, err := s.Users.GetActiveHumanPlatformRole(ctx, userID)
	if err != nil {
		return err
	}
	if !found || role != "admin" {
		return domain.ErrAdminPermissionRequired()
	}
	return nil
}

// CreateRegistrationInvite issues a new single-use 7-day registration invitation.
func (s *Service) CreateRegistrationInvite(ctx context.Context, userID string) (domain.RegistrationInvite, string, error) {
	if err := s.RequireAdmin(ctx, userID); err != nil {
		return domain.RegistrationInvite{}, "", err
	}
	bootstrap := s.BootstrapCode()
	code := s.Tokens.RandomHex(32)
	for code == bootstrap {
		code = s.Tokens.RandomHex(32)
	}
	invite, err := s.Invites.CreateInvite(ctx, s.Tokens.NewID(), s.Tokens.HashToken(code), userID)
	return invite, code, err
}

// RegistrationInvites lists all registration invitations for an administrator.
func (s *Service) RegistrationInvites(ctx context.Context, userID string) ([]domain.RegistrationInvite, error) {
	if err := s.RequireAdmin(ctx, userID); err != nil {
		return nil, err
	}
	return s.Invites.ListAllInvites(ctx)
}

// RevokeRegistrationInvite revokes an unused registration invitation.
func (s *Service) RevokeRegistrationInvite(ctx context.Context, userID, inviteID string) error {
	if err := s.RequireAdmin(ctx, userID); err != nil {
		return err
	}
	return s.Invites.RevokeUnusedInvite(ctx, inviteID)
}
