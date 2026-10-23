// Package pg implements the PostgreSQL outbound repository adapters for the
// Agent Runtime engine using the shared teamie/pkg/postgres mechanism.
package pg

import (
	"context"
	"fmt"

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

// Repository implements the domain persistence ports against PostgreSQL.
type Repository struct {
	db *postgres.DB
}

// New constructs a Repository backed by a context-aware postgres.DB handle.
func New(db *postgres.DB) *Repository {
	return &Repository{db: db}
}

// NewFromPool constructs a Repository backed by a pgxpool.Pool.
func NewFromPool(pool *pgxpool.Pool) *Repository {
	return &Repository{db: postgres.New(pool)}
}

var (
	_ domain.TxManager             = (*Repository)(nil)
	_ domain.SessionRepository     = (*Repository)(nil)
	_ domain.ScheduleRepository    = (*Repository)(nil)
	_ domain.ToolReceiptRepository = (*Repository)(nil)
	_ domain.ArchiveRepository     = (*Repository)(nil)
	_ domain.InspectRepository     = (*Repository)(nil)
)

// WithinTx executes fn inside an atomic database transaction.
func (r *Repository) WithinTx(ctx context.Context, fn func(ctx context.Context) error) error {
	return r.db.WithinTx(ctx, fn)
}

// Ping verifies database connectivity.
func (r *Repository) Ping(ctx context.Context) error {
	return r.db.Ping(ctx)
}
