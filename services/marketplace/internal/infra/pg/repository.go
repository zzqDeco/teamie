// Package pg implements the Marketplace outbound repository ports backed by
// PostgreSQL using the shared teamie/pkg/postgres mechanism.
package pg

import (
	"context"
	_ "embed"
	"errors"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"teamie/pkg/postgres"
	"teamie/services/marketplace/internal/domain"
)

//go:embed migrations/001_marketplace.sql
var SchemaV1 string

const (
	migrationLockID        int64 = 704801
	developerQuotaLockSeed int64 = 704802
)

// Migrate applies all Marketplace PostgreSQL schema migrations under advisory lock 704801.
func Migrate(ctx context.Context, pool *pgxpool.Pool) error {
	return postgres.Migrate(ctx, pool, migrationLockID, "marketplace_schema_migrations", []postgres.Migration{
		{Version: 1, SQL: SchemaV1},
	})
}

// Repository implements domain.MarketplaceRepository and domain.TxManager over postgres.DB.
type Repository struct {
	DB *postgres.DB
}

// New creates a PostgreSQL-backed Marketplace repository.
func New(db *postgres.DB) *Repository {
	return &Repository{DB: db}
}

func (r *Repository) WithinTx(ctx context.Context, fn func(ctx context.Context) error) error {
	return r.DB.WithinTx(ctx, fn)
}

func (r *Repository) LockDeveloperQuota(ctx context.Context, ownerID string) error {
	return r.DB.AdvisoryXactLockKey(ctx, ownerID, developerQuotaLockSeed)
}

func (r *Repository) Ping(ctx context.Context) error {
	return r.DB.Ping(ctx)
}

func notFound(err error) error {
	if postgres.IsNoRows(err) {
		return domain.ErrNotFound()
	}
	return err
}

func mapWriteError(err error) error {
	if err == nil {
		return nil
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return domain.ErrDuplicateMarketplaceSlug()
	}
	return notFound(err)
}
