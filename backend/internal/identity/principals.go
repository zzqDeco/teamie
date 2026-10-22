package identity

import (
	"context"
	"time"

	"teamie/backend/internal/api"
)

func (s *Store) ProvisionPrincipal(ctx context.Context, id, name, kind string, enabled, deleted bool, revision int64) (api.User, error) {
	u, err := s.App().ProvisionPrincipal(ctx, id, name, kind, enabled, deleted, revision)
	return toAPIUser(u), compatErr(err)
}

func (s *Store) IssuePrincipalToken(ctx context.Context, id string) (string, time.Time, error) {
	token, expiry, err := s.App().IssuePrincipalToken(ctx, id)
	return token, expiry, compatErr(err)
}

func (s *Store) ValidatePrincipal(ctx context.Context, token, key string) (api.Session, error) {
	ss, err := s.App().ValidatePrincipal(ctx, token, key)
	return toAPISession(ss), compatErr(err)
}
