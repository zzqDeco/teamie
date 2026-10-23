package app

import (
	"context"
	"strings"
	"time"

	"teamie/services/identity/internal/domain"
)

// AuthApp is an alias for Service focusing on login, session validation, and password lifecycle.
type AuthApp = Service

// IssueSession creates a new 7-day browser session inside the caller's transaction
// while opportunistically retiring up to 1000 expired rows with SKIP LOCKED.
func (s *Service) IssueSession(ctx context.Context, u domain.User) (domain.Session, string, error) {
	token := s.Tokens.RandomHex(32)
	csrf := s.Tokens.RandomHex(32)
	err := s.Sessions.RetireExpiredBatchAndInsert(ctx, u.ID, s.Tokens.HashToken(token), csrf, time.Now().Add(domain.HumanSessionTTL))
	return domain.Session{User: u, CSRFToken: csrf}, token, err
}

// Login authenticates a human account with constant-time dummy hash fallback and
// creates a fresh session under a shared user row lock.
func (s *Service) Login(ctx context.Context, email, password string) (domain.Session, string, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	u, hash, found, err := s.Users.FindHumanForLogin(ctx, email)
	if err != nil {
		return domain.Session{}, "", err
	}
	if !found {
		hash = s.Passwords.DummyHash()
	}
	if len(password) > 72 || s.Passwords.Compare(hash, password) != nil || u.ID == "" {
		return domain.Session{}, "", domain.ErrInvalidCredentials()
	}
	return s.LoginSession(ctx, u, hash)
}

// LoginSession locks the human row FOR SHARE to verify the password hash was not
// concurrently rotated before creating the session.
func (s *Service) LoginSession(ctx context.Context, u domain.User, expectedHash string) (domain.Session, string, error) {
	var session domain.Session
	var token string
	err := s.Tx.WithinTx(ctx, func(txCtx context.Context) error {
		currentHash, found, lockErr := s.Users.LockHumanForShare(txCtx, u.ID)
		if lockErr != nil {
			return lockErr
		}
		if !found || currentHash != expectedHash {
			return domain.ErrInvalidCredentials()
		}
		ss, tok, sessErr := s.IssueSession(txCtx, u)
		if sessErr != nil {
			return sessErr
		}
		session = ss
		token = tok
		return nil
	})
	return session, token, err
}

// Validate verifies a 64-hex-character browser session token.
func (s *Service) Validate(ctx context.Context, token string) (domain.Session, error) {
	if len(token) != 64 {
		return domain.Session{}, domain.ErrUnauthenticatedSignIn()
	}
	ss, found, err := s.Sessions.FindActiveHumanSession(ctx, s.Tokens.HashToken(token))
	if err != nil {
		return domain.Session{}, err
	}
	if !found {
		return domain.Session{}, domain.ErrSessionExpiredOrRevoked()
	}
	return ss, nil
}

// Logout revokes a single browser session token.
func (s *Service) Logout(ctx context.Context, token string) error {
	return s.Sessions.DeleteByTokenHash(ctx, s.Tokens.HashToken(token))
}

// ChangePassword rotates a human account's password, revoking all sessions and
// active password reset links under the user-before-session lock order.
func (s *Service) ChangePassword(ctx context.Context, userID, sessionToken, currentPassword, newPassword string) error {
	if len(sessionToken) != 64 {
		return domain.ErrUnauthenticatedSignIn()
	}
	return s.Tx.WithinTx(ctx, func(txCtx context.Context) error {
		hash, found, err := s.Users.LockHumanForUpdate(txCtx, userID)
		if err != nil {
			return err
		}
		if !found {
			return domain.ErrSessionExpiredOrRevoked()
		}
		if err = s.validHumanSession(txCtx, userID, sessionToken); err != nil {
			return err
		}
		if err = domain.ValidatePassword(newPassword); err != nil {
			return err
		}
		if len(currentPassword) > 72 || s.Passwords.Compare(hash, currentPassword) != nil {
			return domain.ErrInvalidCurrentPassword()
		}
		if currentPassword == newPassword || s.Passwords.Compare(hash, newPassword) == nil {
			return domain.ErrPasswordUnchanged()
		}
		nextHash, err := s.Passwords.Hash(newPassword)
		if err != nil {
			return err
		}
		if err = s.Users.UpdatePasswordHash(txCtx, userID, nextHash); err != nil {
			return err
		}
		if err = s.Sessions.DeleteAllForUser(txCtx, userID); err != nil {
			return err
		}
		return s.PasswordResets.RevokeActiveForUser(txCtx, userID)
	})
}

func (s *Service) validHumanSession(ctx context.Context, userID, sessionToken string) error {
	if len(sessionToken) != 64 {
		return domain.ErrUnauthenticatedSignIn()
	}
	valid, err := s.Sessions.LockActiveHumanSessionForShare(ctx, userID, s.Tokens.HashToken(sessionToken))
	if err != nil {
		return err
	}
	if !valid {
		return domain.ErrSessionExpiredOrRevoked()
	}
	return nil
}
