package app

import (
	"context"
	"time"

	"teamie/backend/internal/identity/domain"
)

// ProvisionPrincipal creates or updates a machine principal at a monotonically
// increasing revision and invalidates any previously issued principal tokens.
func (s *Service) ProvisionPrincipal(ctx context.Context, id, name, kind string, enabled, deleted bool, revision int64) (domain.User, error) {
	var u domain.User
	if err := domain.ValidatePrincipalLifecycle(id, name, kind, enabled, deleted, revision); err != nil {
		return u, err
	}
	err := s.Tx.WithinTx(ctx, func(txCtx context.Context) error {
		if lockErr := s.Tx.LockPrincipal(txCtx, id); lockErr != nil {
			return lockErr
		}
		existing, oldEnabled, deletedAt, oldRevision, found, findErr := s.Principals.LockPrincipalForUpdate(txCtx, id)
		if findErr != nil {
			return findErr
		}
		if found {
			u = existing
			if u.Kind != "agent" {
				return domain.ErrHumanNotMachinePrincipal()
			}
			if revision < oldRevision {
				return nil
			}
			if revision == oldRevision {
				if u.DisplayName != name || oldEnabled != enabled || (deletedAt != nil) != deleted {
					return domain.ErrPrincipalRevisionConflict()
				}
				return nil
			}
			if deletedAt != nil && !deleted {
				return domain.ErrPrincipalDeleted()
			}
			if err := s.Principals.UpdateAgentPrincipal(txCtx, id, name, enabled, deleted, revision); err != nil {
				return err
			}
		} else {
			createdAt, err := s.Principals.CreateAgentPrincipal(txCtx, id, name, kind, enabled, deleted, revision)
			if err != nil {
				return err
			}
			u = domain.User{
				ID:           id,
				DisplayName:  name,
				Kind:         kind,
				PlatformRole: "user",
				CreatedAt:    createdAt,
			}
		}
		u.DisplayName = name
		return s.Principals.DeletePrincipalTokens(txCtx, id)
	})
	return u, err
}

// IssuePrincipalToken issues a short-lived 15-minute bearer token for an active machine principal.
func (s *Service) IssuePrincipalToken(ctx context.Context, id string) (string, time.Time, error) {
	var token string
	var expiry time.Time
	err := s.Tx.WithinTx(ctx, func(txCtx context.Context) error {
		enabled, found, lockErr := s.Principals.LockAgentEnabled(txCtx, id)
		if lockErr != nil {
			return lockErr
		}
		if !found || !enabled {
			return domain.ErrPrincipalDisabled()
		}
		token = s.Tokens.RandomHex(32)
		expiry = time.Now().UTC().Add(15 * time.Minute)
		return s.Principals.UpsertPrincipalToken(txCtx, id, s.Tokens.HashToken(token), expiry)
	})
	return token, expiry, err
}

// ValidatePrincipal authenticates a machine principal bearer token and runtime service key.
func (s *Service) ValidatePrincipal(ctx context.Context, token, key string) (domain.Session, error) {
	if !domain.ValidServiceKey(s.RuntimeToken, key) || len(token) != 64 {
		return domain.Session{}, domain.ErrInvalidParticipantCredential()
	}
	u, found, err := s.Principals.FindActiveAgentByTokenHash(ctx, s.Tokens.HashToken(token))
	if err != nil {
		return domain.Session{}, err
	}
	if !found {
		return domain.Session{}, domain.ErrParticipantCredentialExpired()
	}
	return domain.Session{User: u, ServicePrincipal: true}, nil
}
