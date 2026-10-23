package pg

import (
	"context"
	"time"

	"teamie/pkg/postgres"
	"teamie/services/identity/internal/domain"
)

// --- SessionRepository ---

func (r *Repository) RetireExpiredBatchAndInsert(ctx context.Context, userID, tokenHash, csrfToken string, expiresAt time.Time) error {
	conn := r.DB.Conn(ctx)
	if _, err := conn.Exec(ctx, `DELETE FROM identity_sessions WHERE token_hash IN (
		SELECT token_hash FROM identity_sessions WHERE expires_at<=now()
		ORDER BY expires_at,token_hash LIMIT 1000 FOR UPDATE SKIP LOCKED)`); err != nil {
		return err
	}
	_, err := conn.Exec(ctx,
		"INSERT INTO identity_sessions(token_hash,user_id,csrf_token,expires_at) VALUES($1,$2,$3,$4)",
		tokenHash, userID, csrfToken, expiresAt,
	)
	return err
}

func (r *Repository) FindActiveHumanSession(ctx context.Context, tokenHash string) (domain.Session, bool, error) {
	var ss domain.Session
	err := r.DB.Conn(ctx).QueryRow(ctx, `SELECT u.id,u.email,u.display_name,u.kind,u.platform_role,u.created_at,s.csrf_token
		FROM identity_sessions s JOIN identity_users u ON u.id=s.user_id
		WHERE s.token_hash=$1 AND s.expires_at>now() AND u.kind='human' AND u.enabled AND u.deleted_at IS NULL`,
		tokenHash,
	).Scan(&ss.User.ID, &ss.User.Email, &ss.User.DisplayName, &ss.User.Kind, &ss.User.PlatformRole, &ss.User.CreatedAt, &ss.CSRFToken)
	if postgres.IsNoRows(err) {
		return domain.Session{}, false, nil
	}
	if err != nil {
		return domain.Session{}, false, err
	}
	return ss, true, nil
}

func (r *Repository) LockActiveHumanSessionForShare(ctx context.Context, userID, tokenHash string) (bool, error) {
	var valid bool
	err := r.DB.Conn(ctx).QueryRow(ctx, `SELECT true FROM identity_sessions
		WHERE user_id=$1 AND token_hash=$2 AND expires_at>clock_timestamp() FOR SHARE`,
		userID, tokenHash,
	).Scan(&valid)
	if postgres.IsNoRows(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return valid, nil
}

func (r *Repository) DeleteByTokenHash(ctx context.Context, tokenHash string) error {
	_, err := r.DB.Conn(ctx).Exec(ctx, "DELETE FROM identity_sessions WHERE token_hash=$1", tokenHash)
	return err
}

func (r *Repository) DeleteAllForUser(ctx context.Context, userID string) error {
	_, err := r.DB.Conn(ctx).Exec(ctx, "DELETE FROM identity_sessions WHERE user_id=$1", userID)
	return err
}
