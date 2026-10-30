// Package pg implements the PostgreSQL ConfigStore outbound port for the Model Gateway.
package pg

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"teamie/pkg/postgres"
	"teamie/services/llm-gateway/internal/domain"
)

// GatewaySchemaVersion is the current durable PostgreSQL schema version for llm-gateway.
const GatewaySchemaVersion = 6

// DefaultPingCacheTTL bounds how long repeated idle Agent Ping checks reuse an
// unchanged configuration snapshot before re-querying PostgreSQL. Catalog,
// routing, and web-search mutations through PGStore invalidate the cache immediately.
const DefaultPingCacheTTL = 5 * time.Second

const (
	gatewayMigrationLockID = 7463260102
	pingLoadTimeout        = 3 * time.Second

	loadRowRouting   = 1
	loadRowWebSearch = 2
	loadRowProvider  = 3
	loadRowModel     = 4
)

const gatewayMissingStatsSQL = `
SELECT EXISTS (
	SELECT 1
	FROM pg_class
	WHERE oid IN (
		'gateway_model_routing'::regclass,
		'gateway_web_search_config'::regclass,
		'gateway_providers'::regclass,
		'gateway_models'::regclass
	)
	  AND reltuples < 0
)`

const gatewayAnalyzeConfigSQL = `ANALYZE gateway_model_routing, gateway_web_search_config, gateway_providers, gateway_models`

// LoadConfigSQL reads routing, web-search settings, providers, and models in one
// join-free, sort-free PostgreSQL MVCC statement snapshot so missing table
// statistics can never multiply row estimates across singleton and catalog tables.
const LoadConfigSQL = `
SELECT
	1 AS row_kind,
	r.default_model_id AS id,
	'' AS parent_id,
	'' AS display_name,
	'' AS description,
	'' AS target,
	NULL::bytea AS encrypted_key,
	0::bigint AS context_window_tokens,
	false AS enabled,
	r.updated_at AS created_at,
	r.updated_at AS updated_at
FROM (SELECT default_model_id, updated_at FROM gateway_model_routing WHERE id = true LIMIT 1) r
UNION ALL
SELECT
	2 AS row_kind,
	'',
	'',
	'',
	'',
	'',
	w.encrypted_key,
	0::bigint,
	w.enabled,
	w.updated_at,
	w.updated_at
FROM (SELECT encrypted_key, enabled, updated_at FROM gateway_web_search_config WHERE id = true LIMIT 1) w
UNION ALL
SELECT
	3 AS row_kind,
	p.id,
	'',
	p.display_name,
	'',
	p.base_url,
	p.encrypted_key,
	0::bigint,
	p.enabled,
	p.created_at,
	p.updated_at
FROM gateway_providers p
UNION ALL
SELECT
	4 AS row_kind,
	m.id,
	m.provider_id,
	m.display_name,
	m.description,
	m.model,
	NULL::bytea,
	m.context_window_tokens,
	m.enabled,
	m.created_at,
	m.updated_at
FROM gateway_models m`

// ErrUnsupportedGatewaySchema indicates a newer or incomplete gateway_schema_migrations ledger.
var ErrUnsupportedGatewaySchema = errors.New("unsupported gateway schema")

//go:embed migrations/001_gateway.sql
var SchemaSQL string

var _ domain.ConfigStore = (*PGStore)(nil)

type pingFlight struct {
	done chan struct{}
	cfg  domain.Config
	err  error
}

// PGStore persists gateway configuration and usage records in PostgreSQL using pkg/postgres.DB.
type PGStore struct {
	DB           *postgres.DB
	PingCacheTTL time.Duration
	Now          func() time.Time

	cacheMu      sync.Mutex
	writeGen     uint64
	activeWrites int
	cachedCfg    domain.Config
	cachedUntil  time.Time
	cachedValid  bool
	inFlight     *pingFlight
	inFlightGen  uint64
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

func ensureGatewayConfigStats(ctx context.Context, db *postgres.DB) error {
	return db.WithinTx(ctx, func(txCtx context.Context) error {
		if err := db.AdvisoryXactLock(txCtx, gatewayMigrationLockID); err != nil {
			return err
		}
		conn := db.Conn(txCtx)
		var missing bool
		if err := conn.QueryRow(txCtx, gatewayMissingStatsSQL).Scan(&missing); err != nil {
			return err
		}
		if !missing {
			return nil
		}
		_, err := conn.Exec(txCtx, gatewayAnalyzeConfigSQL)
		return err
	})
}

// Open connects to PostgreSQL using pkg/postgres.OpenDB and runs idempotent migrations inside WithinTx.
func Open(ctx context.Context, url string) (*PGStore, error) {
	db, err := postgres.OpenDB(ctx, url, postgres.WithMaxConns(3), postgres.WithMinConns(0), postgres.WithMaxConnIdleTime(time.Minute))
	if err != nil {
		return nil, errors.New("invalid gateway database configuration")
	}
	s := &PGStore{DB: db}
	if err := postgres.MigrateSQL(ctx, db, gatewayMigrationLockID, SchemaSQL, ValidateGatewaySchema); err != nil {
		db.Close()
		return nil, err
	}
	if err := ensureGatewayConfigStats(ctx, db); err != nil {
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

func (s *PGStore) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *PGStore) pingTTL() time.Duration {
	if s.PingCacheTTL > 0 {
		return s.PingCacheTTL
	}
	return DefaultPingCacheTTL
}

func cloneBytes(b []byte) []byte {
	if len(b) == 0 {
		return nil
	}
	return slices.Clone(b)
}

func cloneConfig(c domain.Config) domain.Config {
	out := c
	out.WebSearch.CipherKey = cloneBytes(c.WebSearch.CipherKey)
	if c.Providers != nil {
		out.Providers = make([]domain.ProviderProfile, len(c.Providers))
		for i, p := range c.Providers {
			p.CipherKey = cloneBytes(p.CipherKey)
			out.Providers[i] = p
		}
	}
	if c.Models != nil {
		out.Models = slices.Clone(c.Models)
	}
	return out
}

func (s *PGStore) beginConfigWrite() {
	s.cacheMu.Lock()
	s.activeWrites++
	s.writeGen++
	s.cachedValid = false
	s.cachedCfg = domain.Config{}
	s.inFlight = nil
	s.cacheMu.Unlock()
}

func (s *PGStore) endConfigWrite() {
	s.cacheMu.Lock()
	s.activeWrites--
	s.writeGen++
	s.cachedValid = false
	s.cachedCfg = domain.Config{}
	s.inFlight = nil
	s.cacheMu.Unlock()
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
// from a single join-free SQL MVCC snapshot.
func (s *PGStore) Load(ctx context.Context) (domain.Config, error) {
	rows, err := s.DB.Conn(ctx).Query(ctx, LoadConfigSQL)
	if err != nil {
		return domain.Config{}, err
	}
	defer rows.Close()
	var (
		c            domain.Config
		sawRouting   bool
		sawWebSearch bool
	)
	for rows.Next() {
		var (
			rowKind             int32
			id, parentID        string
			displayName         string
			description, target string
			cipherKey           []byte
			contextWindowTokens int64
			enabled             bool
			createdAt           time.Time
			updatedAt           time.Time
		)
		if err := rows.Scan(
			&rowKind,
			&id,
			&parentID,
			&displayName,
			&description,
			&target,
			&cipherKey,
			&contextWindowTokens,
			&enabled,
			&createdAt,
			&updatedAt,
		); err != nil {
			return domain.Config{}, err
		}
		switch rowKind {
		case loadRowRouting:
			sawRouting = true
			c.Routing = domain.ModelRoutingConfig{
				DefaultModelID: id,
				UpdatedAt:      updatedAt,
			}
		case loadRowWebSearch:
			sawWebSearch = true
			c.WebSearch = domain.WebSearchConfig{
				CipherKey: cloneBytes(cipherKey),
				Enabled:   enabled,
				UpdatedAt: updatedAt,
			}
		case loadRowProvider:
			c.Providers = append(c.Providers, domain.ProviderProfile{
				ID:          id,
				DisplayName: displayName,
				BaseURL:     target,
				CipherKey:   cloneBytes(cipherKey),
				Enabled:     enabled,
				CreatedAt:   createdAt,
				UpdatedAt:   updatedAt,
			})
		case loadRowModel:
			c.Models = append(c.Models, domain.ModelProfile{
				ID:                  id,
				ProviderID:          parentID,
				DisplayName:         displayName,
				Description:         description,
				Model:               target,
				ContextWindowTokens: contextWindowTokens,
				Enabled:             enabled,
				CreatedAt:           createdAt,
				UpdatedAt:           updatedAt,
			})
		default:
			return domain.Config{}, fmt.Errorf("unexpected gateway config row kind %d", rowKind)
		}
	}
	if err := rows.Err(); err != nil {
		return domain.Config{}, err
	}
	if !sawRouting || !sawWebSearch {
		return domain.Config{}, errors.New("gateway configuration not initialized")
	}
	slices.SortFunc(c.Providers, func(a, b domain.ProviderProfile) int {
		if !a.CreatedAt.Equal(b.CreatedAt) {
			if a.CreatedAt.Before(b.CreatedAt) {
				return -1
			}
			return 1
		}
		return strings.Compare(a.ID, b.ID)
	})
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

// LoadForPing returns a recent consistent configuration snapshot for periodic
// Agent Ping checks, coalescing concurrent misses and invalidating immediately
// on any PGStore configuration write. Live inference, connection tests, web
// tools, and administrator/catalog reads continue calling Load directly.
func (s *PGStore) LoadForPing(ctx context.Context) (domain.Config, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return domain.Config{}, err
	}
	if _, inTx := postgres.TxFromContext(ctx); inTx {
		return s.Load(ctx)
	}

	now := s.now()
	s.cacheMu.Lock()
	if s.activeWrites == 0 && s.cachedValid && now.Before(s.cachedUntil) {
		cfg := cloneConfig(s.cachedCfg)
		s.cacheMu.Unlock()
		return cfg, nil
	}
	if s.inFlight != nil && s.inFlightGen == s.writeGen && s.activeWrites == 0 {
		call := s.inFlight
		s.cacheMu.Unlock()
		select {
		case <-call.done:
			if call.err != nil {
				return domain.Config{}, call.err
			}
			return cloneConfig(call.cfg), nil
		case <-ctx.Done():
			return domain.Config{}, ctx.Err()
		}
	}
	if s.activeWrites > 0 {
		s.cacheMu.Unlock()
		return s.Load(ctx)
	}

	gen := s.writeGen
	call := &pingFlight{done: make(chan struct{})}
	s.inFlight = call
	s.inFlightGen = gen
	s.cacheMu.Unlock()

	go func() {
		loadCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), pingLoadTimeout)
		cfg, err := s.Load(loadCtx)
		cancel()

		s.cacheMu.Lock()
		if s.inFlight == call {
			s.inFlight = nil
		}
		if err == nil && s.activeWrites == 0 && s.writeGen == gen {
			s.cachedCfg = cloneConfig(cfg)
			s.cachedUntil = s.now().Add(s.pingTTL())
			s.cachedValid = true
		}
		call.cfg = cfg
		call.err = err
		close(call.done)
		s.cacheMu.Unlock()
	}()

	select {
	case <-call.done:
		if call.err != nil {
			return domain.Config{}, call.err
		}
		return cloneConfig(call.cfg), nil
	case <-ctx.Done():
		return domain.Config{}, ctx.Err()
	}
}

// UpdateCatalog serializes provider and model catalog mutations with routing validation.
// It does not modify routing intent, search settings or the retired gateway_config record.
func (s *PGStore) UpdateCatalog(ctx context.Context, change func([]domain.ProviderProfile, []domain.ModelProfile) ([]domain.ProviderProfile, []domain.ModelProfile, error)) (domain.Config, error) {
	s.beginConfigWrite()
	defer s.endConfigWrite()
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
	s.beginConfigWrite()
	defer s.endConfigWrite()
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
	s.beginConfigWrite()
	defer s.endConfigWrite()
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
