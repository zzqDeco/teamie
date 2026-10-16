package identity

import (
	"context"
	"errors"
	"github.com/jackc/pgx/v5"
	"teamie/backend/internal/api"
	"time"
)

func (s *Store) ProvisionPrincipal(ctx context.Context, id, name, kind string, enabled, deleted bool, revision int64) (api.User, error) {
	var u api.User
	if !api.BoundedString(id, 1, 128) || !api.BoundedString(name, 1, 80) || kind != "agent" || revision < 1 || enabled && deleted {
		return u, api.Err(400, "invalid_principal", "Invalid principal lifecycle request")
	}
	tx, e := s.DB.Begin(ctx)
	if e != nil {
		return u, e
	}
	defer tx.Rollback(ctx)
	if _, e = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1,704105))", id); e != nil {
		return u, e
	}
	var oldRevision int64
	var oldEnabled bool
	var deletedAt *time.Time
	e = tx.QueryRow(ctx, "SELECT id,COALESCE(email,''),display_name,kind,platform_role,created_at,enabled,deleted_at,principal_revision FROM identity_users WHERE id=$1 FOR UPDATE", id).Scan(&u.ID, &u.Email, &u.DisplayName, &u.Kind, &u.PlatformRole, &u.CreatedAt, &oldEnabled, &deletedAt, &oldRevision)
	if e != nil && !errors.Is(e, pgx.ErrNoRows) {
		return u, e
	}
	if e == nil {
		if u.Kind != "agent" {
			return u, api.Err(409, "invalid_principal", "Human identities cannot be managed as machine principals")
		}
		if revision < oldRevision {
			return u, tx.Commit(ctx)
		}
		if revision == oldRevision {
			if u.DisplayName != name || oldEnabled != enabled || (deletedAt != nil) != deleted {
				return u, api.Err(409, "revision_conflict", "Principal revision conflicts with persisted state")
			}
			return u, tx.Commit(ctx)
		}
		if deletedAt != nil && !deleted {
			return u, api.Err(410, "principal_deleted", "Deleted principals cannot be reactivated")
		}
		_, e = tx.Exec(ctx, "UPDATE identity_users SET display_name=$2,enabled=$3,deleted_at=CASE WHEN $4 THEN COALESCE(deleted_at,now()) ELSE NULL END,principal_revision=$5 WHERE id=$1", id, name, enabled, deleted, revision)
	} else {
		u = api.User{ID: id, DisplayName: name, Kind: kind, PlatformRole: "user"}
		e = tx.QueryRow(ctx, "INSERT INTO identity_users(id,email,display_name,kind,password_hash,platform_role,enabled,deleted_at,principal_revision) VALUES($1,NULL,$2,$3,NULL,'user',$4,CASE WHEN $5 THEN now() ELSE NULL END,$6) RETURNING created_at", id, name, kind, enabled, deleted, revision).Scan(&u.CreatedAt)
	}
	if e != nil {
		return u, e
	}
	u.DisplayName = name
	if _, e = tx.Exec(ctx, "DELETE FROM principal_tokens WHERE principal_id=$1", id); e != nil {
		return u, e
	}
	return u, tx.Commit(ctx)
}
func (s *Store) IssuePrincipalToken(ctx context.Context, id string) (string, time.Time, error) {
	tx, e := s.DB.Begin(ctx)
	if e != nil {
		return "", time.Time{}, e
	}
	defer tx.Rollback(ctx)
	var enabled bool
	e = tx.QueryRow(ctx, "SELECT enabled AND deleted_at IS NULL FROM identity_users WHERE id=$1 AND kind='agent' FOR UPDATE", id).Scan(&enabled)
	if errors.Is(e, pgx.ErrNoRows) || e == nil && !enabled {
		return "", time.Time{}, api.Err(403, "principal_disabled", "Principal is unavailable or disabled")
	}
	if e != nil {
		return "", time.Time{}, e
	}
	token, expiry := api.Random(32), time.Now().UTC().Add(15*time.Minute)
	_, e = tx.Exec(ctx, "INSERT INTO principal_tokens(principal_id,token_hash,expires_at) VALUES($1,$2,$3) ON CONFLICT(principal_id) DO UPDATE SET token_hash=excluded.token_hash,expires_at=excluded.expires_at,created_at=now()", id, api.Hash(token), expiry)
	if e != nil {
		return "", expiry, e
	}
	return token, expiry, tx.Commit(ctx)
}
func (s *Store) ValidatePrincipal(ctx context.Context, token, key string) (api.Session, error) {
	var ss api.Session
	if !api.ValidServiceKey(s.RuntimeToken, key) || len(token) != 64 {
		return ss, api.Err(401, "unauthenticated", "Invalid participant credential")
	}
	e := s.DB.QueryRow(ctx, "SELECT u.id,COALESCE(u.email,''),u.display_name,u.kind,u.platform_role,u.created_at FROM principal_tokens t JOIN identity_users u ON u.id=t.principal_id WHERE t.token_hash=$1 AND t.expires_at>now() AND u.kind='agent' AND u.enabled AND u.deleted_at IS NULL", api.Hash(token)).Scan(&ss.User.ID, &ss.User.Email, &ss.User.DisplayName, &ss.User.Kind, &ss.User.PlatformRole, &ss.User.CreatedAt)
	if errors.Is(e, pgx.ErrNoRows) {
		e = api.Err(401, "unauthenticated", "Participant credential expired or revoked")
	}
	ss.ServicePrincipal = e == nil
	return ss, e
}
