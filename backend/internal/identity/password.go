package identity

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"golang.org/x/crypto/bcrypt"
	"teamie/backend/internal/api"
)

func validatePassword(password string) error {
	if len([]rune(password)) < 12 || len(password) > 72 {
		return api.Err(400, "invalid_password", "Password must contain at least 12 characters and at most 72 bytes")
	}
	return nil
}

// Password changes and session creation lock the same user row. A login that
// checked an old password before a change cannot create a session afterwards.
func (s *Store) loginSession(ctx context.Context, u api.User, expectedHash string) (api.Session, string, error) {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return api.Session{}, "", err
	}
	defer tx.Rollback(ctx)
	var currentHash string
	err = tx.QueryRow(ctx, "SELECT password_hash FROM identity_users WHERE id=$1 AND kind='human' AND enabled AND deleted_at IS NULL FOR SHARE", u.ID).Scan(&currentHash)
	if errors.Is(err, pgx.ErrNoRows) || err == nil && currentHash != expectedHash {
		return api.Session{}, "", api.Err(401, "invalid_credentials", "Email or password is incorrect")
	}
	if err != nil {
		return api.Session{}, "", err
	}
	ss, token, err := s.newSession(ctx, tx, u)
	if err == nil {
		err = tx.Commit(ctx)
	}
	return ss, token, err
}

func (s *Store) ChangePassword(ctx context.Context, userID, sessionToken, currentPassword, newPassword string) error {
	if err := validatePassword(newPassword); err != nil {
		return err
	}
	if len(sessionToken) != 64 {
		return api.Err(401, "unauthenticated", "Sign in to continue")
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var hash string
	err = tx.QueryRow(ctx, `SELECT u.password_hash FROM identity_users u JOIN identity_sessions s ON s.user_id=u.id
		WHERE u.id=$1 AND s.token_hash=$2 AND s.expires_at>now() AND u.kind='human' AND u.enabled AND u.deleted_at IS NULL
		FOR UPDATE OF u,s`, userID, api.Hash(sessionToken)).Scan(&hash)
	if errors.Is(err, pgx.ErrNoRows) {
		return api.Err(401, "unauthenticated", "Session expired or revoked")
	}
	if err != nil {
		return err
	}
	if len(currentPassword) > 72 || bcrypt.CompareHashAndPassword([]byte(hash), []byte(currentPassword)) != nil {
		// A wrong current password leaves the signed-in session intact.
		return api.Err(400, "invalid_current_password", "Current password is incorrect")
	}
	// bcrypt can treat different byte strings as the same effective key. The
	// changed credential must reject the previous password, not just look new.
	if currentPassword == newPassword || bcrypt.CompareHashAndPassword([]byte(hash), []byte(newPassword)) == nil {
		return api.Err(400, "password_unchanged", "Choose a different new password")
	}
	nextHash, err := bcrypt.GenerateFromPassword([]byte(newPassword), 12)
	if err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, "UPDATE identity_users SET password_hash=$2 WHERE id=$1", userID, string(nextHash)); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, "DELETE FROM identity_sessions WHERE user_id=$1", userID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
