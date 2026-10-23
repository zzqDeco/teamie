package app

import (
	"context"

	"teamie/services/identity/internal/domain"
)

// RegisterApp is an alias for Service focusing on invitation-gated registration use cases.
type RegisterApp = Service

// Register validates admission via bootstrap or single-use invite, hashes the
// password outside the registration lock, then atomically creates the user,
// session, and invite redemption inside a ReadCommitted transaction.
func (s *Service) Register(ctx context.Context, email, password, name, code string) (domain.Session, string, error) {
	email, err := domain.NormalizeEmail(email)
	if err != nil {
		return domain.Session{}, "", err
	}
	name, err = domain.ValidateDisplayName(name)
	if err != nil {
		return domain.Session{}, "", err
	}
	if err = domain.ValidatePassword(password); err != nil {
		return domain.Session{}, "", err
	}
	// Cheap eligibility check avoids hashing requests with unusable codes. The
	// transaction repeats it under the registration lock before changing state.
	if _, _, err = s.RegistrationAdmission(ctx, code, false); err != nil {
		return domain.Session{}, "", err
	}
	hash, err := s.Passwords.Hash(password)
	if err != nil {
		return domain.Session{}, "", err
	}
	var session domain.Session
	var token string
	err = s.Tx.WithinReadCommittedTx(ctx, func(txCtx context.Context) error {
		if lockErr := s.Tx.LockRegistration(txCtx); lockErr != nil {
			return lockErr
		}
		role, inviteID, admErr := s.RegistrationAdmission(txCtx, code, true)
		if admErr != nil {
			return admErr
		}
		u := domain.User{
			ID:           s.Tokens.NewID(),
			Email:        email,
			DisplayName:  name,
			Kind:         "human",
			PlatformRole: role,
		}
		createdAt, createErr := s.Users.CreateHuman(txCtx, u, hash)
		if createErr != nil {
			return createErr
		}
		u.CreatedAt = createdAt
		ss, tok, sessErr := s.IssueSession(txCtx, u)
		if sessErr != nil {
			return sessErr
		}
		if inviteID != "" {
			if useErr := s.Invites.MarkInviteUsed(txCtx, inviteID, u.ID); useErr != nil {
				return useErr
			}
		}
		session = ss
		token = tok
		return nil
	})
	return session, token, err
}
