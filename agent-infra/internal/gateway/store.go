package gateway

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Config struct {
	BaseURL   string    `json:"baseUrl"`
	Model     string    `json:"model"`
	CipherKey []byte    `json:"-"`
	Enabled   bool      `json:"enabled"`
	UpdatedAt time.Time `json:"updatedAt"`
}

func (c Config) Configured() bool { return c.BaseURL != "" && c.Model != "" && len(c.CipherKey) > 0 }
func (c Config) View() map[string]any {
	status := "unconfigured"
	if c.Configured() {
		status = "disabled"
		if c.Enabled {
			status = "ready"
		}
	}
	return map[string]any{"config": map[string]any{"baseUrl": c.BaseURL, "model": c.Model, "hasApiKey": len(c.CipherKey) > 0, "enabled": c.Enabled, "configured": c.Configured(), "updatedAt": c.UpdatedAt}, "status": status}
}

type Usage struct {
	OwnerID, AgentID, SessionID, RunID, Status string
	PromptTokens, CompletionTokens             int64
	LatencyMS                                  int64
}
type Store interface {
	Load(context.Context) (Config, error)
	Save(context.Context, Config, *string, bool, Cipher) (Config, error)
	Record(context.Context, Usage) error
	Ready(context.Context) error
}
type PGStore struct{ DB *pgxpool.Pool }

func Open(ctx context.Context, url string) (*PGStore, error) {
	cfg, e := pgxpool.ParseConfig(url)
	if e != nil {
		return nil, errors.New("invalid gateway database configuration")
	}
	cfg.MaxConns = 3
	cfg.MinConns = 0
	cfg.MaxConnIdleTime = time.Minute
	db, e := pgxpool.NewWithConfig(ctx, cfg)
	if e != nil {
		return nil, e
	}
	s := &PGStore{db}
	tx, e := db.Begin(ctx)
	if e != nil {
		db.Close()
		return nil, e
	}
	defer tx.Rollback(ctx)
	_, e = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(7463260102); CREATE TABLE IF NOT EXISTS gateway_schema_migrations(version INTEGER PRIMARY KEY,applied_at TIMESTAMPTZ NOT NULL DEFAULT now()); CREATE TABLE IF NOT EXISTS gateway_config(id BOOLEAN PRIMARY KEY DEFAULT true CHECK(id),base_url TEXT NOT NULL DEFAULT '',model TEXT NOT NULL DEFAULT '',encrypted_key BYTEA,enabled BOOLEAN NOT NULL DEFAULT false,updated_at TIMESTAMPTZ NOT NULL DEFAULT now()); INSERT INTO gateway_config(id)VALUES(true)ON CONFLICT DO NOTHING; CREATE TABLE IF NOT EXISTS gateway_usage(id BIGSERIAL PRIMARY KEY,owner_id TEXT NOT NULL,agent_id TEXT NOT NULL,session_id TEXT NOT NULL,run_id TEXT NOT NULL,status TEXT NOT NULL,prompt_tokens BIGINT NOT NULL,completion_tokens BIGINT NOT NULL,latency_ms BIGINT NOT NULL,created_at TIMESTAMPTZ NOT NULL DEFAULT now()); CREATE INDEX IF NOT EXISTS gateway_usage_created ON gateway_usage(created_at); INSERT INTO gateway_schema_migrations(version)VALUES(1)ON CONFLICT DO NOTHING;`)
	if e == nil {
		e = tx.Commit(ctx)
	}
	if e != nil {
		db.Close()
		return nil, e
	}
	return s, nil
}
func (s *PGStore) Ready(ctx context.Context) error { return s.DB.Ping(ctx) }
func scan(row pgx.Row) (c Config, e error) {
	e = row.Scan(&c.BaseURL, &c.Model, &c.CipherKey, &c.Enabled, &c.UpdatedAt)
	return
}
func (s *PGStore) Load(ctx context.Context) (Config, error) {
	return scan(s.DB.QueryRow(ctx, `SELECT base_url,model,encrypted_key,enabled,updated_at FROM gateway_config WHERE id`))
}
func (s *PGStore) Save(ctx context.Context, c Config, key *string, clear bool, crypto Cipher) (Config, error) {
	tx, e := s.DB.Begin(ctx)
	if e != nil {
		return Config{}, e
	}
	defer tx.Rollback(ctx)
	old, e := scan(tx.QueryRow(ctx, `SELECT base_url,model,encrypted_key,enabled,updated_at FROM gateway_config WHERE id FOR UPDATE`))
	if e != nil {
		return Config{}, e
	}
	c.CipherKey = old.CipherKey
	if clear {
		c.CipherKey = nil
	}
	if key != nil {
		c.CipherKey, e = crypto.Encrypt(*key)
		if e != nil {
			return Config{}, e
		}
	}
	c, e = scan(tx.QueryRow(ctx, `UPDATE gateway_config SET base_url=$1,model=$2,encrypted_key=$3,enabled=$4,updated_at=now() WHERE id RETURNING base_url,model,encrypted_key,enabled,updated_at`, c.BaseURL, c.Model, c.CipherKey, c.Enabled))
	if e != nil {
		return Config{}, e
	}
	return c, tx.Commit(ctx)
}
func (s *PGStore) Record(ctx context.Context, u Usage) error {
	_, e := s.DB.Exec(ctx, `INSERT INTO gateway_usage(owner_id,agent_id,session_id,run_id,status,prompt_tokens,completion_tokens,latency_ms)VALUES($1,$2,$3,$4,$5,$6,$7,$8);`, u.OwnerID, u.AgentID, u.SessionID, u.RunID, u.Status, u.PromptTokens, u.CompletionTokens, u.LatencyMS)
	return e
}
func (s *PGStore) Prune(ctx context.Context) error {
	_, e := s.DB.Exec(ctx, `DELETE FROM gateway_usage WHERE id IN(SELECT id FROM gateway_usage WHERE created_at<now()-interval '30 days' OR id <= (SELECT coalesce(max(id),0)-100000 FROM gateway_usage) ORDER BY id LIMIT 10000)`)
	return e
}
