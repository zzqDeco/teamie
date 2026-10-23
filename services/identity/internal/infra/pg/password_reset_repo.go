package pg

import (
	"context"
	"time"

	"teamie/pkg/postgres"
	"teamie/services/identity/internal/domain"
)

// --- PasswordResetRepository ---

func (r *Repository) RevokeActiveForUser(ctx context.Context, userID string) error {
	_, err := r.DB.Conn(ctx).Exec(ctx,
		"UPDATE identity_password_resets SET revoked_at=clock_timestamp() WHERE user_id=$1 AND used_at IS NULL AND revoked_at IS NULL",
		userID,
	)
	return err
}

func (r *Repository) IssuePasswordReset(ctx context.Context, id, userID, tokenHash string, createdBy *string) (domain.PasswordReset, error) {
	reset := domain.PasswordReset{ID: id}
	err := r.DB.Conn(ctx).QueryRow(ctx, `WITH issued AS (SELECT clock_timestamp() AS at)
		INSERT INTO identity_password_resets(id,user_id,token_hash,created_by,created_at,expires_at)
		SELECT $1,$2,$3,$4,at,at+interval '1 hour' FROM issued RETURNING created_at,expires_at`,
		id, userID, tokenHash, createdBy,
	).Scan(&reset.CreatedAt, &reset.ExpiresAt)
	return reset, err
}

func (r *Repository) InspectActive(ctx context.Context, tokenHash string) (userID, displayName, email string, expiresAt time.Time, found bool, err error) {
	err = r.DB.Conn(ctx).QueryRow(ctx, `SELECT u.id,u.display_name,u.email,r.expires_at
		FROM identity_password_resets r JOIN identity_users u ON u.id=r.user_id
		WHERE r.token_hash=$1 AND r.used_at IS NULL AND r.revoked_at IS NULL AND r.expires_at>clock_timestamp()
		AND u.kind='human' AND u.enabled AND u.deleted_at IS NULL`,
		tokenHash,
	).Scan(&userID, &displayName, &email, &expiresAt)
	if postgres.IsNoRows(err) {
		return "", "", "", time.Time{}, false, nil
	}
	if err != nil {
		return "", "", "", time.Time{}, false, err
	}
	return userID, displayName, email, expiresAt, true, nil
}

func (r *Repository) FindUserIDByTokenHash(ctx context.Context, tokenHash string) (string, bool, error) {
	var userID string
	err := r.DB.Conn(ctx).QueryRow(ctx, "SELECT user_id FROM identity_password_resets WHERE token_hash=$1", tokenHash).Scan(&userID)
	if postgres.IsNoRows(err) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return userID, true, nil
}

func (r *Repository) LockActiveForUpdate(ctx context.Context, tokenHash, userID string) (string, bool, error) {
	var resetID string
	err := r.DB.Conn(ctx).QueryRow(ctx, `SELECT id FROM identity_password_resets
		WHERE token_hash=$1 AND user_id=$2 AND used_at IS NULL AND revoked_at IS NULL
		AND expires_at>clock_timestamp() FOR UPDATE`,
		tokenHash, userID,
	).Scan(&resetID)
	if postgres.IsNoRows(err) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return resetID, true, nil
}

func (r *Repository) MarkPasswordResetUsed(ctx context.Context, resetID string) error {
	_, err := r.DB.Conn(ctx).Exec(ctx, "UPDATE identity_password_resets SET used_at=clock_timestamp() WHERE id=$1", resetID)
	return err
}
