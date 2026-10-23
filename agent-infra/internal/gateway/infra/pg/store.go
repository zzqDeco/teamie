// Package pg implements the PostgreSQL ConfigStore outbound port for the Model Gateway.
package pg

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"teamie/agent-infra/internal/gateway/domain"
	"teamie/backend/pkg/postgres"
)

var _ domain.ConfigStore = (*PGStore)(nil)

// PGStore persists gateway configuration and usage records in PostgreSQL.
type PGStore struct {
	DB *pgxpool.Pool
}

// Open connects to PostgreSQL using backend/pkg/postgres.Open and runs idempotent migrations.
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
	_, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(7463260102); CREATE TABLE IF NOT EXISTS gateway_schema_migrations(version INTEGER PRIMARY KEY,applied_at TIMESTAMPTZ NOT NULL DEFAULT now()); CREATE TABLE IF NOT EXISTS gateway_config(id BOOLEAN PRIMARY KEY DEFAULT true CHECK(id),base_url TEXT NOT NULL DEFAULT '',model TEXT NOT NULL DEFAULT '',encrypted_key BYTEA,enabled BOOLEAN NOT NULL DEFAULT false,updated_at TIMESTAMPTZ NOT NULL DEFAULT now()); INSERT INTO gateway_config(id)VALUES(true)ON CONFLICT DO NOTHING; CREATE TABLE IF NOT EXISTS gateway_usage(id BIGSERIAL PRIMARY KEY,owner_id TEXT NOT NULL,agent_id TEXT NOT NULL,session_id TEXT NOT NULL,run_id TEXT NOT NULL,status TEXT NOT NULL,prompt_tokens BIGINT NOT NULL,completion_tokens BIGINT NOT NULL,latency_ms BIGINT NOT NULL,created_at TIMESTAMPTZ NOT NULL DEFAULT now()); CREATE INDEX IF NOT EXISTS gateway_usage_created ON gateway_usage(created_at); INSERT INTO gateway_schema_migrations(version)VALUES(1)ON CONFLICT DO NOTHING;`)
	if err == nil {
		_, err = tx.Exec(ctx, `ALTER TABLE gateway_config ADD COLUMN IF NOT EXISTS context_window_tokens BIGINT NOT NULL DEFAULT 16384 CHECK(context_window_tokens BETWEEN 4096 AND 2000000); INSERT INTO gateway_schema_migrations(version)VALUES(2)ON CONFLICT DO NOTHING;`)
	}
	if err == nil {
		_, err = tx.Exec(ctx, `ALTER TABLE gateway_config ADD COLUMN IF NOT EXISTS web_search_encrypted_key BYTEA, ADD COLUMN IF NOT EXISTS web_search_enabled BOOLEAN NOT NULL DEFAULT false; INSERT INTO gateway_schema_migrations(version)VALUES(3)ON CONFLICT DO NOTHING;`)
	}
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

// Record inserts a model usage audit record.
func (s *PGStore) Record(ctx context.Context, u domain.Usage) error {
	_, err := s.DB.Exec(ctx, `INSERT INTO gateway_usage(owner_id,agent_id,session_id,run_id,status,prompt_tokens,completion_tokens,latency_ms)VALUES($1,$2,$3,$4,$5,$6,$7,$8);`, u.OwnerID, u.AgentID, u.SessionID, u.RunID, u.Status, u.PromptTokens, u.CompletionTokens, u.LatencyMS)
	return err
}

// Prune deletes old or excess usage audit rows.
func (s *PGStore) Prune(ctx context.Context) error {
	_, err := s.DB.Exec(ctx, `DELETE FROM gateway_usage WHERE id IN(SELECT id FROM gateway_usage WHERE created_at<now()-interval '30 days' OR id <= (SELECT coalesce(max(id),0)-100000 FROM gateway_usage) ORDER BY id LIMIT 10000)`)
	return err
}
