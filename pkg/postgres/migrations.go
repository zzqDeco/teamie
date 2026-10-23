package postgres

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Migration represents a single numbered SQL migration step.
type Migration struct {
	Version int
	SQL     string
}

// Migrate applies immutable, contiguous forward migrations under the service's
// existing transaction lock. A prior v1 ledger is adopted without replay/edit.
func Migrate(ctx context.Context, db *pgxpool.Pool, lock int64, ledger string, migrations []Migration) error {
	tx, err := db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock($1)", lock); err != nil {
		return err
	}
	table := pgx.Identifier{ledger}.Sanitize()
	if _, err = tx.Exec(ctx, "CREATE TABLE IF NOT EXISTS "+table+" (version integer PRIMARY KEY,applied_at timestamptz NOT NULL DEFAULT now())"); err != nil {
		return err
	}
	var head, count int
	if err = tx.QueryRow(ctx, "SELECT COALESCE(max(version),0),count(*) FROM "+table).Scan(&head, &count); err != nil {
		return err
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
		if _, err = tx.Exec(ctx, m.SQL); err != nil {
			return fmt.Errorf("migration %s/%d: %w", ledger, m.Version, err)
		}
		if _, err = tx.Exec(ctx, "INSERT INTO "+table+"(version) VALUES($1) ON CONFLICT DO NOTHING", m.Version); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// MigrateSQL applies a self-managed migration script under an advisory transaction lock
// using the context-aware DB handle, running an optional precheck before DDL execution.
func MigrateSQL(ctx context.Context, db *DB, lock int64, sql string, precheck func(ctx context.Context, q Querier) error) error {
	return db.WithinTx(ctx, func(txCtx context.Context) error {
		if err := db.AdvisoryXactLock(txCtx, lock); err != nil {
			return err
		}
		conn := db.Conn(txCtx)
		if precheck != nil {
			if err := precheck(txCtx, conn); err != nil {
				return err
			}
		}
		_, err := conn.Exec(txCtx, sql)
		return err
	})
}

