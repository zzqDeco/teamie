// Package pg implements the PostgreSQL outbound repository adapters for the
// Agent Runtime engine using the shared teamie/pkg/postgres mechanism.
package pg

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"teamie/pkg/postgres"
	"teamie/services/agent-runtime/internal/domain"
)

// OpenPool opens the Agent Runtime's dedicated PostgreSQL connection pool with
// UTC normalization, 5s connect timeout, MaxConns=4, MinConns=0, and the writer schema guard setting.
func OpenPool(ctx context.Context, url, writerSchemaSetting string, schemaVersion int) (*pgxpool.Pool, error) {
	opts := []postgres.Option{
		postgres.WithMaxConns(4),
		postgres.WithMinConns(0),
	}
	if writerSchemaSetting != "" {
		opts = append(opts, postgres.WithRuntimeParam(writerSchemaSetting, fmt.Sprint(schemaVersion)))
	}
	return postgres.Open(ctx, url, opts...)
}

// IsNoRows reports whether err is pgx.ErrNoRows.
func IsNoRows(err error) bool {
	return postgres.IsNoRows(err)
}

// IsRetryableError reports whether err is a transient PostgreSQL connection or serialization error.
func IsRetryableError(err error) bool {
	var databaseError *pgconn.PgError
	if errors.As(err, &databaseError) {
		code := databaseError.Code
		return strings.HasPrefix(code, "08") || strings.HasPrefix(code, "53") || code == "40001" || code == "40P01" || code == "57P01" || code == "57P02" || code == "57P03"
	}
	return pgconn.SafeToRetry(err) || pgconn.Timeout(err)
}

// Repository implements the domain persistence ports against PostgreSQL.
type Repository struct {
	db   *postgres.DB
	pool *pgxpool.Pool
}

// New constructs a Repository backed by a context-aware postgres.DB handle.
func New(db *postgres.DB) *Repository {
	var pool *pgxpool.Pool
	if db != nil {
		pool = db.Pool()
	}
	return &Repository{db: db, pool: pool}
}

// NewFromPool constructs a Repository backed by a pgxpool.Pool.
func NewFromPool(pool *pgxpool.Pool) *Repository {
	return &Repository{db: postgres.New(pool), pool: pool}
}

var (
	_ domain.TxManager             = (*Repository)(nil)
	_ domain.SessionRepository     = (*Repository)(nil)
	_ domain.ScheduleRepository    = (*Repository)(nil)
	_ domain.ToolReceiptRepository = (*Repository)(nil)
	_ domain.ArchiveRepository     = (*Repository)(nil)
	_ domain.InspectRepository     = (*Repository)(nil)
)

// Close closes the underlying connection pool.
func (r *Repository) Close() {
	if r != nil && r.db != nil {
		r.db.Close()
	}
}

// WithinTx executes fn inside an atomic database transaction.
func (r *Repository) WithinTx(ctx context.Context, fn func(ctx context.Context) error) error {
	return r.db.WithinTx(ctx, fn)
}

// Ping verifies database connectivity.
func (r *Repository) Ping(ctx context.Context) error {
	return r.db.Ping(ctx)
}

// Exec executes a statement using the active transaction in ctx or the connection pool.
func (r *Repository) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	return r.db.Exec(ctx, sql, args...)
}

// Query executes a multi-row query using the active transaction in ctx or the connection pool.
func (r *Repository) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	return r.db.Query(ctx, sql, args...)
}

// QueryRow executes a single-row query using the active transaction in ctx or the connection pool.
func (r *Repository) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	return r.db.QueryRow(ctx, sql, args...)
}

// Begin starts a raw pgx transaction on the underlying pool.
func (r *Repository) Begin(ctx context.Context) (pgx.Tx, error) {
	return r.pool.Begin(ctx)
}

// CheckLeaseTx locks the agent row FOR UPDATE on tx and verifies the active lease fence.
func CheckLeaseTx(ctx context.Context, tx pgx.Tx, l *domain.Lease) error {
	var ok bool
	err := tx.QueryRow(ctx, `SELECT lease_owner=$2 AND fence=$3 AND lease_until>now() FROM runtime_agents WHERE agent_id=$1 FOR UPDATE`, l.AgentID, l.Owner, l.Fence).Scan(&ok)
	if err != nil {
		return err
	}
	if !ok {
		return domain.ErrLeaseLost
	}
	return nil
}

// AcceptSessionAttemptTx records accepted session attempts for runID on tx.
func AcceptSessionAttemptTx(ctx context.Context, tx pgx.Tx, agentID, runID string) error {
	_, err := tx.Exec(ctx, `INSERT INTO runtime_session_acceptances(agent_id,attempt_id,run_id) SELECT $1,unnest(array_append(context_attempts,attempt_id)),id FROM runtime_runs WHERE id=$2 ON CONFLICT DO NOTHING`, agentID, runID)
	return err
}
