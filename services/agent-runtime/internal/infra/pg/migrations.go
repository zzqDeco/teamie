package pg

import (
	"context"
	_ "embed"
	"errors"
	"fmt"

	"teamie/pkg/postgres"
)

const RuntimeSchemaVersion = 12
const RuntimeWriterSchemaSetting = "teamie.runtime_writer_schema"

var ErrUnsupportedRuntimeSchema = errors.New("unsupported runtime schema")

var (
	//go:embed migrations/001_runtime.sql
	SchemaSQL string
	//go:embed migrations/002_native_v3.sql
	SchemaV2SQL string
	//go:embed migrations/003_context_budget.sql
	SchemaV3SQL string
	//go:embed migrations/003a_context_archive.sql
	ContextArchiveSchemaSQL string
	//go:embed migrations/003b_context_recovery.sql
	ContextRecoverySchemaSQL string
	//go:embed migrations/004_output_budget.sql
	SchemaV4SQL string
	//go:embed migrations/005_current_input.sql
	SchemaV5SQL string
	//go:embed migrations/006_history_coalescing.sql
	SchemaV6SQL string
	//go:embed migrations/007_notification_coalescing.sql
	SchemaV7SQL string
	//go:embed migrations/008_long_context.sql
	SchemaV8SQL string
	//go:embed migrations/009_calibrated_budget.sql
	SchemaV9SQL string
	//go:embed migrations/010_event_driven_budget.sql
	SchemaV10SQL string
	//go:embed migrations/011_web_budget.sql
	SchemaV11SQL string
	//go:embed migrations/012_schedule.sql
	SchemaV12SQL string
)

// ValidateRuntimeSchema validates the schema ledger before any DDL, including
// replacement of the writer guard. The ledger is written atomically by Open, so
// an existing empty or gapped ledger is an incomplete schema rather than a fresh
// database to silently repair.
func ValidateRuntimeSchema(ctx context.Context, q postgres.Querier) error {
	var exists bool
	if err := q.QueryRow(ctx, `SELECT to_regclass('agent_runtime_schema_migrations') IS NOT NULL`).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return nil
	}
	rows, err := q.Query(ctx, `SELECT version FROM agent_runtime_schema_migrations ORDER BY version`)
	if err != nil {
		return err
	}
	defer rows.Close()
	want := 1
	for rows.Next() {
		var version int
		if err = rows.Scan(&version); err != nil {
			return err
		}
		if version != want || version > RuntimeSchemaVersion {
			return fmt.Errorf("%w: found version %d, expected %d through %d", ErrUnsupportedRuntimeSchema, version, want, RuntimeSchemaVersion)
		}
		want++
	}
	if err = rows.Err(); err != nil {
		return err
	}
	if want == 1 {
		return fmt.Errorf("%w: existing ledger is empty", ErrUnsupportedRuntimeSchema)
	}
	return nil
}

// OpenRepository opens the Agent Runtime PostgreSQL pool, validates and applies
// forward schema migrations under advisory lock 704205 via pkg/postgres, and returns a Repository.
func OpenRepository(ctx context.Context, url string) (*Repository, error) {
	pool, err := OpenPool(ctx, url, RuntimeWriterSchemaSetting, RuntimeSchemaVersion)
	if err != nil {
		return nil, err
	}
	repo := NewFromPool(pool)
	if err := repo.WithinTx(ctx, func(txCtx context.Context) error {
		if err := repo.db.AdvisoryXactLock(txCtx, 704205); err != nil {
			return err
		}
		conn := repo.db.Conn(txCtx)
		if err := ValidateRuntimeSchema(txCtx, conn); err != nil {
			return err
		}
		_, err := conn.Exec(txCtx, SchemaSQL+SchemaV2SQL+SchemaV3SQL+ContextArchiveSchemaSQL+ContextRecoverySchemaSQL+SchemaV4SQL+SchemaV5SQL+SchemaV6SQL+SchemaV7SQL+SchemaV8SQL+SchemaV9SQL+SchemaV10SQL+SchemaV11SQL+SchemaV12SQL)
		return err
	}); err != nil {
		repo.Close()
		return nil, err
	}
	return repo, nil
}
