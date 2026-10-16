package database

import (
	"context"
	"fmt"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Migration struct {
	Version int
	SQL     string
}

// Migrate applies immutable, contiguous forward migrations under the service's
// existing transaction lock. A prior v1 ledger is adopted without replay/edit.
func Migrate(ctx context.Context, db *pgxpool.Pool, lock int64, ledger string, migrations []Migration) error {
	tx, e := db.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	if _, e = tx.Exec(ctx, "SELECT pg_advisory_xact_lock($1)", lock); e != nil {
		return e
	}
	table := pgx.Identifier{ledger}.Sanitize()
	if _, e = tx.Exec(ctx, "CREATE TABLE IF NOT EXISTS "+table+" (version integer PRIMARY KEY,applied_at timestamptz NOT NULL DEFAULT now())"); e != nil {
		return e
	}
	var head, count int
	if e = tx.QueryRow(ctx, "SELECT COALESCE(max(version),0),count(*) FROM "+table).Scan(&head, &count); e != nil {
		return e
	}
	if head != count || head > len(migrations) {
		return fmt.Errorf("unsupported %s schema version %d", ledger, head)
	}
	for i, m := range migrations {
		if m.Version != i+1 {
			return fmt.Errorf("noncontiguous migration %d", m.Version)
		}
		if m.Version <= head {
			continue
		}
		if _, e = tx.Exec(ctx, m.SQL); e != nil {
			return fmt.Errorf("migration %s/%d: %w", ledger, m.Version, e)
		}
		if _, e = tx.Exec(ctx, "INSERT INTO "+table+"(version) VALUES($1) ON CONFLICT DO NOTHING", m.Version); e != nil {
			return e
		}
	}
	return tx.Commit(ctx)
}
