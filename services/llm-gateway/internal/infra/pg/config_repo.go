// Package pg implements the PostgreSQL ConfigStore outbound port for the Model Gateway.
package pg

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"teamie/pkg/postgres"
	"teamie/services/llm-gateway/internal/domain"
)

// GatewaySchemaVersion is the current durable PostgreSQL schema version for llm-gateway.
const GatewaySchemaVersion = 6

// ErrUnsupportedGatewaySchema indicates a newer or incomplete gateway_schema_migrations ledger.
var ErrUnsupportedGatewaySchema = errors.New("unsupported gateway schema")

//go:embed migrations/001_gateway.sql
var SchemaSQL string

var _ domain.ConfigStore = (*PGStore)(nil)

// PGStore persists gateway configuration and usage records in PostgreSQL using pkg/postgres.DB.
type PGStore struct {
	DB *postgres.DB
}

// ValidateGatewaySchema enforces contiguous forward schema versions up to GatewaySchemaVersion
// before executing DDL, preventing older binaries from running against newer schemas.
func ValidateGatewaySchema(ctx context.Context, q postgres.Querier) error {
	var exists bool
	if err := q.QueryRow(ctx, `SELECT to_regclass('gateway_schema_migrations') IS NOT NULL`).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return nil
	}
	rows, err := q.Query(ctx, `SELECT version FROM gateway_schema_migrations ORDER BY version`)
	if err != nil {
		return err
	}
	defer rows.Close()
	want := 1
	for rows.Next() {
		var version int
		if err = rows.Scan(&version); err != nil {
			return err
		}
		if version != want || version > GatewaySchemaVersion {
			return fmt.Errorf("%w: found version %d, expected %d through %d", ErrUnsupportedGatewaySchema, version, want, GatewaySchemaVersion)
		}
		want++
	}
	if err = rows.Err(); err != nil {
		return err
	}
	if want == 1 {
		return fmt.Errorf("%w: existing ledger is empty", ErrUnsupportedGatewaySchema)
	}
	return nil
}

// Open connects to PostgreSQL using pkg/postgres.OpenDB and runs idempotent migrations inside WithinTx.
func Open(ctx context.Context, url string) (*PGStore, error) {
	db, err := postgres.OpenDB(ctx, url, postgres.WithMaxConns(3), postgres.WithMinConns(0), postgres.WithMaxConnIdleTime(time.Minute))
	if err != nil {
		return nil, errors.New("invalid gateway database configuration")
	}
	s := &PGStore{DB: db}
	if err := postgres.MigrateSQL(ctx, db, 7463260102, SchemaSQL, ValidateGatewaySchema); err != nil {
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

func (s *PGStore) loadProviders(ctx context.Context, forUpdate bool) ([]domain.ProviderProfile, error) {
	query := `SELECT id,display_name,base_url,encrypted_key,enabled,created_at,updated_at FROM gateway_providers ORDER BY created_at ASC, id ASC`
	if forUpdate {
		query += ` FOR UPDATE`
	}
	rows, err := s.DB.Conn(ctx).Query(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var providers []domain.ProviderProfile
	for rows.Next() {
		var p domain.ProviderProfile
		if err := rows.Scan(&p.ID, &p.DisplayName, &p.BaseURL, &p.CipherKey, &p.Enabled, &p.CreatedAt, &p.UpdatedAt); err != nil {
			return nil, err
		}
		providers = append(providers, p)
	}
	return providers, rows.Err()
}

func (s *PGStore) loadModels(ctx context.Context, forUpdate bool) ([]domain.ModelProfile, error) {
	query := `SELECT id,provider_id,display_name,description,model,context_window_tokens,enabled,created_at,updated_at FROM gateway_models ORDER BY created_at ASC, id ASC`
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
		if err := rows.Scan(&m.ID, &m.ProviderID, &m.DisplayName, &m.Description, &m.Model, &m.ContextWindowTokens, &m.Enabled, &m.CreatedAt, &m.UpdatedAt); err != nil {
			return nil, err
		}
		models = append(models, m)
	}
	return models, rows.Err()
}

// Load assembles a read-only view of providers, models, routing and web search
// from a single SQL MVCC snapshot.
func (s *PGStore) Load(ctx context.Context) (domain.Config, error) {
	rows, err := s.DB.Conn(ctx).Query(ctx, `SELECT r.default_model_id,r.updated_at,w.encrypted_key,w.enabled,w.updated_at,p.id,p.display_name,p.base_url,p.encrypted_key,p.enabled,p.created_at,p.updated_at,m.id,m.provider_id,m.display_name,m.description,m.model,m.context_window_tokens,m.enabled,m.created_at,m.updated_at FROM gateway_model_routing r CROSS JOIN gateway_web_search_config w LEFT JOIN gateway_providers p ON true LEFT JOIN gateway_models m ON m.provider_id=p.id WHERE r.id AND w.id ORDER BY p.created_at ASC, p.id ASC, m.created_at ASC, m.id ASC`)
	if err != nil {
		return domain.Config{}, err
	}
	defer rows.Close()
	var (
		c             domain.Config
		sawRow        bool
		seenProviders = make(map[string]bool)
	)
	for rows.Next() {
		var (
			defaultModelID                                      string
			routingUpdatedAt, webUpdatedAt                      time.Time
			webCipherKey, pCipherKey                            []byte
			webEnabled                                          bool
			pID, pDisplayName, pBaseURL                         *string
			pEnabled                                            *bool
			pCreatedAt, pUpdatedAt                              *time.Time
			mID, mProviderID, mDisplayName, mDescription, mModel *string
			mContextWindowTokens                                *int64
			mEnabled                                            *bool
			mCreatedAt, mUpdatedAt                              *time.Time
		)
		if err := rows.Scan(
			&defaultModelID,
			&routingUpdatedAt,
			&webCipherKey,
			&webEnabled,
			&webUpdatedAt,
			&pID,
			&pDisplayName,
			&pBaseURL,
			&pCipherKey,
			&pEnabled,
			&pCreatedAt,
			&pUpdatedAt,
			&mID,
			&mProviderID,
			&mDisplayName,
			&mDescription,
			&mModel,
			&mContextWindowTokens,
			&mEnabled,
			&mCreatedAt,
			&mUpdatedAt,
		); err != nil {
			return domain.Config{}, err
		}
		if !sawRow {
			sawRow = true
			c.Routing = domain.ModelRoutingConfig{
				DefaultModelID: defaultModelID,
				UpdatedAt:      routingUpdatedAt,
			}
			c.WebSearch = domain.WebSearchConfig{
				CipherKey: webCipherKey,
				Enabled:   webEnabled,
				UpdatedAt: webUpdatedAt,
			}
		}
		if pID != nil && !seenProviders[*pID] {
			seenProviders[*pID] = true
			c.Providers = append(c.Providers, domain.ProviderProfile{
				ID:          *pID,
				DisplayName: *pDisplayName,
				BaseURL:     *pBaseURL,
				CipherKey:   pCipherKey,
				Enabled:     *pEnabled,
				CreatedAt:   *pCreatedAt,
				UpdatedAt:   *pUpdatedAt,
			})
		}
		if mID != nil {
			c.Models = append(c.Models, domain.ModelProfile{
				ID:                  *mID,
				ProviderID:          *mProviderID,
				DisplayName:         *mDisplayName,
				Description:         *mDescription,
				Model:               *mModel,
				ContextWindowTokens: *mContextWindowTokens,
				Enabled:             *mEnabled,
				CreatedAt:           *mCreatedAt,
				UpdatedAt:           *mUpdatedAt,
			})
		}
	}
	if err := rows.Err(); err != nil {
		return domain.Config{}, err
	}
	if !sawRow {
		return domain.Config{}, errors.New("gateway configuration not initialized")
	}
	slices.SortFunc(c.Models, func(a, b domain.ModelProfile) int {
		if !a.CreatedAt.Equal(b.CreatedAt) {
			if a.CreatedAt.Before(b.CreatedAt) {
				return -1
			}
			return 1
		}
		return strings.Compare(a.ID, b.ID)
	})
	return c, nil
}

// UpdateCatalog serializes provider and model catalog mutations with routing validation.
// It does not modify routing intent, search settings or the retired gateway_config record.
func (s *PGStore) UpdateCatalog(ctx context.Context, change func([]domain.ProviderProfile, []domain.ModelProfile) ([]domain.ProviderProfile, []domain.ModelProfile, error)) (domain.Config, error) {
	var c domain.Config
	err := s.WithinTx(ctx, func(ctx context.Context) error {
		if _, err := s.DB.Conn(ctx).Exec(ctx, `SELECT id FROM gateway_model_routing WHERE id FOR UPDATE`); err != nil {
			return err
		}
		providers, err := s.loadProviders(ctx, true)
		if err != nil {
			return err
		}
		models, err := s.loadModels(ctx, true)
		if err != nil {
			return err
		}
		nextProviders, nextModels, err := change(providers, models)
		if err != nil {
			return err
		}
		providerSet := make(map[string]bool, len(nextProviders))
		keepProviders := make([]string, 0, len(nextProviders))
		for _, p := range nextProviders {
			providerSet[p.ID] = true
			keepProviders = append(keepProviders, p.ID)
		}
		keepModels := make([]string, 0, len(nextModels))
		for _, m := range nextModels {
			if !providerSet[m.ProviderID] {
				return domain.Err(404, "not_found", "Model provider not found")
			}
			keepModels = append(keepModels, m.ID)
		}
		if _, err = s.DB.Conn(ctx).Exec(ctx, `DELETE FROM gateway_models WHERE NOT (id = ANY($1::text[]))`, keepModels); err != nil {
			return err
		}
		for _, p := range nextProviders {
			if _, err = s.DB.Conn(ctx).Exec(ctx, `INSERT INTO gateway_providers(id,display_name,base_url,encrypted_key,enabled) VALUES($1,$2,$3,$4,$5) ON CONFLICT(id) DO UPDATE SET display_name=EXCLUDED.display_name,base_url=EXCLUDED.base_url,encrypted_key=EXCLUDED.encrypted_key,enabled=EXCLUDED.enabled,updated_at=now() WHERE (gateway_providers.display_name,gateway_providers.base_url,gateway_providers.encrypted_key,gateway_providers.enabled) IS DISTINCT FROM (EXCLUDED.display_name,EXCLUDED.base_url,EXCLUDED.encrypted_key,EXCLUDED.enabled)`, p.ID, p.DisplayName, p.BaseURL, p.CipherKey, p.Enabled); err != nil {
				return err
			}
		}
		for _, m := range nextModels {
			if _, err = s.DB.Conn(ctx).Exec(ctx, `INSERT INTO gateway_models(id,provider_id,display_name,description,model,context_window_tokens,enabled) VALUES($1,$2,$3,$4,$5,$6,$7) ON CONFLICT(id) DO UPDATE SET provider_id=EXCLUDED.provider_id,display_name=EXCLUDED.display_name,description=EXCLUDED.description,model=EXCLUDED.model,context_window_tokens=EXCLUDED.context_window_tokens,enabled=EXCLUDED.enabled,updated_at=now() WHERE (gateway_models.provider_id,gateway_models.display_name,gateway_models.description,gateway_models.model,gateway_models.context_window_tokens,gateway_models.enabled) IS DISTINCT FROM (EXCLUDED.provider_id,EXCLUDED.display_name,EXCLUDED.description,EXCLUDED.model,EXCLUDED.context_window_tokens,EXCLUDED.enabled)`, m.ID, m.ProviderID, m.DisplayName, m.Description, m.Model, m.ContextWindow(), m.Enabled); err != nil {
				return err
			}
		}
		if _, err = s.DB.Conn(ctx).Exec(ctx, `DELETE FROM gateway_providers WHERE NOT (id = ANY($1::text[]))`, keepProviders); err != nil {
			return err
		}
		c, err = s.Load(ctx)
		return err
	})
	if err != nil {
		return domain.Config{}, err
	}
	return c, nil
}

func (s *PGStore) UpdateRouting(ctx context.Context, routing domain.ModelRoutingConfig) (domain.Config, error) {
	var c domain.Config
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
		if _, err := s.DB.Conn(ctx).Exec(ctx, `UPDATE gateway_model_routing SET default_model_id=$1,updated_at=now() WHERE id`, routing.DefaultModelID); err != nil {
			return err
		}
		var err error
		c, err = s.Load(ctx)
		return err
	})
	if err != nil {
		return domain.Config{}, err
	}
	return c, nil
}

func (s *PGStore) UpdateWebSearch(ctx context.Context, change func(domain.WebSearchConfig) (domain.WebSearchConfig, error)) (domain.Config, error) {
	var c domain.Config
	err := s.WithinTx(ctx, func(ctx context.Context) error {
		var old domain.WebSearchConfig
		if err := s.DB.Conn(ctx).QueryRow(ctx, `SELECT encrypted_key,enabled,updated_at FROM gateway_web_search_config WHERE id FOR UPDATE`).Scan(&old.CipherKey, &old.Enabled, &old.UpdatedAt); err != nil {
			return err
		}
		next, err := change(old)
		if err != nil {
			return err
		}
		if _, err = s.DB.Conn(ctx).Exec(ctx, `UPDATE gateway_web_search_config SET encrypted_key=$1,enabled=$2,updated_at=now() WHERE id`, next.CipherKey, next.Enabled); err != nil {
			return err
		}
		c, err = s.Load(ctx)
		return err
	})
	if err != nil {
		return domain.Config{}, err
	}
	return c, nil
}
