// Package pg implements the Identity outbound repository ports backed by
// PostgreSQL using the shared teamie/pkg/postgres mechanism.
package pg

import (
	"context"
	_ "embed"

	"github.com/jackc/pgx/v5/pgxpool"
	"teamie/pkg/postgres"
)

//go:embed schema.sql
var SchemaV1 string

//go:embed migrations/002_principals.sql
var SchemaV2 string

//go:embed migrations/003_password_resets.sql
var SchemaV3 string

// Migrate applies all Identity PostgreSQL schema migrations under advisory lock 704101.
func Migrate(ctx context.Context, pool *pgxpool.Pool) error {
	return postgres.Migrate(ctx, pool, 704101, "identity_schema_migrations", []postgres.Migration{
		{Version: 1, SQL: SchemaV1},
		{Version: 2, SQL: SchemaV2},
		{Version: 3, SQL: SchemaV3},
	})
}

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
