package identity

import (
	"context"
	"crypto/subtle"
	"errors"
	"github.com/jackc/pgx/v5"
	"teamie/backend/internal/api"
	"time"
)

const DefaultBootstrapInviteCode = "teamie-bootstrap"

type rowQuerier interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func invalidInvite() error {
	return api.Err(403, "invalid_invite", "This registration invitation is invalid, expired, revoked, or already used")
}
func (s *Store) BootstrapRequired(ctx context.Context) (bool, error) {
	var required bool
	e := s.DB.QueryRow(ctx, "SELECT NOT EXISTS(SELECT 1 FROM identity_users)").Scan(&required)
	return required, e
}
func (s *Store) registrationAdmission(ctx context.Context, q rowQuerier, code string, lock bool) (role, inviteID string, err error) {
	var initialized bool
	if err = q.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM identity_users)").Scan(&initialized); err != nil {
		return
	}
	bootstrap := subtle.ConstantTimeCompare([]byte(code), []byte(s.BootstrapInviteCode)) == 1
	if !initialized {
		if !bootstrap {
			return "", "", invalidInvite()
		}
		return "admin", "", nil
	}
	if bootstrap || len(code) != 64 {
		return "", "", invalidInvite()
	}
	query := "SELECT id FROM registration_invites WHERE code_hash=$1 AND used_at IS NULL AND revoked_at IS NULL AND expires_at>now()"
	if lock {
		query += " FOR UPDATE"
	}
	if err = q.QueryRow(ctx, query, api.Hash(code)).Scan(&inviteID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			err = invalidInvite()
		}
		return "", "", err
	}
	return "user", inviteID, nil
}
func (s *Store) requireAdmin(ctx context.Context, user string) error {
	var role string
	e := s.DB.QueryRow(ctx, "SELECT platform_role FROM identity_users WHERE id=$1", user).Scan(&role)
	if errors.Is(e, pgx.ErrNoRows) || e == nil && role != "admin" {
		return api.Err(403, "forbidden", "Platform administrator permission is required")
	}
	return e
}

type RegistrationInvite struct {
	ID        string     `json:"id"`
	CreatedAt time.Time  `json:"createdAt"`
	ExpiresAt time.Time  `json:"expiresAt"`
	UsedAt    *time.Time `json:"usedAt,omitempty"`
	RevokedAt *time.Time `json:"revokedAt,omitempty"`
	UsedBy    *string    `json:"usedBy,omitempty"`
}

func (s *Store) CreateRegistrationInvite(ctx context.Context, user string) (RegistrationInvite, string, error) {
	var invite RegistrationInvite
	if e := s.requireAdmin(ctx, user); e != nil {
		return invite, "", e
	}
	code := api.Random(32)
	for code == s.BootstrapInviteCode {
		code = api.Random(32)
	}
	invite.ID = api.ID()
	e := s.DB.QueryRow(ctx, "INSERT INTO registration_invites(id,code_hash,created_by,expires_at) VALUES($1,$2,$3,now()+interval '7 days') RETURNING created_at,expires_at", invite.ID, api.Hash(code), user).Scan(&invite.CreatedAt, &invite.ExpiresAt)
	return invite, code, e
}
func (s *Store) RegistrationInvites(ctx context.Context, user string) ([]RegistrationInvite, error) {
	if e := s.requireAdmin(ctx, user); e != nil {
		return nil, e
	}
	rows, e := s.DB.Query(ctx, "SELECT id,created_at,expires_at,used_at,revoked_at,used_by FROM registration_invites ORDER BY created_at DESC,id")
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []RegistrationInvite{}
	for rows.Next() {
		var invite RegistrationInvite
		if e = rows.Scan(&invite.ID, &invite.CreatedAt, &invite.ExpiresAt, &invite.UsedAt, &invite.RevokedAt, &invite.UsedBy); e != nil {
			return nil, e
		}
		out = append(out, invite)
	}
	return out, rows.Err()
}
func (s *Store) RevokeRegistrationInvite(ctx context.Context, user, id string) error {
	if e := s.requireAdmin(ctx, user); e != nil {
		return e
	}
	_, e := s.DB.Exec(ctx, "UPDATE registration_invites SET revoked_at=COALESCE(revoked_at,now()) WHERE id=$1 AND used_at IS NULL", id)
	return e
}
