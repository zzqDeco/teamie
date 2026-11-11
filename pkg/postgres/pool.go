// Package postgres provides shared PostgreSQL pool initialization, transaction
// context propagation, advisory-locked schema migrations, and error assertions.
// It contains only generic database mechanisms and never embeds service schemas
// or cross-service queries.
package postgres

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

// DefaultMaxConns bounds each service pool so steady-state plus one transient A/B
// canary slot stays comfortably within PostgreSQL max_connections=40.
const DefaultMaxConns int32 = 4

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

func hasConnSetting(connString, key string) bool {
	if u, err := url.Parse(connString); err == nil && u.Scheme != "" {
		if u.Query().Has(key) {
			return true
		}
	}
	for _, token := range strings.Fields(connString) {
		if strings.HasPrefix(token, key+"=") {
			return true
		}
	}
	return false
}

func buildPoolConfig(connString string, opts ...Option) (*pgxpool.Config, error) {
	cfg, err := pgxpool.ParseConfig(connString)
	if err != nil {
		return nil, err
	}
	if !hasConnSetting(connString, "pool_max_conns") {
		cfg.MaxConns = DefaultMaxConns
	}
	if !hasConnSetting(connString, "pool_min_conns") {
		cfg.MinConns = 0
	}
	if !hasConnSetting(connString, "pool_max_conn_idle_time") {
		cfg.MaxConnIdleTime = time.Minute
	}
	if raw := strings.TrimSpace(os.Getenv("PGPOOL_MAX_CONNS")); raw != "" {
		n, parseErr := strconv.ParseInt(raw, 10, 32)
		if parseErr != nil || n <= 0 {
			return nil, fmt.Errorf("invalid PGPOOL_MAX_CONNS %q", raw)
		}
		cfg.MaxConns = int32(n)
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
	if cfg.MinConns > cfg.MaxConns {
		cfg.MinConns = cfg.MaxConns
	}
	return cfg, nil
}

// OpenPool creates only the caller service's configured pool. It never selects a
// foreign schema or database. Wire timestamps remain UTC on every host.
func OpenPool(ctx context.Context, url string, opts ...Option) (*pgxpool.Pool, error) {
	cfg, err := buildPoolConfig(url, opts...)
	if err != nil {
		return nil, err
	}
	return pgxpool.NewWithConfig(ctx, cfg)
}

// Open is an alias for OpenPool.
func Open(ctx context.Context, url string, opts ...Option) (*pgxpool.Pool, error) {
	return OpenPool(ctx, url, opts...)
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
