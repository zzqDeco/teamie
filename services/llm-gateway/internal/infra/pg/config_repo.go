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

// Load assembles a read-only view of the three independently stored resources.
func (s *PGStore) Load(ctx context.Context) (domain.Config, error) {
	var c domain.Config
	err := s.DB.Conn(ctx).QueryRow(ctx, `SELECT r.default_model_id,r.updated_at,w.encrypted_key,w.enabled,w.updated_at FROM gateway_model_routing r CROSS JOIN gateway_web_search_config w WHERE r.id AND w.id`).Scan(&c.Routing.DefaultModelID, &c.Routing.UpdatedAt, &c.WebSearch.CipherKey, &c.WebSearch.Enabled, &c.WebSearch.UpdatedAt)
	if err != nil {
		return domain.Config{}, err
	}
	c.Models, err = s.loadModels(ctx, false)
	return c, err
}

// UpdateModels serializes catalog mutations with routing validation. It does not
// modify routing intent, search settings or the retired gateway_config record.
func (s *PGStore) UpdateModels(ctx context.Context, change func([]domain.ModelProfile) ([]domain.ModelProfile, error)) (domain.Config, error) {
	err := s.WithinTx(ctx, func(ctx context.Context) error {
		if _, err := s.DB.Conn(ctx).Exec(ctx, `SELECT id FROM gateway_model_routing WHERE id FOR UPDATE`); err != nil {
			return err
		}
		models, err := s.loadModels(ctx, true)
		if err != nil {
			return err
		}
		models, err = change(models)
		if err != nil {
			return err
		}
		keep := make([]string, 0, len(models))
		for _, m := range models {
			keep = append(keep, m.ID)
		}
		if _, err = s.DB.Conn(ctx).Exec(ctx, `DELETE FROM gateway_models WHERE NOT (id = ANY($1::text[]))`, keep); err != nil {
			return err
		}
		for _, m := range models {
			if _, err = s.DB.Conn(ctx).Exec(ctx, `INSERT INTO gateway_models(id,display_name,description,base_url,model,encrypted_key,context_window_tokens,enabled) VALUES($1,$2,$3,$4,$5,$6,$7,$8) ON CONFLICT(id) DO UPDATE SET display_name=EXCLUDED.display_name,description=EXCLUDED.description,base_url=EXCLUDED.base_url,model=EXCLUDED.model,encrypted_key=EXCLUDED.encrypted_key,context_window_tokens=EXCLUDED.context_window_tokens,enabled=EXCLUDED.enabled,updated_at=now() WHERE (gateway_models.display_name,gateway_models.description,gateway_models.base_url,gateway_models.model,gateway_models.encrypted_key,gateway_models.context_window_tokens,gateway_models.enabled) IS DISTINCT FROM (EXCLUDED.display_name,EXCLUDED.description,EXCLUDED.base_url,EXCLUDED.model,EXCLUDED.encrypted_key,EXCLUDED.context_window_tokens,EXCLUDED.enabled)`, m.ID, m.DisplayName, m.Description, m.BaseURL, m.Model, m.CipherKey, m.ContextWindow(), m.Enabled); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return domain.Config{}, err
	}
	return s.Load(ctx)
}

func (s *PGStore) UpdateRouting(ctx context.Context, routing domain.ModelRoutingConfig) (domain.Config, error) {
	err := s.WithinTx(ctx, func(ctx context.Context) error {
		if _, err := s.DB.Conn(ctx).Exec(ctx, `SELECT id FROM gateway_model_routing WHERE id FOR UPDATE`); err != nil {
			return err
		}
		if routing.DefaultModelID != "" {
			var exists bool
			if err := s.DB.Conn(ctx).QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM gateway_models WHERE id=$1)`, routing.DefaultModelID).Scan(&exists); err != nil {
				return err
			}
			if !exists {
				return domain.Err(404, "not_found", "Model profile not found")
			}
		}
		_, err := s.DB.Conn(ctx).Exec(ctx, `UPDATE gateway_model_routing SET default_model_id=$1,updated_at=now() WHERE id`, routing.DefaultModelID)
		return err
	})
	if err != nil {
		return domain.Config{}, err
	}
	return s.Load(ctx)
}

func (s *PGStore) UpdateWebSearch(ctx context.Context, change func(domain.WebSearchConfig) (domain.WebSearchConfig, error)) (domain.Config, error) {
	err := s.WithinTx(ctx, func(ctx context.Context) error {
		var old domain.WebSearchConfig
		if err := s.DB.Conn(ctx).QueryRow(ctx, `SELECT encrypted_key,enabled,updated_at FROM gateway_web_search_config WHERE id FOR UPDATE`).Scan(&old.CipherKey, &old.Enabled, &old.UpdatedAt); err != nil {
			return err
		}
		next, err := change(old)
		if err != nil {
			return err
		}
		_, err = s.DB.Conn(ctx).Exec(ctx, `UPDATE gateway_web_search_config SET encrypted_key=$1,enabled=$2,updated_at=now() WHERE id`, next.CipherKey, next.Enabled)
		return err
	})
	if err != nil {
		return domain.Config{}, err
	}
	return s.Load(ctx)
}
