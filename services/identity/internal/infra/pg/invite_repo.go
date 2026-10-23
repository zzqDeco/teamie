package pg

import (
	"context"

	"teamie/pkg/postgres"
	"teamie/services/identity/internal/domain"
)

// --- InviteRepository ---

func (r *Repository) FindValidInviteID(ctx context.Context, codeHash string, lockForUpdate bool) (string, bool, error) {
	query := "SELECT id FROM registration_invites WHERE code_hash=$1 AND used_at IS NULL AND revoked_at IS NULL AND expires_at>now()"
	if lockForUpdate {
		query += " FOR UPDATE"
	}
	var inviteID string
	err := r.DB.Conn(ctx).QueryRow(ctx, query, codeHash).Scan(&inviteID)
	if postgres.IsNoRows(err) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return inviteID, true, nil
}

func (r *Repository) MarkInviteUsed(ctx context.Context, inviteID, usedByUserID string) error {
	_, err := r.DB.Conn(ctx).Exec(ctx, "UPDATE registration_invites SET used_at=now(),used_by=$2 WHERE id=$1", inviteID, usedByUserID)
	return err
}

func (r *Repository) CreateInvite(ctx context.Context, id, codeHash, createdBy string) (domain.RegistrationInvite, error) {
	invite := domain.RegistrationInvite{ID: id}
	err := r.DB.Conn(ctx).QueryRow(ctx,
		"INSERT INTO registration_invites(id,code_hash,created_by,expires_at) VALUES($1,$2,$3,now()+interval '7 days') RETURNING created_at,expires_at",
		id, codeHash, createdBy,
	).Scan(&invite.CreatedAt, &invite.ExpiresAt)
	return invite, err
}

func (r *Repository) ListAllInvites(ctx context.Context) ([]domain.RegistrationInvite, error) {
	rows, err := r.DB.Conn(ctx).Query(ctx,
		"SELECT id,created_at,expires_at,used_at,revoked_at,used_by FROM registration_invites ORDER BY created_at DESC,id",
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.RegistrationInvite{}
	for rows.Next() {
		var invite domain.RegistrationInvite
		if err = rows.Scan(&invite.ID, &invite.CreatedAt, &invite.ExpiresAt, &invite.UsedAt, &invite.RevokedAt, &invite.UsedBy); err != nil {
			return nil, err
		}
		out = append(out, invite)
	}
	return out, rows.Err()
}

func (r *Repository) RevokeUnusedInvite(ctx context.Context, inviteID string) error {
	_, err := r.DB.Conn(ctx).Exec(ctx,
		"UPDATE registration_invites SET revoked_at=COALESCE(revoked_at,now()) WHERE id=$1 AND used_at IS NULL",
		inviteID,
	)
	return err
}
