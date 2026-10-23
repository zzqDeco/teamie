package postgres

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Querier abstracts *pgxpool.Pool and pgx.Tx so repository implementations work
// transparently whether invoked standalone or inside an application transaction.
type Querier interface {
	Exec(ctx context.Context, sql string, arguments ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

type txKey struct{}

// WithTx returns a derived context carrying tx as the active transaction.
func WithTx(ctx context.Context, tx pgx.Tx) context.Context {
	return context.WithValue(ctx, txKey{}, tx)
}

// TxFromContext extracts the active pgx.Tx from ctx if present.
func TxFromContext(ctx context.Context) (pgx.Tx, bool) {
	tx, ok := ctx.Value(txKey{}).(pgx.Tx)
	return tx, ok && tx != nil
}

// Conn returns the active pgx.Tx in ctx if inside WithinTx, or the underlying
// connection pool otherwise.
func (db *DB) Conn(ctx context.Context) Querier {
	if tx, ok := TxFromContext(ctx); ok {
		return tx
	}
	return db.pool
}

// WithinTx executes fn inside a database transaction. If ctx already carries an
// active transaction, fn runs within that existing transaction.
func (db *DB) WithinTx(ctx context.Context, fn func(ctx context.Context) error) error {
	if _, ok := TxFromContext(ctx); ok {
		return fn(ctx)
	}
	tx, err := db.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err := fn(WithTx(ctx, tx)); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// WithinReadCommittedTx executes fn inside an explicit ReadCommitted transaction.
func (db *DB) WithinReadCommittedTx(ctx context.Context, fn func(ctx context.Context) error) error {
	if _, ok := TxFromContext(ctx); ok {
		return fn(ctx)
	}
	tx, err := db.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err := fn(WithTx(ctx, tx)); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// AdvisoryXactLock acquires a transaction-scoped advisory lock on the current connection/tx.
func (db *DB) AdvisoryXactLock(ctx context.Context, lockID int64) error {
	_, err := db.Conn(ctx).Exec(ctx, "SELECT pg_advisory_xact_lock($1)", lockID)
	return err
}

// AdvisoryXactLockKey acquires a transaction-scoped advisory lock hashed by key and seed.
func (db *DB) AdvisoryXactLockKey(ctx context.Context, key string, seed int64) error {
	_, err := db.Conn(ctx).Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1,$2))", key, seed)
	return err
}
