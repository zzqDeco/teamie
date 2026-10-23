// Package pg implements the PostgreSQL ConfigStore outbound port for the Model Gateway.
package pg

import (
	"context"
	_ "embed"
	"errors"
	"time"

	"teamie/pkg/postgres"
	"teamie/services/llm-gateway/internal/domain"
)

//go:embed migrations/001_gateway.sql
var SchemaSQL string

var _ domain.ConfigStore = (*PGStore)(nil)

type rowScanner interface {
	Scan(dest ...any) error
}

// PGStore persists gateway configuration and usage records in PostgreSQL using pkg/postgres.DB.
type PGStore struct {
	DB *postgres.DB
}

// Open connects to PostgreSQL using pkg/postgres.OpenDB and runs idempotent migrations inside WithinTx.
func Open(ctx context.Context, url string) (*PGStore, error) {
	db, err := postgres.OpenDB(ctx, url, postgres.WithMaxConns(3), postgres.WithMinConns(0), postgres.WithMaxConnIdleTime(time.Minute))
	if err != nil {
		return nil, errors.New("invalid gateway database configuration")
	}
	s := &PGStore{DB: db}
	if err := db.WithinTx(ctx, func(txCtx context.Context) error {
		if lockErr := db.AdvisoryXactLock(txCtx, 7463260102); lockErr != nil {
			return lockErr
		}
		_, execErr := db.Conn(txCtx).Exec(txCtx, SchemaSQL)
		return execErr
	}); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

// WithinTx executes fn inside a PostgreSQL transaction via pkg/postgres.DB.
func (s *PGStore) WithinTx(ctx context.Context, fn func(ctx context.Context) error) error {
	return s.DB.WithinTx(ctx, fn)
}

// Ready pings the database pool.
func (s *PGStore) Ready(ctx context.Context) error {
	return s.DB.Ping(ctx)
}

func scan(row rowScanner) (c domain.Config, err error) {
	err = row.Scan(&c.BaseURL, &c.Model, &c.CipherKey, &c.Enabled, &c.UpdatedAt, &c.ContextWindowTokens, &c.WebSearchCipherKey, &c.WebSearchEnabled)
	return
}

// Load retrieves the current gateway configuration.
func (s *PGStore) Load(ctx context.Context) (domain.Config, error) {
	return scan(s.DB.Conn(ctx).QueryRow(ctx, `SELECT base_url,model,encrypted_key,enabled,updated_at,context_window_tokens,web_search_encrypted_key,web_search_enabled FROM gateway_config WHERE id`))
}

// LockForUpdate locks and returns the singleton gateway_config row within the active transaction.
func (s *PGStore) LockForUpdate(ctx context.Context) (domain.Config, error) {
	return scan(s.DB.Conn(ctx).QueryRow(ctx, `SELECT base_url,model,encrypted_key,enabled,updated_at,context_window_tokens,web_search_encrypted_key,web_search_enabled FROM gateway_config WHERE id FOR UPDATE`))
}

// Update persists merged gateway_config fields within the active transaction.
func (s *PGStore) Update(ctx context.Context, c domain.Config) (domain.Config, error) {
	return scan(s.DB.Conn(ctx).QueryRow(ctx, `UPDATE gateway_config SET base_url=$1,model=$2,encrypted_key=$3,enabled=$4,context_window_tokens=$5,web_search_encrypted_key=$6,web_search_enabled=$7,updated_at=now() WHERE id RETURNING base_url,model,encrypted_key,enabled,updated_at,context_window_tokens,web_search_encrypted_key,web_search_enabled`, c.BaseURL, c.Model, c.CipherKey, c.Enabled, c.ContextWindowTokens, c.WebSearchCipherKey, c.WebSearchEnabled))
}

// Save updates the gateway configuration within a transaction using WithinTx.
func (s *PGStore) Save(ctx context.Context, c domain.Config, key *string, clear bool, crypto domain.Cipher) (domain.Config, error) {
	var updated domain.Config
	err := s.WithinTx(ctx, func(txCtx context.Context) error {
		old, err := s.LockForUpdate(txCtx)
		if err != nil {
			return err
		}
		merged, err := domain.MergeSavedConfig(old, c, key, clear, crypto)
		if err != nil {
			return err
		}
		updated, err = s.Update(txCtx, merged)
		return err
	})
	return updated, err
}
