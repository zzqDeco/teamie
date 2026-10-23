package pg

import (
	"context"
	"time"

	"teamie/pkg/postgres"
	"teamie/services/identity/internal/domain"
)

// --- UserRepository & PrincipalRepository ---

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
