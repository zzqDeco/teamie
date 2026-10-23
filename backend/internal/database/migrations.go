package database

import (
	"context"
	"teamie/backend/pkg/postgres"

	"github.com/jackc/pgx/v5/pgxpool"
)

type Migration = postgres.Migration

// Migrate delegates to the shared postgres.Migrate mechanism.
func Migrate(ctx context.Context, db *pgxpool.Pool, lock int64, ledger string, migrations []Migration) error {
	return postgres.Migrate(ctx, db, lock, ledger, migrations)
}
