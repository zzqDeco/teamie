package database

import (
	"context"
	"teamie/backend/pkg/postgres"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Open delegates to the shared postgres.Open mechanism so all services share
// identical UTC codec and connection timeout settings.
func Open(ctx context.Context, url string) (*pgxpool.Pool, error) {
	return postgres.Open(ctx, url)
}
