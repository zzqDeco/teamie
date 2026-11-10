package pg_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"teamie/pkg/postgres/testdb"
	"teamie/services/llm-gateway/internal/domain"
	"teamie/services/llm-gateway/internal/infra/pg"
)

// schemaV6OnlySQL reproduces a Gateway Schema 6 database prior to upgrading to Schema 7.
const schemaV6OnlySQL = `
CREATE TABLE gateway_schema_migrations(version INTEGER PRIMARY KEY, applied_at TIMESTAMPTZ NOT NULL DEFAULT now());
CREATE TABLE gateway_config(
	id BOOLEAN PRIMARY KEY DEFAULT true CHECK(id),
	base_url TEXT NOT NULL DEFAULT '',
	model TEXT NOT NULL DEFAULT '',
	encrypted_key BYTEA,
	enabled BOOLEAN NOT NULL DEFAULT false,
	updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
	context_window_tokens BIGINT NOT NULL DEFAULT 16384 CHECK(context_window_tokens BETWEEN 4096 AND 2000000),
	web_search_encrypted_key BYTEA,
	web_search_enabled BOOLEAN NOT NULL DEFAULT false,
	default_model_id TEXT NOT NULL DEFAULT 'default'
);
INSERT INTO gateway_config(id) VALUES(true);
CREATE TABLE gateway_usage(
	id BIGSERIAL PRIMARY KEY,
	owner_id TEXT NOT NULL,
	agent_id TEXT NOT NULL,
	session_id TEXT NOT NULL,
	run_id TEXT NOT NULL,
	status TEXT NOT NULL,
	prompt_tokens BIGINT NOT NULL,
	completion_tokens BIGINT NOT NULL,
	latency_ms BIGINT NOT NULL,
	created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
	model_id TEXT NOT NULL DEFAULT ''
);
CREATE INDEX gateway_usage_created ON gateway_usage(created_at);
CREATE TABLE gateway_model_routing(
	id BOOLEAN PRIMARY KEY DEFAULT true CHECK(id),
	default_model_id TEXT NOT NULL DEFAULT '',
	updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE TABLE gateway_web_search_config(
	id BOOLEAN PRIMARY KEY DEFAULT true CHECK(id),
	encrypted_key BYTEA,
	enabled BOOLEAN NOT NULL DEFAULT false,
	updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE TABLE gateway_providers(
	id TEXT PRIMARY KEY,
	display_name TEXT NOT NULL DEFAULT '',
	base_url TEXT NOT NULL DEFAULT '',
	encrypted_key BYTEA,
	enabled BOOLEAN NOT NULL DEFAULT true,
	created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
	updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE TABLE gateway_models(
	id TEXT PRIMARY KEY,
	provider_id TEXT NOT NULL REFERENCES gateway_providers(id) ON DELETE RESTRICT,
	display_name TEXT NOT NULL DEFAULT '',
	description TEXT NOT NULL DEFAULT '',
	model TEXT NOT NULL DEFAULT '',
	context_window_tokens BIGINT NOT NULL DEFAULT 16384 CHECK(context_window_tokens BETWEEN 4096 AND 2000000),
	enabled BOOLEAN NOT NULL DEFAULT true,
	created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
	updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX gateway_models_provider_id ON gateway_models(provider_id);
INSERT INTO gateway_schema_migrations(version) VALUES (1),(2),(3),(4),(5),(6);
`

func TestSchema6To7MigrationPreservesAllStoresAndAdvancesFence(t *testing.T) {
	if pg.GatewaySchemaVersion != 7 {
		t.Fatalf("expected GatewaySchemaVersion = 7, got %d", pg.GatewaySchemaVersion)
	}
	ctx := context.Background()
	baseDSN := testdb.URL(t)
	_, schemaDSN := newIsolatedSchemaDSN(t, baseDSN, "gateway_mig_v6_v7")

	conn, err := pgx.Connect(ctx, schemaDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)

	if _, err := conn.Exec(ctx, schemaV6OnlySQL); err != nil {
		t.Fatalf("seed v6 schema failed: %v", err)
	}

	tProv := time.Date(2026, 11, 1, 10, 0, 0, 123000, time.UTC)
	tModel := time.Date(2026, 11, 1, 10, 5, 0, 456000, time.UTC)
	tRoute := time.Date(2026, 11, 1, 10, 6, 0, 789000, time.UTC)
	tSearch := time.Date(2026, 11, 1, 10, 7, 0, 321000, time.UTC)
	provCipher := []byte("v6-provider-ciphertext-exact")
	webCipher := []byte("v6-websearch-ciphertext-exact")

	if _, err := conn.Exec(ctx, `
		INSERT INTO gateway_providers(id, display_name, base_url, encrypted_key, enabled, created_at, updated_at)
		VALUES ('prov-v6', 'V6 Provider', 'https://1.1.1.1/v1', $1, true, $2, $2)`,
		provCipher, tProv); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(ctx, `
		INSERT INTO gateway_models(id, provider_id, display_name, description, model, context_window_tokens, enabled, created_at, updated_at)
		VALUES ('mod-v6', 'prov-v6', 'V6 Model', 'kept across v7', 'gpt-4o', 65536, false, $1, $1)`,
		tModel); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(ctx, `
		INSERT INTO gateway_model_routing(id, default_model_id, updated_at)
		VALUES (true, 'mod-v6', $1)`, tRoute); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(ctx, `
		INSERT INTO gateway_web_search_config(id, encrypted_key, enabled, updated_at)
		VALUES (true, $1, true, $2)`, webCipher, tSearch); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(ctx, `
		INSERT INTO gateway_usage(owner_id, agent_id, session_id, run_id, model_id, status, prompt_tokens, completion_tokens, latency_ms)
		VALUES ('owner-1', 'agent-1', 'sess-1', 'run-1', 'mod-v6', 'success', 42, 17, 120)`); err != nil {
		t.Fatal(err)
	}

	// Upgrade v6 -> v7 via pg.Open, then reopen idempotently.
	store, err := pg.Open(ctx, schemaDSN)
	if err != nil {
		t.Fatalf("pg.Open v6->v7 upgrade failed: %v", err)
	}
	store.DB.Close()

	store, err = pg.Open(ctx, schemaDSN)
	if err != nil {
		t.Fatalf("idempotent pg.Open on v7 failed: %v", err)
	}
	defer store.DB.Close()

	var maxVer, countVer int
	if err := conn.QueryRow(ctx, `SELECT max(version), count(*) FROM gateway_schema_migrations`).Scan(&maxVer, &countVer); err != nil {
		t.Fatal(err)
	}
	if maxVer != 7 || countVer != 7 {
		t.Fatalf("expected ledger 1..7, got max=%d count=%d", maxVer, countVer)
	}

	cfg, err := store.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Providers) != 1 || cfg.Providers[0].ID != "prov-v6" || !bytes.Equal(cfg.Providers[0].CipherKey, provCipher) || !cfg.Providers[0].UpdatedAt.Equal(tProv) {
		t.Fatalf("provider row altered by v7 migration: %+v", cfg.Providers)
	}
	if len(cfg.Models) != 1 || cfg.Models[0].ID != "mod-v6" || cfg.Models[0].Enabled || cfg.Models[0].ContextWindowTokens != 65536 || !cfg.Models[0].UpdatedAt.Equal(tModel) {
		t.Fatalf("model row altered by v7 migration: %+v", cfg.Models)
	}
	if cfg.Routing.DefaultModelID != "mod-v6" || !cfg.Routing.UpdatedAt.Equal(tRoute) {
		t.Fatalf("routing row altered by v7 migration: %+v", cfg.Routing)
	}
	if !cfg.WebSearch.Enabled || !bytes.Equal(cfg.WebSearch.CipherKey, webCipher) || !cfg.WebSearch.UpdatedAt.Equal(tSearch) {
		t.Fatalf("web search row altered by v7 migration: %+v", cfg.WebSearch)
	}
	var usageCount int
	var promptTokens, completionTokens, latencyMS int64
	if err := conn.QueryRow(ctx, `SELECT count(*), coalesce(sum(prompt_tokens),0), coalesce(sum(completion_tokens),0), coalesce(sum(latency_ms),0) FROM gateway_usage`).Scan(&usageCount, &promptTokens, &completionTokens, &latencyMS); err != nil {
		t.Fatal(err)
	}
	if usageCount != 1 || promptTokens != 42 || completionTokens != 17 || latencyMS != 120 {
		t.Fatalf("usage rows altered by v7 migration: count=%d prompt=%d completion=%d latency=%d", usageCount, promptTokens, completionTokens, latencyMS)
	}
	tests, err := store.LoadModelTests(ctx)
	if err != nil || len(tests) != 0 {
		t.Fatalf("expected empty gateway_model_tests after v7 migration, got %+v (%v)", tests, err)
	}
}

func TestModelTestStoreOrderingConstraintsCascadeAndConfigIsolation(t *testing.T) {
	ctx := context.Background()
	baseDSN := testdb.URL(t)
	_, schemaDSN := newIsolatedSchemaDSN(t, baseDSN, "gateway_model_tests_pg")

	store, err := pg.Open(ctx, schemaDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer store.DB.Close()

	conn, err := pgx.Connect(ctx, schemaDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)

	cfg, err := store.UpdateCatalog(ctx, func(_ []domain.ProviderProfile, _ []domain.ModelProfile) ([]domain.ProviderProfile, []domain.ModelProfile, error) {
		return []domain.ProviderProfile{{
				ID:          "prov-1",
				DisplayName: "Provider 1",
				BaseURL:     "https://1.1.1.1/v1",
				CipherKey:   []byte("cipher-1"),
				Enabled:     true,
			}}, []domain.ModelProfile{
				{ID: "model-a", ProviderID: "prov-1", DisplayName: "Model A", Model: "gpt-4o", ContextWindowTokens: 32768, Enabled: true},
				{ID: "model-b", ProviderID: "prov-1", DisplayName: "Model B", Model: "gpt-4o-mini", ContextWindowTokens: 16384, Enabled: true},
			}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	p1, _ := cfg.ProviderByID("prov-1")
	mA, _ := cfg.ModelByID("model-a")
	mB, _ := cfg.ModelByID("model-b")

	// 1. Foreign-key enforcement rejects nonexistent model_id.
	if err := store.RecordModelTest(ctx, domain.ModelTestRecord{
		ModelID:           "does-not-exist",
		OK:                true,
		TestedAt:          time.Now().UTC(),
		ModelUpdatedAt:    mA.UpdatedAt,
		ProviderUpdatedAt: p1.UpdatedAt,
	}); !errors.Is(err, domain.ErrModelTestTargetMissing) {
		t.Fatalf("expected ErrModelTestTargetMissing for nonexistent model_id, got %v", err)
	}

	// 2. Database CHECK constraints reject ok/error_code violations, unallowlisted error_code, and negative latency_ms.
	tNow := time.Date(2026, 11, 7, 8, 0, 0, 0, time.UTC)
	for _, badSQL := range []struct {
		name      string
		ok        bool
		latency   *int64
		errorCode *string
	}{
		{name: "ok_with_error_code", ok: true, errorCode: strPtr("upstream_error")},
		{name: "failed_without_error_code", ok: false, errorCode: nil},
		{name: "unallowlisted_raw_secret", ok: false, errorCode: strPtr("https://secret.example sk-live-secret")},
		{name: "negative_latency", ok: true, latency: int64Ptr(-5), errorCode: nil},
	} {
		if _, err := conn.Exec(ctx, `INSERT INTO gateway_model_tests(model_id, ok, tested_at, latency_ms, error_code, model_updated_at, provider_updated_at) VALUES ('model-a', $1, $2, $3, $4, $5, $6)`,
			badSQL.ok, tNow, badSQL.latency, badSQL.errorCode, mA.UpdatedAt, p1.UpdatedAt); err == nil {
			t.Fatalf("expected PostgreSQL CHECK constraint to reject %s", badSQL.name)
		}
	}

	// 3. Deterministic out-of-order and concurrent tested_at ordering: newest tested_at wins.
	tBase := time.Date(2026, 11, 7, 9, 0, 0, 0, time.UTC)
	latNew := int64(38)
	latOld := int64(910)
	if err := store.RecordModelTest(ctx, domain.ModelTestRecord{
		ModelID:           "model-a",
		OK:                true,
		TestedAt:          tBase.Add(10 * time.Second),
		LatencyMS:         &latNew,
		ErrorCode:         "should_be_dropped_when_ok",
		ModelUpdatedAt:    mA.UpdatedAt,
		ProviderUpdatedAt: p1.UpdatedAt,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordModelTest(ctx, domain.ModelTestRecord{
		ModelID:           "model-a",
		OK:                false,
		TestedAt:          tBase.Add(5 * time.Second), // older tested_at must NOT overwrite newer row
		LatencyMS:         &latOld,
		ErrorCode:         "upstream_timeout",
		ModelUpdatedAt:    mA.UpdatedAt,
		ProviderUpdatedAt: p1.UpdatedAt,
	}); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.LoadModelTests(ctx)
	if err != nil {
		t.Fatal(err)
	}
	gotA := loaded["model-a"]
	if !gotA.OK || !gotA.TestedAt.Equal(tBase.Add(10*time.Second)) || gotA.LatencyMS == nil || *gotA.LatencyMS != 38 || gotA.ErrorCode != "" {
		t.Fatalf("stale tested_at overwrote newer test record: %+v", gotA)
	}

	// Concurrent 24-goroutine race against model-b: maximum tested_at must deterministically win.
	const workers = 24
	winnerTime := tBase.Add(time.Duration(workers) * time.Second)
	winnerLat := int64(77)
	order := rand.Perm(workers)
	var wg sync.WaitGroup
	errCh := make(chan error, workers)
	for _, idx := range order {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			ts := tBase.Add(time.Duration(i+1) * time.Second)
			isWinner := ts.Equal(winnerTime)
			lat := int64(100 + i)
			code := "upstream_error"
			if isWinner {
				lat = winnerLat
				code = "provider_auth_failed"
			}
			errCh <- store.RecordModelTest(ctx, domain.ModelTestRecord{
				ModelID:           "model-b",
				OK:                false,
				TestedAt:          ts,
				LatencyMS:         &lat,
				ErrorCode:         code,
				ModelUpdatedAt:    mB.UpdatedAt,
				ProviderUpdatedAt: p1.UpdatedAt,
			})
		}(idx)
	}
	wg.Wait()
	close(errCh)
	for wErr := range errCh {
		if wErr != nil {
			t.Fatalf("concurrent RecordModelTest failed: %v", wErr)
		}
	}
	loaded, err = store.LoadModelTests(ctx)
	if err != nil {
		t.Fatal(err)
	}
	gotB := loaded["model-b"]
	if gotB.OK || !gotB.TestedAt.Equal(winnerTime) || gotB.LatencyMS == nil || *gotB.LatencyMS != winnerLat || gotB.ErrorCode != "provider_auth_failed" {
		t.Fatalf("concurrent tested_at winner mismatch: %+v", gotB)
	}

	// 4. Deterministic PostgreSQL lock proof: holding ACCESS EXCLUSIVE lock on gateway_model_tests
	// never blocks Load, LoadForPing, UpdateCatalog, UpdateRouting, UpdateWebSearch, or Record.
	lockDB, err := pg.Open(ctx, schemaDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer lockDB.DB.Close()

	if err := lockDB.WithinTx(ctx, func(txCtx context.Context) error {
		if _, err := lockDB.DB.Conn(txCtx).Exec(txCtx, `LOCK TABLE gateway_model_tests IN ACCESS EXCLUSIVE MODE`); err != nil {
			return err
		}
		opCtx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
		defer cancel()
		if _, err := store.Load(opCtx); err != nil {
			return fmt.Errorf("Load touched locked gateway_model_tests: %w", err)
		}
		if _, err := store.LoadForPing(opCtx); err != nil {
			return fmt.Errorf("LoadForPing touched locked gateway_model_tests: %w", err)
		}
		if _, err := store.UpdateRouting(opCtx, domain.ModelRoutingConfig{DefaultModelID: "model-a"}); err != nil {
			return fmt.Errorf("UpdateRouting touched locked gateway_model_tests: %w", err)
		}
		if _, err := store.UpdateWebSearch(opCtx, func(w domain.WebSearchConfig) (domain.WebSearchConfig, error) {
			w.Enabled = true
			w.CipherKey = []byte("web-k")
			return w, nil
		}); err != nil {
			return fmt.Errorf("UpdateWebSearch touched locked gateway_model_tests: %w", err)
		}
		if err := store.Record(opCtx, domain.Usage{OwnerID: "o", AgentID: "a", SessionID: "s", RunID: "r", ModelID: "model-a", Status: "success", LatencyMS: 5}); err != nil {
			return fmt.Errorf("Record usage touched locked gateway_model_tests: %w", err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	// 5. Cascade deletion: deleting model-a removes its gateway_model_tests row while preserving model-b's row.
	if _, err := store.UpdateCatalog(ctx, func(providers []domain.ProviderProfile, models []domain.ModelProfile) ([]domain.ProviderProfile, []domain.ModelProfile, error) {
		return providers, []domain.ModelProfile{models[1]}, nil
	}); err != nil {
		t.Fatal(err)
	}
	afterDelete, err := store.LoadModelTests(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := afterDelete["model-a"]; exists {
		t.Fatalf("expected ON DELETE CASCADE to delete model-a test row, got %+v", afterDelete)
	}
	if _, exists := afterDelete["model-b"]; !exists {
		t.Fatalf("deleting model-a unexpectedly removed model-b test row: %+v", afterDelete)
	}
}

func strPtr(s string) *string { return &s }
func int64Ptr(v int64) *int64 { return &v }
