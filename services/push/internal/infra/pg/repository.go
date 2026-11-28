// Package pg implements the Push outbound repository ports backed by
// PostgreSQL using the shared teamie/pkg/postgres mechanism.
package pg

import (
	"context"
	_ "embed"

	"github.com/jackc/pgx/v5/pgxpool"
	"teamie/pkg/postgres"
	"teamie/services/push/internal/domain"
)

//go:embed migrations/001_push.sql
var SchemaV1 string

const (
	migrationLockID        int64 = 704901
	subscriptionLockSeed   int64 = 704902
)

// Migrate applies all Push PostgreSQL schema migrations under advisory lock 704901.
func Migrate(ctx context.Context, pool *pgxpool.Pool) error {
	return postgres.Migrate(ctx, pool, migrationLockID, "push_schema_migrations", []postgres.Migration{
		{Version: 1, SQL: SchemaV1},
	})
}

// Repository implements domain.SubscriptionRepository and domain.TxManager over postgres.DB.
type Repository struct {
	DB *postgres.DB
}

// New creates a PostgreSQL-backed Push subscription repository.
func New(db *postgres.DB) *Repository {
	return &Repository{DB: db}
}

func (r *Repository) WithinTx(ctx context.Context, fn func(ctx context.Context) error) error {
	return r.DB.WithinTx(ctx, fn)
}

func (r *Repository) LockUserSubscriptions(ctx context.Context, userID string) error {
	return r.DB.AdvisoryXactLockKey(ctx, userID, subscriptionLockSeed)
}

func (r *Repository) Ping(ctx context.Context) error {
	return r.DB.Ping(ctx)
}

func (r *Repository) ListByUser(ctx context.Context, userID string) ([]domain.PushSubscription, error) {
	rows, err := r.DB.Conn(ctx).Query(ctx, `
SELECT id, user_id, endpoint_hash, endpoint, endpoint_host, p256dh, auth, user_agent,
       created_at, updated_at, last_success_at, last_error_code
FROM push_subscriptions
WHERE user_id = $1
ORDER BY updated_at DESC, id DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanSubscriptions(rows)
}

func (r *Repository) ListByUserIDs(ctx context.Context, userIDs []string) ([]domain.PushSubscription, error) {
	if len(userIDs) == 0 {
		return []domain.PushSubscription{}, nil
	}
	rows, err := r.DB.Conn(ctx).Query(ctx, `
SELECT id, user_id, endpoint_hash, endpoint, endpoint_host, p256dh, auth, user_agent,
       created_at, updated_at, last_success_at, last_error_code
FROM push_subscriptions
WHERE user_id = ANY($1::text[])
ORDER BY user_id, updated_at DESC, id DESC`, userIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanSubscriptions(rows)
}

func (r *Repository) UpsertAndTrim(ctx context.Context, sub domain.PushSubscription, maxPerUser int) (domain.PushSubscription, error) {
	conn := r.DB.Conn(ctx)
	var saved domain.PushSubscription
	err := conn.QueryRow(ctx, `
INSERT INTO push_subscriptions(id, user_id, endpoint_hash, endpoint, endpoint_host, p256dh, auth, user_agent)
VALUES($1, $2, $3, $4, $5, $6, $7, $8)
ON CONFLICT (endpoint_hash) DO UPDATE
SET id = CASE WHEN push_subscriptions.user_id = EXCLUDED.user_id THEN push_subscriptions.id ELSE EXCLUDED.id END,
    user_id = EXCLUDED.user_id,
    endpoint = EXCLUDED.endpoint,
    endpoint_host = EXCLUDED.endpoint_host,
    p256dh = EXCLUDED.p256dh,
    auth = EXCLUDED.auth,
    user_agent = EXCLUDED.user_agent,
    created_at = CASE WHEN push_subscriptions.user_id = EXCLUDED.user_id THEN push_subscriptions.created_at ELSE now() END,
    updated_at = now(),
    last_success_at = CASE WHEN push_subscriptions.user_id = EXCLUDED.user_id THEN push_subscriptions.last_success_at ELSE NULL END,
    last_error_code = ''
RETURNING id, user_id, endpoint_hash, endpoint, endpoint_host, p256dh, auth, user_agent,
          created_at, updated_at, last_success_at, last_error_code`,
		sub.ID, sub.UserID, sub.EndpointHash, sub.Endpoint, sub.EndpointHost, sub.P256DH, sub.Auth, sub.UserAgent,
	).Scan(
		&saved.ID, &saved.UserID, &saved.EndpointHash, &saved.Endpoint, &saved.EndpointHost,
		&saved.P256DH, &saved.Auth, &saved.UserAgent,
		&saved.CreatedAt, &saved.UpdatedAt, &saved.LastSuccessAt, &saved.LastErrorCode,
	)
	if err != nil {
		return domain.PushSubscription{}, err
	}
	if maxPerUser > 0 {
		keepOthers := maxPerUser - 1
		if keepOthers < 0 {
			keepOthers = 0
		}
		if _, err = conn.Exec(ctx, `
DELETE FROM push_subscriptions
WHERE user_id = $1 AND id <> $3 AND id IN (
  SELECT id FROM push_subscriptions
  WHERE user_id = $1 AND id <> $3
  ORDER BY updated_at DESC, id DESC
  OFFSET $2
)`, saved.UserID, keepOthers, saved.ID); err != nil {
			return domain.PushSubscription{}, err
		}
	}
	return saved, nil
}

func (r *Repository) DeleteByUserEndpointHash(ctx context.Context, userID, endpointHash string) (bool, error) {
	tag, err := r.DB.Conn(ctx).Exec(ctx, `
DELETE FROM push_subscriptions WHERE user_id = $1 AND endpoint_hash = $2`, userID, endpointHash)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

func (r *Repository) DeleteByID(ctx context.Context, id string) error {
	_, err := r.DB.Conn(ctx).Exec(ctx, `DELETE FROM push_subscriptions WHERE id = $1`, id)
	return err
}

func (r *Repository) RecordDeliveryResult(ctx context.Context, id string, ok bool, errCode string) error {
	if ok {
		_, err := r.DB.Conn(ctx).Exec(ctx, `
UPDATE push_subscriptions SET last_success_at = now(), last_error_code = '' WHERE id = $1`, id)
		return err
	}
	_, err := r.DB.Conn(ctx).Exec(ctx, `
UPDATE push_subscriptions SET last_error_code = $2 WHERE id = $1`, id, errCode)
	return err
}

type rowScanner interface {
	Next() bool
	Scan(dest ...any) error
	Err() error
}

func scanSubscriptions(rows rowScanner) ([]domain.PushSubscription, error) {
	out := []domain.PushSubscription{}
	for rows.Next() {
		var s domain.PushSubscription
		if err := rows.Scan(
			&s.ID, &s.UserID, &s.EndpointHash, &s.Endpoint, &s.EndpointHost,
			&s.P256DH, &s.Auth, &s.UserAgent,
			&s.CreatedAt, &s.UpdatedAt, &s.LastSuccessAt, &s.LastErrorCode,
		); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}
