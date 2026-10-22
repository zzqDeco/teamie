package app

import (
	"context"
	"teamie/backend/internal/identity/domain"
)

func (s *Service) lockResetAdmin(ctx context.Context, adminID, sessionToken, targetID string) error {
	adminFound, adminEnabled, adminKind, adminRole, targetFound, err := s.Users.LockUsersForAdminReset(ctx, adminID, targetID)
	if err != nil {
		return err
	}
	if !adminFound || !adminEnabled {
		return domain.ErrSessionExpiredOrRevoked()
	}
	if adminKind != "human" || adminRole != "admin" {
		return domain.ErrAdminPermissionRequired()
	}
	if err = s.validHumanSession(ctx, adminID, sessionToken); err != nil {
		return err
	}
	if !targetFound {
		return domain.ErrUserNotFound()
	}
	return nil
}

func (s *Service) requirePasswordResetEnabled() error {
	if s.Flags == nil || !s.Flags.PasswordResetEnabled() {
		return domain.ErrPasswordResetDisabled()
	}
	return nil
}

func (s *Service) issuePasswordReset(ctx context.Context, userID string, createdBy *string) (domain.PasswordReset, string, error) {
	if err := s.PasswordResets.RevokeActiveForUser(ctx, userID); err != nil {
		return domain.PasswordReset{}, "", err
	}
	token := s.Tokens.RandomHex(32)
	reset, err := s.PasswordResets.IssuePasswordReset(ctx, s.Tokens.NewID(), userID, s.Tokens.HashToken(token), createdBy)
	return reset, token, err
}

// AdminUsers returns a paginated list of active human accounts for an administrator.
func (s *Service) AdminUsers(ctx context.Context, adminID, sessionToken, cursor string, limit int, query string) (domain.AdminUserPage, error) {
	page := domain.AdminUserPage{Users: []domain.User{}}
	afterID, normLimit, normQuery, pattern, err := domain.ValidateAdminUsersQuery(cursor, limit, query)
	if err != nil {
		return page, err
	}
	err = s.Tx.WithinTx(ctx, func(txCtx context.Context) error {
		if lockErr := s.lockResetAdmin(txCtx, adminID, sessionToken, adminID); lockErr != nil {
			return lockErr
		}
		users, listErr := s.Users.ListAdminHumans(txCtx, afterID, normQuery, pattern, normLimit+1)
		if listErr != nil {
			return listErr
		}
		if len(users) > normLimit {
			page.Users = users[:normLimit]
			page.Cursor = domain.EncodeUsersCursor(page.Users[normLimit-1].ID)
		} else {
			page.Users = users
		}
		return nil
	})
	return page, err
}

// CreatePasswordResetLink issues a single-use 1-hour password reset link on behalf of an administrator.
func (s *Service) CreatePasswordResetLink(ctx context.Context, adminID, sessionToken, targetID string) (domain.PasswordReset, string, error) {
	var reset domain.PasswordReset
	var token string
	err := s.Tx.WithinTx(ctx, func(txCtx context.Context) error {
		if lockErr := s.lockResetAdmin(txCtx, adminID, sessionToken, targetID); lockErr != nil {
			return lockErr
		}
		if flagErr := s.requirePasswordResetEnabled(); flagErr != nil {
			return flagErr
		}
		r, tok, issueErr := s.issuePasswordReset(txCtx, targetID, &adminID)
		if issueErr != nil {
			return issueErr
		}
		reset = r
		token = tok
		return nil
	})
	return reset, token, err
}

// OperatorPasswordResetLink issues a password reset link from the server-side identityctl CLI.
func (s *Service) OperatorPasswordResetLink(ctx context.Context, email string) (domain.PasswordReset, string, error) {
	var reset domain.PasswordReset
	email, err := domain.NormalizeEmail(email)
	if err != nil {
		return reset, "", err
	}
	if err = s.requirePasswordResetEnabled(); err != nil {
		return reset, "", err
	}
	var token string
	err = s.Tx.WithinTx(ctx, func(txCtx context.Context) error {
		userID, found, lockErr := s.Users.LockHumanByEmailForUpdate(txCtx, email)
		if lockErr != nil {
			return lockErr
		}
		if !found {
			return domain.ErrUserNotFound()
		}
		if flagErr := s.requirePasswordResetEnabled(); flagErr != nil {
			return flagErr
		}
		r, tok, issueErr := s.issuePasswordReset(txCtx, userID, nil)
		if issueErr != nil {
			return issueErr
		}
		reset = r
		token = tok
		return nil
	})
	return reset, token, err
}

// InspectPasswordReset returns masked account metadata for an active reset token without consuming it.
func (s *Service) InspectPasswordReset(ctx context.Context, token string) (domain.PasswordResetInspection, error) {
	var result domain.PasswordResetInspection
	if err := domain.ValidateResetTokenFormat(token); err != nil {
		return result, err
	}
	id, name, email, expiresAt, found, err := s.PasswordResets.InspectActive(ctx, s.Tokens.HashToken(token))
	if err != nil {
		return result, err
	}
	if !found {
		return result, domain.ErrInvalidResetLink()
	}
	return domain.PasswordResetInspection{
		Account: domain.PasswordResetAccount{
			ID:          id,
			DisplayName: name,
			EmailMasked: domain.MaskEmail(email),
		},
		ExpiresAt: expiresAt,
	}, nil
}

// ResetPassword redeems a password reset link, rotates the user's password, and revokes all active sessions.
func (s *Service) ResetPassword(ctx context.Context, token, newPassword string) error {
	if err := domain.ValidateResetTokenFormat(token); err != nil {
		return err
	}
	hash := s.Tokens.HashToken(token)
	userID, found, err := s.PasswordResets.FindUserIDByTokenHash(ctx, hash)
	if err != nil {
		return err
	}
	if !found {
		return domain.ErrInvalidResetLink()
	}
	return s.Tx.WithinTx(ctx, func(txCtx context.Context) error {
		currentHash, userFound, lockErr := s.Users.LockHumanForUpdate(txCtx, userID)
		if lockErr != nil {
			return lockErr
		}
		if !userFound {
			return domain.ErrInvalidResetLink()
		}
		resetID, resetFound, resetErr := s.PasswordResets.LockActiveForUpdate(txCtx, hash, userID)
		if resetErr != nil {
			return resetErr
		}
		if !resetFound {
			return domain.ErrInvalidResetLink()
		}
		if err := domain.ValidatePassword(newPassword); err != nil {
			return err
		}
		if s.Passwords.Compare(currentHash, newPassword) == nil {
			return domain.ErrPasswordUnchanged()
		}
		nextHash, hashErr := s.Passwords.Hash(newPassword)
		if hashErr != nil {
			return hashErr
		}
		if err := s.Users.UpdatePasswordHash(txCtx, userID, nextHash); err != nil {
			return err
		}
		if err := s.PasswordResets.MarkPasswordResetUsed(txCtx, resetID); err != nil {
			return err
		}
		return s.Sessions.DeleteAllForUser(txCtx, userID)
	})
}
