package identity

import (
	"context"
	"teamie/backend/internal/api"
	"teamie/backend/internal/identity/domain"
)

type PasswordReset = domain.PasswordReset
type PasswordResetAccount = domain.PasswordResetAccount
type PasswordResetInspection = domain.PasswordResetInspection

type AdminUserPage struct {
	Users  []api.User `json:"users"`
	Cursor string     `json:"cursor,omitempty"`
}

func (s *Store) AdminUsers(ctx context.Context, adminID, sessionToken, cursor string, limit int, query string) (AdminUserPage, error) {
	page, err := s.App().AdminUsers(ctx, adminID, sessionToken, cursor, limit, query)
	if err != nil {
		return AdminUserPage{Users: []api.User{}}, compatErr(err)
	}
	users := make([]api.User, 0, len(page.Users))
	for _, u := range page.Users {
		users = append(users, toAPIUser(u))
	}
	return AdminUserPage{Users: users, Cursor: page.Cursor}, nil
}

func (s *Store) CreatePasswordResetLink(ctx context.Context, adminID, sessionToken, targetID string) (PasswordReset, string, error) {
	reset, token, err := s.App().CreatePasswordResetLink(ctx, adminID, sessionToken, targetID)
	return reset, token, compatErr(err)
}

func (s *Store) OperatorPasswordResetLink(ctx context.Context, email string) (PasswordReset, string, error) {
	reset, token, err := s.App().OperatorPasswordResetLink(ctx, email)
	return reset, token, compatErr(err)
}

func (s *Store) InspectPasswordReset(ctx context.Context, token string) (PasswordResetInspection, error) {
	res, err := s.App().InspectPasswordReset(ctx, token)
	return res, compatErr(err)
}

func (s *Store) ResetPassword(ctx context.Context, token, newPassword string) error {
	return compatErr(s.App().ResetPassword(ctx, token, newPassword))
}
