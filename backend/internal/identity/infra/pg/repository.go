// Package pg implements the Identity outbound repository ports backed by
// PostgreSQL using the shared teamie/backend/pkg/postgres mechanism.
package pg

import (
	"context"
	"time"

	"teamie/backend/internal/identity/domain"
	"teamie/backend/pkg/postgres"
)

const (
	registrationLockID  int64 = 704104
	principalLockSeedID int64 = 704105
)

// Repository implements the Identity domain repository and TxManager interfaces
// on top of the shared context-aware postgres.DB handle.
type Repository struct {
	DB *postgres.DB
}

// New creates a PostgreSQL-backed Identity repository.
func New(db *postgres.DB) *Repository {
	return &Repository{DB: db}
}

func (r *Repository) WithinTx(ctx context.Context, fn func(ctx context.Context) error) error {
	return r.DB.WithinTx(ctx, fn)
}

func (r *Repository) WithinReadCommittedTx(ctx context.Context, fn func(ctx context.Context) error) error {
	return r.DB.WithinReadCommittedTx(ctx, fn)
}

func (r *Repository) LockRegistration(ctx context.Context) error {
	return r.DB.AdvisoryXactLock(ctx, registrationLockID)
}

func (r *Repository) LockPrincipal(ctx context.Context, principalID string) error {
	return r.DB.AdvisoryXactLockKey(ctx, principalID, principalLockSeedID)
}

func (r *Repository) Ping(ctx context.Context) error {
	return r.DB.Ping(ctx)
}

// --- UserRepository ---

func (r *Repository) AnyUserExists(ctx context.Context) (bool, error) {
	var exists bool
	err := r.DB.Conn(ctx).QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM identity_users)").Scan(&exists)
	return exists, err
}

func (r *Repository) CreateHuman(ctx context.Context, u domain.User, passwordHash string) (time.Time, error) {
	var createdAt time.Time
	err := r.DB.Conn(ctx).QueryRow(ctx,
		"INSERT INTO identity_users(id,email,display_name,password_hash,platform_role) VALUES($1,$2,$3,$4,$5) RETURNING created_at",
		u.ID, u.Email, u.DisplayName, passwordHash, u.PlatformRole,
	).Scan(&createdAt)
	if err != nil {
		if postgres.IsUniqueViolation(err) {
			return time.Time{}, domain.ErrEmailInUse()
		}
		return time.Time{}, err
	}
	return createdAt, nil
}

func (r *Repository) FindHumanForLogin(ctx context.Context, email string) (domain.User, string, bool, error) {
	var u domain.User
	var hash string
	err := r.DB.Conn(ctx).QueryRow(ctx,
		"SELECT id,email,display_name,kind,platform_role,created_at,password_hash FROM identity_users WHERE email=$1 AND kind='human' AND enabled AND deleted_at IS NULL",
		email,
	).Scan(&u.ID, &u.Email, &u.DisplayName, &u.Kind, &u.PlatformRole, &u.CreatedAt, &hash)
	if postgres.IsNoRows(err) {
		return domain.User{}, "", false, nil
	}
	if err != nil {
		return domain.User{}, "", false, err
	}
	return u, hash, true, nil
}

func (r *Repository) LockHumanForShare(ctx context.Context, userID string) (string, bool, error) {
	var currentHash string
	err := r.DB.Conn(ctx).QueryRow(ctx,
		"SELECT password_hash FROM identity_users WHERE id=$1 AND kind='human' AND enabled AND deleted_at IS NULL FOR SHARE",
		userID,
	).Scan(&currentHash)
	if postgres.IsNoRows(err) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return currentHash, true, nil
}

func (r *Repository) LockHumanForUpdate(ctx context.Context, userID string) (string, bool, error) {
	var currentHash string
	err := r.DB.Conn(ctx).QueryRow(ctx,
		"SELECT password_hash FROM identity_users WHERE id=$1 AND kind='human' AND enabled AND deleted_at IS NULL FOR UPDATE",
		userID,
	).Scan(&currentHash)
	if postgres.IsNoRows(err) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return currentHash, true, nil
}

func (r *Repository) LockHumanByEmailForUpdate(ctx context.Context, email string) (string, bool, error) {
	var userID string
	err := r.DB.Conn(ctx).QueryRow(ctx,
		"SELECT id FROM identity_users WHERE email=$1 AND kind='human' AND enabled AND deleted_at IS NULL FOR UPDATE",
		email,
	).Scan(&userID)
	if postgres.IsNoRows(err) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return userID, true, nil
}

func (r *Repository) LockUsersForAdminReset(ctx context.Context, adminID, targetID string) (adminFound, adminEnabled bool, adminKind, adminRole string, targetFound bool, err error) {
	rows, err := r.DB.Conn(ctx).Query(ctx, `SELECT id,kind,platform_role,enabled,deleted_at IS NOT NULL
		FROM identity_users WHERE id=ANY($1) ORDER BY id FOR UPDATE`, []string{adminID, targetID})
	if err != nil {
		return false, false, "", "", false, err
	}
	defer rows.Close()
	for rows.Next() {
		var id, kind, role string
		var enabled, deleted bool
		if scanErr := rows.Scan(&id, &kind, &role, &enabled, &deleted); scanErr != nil {
			return false, false, "", "", false, scanErr
		}
		if id == adminID {
			adminFound, adminEnabled, adminRole, adminKind = true, enabled && !deleted, role, kind
		}
		if id == targetID && kind == "human" && enabled && !deleted {
			targetFound = true
		}
	}
	if err = rows.Err(); err != nil {
		return false, false, "", "", false, err
	}
	return adminFound, adminEnabled, adminKind, adminRole, targetFound, nil
}

func (r *Repository) GetActiveHumanPlatformRole(ctx context.Context, userID string) (string, bool, error) {
	var role string
	err := r.DB.Conn(ctx).QueryRow(ctx,
		"SELECT platform_role FROM identity_users WHERE id=$1 AND kind='human' AND enabled AND deleted_at IS NULL",
		userID,
	).Scan(&role)
	if postgres.IsNoRows(err) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return role, true, nil
}

func (r *Repository) UpdateDisplayName(ctx context.Context, userID, displayName string) (domain.User, error) {
	var u domain.User
	err := r.DB.Conn(ctx).QueryRow(ctx,
		"UPDATE identity_users SET display_name=$2 WHERE id=$1 RETURNING id,email,display_name,kind,platform_role,created_at",
		userID, displayName,
	).Scan(&u.ID, &u.Email, &u.DisplayName, &u.Kind, &u.PlatformRole, &u.CreatedAt)
	return u, err
}

func (r *Repository) UpdatePasswordHash(ctx context.Context, userID, passwordHash string) error {
	_, err := r.DB.Conn(ctx).Exec(ctx, "UPDATE identity_users SET password_hash=$2 WHERE id=$1", userID, passwordHash)
	return err
}

func (r *Repository) FindByIDs(ctx context.Context, ids []string) ([]domain.User, error) {
	users := []domain.User{}
	rows, err := r.DB.Conn(ctx).Query(ctx,
		"SELECT id,COALESCE(email,''),display_name,kind,platform_role,created_at FROM identity_users WHERE id=ANY($1)",
		ids,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var u domain.User
		if err = rows.Scan(&u.ID, &u.Email, &u.DisplayName, &u.Kind, &u.PlatformRole, &u.CreatedAt); err != nil {
			return nil, err
		}
		users = append(users, u)
	}
	return users, rows.Err()
}

func (r *Repository) ListAdminHumans(ctx context.Context, afterID, query, likePattern string, limit int) ([]domain.User, error) {
	users := []domain.User{}
	rows, err := r.DB.Conn(ctx).Query(ctx, `SELECT id,email,display_name,kind,platform_role,created_at
		FROM identity_users WHERE kind='human' AND enabled AND deleted_at IS NULL AND id>$1
		AND ($2='' OR email ILIKE $3 ESCAPE '\' OR display_name ILIKE $3 ESCAPE '\')
		ORDER BY id LIMIT $4`, afterID, query, likePattern, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var u domain.User
		if err = rows.Scan(&u.ID, &u.Email, &u.DisplayName, &u.Kind, &u.PlatformRole, &u.CreatedAt); err != nil {
			return nil, err
		}
		users = append(users, u)
	}
	return users, rows.Err()
}

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

// --- PrincipalRepository ---

func (r *Repository) LockPrincipalForUpdate(ctx context.Context, id string) (domain.User, bool, *time.Time, int64, bool, error) {
	var u domain.User
	var oldEnabled bool
	var deletedAt *time.Time
	var oldRevision int64
	err := r.DB.Conn(ctx).QueryRow(ctx,
		"SELECT id,COALESCE(email,''),display_name,kind,platform_role,created_at,enabled,deleted_at,principal_revision FROM identity_users WHERE id=$1 FOR UPDATE",
		id,
	).Scan(&u.ID, &u.Email, &u.DisplayName, &u.Kind, &u.PlatformRole, &u.CreatedAt, &oldEnabled, &deletedAt, &oldRevision)
	if postgres.IsNoRows(err) {
		return domain.User{}, false, nil, 0, false, nil
	}
	if err != nil {
		return domain.User{}, false, nil, 0, false, err
	}
	return u, oldEnabled, deletedAt, oldRevision, true, nil
}

func (r *Repository) CreateAgentPrincipal(ctx context.Context, id, name, kind string, enabled, deleted bool, revision int64) (time.Time, error) {
	var createdAt time.Time
	err := r.DB.Conn(ctx).QueryRow(ctx,
		"INSERT INTO identity_users(id,email,display_name,kind,password_hash,platform_role,enabled,deleted_at,principal_revision) VALUES($1,NULL,$2,$3,NULL,'user',$4,CASE WHEN $5 THEN now() ELSE NULL END,$6) RETURNING created_at",
		id, name, kind, enabled, deleted, revision,
	).Scan(&createdAt)
	return createdAt, err
}

func (r *Repository) UpdateAgentPrincipal(ctx context.Context, id, name string, enabled, deleted bool, revision int64) error {
	_, err := r.DB.Conn(ctx).Exec(ctx,
		"UPDATE identity_users SET display_name=$2,enabled=$3,deleted_at=CASE WHEN $4 THEN COALESCE(deleted_at,now()) ELSE NULL END,principal_revision=$5 WHERE id=$1",
		id, name, enabled, deleted, revision,
	)
	return err
}

func (r *Repository) DeletePrincipalTokens(ctx context.Context, principalID string) error {
	_, err := r.DB.Conn(ctx).Exec(ctx, "DELETE FROM principal_tokens WHERE principal_id=$1", principalID)
	return err
}

func (r *Repository) LockAgentEnabled(ctx context.Context, id string) (bool, bool, error) {
	var enabled bool
	err := r.DB.Conn(ctx).QueryRow(ctx,
		"SELECT enabled AND deleted_at IS NULL FROM identity_users WHERE id=$1 AND kind='agent' FOR UPDATE",
		id,
	).Scan(&enabled)
	if postgres.IsNoRows(err) {
		return false, false, nil
	}
	if err != nil {
		return false, false, err
	}
	return enabled, true, nil
}

func (r *Repository) UpsertPrincipalToken(ctx context.Context, principalID, tokenHash string, expiresAt time.Time) error {
	_, err := r.DB.Conn(ctx).Exec(ctx,
		"INSERT INTO principal_tokens(principal_id,token_hash,expires_at) VALUES($1,$2,$3) ON CONFLICT(principal_id) DO UPDATE SET token_hash=excluded.token_hash,expires_at=excluded.expires_at,created_at=now()",
		principalID, tokenHash, expiresAt,
	)
	return err
}

func (r *Repository) FindActiveAgentByTokenHash(ctx context.Context, tokenHash string) (domain.User, bool, error) {
	var u domain.User
	err := r.DB.Conn(ctx).QueryRow(ctx,
		"SELECT u.id,COALESCE(u.email,''),u.display_name,u.kind,u.platform_role,u.created_at FROM principal_tokens t JOIN identity_users u ON u.id=t.principal_id WHERE t.token_hash=$1 AND t.expires_at>now() AND u.kind='agent' AND u.enabled AND u.deleted_at IS NULL",
		tokenHash,
	).Scan(&u.ID, &u.Email, &u.DisplayName, &u.Kind, &u.PlatformRole, &u.CreatedAt)
	if postgres.IsNoRows(err) {
		return domain.User{}, false, nil
	}
	if err != nil {
		return domain.User{}, false, err
	}
	return u, true, nil
}
