// Package postgres provides shared PostgreSQL pool initialization, transaction
// context propagation, advisory-locked schema migrations, and error assertions.
// It contains only generic database mechanisms and never embeds service schemas
// or cross-service queries.
package postgres

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Option customizes a pgxpool.Config after default UTC and timeout rules are applied.
type Option func(*pgxpool.Config)

// WithMaxConns sets the maximum pool connections.
func WithMaxConns(n int32) Option {
	return func(cfg *pgxpool.Config) { cfg.MaxConns = n }
}

// WithMinConns sets the minimum pool connections.
func WithMinConns(n int32) Option {
	return func(cfg *pgxpool.Config) { cfg.MinConns = n }
}

// WithMaxConnIdleTime sets the maximum idle time for pool connections.
func WithMaxConnIdleTime(d time.Duration) Option {
	return func(cfg *pgxpool.Config) { cfg.MaxConnIdleTime = d }
}

// WithRuntimeParam sets a PostgreSQL session runtime parameter on every connection.
func WithRuntimeParam(key, value string) Option {
	return func(cfg *pgxpool.Config) { cfg.ConnConfig.RuntimeParams[key] = value }
}

// Open creates only the caller service's configured pool. It never selects a
// foreign schema or database. Wire timestamps remain UTC on every host.
func Open(ctx context.Context, url string, opts ...Option) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, err
	}
	cfg.ConnConfig.RuntimeParams["timezone"] = "UTC"
	cfg.ConnConfig.ConnectTimeout = 5 * time.Second
	cfg.AfterConnect = func(ctx context.Context, conn *pgx.Conn) error {
		conn.TypeMap().RegisterType(&pgtype.Type{
			Name:  "timestamptz",
			OID:   pgtype.TimestamptzOID,
			Codec: &pgtype.TimestamptzCodec{ScanLocation: time.UTC},
		})
		return nil
	}
	for _, opt := range opts {
		if opt != nil {
			opt(cfg)
		}
	}
	return pgxpool.NewWithConfig(ctx, cfg)
}

// DB wraps a pgxpool.Pool with context-aware transaction propagation so domain
// and application layers never depend on pgx types directly.
type DB struct {
	pool *pgxpool.Pool
}

// New wraps an existing pgxpool.Pool in a context-aware DB handle.
func New(pool *pgxpool.Pool) *DB {
	return &DB{pool: pool}
}

// OpenDB opens a UTC-normalized connection pool and returns a context-aware DB.
func OpenDB(ctx context.Context, url string, opts ...Option) (*DB, error) {
	pool, err := Open(ctx, url, opts...)
	if err != nil {
		return nil, err
	}
	return &DB{pool: pool}, nil
}

// Pool returns the underlying pgxpool.Pool.
func (db *DB) Pool() *pgxpool.Pool {
	return db.pool
}

// Close closes all connections in the pool.
func (db *DB) Close() {
	if db != nil && db.pool != nil {
		db.pool.Close()
	}
}

// Ping verifies database connectivity.
func (db *DB) Ping(ctx context.Context) error {
	return db.pool.Ping(ctx)
}
