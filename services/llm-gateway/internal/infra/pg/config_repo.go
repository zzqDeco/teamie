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
	if err := postgres.MigrateSQL(ctx, db, 7463260102, SchemaSQL, nil); err != nil {
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
	err = row.Scan(&c.BaseURL, &c.Model, &c.CipherKey, &c.Enabled, &c.UpdatedAt, &c.ContextWindowTokens, &c.WebSearchCipherKey, &c.WebSearchEnabled, &c.DefaultModelID)
	return
}

func (s *PGStore) loadModels(ctx context.Context, forUpdate bool) ([]domain.ModelProfile, error) {
	query := `SELECT id,display_name,description,base_url,model,encrypted_key,context_window_tokens,enabled,created_at,updated_at FROM gateway_models ORDER BY created_at ASC, id ASC`
	if forUpdate {
		query += ` FOR UPDATE`
	}
	rows, err := s.DB.Conn(ctx).Query(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var models []domain.ModelProfile
	for rows.Next() {
		var m domain.ModelProfile
		if err := rows.Scan(&m.ID, &m.DisplayName, &m.Description, &m.BaseURL, &m.Model, &m.CipherKey, &m.ContextWindowTokens, &m.Enabled, &m.CreatedAt, &m.UpdatedAt); err != nil {
			return nil, err
		}
		models = append(models, m)
	}
	return models, rows.Err()
}

// Load retrieves the current gateway configuration and model catalog.
func (s *PGStore) Load(ctx context.Context) (domain.Config, error) {
	c, err := scan(s.DB.Conn(ctx).QueryRow(ctx, `SELECT base_url,model,encrypted_key,enabled,updated_at,context_window_tokens,web_search_encrypted_key,web_search_enabled,default_model_id FROM gateway_config WHERE id`))
	if err != nil {
		return domain.Config{}, err
	}
	models, err := s.loadModels(ctx, false)
	if err != nil {
		return domain.Config{}, err
	}
	c.Models = models
	if len(c.Models) > 0 {
		c.SyncPrimaryFromModels()
	}
	return c, nil
}

// LockForUpdate locks and returns the singleton gateway_config row and gateway_models within the active transaction.
func (s *PGStore) LockForUpdate(ctx context.Context) (domain.Config, error) {
	c, err := scan(s.DB.Conn(ctx).QueryRow(ctx, `SELECT base_url,model,encrypted_key,enabled,updated_at,context_window_tokens,web_search_encrypted_key,web_search_enabled,default_model_id FROM gateway_config WHERE id FOR UPDATE`))
	if err != nil {
		return domain.Config{}, err
	}
	models, err := s.loadModels(ctx, true)
	if err != nil {
		return domain.Config{}, err
	}
	c.Models = models
	return c, nil
}

// Update persists merged gateway_config fields and gateway_models rows within the active transaction.
func (s *PGStore) Update(ctx context.Context, c domain.Config) (domain.Config, error) {
	defID := c.DefaultModelID
	if defID == "" {
		defID = "default"
	}
	updated, err := scan(s.DB.Conn(ctx).QueryRow(ctx, `UPDATE gateway_config SET base_url=$1,model=$2,encrypted_key=$3,enabled=$4,context_window_tokens=$5,web_search_encrypted_key=$6,web_search_enabled=$7,default_model_id=$8,updated_at=now() WHERE id RETURNING base_url,model,encrypted_key,enabled,updated_at,context_window_tokens,web_search_encrypted_key,web_search_enabled,default_model_id`, c.BaseURL, c.Model, c.CipherKey, c.Enabled, c.ContextWindowTokens, c.WebSearchCipherKey, c.WebSearchEnabled, defID))
	if err != nil {
		return domain.Config{}, err
	}
	keepIDs := make([]string, 0, len(c.Models))
	for _, m := range c.Models {
		keepIDs = append(keepIDs, m.ID)
	}
	if len(keepIDs) == 0 {
		if _, err := s.DB.Conn(ctx).Exec(ctx, `DELETE FROM gateway_models`); err != nil {
			return domain.Config{}, err
		}
	} else {
		if _, err := s.DB.Conn(ctx).Exec(ctx, `DELETE FROM gateway_models WHERE NOT (id = ANY($1))`, keepIDs); err != nil {
			return domain.Config{}, err
		}
		for _, m := range c.Models {
			displayName := m.DisplayName
			if displayName == "" {
				displayName = m.Model
			}
			if _, err := s.DB.Conn(ctx).Exec(ctx, `INSERT INTO gateway_models(id,display_name,description,base_url,model,encrypted_key,context_window_tokens,enabled,created_at,updated_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,now(),now()) ON CONFLICT(id) DO UPDATE SET display_name=EXCLUDED.display_name,description=EXCLUDED.description,base_url=EXCLUDED.base_url,model=EXCLUDED.model,encrypted_key=EXCLUDED.encrypted_key,context_window_tokens=EXCLUDED.context_window_tokens,enabled=EXCLUDED.enabled,updated_at=now()`, m.ID, displayName, m.Description, m.BaseURL, m.Model, m.CipherKey, m.ContextWindow(), m.Enabled); err != nil {
				return domain.Config{}, err
			}
		}
	}
	models, err := s.loadModels(ctx, false)
	if err != nil {
		return domain.Config{}, err
	}
	updated.Models = models
	if len(updated.Models) > 0 {
		updated.SyncPrimaryFromModels()
	}
	return updated, nil
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
