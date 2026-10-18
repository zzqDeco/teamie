package engine

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

const runtimeSchemaVersion = 8
const runtimeWriterSchemaSetting = "teamie.runtime_writer_schema"

var errUnsupportedRuntimeSchema = errors.New("unsupported runtime schema")

// Validate before any DDL, including replacement of the writer guard. The
// ledger is written atomically by Open, so an existing empty or gapped ledger
// is an incomplete schema rather than a fresh database to silently repair.
func validateRuntimeSchema(ctx context.Context, tx pgx.Tx) error {
	var exists bool
	if err := tx.QueryRow(ctx, `SELECT to_regclass('agent_runtime_schema_migrations') IS NOT NULL`).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return nil
	}
	rows, err := tx.Query(ctx, `SELECT version FROM agent_runtime_schema_migrations ORDER BY version`)
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
		if version != want || version > runtimeSchemaVersion {
			return fmt.Errorf("%w: found version %d, expected %d through %d", errUnsupportedRuntimeSchema, version, want, runtimeSchemaVersion)
		}
		want++
	}
	if err = rows.Err(); err != nil {
		return err
	}
	if want == 1 {
		return fmt.Errorf("%w: existing ledger is empty", errUnsupportedRuntimeSchema)
	}
	return nil
}
