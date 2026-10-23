// Package pg implements the PostgreSQL ConfigStore outbound port for the Model Gateway.
package pg

import (
	"context"
	_ "embed"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"teamie/pkg/postgres"
	"teamie/services/llm-gateway/internal/domain"
)

//go:embed migrations/001_gateway.sql
var SchemaSQL string

var _ domain.ConfigStore = (*PGStore)(nil)

// PGStore persists gateway configuration and usage records in PostgreSQL.
type PGStore struct {
	DB *pgxpool.Pool
}

// Open connects to PostgreSQL using pkg/postgres.Open and runs idempotent migrations.
func Open(ctx context.Context, url string) (*PGStore, error) {
	db, err := postgres.Open(ctx, url, postgres.WithMaxConns(3), postgres.WithMinConns(0), postgres.WithMaxConnIdleTime(time.Minute))
	if err != nil {
		return nil, errors.New("invalid gateway database configuration")
	}
	s := &PGStore{DB: db}
	tx, err := db.Begin(ctx)
	if err != nil {
		db.Close()
		return nil, err
	}
	defer tx.Rollback(ctx)
	_, err = tx.Exec(ctx, SchemaSQL)
	if err == nil {
		err = tx.Commit(ctx)
	}
	if err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

// Ready pings the database pool.
func (s *PGStore) Ready(ctx context.Context) error {
	return s.DB.Ping(ctx)
}

func scan(row pgx.Row) (c domain.Config, err error) {
	err = row.Scan(&c.BaseURL, &c.Model, &c.CipherKey, &c.Enabled, &c.UpdatedAt, &c.ContextWindowTokens, &c.WebSearchCipherKey, &c.WebSearchEnabled)
	return
}

// Load retrieves the current gateway configuration.
func (s *PGStore) Load(ctx context.Context) (domain.Config, error) {
	return scan(s.DB.QueryRow(ctx, `SELECT base_url,model,encrypted_key,enabled,updated_at,context_window_tokens,web_search_encrypted_key,web_search_enabled FROM gateway_config WHERE id`))
}

// Save updates the gateway configuration within a serializable row lock.
func (s *PGStore) Save(ctx context.Context, c domain.Config, key *string, clear bool, crypto domain.Cipher) (domain.Config, error) {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return domain.Config{}, err
	}
	defer tx.Rollback(ctx)
	old, err := scan(tx.QueryRow(ctx, `SELECT base_url,model,encrypted_key,enabled,updated_at,context_window_tokens,web_search_encrypted_key,web_search_enabled FROM gateway_config WHERE id FOR UPDATE`))
	if err != nil {
		return domain.Config{}, err
	}
	c, err = domain.MergeSavedConfig(old, c, key, clear, crypto)
	if err != nil {
		return domain.Config{}, err
	}
	c, err = scan(tx.QueryRow(ctx, `UPDATE gateway_config SET base_url=$1,model=$2,encrypted_key=$3,enabled=$4,context_window_tokens=$5,web_search_encrypted_key=$6,web_search_enabled=$7,updated_at=now() WHERE id RETURNING base_url,model,encrypted_key,enabled,updated_at,context_window_tokens,web_search_encrypted_key,web_search_enabled`, c.BaseURL, c.Model, c.CipherKey, c.Enabled, c.ContextWindowTokens, c.WebSearchCipherKey, c.WebSearchEnabled))
	if err != nil {
		return domain.Config{}, err
	}
	return c, tx.Commit(ctx)
}
