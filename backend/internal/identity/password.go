package identity

import (
	"context"
	"teamie/backend/internal/api"
	"teamie/backend/internal/identity/domain"
)

func validatePassword(password string) error {
	return compatErr(domain.ValidatePassword(password))
}

func (s *Store) loginSession(ctx context.Context, u api.User, expectedHash string) (api.Session, string, error) {
	ss, token, err := s.App().LoginSession(ctx, toDomainUser(u), expectedHash)
	return toAPISession(ss), token, compatErr(err)
}

func (s *Store) ChangePassword(ctx context.Context, userID, sessionToken, currentPassword, newPassword string) error {
	return compatErr(s.App().ChangePassword(ctx, userID, sessionToken, currentPassword, newPassword))
}
