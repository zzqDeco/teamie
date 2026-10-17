package identity

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"golang.org/x/crypto/bcrypt"
	"teamie/backend/internal/api"
)

type PasswordReset struct {
	ID        string    `json:"id"`
	CreatedAt time.Time `json:"createdAt"`
	ExpiresAt time.Time `json:"expiresAt"`
}

type PasswordResetAccount struct {
	ID          string `json:"id"`
	DisplayName string `json:"displayName"`
	EmailMasked string `json:"emailMasked"`
}

type PasswordResetInspection struct {
	Account   PasswordResetAccount `json:"account"`
	ExpiresAt time.Time            `json:"expiresAt"`
}

type AdminUserPage struct {
	Users  []api.User `json:"users"`
	Cursor string     `json:"cursor,omitempty"`
}

func invalidResetLink() error {
	return api.Err(400, "invalid_reset_link", "This password reset link is invalid, expired, revoked, or already used")
}

func passwordResetHash(token string) (string, error) {
	if len(token) != 64 {
		return "", invalidResetLink()
	}
	if _, err := hex.DecodeString(token); err != nil {
		return "", invalidResetLink()
	}
	return api.Hash(token), nil
}

// Call only after locking this human's row. This shares the user-before-session
// lock order used by login, password replacement and reset-link issuance.
func validHumanSession(ctx context.Context, tx pgx.Tx, userID, sessionToken string) error {
	if len(sessionToken) != 64 {
		return api.Err(401, "unauthenticated", "Sign in to continue")
	}
	var valid bool
	err := tx.QueryRow(ctx, `SELECT true FROM identity_sessions
		WHERE user_id=$1 AND token_hash=$2 AND expires_at>clock_timestamp() FOR SHARE`, userID, api.Hash(sessionToken)).Scan(&valid)
	if errors.Is(err, pgx.ErrNoRows) {
		return api.Err(401, "unauthenticated", "Session expired or revoked")
	}
	return err
}

// Both users are locked in ID order before the actor's session is checked.
// Two administrators recovering each other cannot take opposite row locks.
func lockResetAdmin(ctx context.Context, tx pgx.Tx, adminID, sessionToken, targetID string) error {
	rows, err := tx.Query(ctx, `SELECT id,kind,platform_role,enabled,deleted_at IS NOT NULL
		FROM identity_users WHERE id=ANY($1) ORDER BY id FOR UPDATE`, []string{adminID, targetID})
	if err != nil {
		return err
	}
	var adminFound, adminEnabled, targetFound bool
	var adminRole, adminKind string
	for rows.Next() {
		var id, kind, role string
		var enabled, deleted bool
		if err = rows.Scan(&id, &kind, &role, &enabled, &deleted); err != nil {
			rows.Close()
			return err
		}
		if id == adminID {
			adminFound, adminEnabled, adminRole, adminKind = true, enabled && !deleted, role, kind
		}
		if id == targetID && kind == "human" && enabled && !deleted {
			targetFound = true
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if !adminFound || !adminEnabled {
		return api.Err(401, "unauthenticated", "Session expired or revoked")
	}
	if adminKind != "human" || adminRole != "admin" {
		return api.Err(403, "forbidden", "Platform administrator permission is required")
	}
	if err = validHumanSession(ctx, tx, adminID, sessionToken); err != nil {
		return err
	}
	if !targetFound {
		return api.Err(404, "user_not_found", "This account is unavailable")
	}
	return nil
}

func (s *Store) AdminUsers(ctx context.Context, adminID, sessionToken, cursor string, limit int, query string) (AdminUserPage, error) {
	page := AdminUserPage{Users: []api.User{}}
	if limit == 0 {
		limit = 50
	}
	if limit < 1 || limit > 100 || len(query) > 800 || !utf8.ValidString(query) || strings.ContainsRune(query, '\x00') || utf8.RuneCountInString(query) > 200 {
		return page, api.Err(400, "invalid_request", "Use a limit from 1 to 100 and a search of at most 200 characters")
	}
	var after string
	if cursor != "" {
		if len(cursor) > 172 {
			return page, api.Err(400, "invalid_cursor", "Invalid users cursor")
		}
		decoded, err := base64.RawURLEncoding.DecodeString(cursor)
		if err != nil || len(decoded) == 0 || len(decoded) > 128 || !utf8.Valid(decoded) || strings.ContainsRune(string(decoded), '\x00') {
			return page, api.Err(400, "invalid_cursor", "Invalid users cursor")
		}
		after = string(decoded)
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return page, err
	}
	defer tx.Rollback(ctx)
	if err = lockResetAdmin(ctx, tx, adminID, sessionToken, adminID); err != nil {
		return page, err
	}
	// Treat user-entered wildcard characters literally, like history search.
	query = strings.TrimSpace(query)
	pattern := "%" + strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(query) + "%"
	rows, err := tx.Query(ctx, `SELECT id,email,display_name,kind,platform_role,created_at
		FROM identity_users WHERE kind='human' AND enabled AND deleted_at IS NULL AND id>$1
		AND ($2='' OR email ILIKE $3 ESCAPE '\' OR display_name ILIKE $3 ESCAPE '\')
		ORDER BY id LIMIT $4`, after, query, pattern, limit+1)
	if err != nil {
		return page, err
	}
	for rows.Next() {
		var user api.User
		if err = rows.Scan(&user.ID, &user.Email, &user.DisplayName, &user.Kind, &user.PlatformRole, &user.CreatedAt); err != nil {
			rows.Close()
			return page, err
		}
		page.Users = append(page.Users, user)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return page, err
	}
	if len(page.Users) > limit {
		page.Users = page.Users[:limit]
		page.Cursor = base64.RawURLEncoding.EncodeToString([]byte(page.Users[limit-1].ID))
	}
	return page, tx.Commit(ctx)
}

func (s *Store) passwordResetEnabled() error {
	if !s.Flags.Load().Flags["auth.password_reset"] {
		return api.Err(403, "feature_disabled", "Password reset link generation is disabled")
	}
	return nil
}

func (s *Store) CreatePasswordResetLink(ctx context.Context, adminID, sessionToken, targetID string) (PasswordReset, string, error) {
	var reset PasswordReset
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return reset, "", err
	}
	defer tx.Rollback(ctx)
	if err = lockResetAdmin(ctx, tx, adminID, sessionToken, targetID); err != nil {
		return reset, "", err
	}
	if err = s.passwordResetEnabled(); err != nil {
		return reset, "", err
	}
	reset, token, err := issuePasswordReset(ctx, tx, targetID, &adminID)
	if err == nil {
		err = tx.Commit(ctx)
	}
	return reset, token, err
}

// OperatorPasswordResetLink is only called by the server-side identityctl
// command. It grants no role or session and accepts no replacement password.
func (s *Store) OperatorPasswordResetLink(ctx context.Context, email string) (PasswordReset, string, error) {
	var reset PasswordReset
	email, err := normalizeEmail(email)
	if err != nil {
		return reset, "", err
	}
	if err = s.passwordResetEnabled(); err != nil {
		return reset, "", err
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return reset, "", err
	}
	defer tx.Rollback(ctx)
	var userID string
	err = tx.QueryRow(ctx, `SELECT id FROM identity_users
		WHERE email=$1 AND kind='human' AND enabled AND deleted_at IS NULL FOR UPDATE`, email).Scan(&userID)
	if errors.Is(err, pgx.ErrNoRows) {
		return reset, "", api.Err(404, "user_not_found", "This account is unavailable")
	}
	if err != nil {
		return reset, "", err
	}
	// Re-read the reloadable flag after waiting for a concurrent credential write.
	if err = s.passwordResetEnabled(); err != nil {
		return reset, "", err
	}
	reset, token, err := issuePasswordReset(ctx, tx, userID, nil)
	if err == nil {
		err = tx.Commit(ctx)
	}
	return reset, token, err
}

// The target user is already locked before reading/updating any reset links.
func issuePasswordReset(ctx context.Context, tx pgx.Tx, userID string, createdBy *string) (PasswordReset, string, error) {
	reset := PasswordReset{ID: api.ID()}
	if _, err := tx.Exec(ctx, `UPDATE identity_password_resets SET revoked_at=clock_timestamp()
		WHERE user_id=$1 AND used_at IS NULL AND revoked_at IS NULL`, userID); err != nil {
		return reset, "", err
	}
	token := api.Random(32)
	err := tx.QueryRow(ctx, `WITH issued AS (SELECT clock_timestamp() AS at)
		INSERT INTO identity_password_resets(id,user_id,token_hash,created_by,created_at,expires_at)
		SELECT $1,$2,$3,$4,at,at+interval '1 hour' FROM issued RETURNING created_at,expires_at`,
		reset.ID, userID, api.Hash(token), createdBy).Scan(&reset.CreatedAt, &reset.ExpiresAt)
	return reset, token, err
}

func maskedResetEmail(email string) string {
	local, domain, ok := strings.Cut(email, "@")
	if !ok || local == "" {
		return "***"
	}
	first, _ := utf8.DecodeRuneInString(local)
	return string(first) + "***@" + domain
}

func (s *Store) InspectPasswordReset(ctx context.Context, token string) (PasswordResetInspection, error) {
	var result PasswordResetInspection
	hash, err := passwordResetHash(token)
	if err != nil {
		return result, err
	}
	var email string
	err = s.DB.QueryRow(ctx, `SELECT u.id,u.display_name,u.email,r.expires_at
		FROM identity_password_resets r JOIN identity_users u ON u.id=r.user_id
		WHERE r.token_hash=$1 AND r.used_at IS NULL AND r.revoked_at IS NULL AND r.expires_at>clock_timestamp()
		AND u.kind='human' AND u.enabled AND u.deleted_at IS NULL`, hash).Scan(
		&result.Account.ID, &result.Account.DisplayName, &email, &result.ExpiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return result, invalidResetLink()
	}
	result.Account.EmailMasked = maskedResetEmail(email)
	return result, err
}

func (s *Store) ResetPassword(ctx context.Context, token, newPassword string) error {
	hash, err := passwordResetHash(token)
	if err != nil {
		return err
	}
	// This lookup deliberately does not lock the link. Every competing operation
	// must acquire the user row first, then recheck the link under that lock.
	var userID string
	err = s.DB.QueryRow(ctx, "SELECT user_id FROM identity_password_resets WHERE token_hash=$1", hash).Scan(&userID)
	if errors.Is(err, pgx.ErrNoRows) {
		return invalidResetLink()
	}
	if err != nil {
		return err
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var currentHash string
	err = tx.QueryRow(ctx, `SELECT password_hash FROM identity_users
		WHERE id=$1 AND kind='human' AND enabled AND deleted_at IS NULL FOR UPDATE`, userID).Scan(&currentHash)
	if errors.Is(err, pgx.ErrNoRows) {
		return invalidResetLink()
	}
	if err != nil {
		return err
	}
	var resetID string
	err = tx.QueryRow(ctx, `SELECT id FROM identity_password_resets
		WHERE token_hash=$1 AND user_id=$2 AND used_at IS NULL AND revoked_at IS NULL
		AND expires_at>clock_timestamp() FOR UPDATE`, hash, userID).Scan(&resetID)
	if errors.Is(err, pgx.ErrNoRows) {
		return invalidResetLink()
	}
	if err != nil {
		return err
	}
	if err = validatePassword(newPassword); err != nil {
		return err
	}
	if bcrypt.CompareHashAndPassword([]byte(currentHash), []byte(newPassword)) == nil {
		return api.Err(400, "password_unchanged", "Choose a different new password")
	}
	nextHash, err := bcrypt.GenerateFromPassword([]byte(newPassword), 12)
	if err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, "UPDATE identity_users SET password_hash=$2 WHERE id=$1", userID, string(nextHash)); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, "UPDATE identity_password_resets SET used_at=clock_timestamp() WHERE id=$1", resetID); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, "DELETE FROM identity_sessions WHERE user_id=$1", userID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
