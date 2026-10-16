package database

import (
	"context"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"time"
)

// Open creates only the caller service's configured pool. It never selects a
// foreign schema or database. Wire timestamps remain UTC on every host.
func Open(ctx context.Context, url string) (*pgxpool.Pool, error) {
	cfg, e := pgxpool.ParseConfig(url)
	if e != nil {
		return nil, e
	}
	cfg.ConnConfig.RuntimeParams["timezone"] = "UTC"
	cfg.ConnConfig.ConnectTimeout = 5 * time.Second
	cfg.AfterConnect = func(ctx context.Context, conn *pgx.Conn) error {
		conn.TypeMap().RegisterType(&pgtype.Type{Name: "timestamptz", OID: pgtype.TimestamptzOID, Codec: &pgtype.TimestamptzCodec{ScanLocation: time.UTC}})
		return nil
	}
	return pgxpool.NewWithConfig(ctx, cfg)
}
