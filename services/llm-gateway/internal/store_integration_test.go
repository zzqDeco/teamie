package gateway_test

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"teamie/services/llm-gateway/internal/app"
	"teamie/services/llm-gateway/internal/domain"
	"teamie/services/llm-gateway/internal/infra/pg"
)

func TestPostgresPreservesLegacyModelWithoutKey(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL_TEST")
	if dsn == "" {
		t.Skip("DATABASE_URL_TEST is unset")
	}
	for _, tc := range []struct {
		name, model string
		enabled     bool
	}{
		{name: "enabled_without_key", model: "saved-model", enabled: true},
		{name: "disabled_without_key", model: "saved-model", enabled: false},
		{name: "partial_settings", model: "", enabled: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			admin, err := pgx.Connect(ctx, dsn)
			if err != nil {
				t.Fatal(err)
			}
			defer admin.Close(ctx)
			schema := fmt.Sprintf("gateway_legacy_%d", time.Now().UnixNano())
			if _, err = admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
				t.Fatal(err)
			}
			defer admin.Exec(ctx, "DROP SCHEMA "+schema+" CASCADE")
			u, err := url.Parse(dsn)
			if err != nil {
				t.Fatal(err)
			}
			q := u.Query()
			q.Set("search_path", schema)
			u.RawQuery = q.Encode()
			legacy, err := pgx.Connect(ctx, u.String())
			if err != nil {
				t.Fatal(err)
			}
			defer legacy.Close(ctx)
			if _, err = legacy.Exec(ctx, strings.Split(pg.SchemaSQL, "CREATE TABLE IF NOT EXISTS gateway_models")[0]); err != nil {
				t.Fatal(err)
			}
			var savedAt time.Time
			if err = legacy.QueryRow(ctx, `UPDATE gateway_config SET base_url='https://1.1.1.1/v1',model=$1,encrypted_key=NULL,enabled=$2,context_window_tokens=131072 WHERE id RETURNING updated_at`, tc.model, tc.enabled).Scan(&savedAt); err != nil {
				t.Fatal(err)
			}
			store, err := Open(ctx, u.String())
			if err != nil {
				t.Fatal(err)
			}
			defer store.DB.Close()
			c, err := store.Load(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if len(c.Providers) != 1 || len(c.Models) != 1 {
				t.Fatalf("legacy model/provider settings disappeared because the key was absent: providers=%d models=%d", len(c.Providers), len(c.Models))
			}
			p := c.Providers[0]
			m := c.Models[0]
			rm := c.ResolveModelProfile(m)
			if p.ID != "default" || p.BaseURL != "https://1.1.1.1/v1" || !p.Enabled || !p.UpdatedAt.Equal(savedAt) {
				t.Fatalf("migration changed saved provider settings: %+v", p)
			}
			if m.ID != "default" || m.ProviderID != "default" || m.Model != tc.model || m.Enabled != tc.enabled || m.ContextWindowTokens != 131072 || !m.UpdatedAt.Equal(savedAt) {
				t.Fatalf("migration changed saved model settings: %+v", m)
			}
			if rm.Configured() || len(p.CipherKey) != 0 || c.ResolveModel("").Status() != "unconfigured" {
				t.Fatal("migration invented a credential or made an unconfigured model available")
			}
		})
	}
}

func TestPostgresSchema5MultiModelMigrationPreservesDistinctProvidersWithoutAutoMerge(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL_TEST")
	if dsn == "" {
		t.Skip("DATABASE_URL_TEST is unset")
	}
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close(ctx)
	schema := fmt.Sprintf("gateway_v5_%d", time.Now().UnixNano())
	if _, err = admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	defer admin.Exec(ctx, "DROP SCHEMA "+schema+" CASCADE")

	u, _ := url.Parse(dsn)
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()

	v5Conn, err := pgx.Connect(ctx, u.String())
	if err != nil {
		t.Fatal(err)
	}
	schemaV5 := strings.Split(pg.SchemaSQL, "-- Split provider connections from model catalog once")[0]
	if _, err = v5Conn.Exec(ctx, schemaV5); err != nil {
		v5Conn.Close(ctx)
		t.Fatal(err)
	}
	crypto := testCipher(t)
	keyAlpha, keyBeta, keyWeb := "secret-alpha-key", "secret-beta-key", "secret-web-key"
	encAlpha, _ := crypto.Encrypt(keyAlpha)
	encBeta, _ := crypto.Encrypt(keyBeta)
	encWeb, _ := crypto.Encrypt(keyWeb)

	// Two models share identical base_url in Schema 5 with DIFFERENT encrypted keys and enabled states;
	// a third model has NULL encrypted_key. Schema 6 MUST NOT auto-merge by base_url.
	t0 := time.Date(2026, 10, 29, 1, 0, 0, 0, time.UTC)
	if _, err = v5Conn.Exec(ctx, `INSERT INTO gateway_models(id,display_name,description,base_url,model,encrypted_key,context_window_tokens,enabled,created_at,updated_at) VALUES
		('m-alpha','Alpha GPT','first','https://1.1.1.1/v1','gpt-4o',$1,65536,true,$3,$3),
		('m-beta','','second','https://1.1.1.1/v1','gpt-4o-mini',$2,131072,false,$4,$4),
		('m-nokey','NoKey Model','third','https://8.8.8.8/v1','deepseek-chat',NULL,16384,true,$5,$5)`,
		encAlpha, encBeta, t0, t0.Add(time.Second), t0.Add(2*time.Second)); err != nil {
		v5Conn.Close(ctx)
		t.Fatal(err)
	}
	if _, err = v5Conn.Exec(ctx, `UPDATE gateway_model_routing SET default_model_id='m-alpha' WHERE id`); err != nil {
		v5Conn.Close(ctx)
		t.Fatal(err)
	}
	if _, err = v5Conn.Exec(ctx, `UPDATE gateway_web_search_config SET encrypted_key=$1, enabled=true WHERE id`, encWeb); err != nil {
		v5Conn.Close(ctx)
		t.Fatal(err)
	}
	v5Conn.Close(ctx)

	store, err := Open(ctx, u.String())
	if err != nil {
		t.Fatal(err)
	}
	defer store.DB.Close()

	var head int
	if err := store.DB.QueryRow(ctx, `SELECT max(version) FROM gateway_schema_migrations`).Scan(&head); err != nil || head != pg.GatewaySchemaVersion {
		t.Fatalf("expected schema version %d, got %d (%v)", pg.GatewaySchemaVersion, head, err)
	}
	var legacyCols int
	if err := store.DB.QueryRow(ctx, `SELECT count(*) FROM information_schema.columns WHERE table_schema=$1 AND table_name='gateway_models' AND column_name IN ('base_url','encrypted_key')`, schema).Scan(&legacyCols); err != nil || legacyCols != 0 {
		t.Fatalf("expected base_url and encrypted_key dropped from gateway_models, got %d (%v)", legacyCols, err)
	}

	c, err := store.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Providers) != 3 || len(c.Models) != 3 {
		t.Fatalf("expected 1-to-1 migration of 3 providers and 3 models, got providers=%d models=%d", len(c.Providers), len(c.Models))
	}
	if c.Routing.DefaultModelID != "m-alpha" || c.EffectiveDefaultModelID() != "m-alpha" {
		t.Fatalf("routing broken after migration: %+v", c.Routing)
	}
	rmAlpha, _ := c.ResolveExactModel("m-alpha")
	rmBeta, _ := c.ResolveExactModel("m-beta")
	rmNoKey, _ := c.ResolveExactModel("m-nokey")

	decAlpha, _ := crypto.Decrypt(rmAlpha.CipherKey)
	decBeta, _ := crypto.Decrypt(rmBeta.CipherKey)
	decWeb, _ := crypto.Decrypt(c.WebSearch.CipherKey)
	if decAlpha != keyAlpha || decBeta != keyBeta || decWeb != keyWeb {
		t.Fatalf("migration corrupted or merged credentials: alpha=%q beta=%q web=%q", decAlpha, decBeta, decWeb)
	}
	if !rmAlpha.Available() || rmAlpha.ProviderDisplayName != "Alpha GPT" || rmAlpha.ContextWindow() != 65536 {
		t.Fatalf("unexpected alpha resolution: %+v", rmAlpha)
	}
	if !rmBeta.ProviderEnabled || rmBeta.ModelEnabled || rmBeta.Available() || rmBeta.ProviderDisplayName != "m-beta" || rmBeta.ContextWindow() != 131072 {
		t.Fatalf("unexpected beta resolution: %+v", rmBeta)
	}
	if rmNoKey.Configured() || rmNoKey.Available() || len(rmNoKey.CipherKey) != 0 {
		t.Fatalf("unexpected nokey resolution: %+v", rmNoKey)
	}

	// Database-level ON DELETE RESTRICT must reject deleting a provider while its model exists.
	if _, err := store.DB.Exec(ctx, `DELETE FROM gateway_providers WHERE id='m-alpha'`); err == nil {
		t.Fatal("expected PostgreSQL ON DELETE RESTRICT to block deleting provider referenced by model")
	}
}

func TestPostgresConfigBoundariesAndRestart(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL_TEST")
	if dsn == "" {
		t.Skip("DATABASE_URL_TEST is unset")
	}
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close(ctx)
	schema := "gateway_boundaries_" + time.Now().Format("150405000000000")
	if _, err = admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	defer admin.Exec(ctx, "DROP SCHEMA "+schema+" CASCADE")
	u, _ := url.Parse(dsn)
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	legacy, err := pgx.Connect(ctx, u.String())
	if err != nil {
		t.Fatal(err)
	}
	crypto := testCipher(t)
	modelKey, webKey := "fixture-model-key", "fixture-web-key"
	encrypted, _ := crypto.Encrypt(modelKey)
	webEncrypted, _ := crypto.Encrypt(webKey)
	// Start at the released schema 3 with real encrypted legacy configuration.
	if _, err = legacy.Exec(ctx, strings.Split(pg.SchemaSQL, "CREATE TABLE IF NOT EXISTS gateway_models")[0]); err != nil {
		t.Fatal(err)
	}
	if _, err = legacy.Exec(ctx, `UPDATE gateway_config SET base_url='https://1.1.1.1/v1',model='model-a',encrypted_key=$1,enabled=true,context_window_tokens=131072,web_search_encrypted_key=$2,web_search_enabled=true WHERE id`, encrypted, webEncrypted); err != nil {
		t.Fatal(err)
	}
	legacy.Close(ctx)
	store, err := Open(ctx, u.String())
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if store != nil {
			store.DB.Close()
		}
	}()
	service := New(store, crypto, testToken)
	c, err := store.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Providers) != 1 || len(c.Models) != 1 || c.Routing.DefaultModelID != "default" || c.ResolveModel("").ContextWindow() != 131072 || !c.WebSearch.Enabled {
		t.Fatal("legacy migration did not preserve configuration")
	}
	actual, _ := crypto.Decrypt(c.Providers[0].CipherKey)
	actualWeb, _ := crypto.Decrypt(c.WebSearch.CipherKey)
	if actual != modelKey || actualWeb != webKey {
		t.Fatal("migration lost encrypted credentials")
	}
	if strings.Contains(string(c.Providers[0].CipherKey), modelKey) || strings.Contains(string(c.WebSearch.CipherKey), webKey) {
		t.Fatal("plaintext credential persisted")
	}

	// Create one Provider "prov-b" and two Models ("b1", "b2") sharing it.
	keyB := "fixture-provider-b-key"
	if _, err = service.App.CreateModelProvider(ctx, app.CreateModelProviderInput{ID: "prov-b", DisplayName: "Provider B", BaseURL: "https://1.1.1.1/v1", APIKey: &keyB, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if _, err = service.App.CreateModelProfile(ctx, app.CreateModelProfileInput{ID: "b1", ProviderID: "prov-b", Model: "model-b1", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if _, err = service.App.CreateModelProfile(ctx, app.CreateModelProfileInput{ID: "b2", ProviderID: "prov-b", Model: "model-b2", Enabled: true}); err != nil {
		t.Fatal(err)
	}

	// Deleting a provider that still owns models must fail with 409 provider_in_use.
	if _, err = service.App.DeleteModelProvider(ctx, "prov-b"); domain.AsError(err).Code != "provider_in_use" {
		t.Fatalf("expected 409 provider_in_use, got %v", err)
	}

	disabled := false
	c, err = service.App.UpdateModelProfile(ctx, app.UpdateModelProfileInput{ID: "default", Enabled: &disabled})
	if err != nil {
		t.Fatal(err)
	}
	if c.ResolveModel("").ID != "b1" || c.Routing.DefaultModelID != "default" {
		t.Fatal("fallback must retain default intent")
	}
	beforeProviders, beforeModels, beforeRouting := c.Providers, c.Models, c.Routing
	// Only change search while default A is disabled.
	nextWebKey := "fixture-new-web-key"
	c, err = service.App.UpdateWebSearch(ctx, app.UpdateWebSearchInput{Enabled: &disabled, APIKey: &nextWebKey})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(c.Providers, beforeProviders) || !reflect.DeepEqual(c.Models, beforeModels) || c.Routing != beforeRouting {
		t.Fatal("search update changed provider, model or routing rows")
	}
	actualWeb, _ = crypto.Decrypt(c.WebSearch.CipherKey)
	if actualWeb != nextWebKey || c.WebSearch.Enabled {
		t.Fatal("search update did not apply")
	}
	beforeSearch := c.WebSearch
	if _, err = service.App.UpdateRouting(ctx, "b1"); err != nil {
		t.Fatal(err)
	}
	c, _ = store.Load(ctx)
	if !reflect.DeepEqual(c.Providers, beforeProviders) || !reflect.DeepEqual(c.Models, beforeModels) || !reflect.DeepEqual(c.WebSearch, beforeSearch) {
		t.Fatal("routing update changed provider, model or search rows")
	}

	// Concurrent independent commands must preserve all writes, including keys.
	var wg sync.WaitGroup
	errs := make(chan error, 3)
	wg.Add(3)
	go func() {
		defer wg.Done()
		name := "Updated B1"
		_, e := service.App.UpdateModelProfile(ctx, app.UpdateModelProfileInput{ID: "b1", DisplayName: &name})
		errs <- e
	}()
	go func() {
		defer wg.Done()
		pName := "Updated Provider B"
		_, e := service.App.UpdateModelProvider(ctx, app.UpdateModelProviderInput{ID: "prov-b", DisplayName: &pName})
		errs <- e
	}()
	go func() {
		defer wg.Done()
		yes := true
		_, e := service.App.UpdateWebSearch(ctx, app.UpdateWebSearchInput{Enabled: &yes})
		errs <- e
	}()
	wg.Wait()
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	c, _ = store.Load(ctx)
	b1 := c.ResolveModel("b1")
	actualB, _ := crypto.Decrypt(b1.CipherKey)
	actualWeb, _ = crypto.Decrypt(c.WebSearch.CipherKey)
	if b1.DisplayName != "Updated B1" || b1.ProviderDisplayName != "Updated Provider B" || actualB != keyB || actualWeb != nextWebKey || !c.WebSearch.Enabled {
		t.Fatal("concurrent writes lost data")
	}

	if _, err = service.App.DeleteModelProfile(ctx, "default"); err != nil {
		t.Fatal(err)
	}
	if _, err = service.App.DeleteModelProvider(ctx, "default"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		store.DB.Close()
		store, err = Open(ctx, u.String())
		if err != nil {
			t.Fatal(err)
		}
		c, err = store.Load(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if len(c.Providers) != 1 || c.Providers[0].ID != "prov-b" || len(c.Models) != 2 {
			t.Fatalf("deleted default resurrected after restart: providers=%+v models=%+v", c.Providers, c.Models)
		}
	}
	service = New(store, crypto, testToken)
	beforeSearch = c.WebSearch
	if _, err = service.App.UpdateModelProvider(ctx, app.UpdateModelProviderInput{ID: "prov-b", ClearAPIKey: true}); err != nil {
		t.Fatal(err)
	}
	c, _ = store.Load(ctx)
	if c.ResolveModel("b1").Configured() || c.ResolveModel("b2").Configured() || !reflect.DeepEqual(c.WebSearch, beforeSearch) {
		t.Fatal("provider key clear did not apply to shared models or changed search")
	}
	if _, err = service.App.DeleteModelProfile(ctx, "b1"); err != nil {
		t.Fatal(err)
	}
	if _, err = service.App.DeleteModelProfile(ctx, "b2"); err != nil {
		t.Fatal(err)
	}
	if _, err = service.App.DeleteModelProvider(ctx, "prov-b"); err != nil {
		t.Fatal(err)
	}
	store.DB.Close()
	store, err = Open(ctx, u.String())
	if err != nil {
		t.Fatal(err)
	}
	c, _ = store.Load(ctx)
	if len(c.Providers) != 0 || len(c.Models) != 0 || !reflect.DeepEqual(c.WebSearch, beforeSearch) {
		t.Fatal("empty catalog resurrected or changed search")
	}
	if err = store.Record(ctx, Usage{OwnerID: "owner", AgentID: "agent", SessionID: "session", RunID: "run", ModelID: "b1", Status: "success", PromptTokens: 12, CompletionTokens: 8, LatencyMS: 3}); err != nil {
		t.Fatal(err)
	}
	var version, count int
	var modelID string
	_ = store.DB.QueryRow(ctx, "SELECT max(version) FROM gateway_schema_migrations").Scan(&version)
	_ = store.DB.QueryRow(ctx, "SELECT count(*) FROM gateway_usage").Scan(&count)
	_ = store.DB.QueryRow(ctx, "SELECT model_id FROM gateway_usage LIMIT 1").Scan(&modelID)
	if version != 6 || count != 1 || modelID != "b1" {
		t.Fatal(version, count, modelID)
	}
}

func TestServicePingBoundedCacheAndModelSwitchRegression(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL_TEST")
	if dsn == "" {
		t.Skip("DATABASE_URL_TEST is unset")
	}
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close(ctx)
	schema := fmt.Sprintf("gateway_ping_svc_%d", time.Now().UnixNano())
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	defer admin.Exec(ctx, "DROP SCHEMA "+schema+" CASCADE")

	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()

	store, err := Open(ctx, u.String())
	if err != nil {
		t.Fatal(err)
	}
	defer store.DB.Close()

	crypto := testCipher(t)
	service := New(store, crypto, testToken)

	keyA, keyB := "provider-a-secret", "provider-b-secret"
	cwA, cwB1, cwB2 := int64(32768), int64(65536), int64(131072)
	if _, err := service.App.CreateModelProvider(ctx, app.CreateModelProviderInput{
		ID: "prov-a", DisplayName: "Provider A", BaseURL: "https://1.1.1.1/v1", APIKey: &keyA, Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.App.CreateModelProvider(ctx, app.CreateModelProviderInput{
		ID: "prov-b", DisplayName: "Provider B", BaseURL: "https://8.8.8.8/v1", APIKey: &keyB, Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.App.CreateModelProfile(ctx, app.CreateModelProfileInput{
		ID: "m-a", ProviderID: "prov-a", Model: "gpt-a", Enabled: true, ContextWindowTokens: &cwA,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.App.CreateModelProfile(ctx, app.CreateModelProfileInput{
		ID: "m-b1", ProviderID: "prov-b", Model: "gpt-b1", Enabled: true, ContextWindowTokens: &cwB1,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.App.CreateModelProfile(ctx, app.CreateModelProfileInput{
		ID: "m-b2", ProviderID: "prov-b", Model: "gpt-b2", Enabled: true, ContextWindowTokens: &cwB2,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.App.UpdateRouting(ctx, "m-a"); err != nil {
		t.Fatal(err)
	}

	cfgID := func(baseURL, model string) string {
		return domain.CountingConfigID(baseURL, model)
	}

	// Prime the Ping snapshot cache once.
	p0 := service.App.Ping(ctx, testToken)
	if !p0.Ready || p0.ModelConfigID == nil || *p0.ModelConfigID != cfgID("https://1.1.1.1/v1", "gpt-a") || *p0.ContextWindowTokens != cwA {
		t.Fatalf("unexpected initial default Ping: %+v", p0)
	}

	// Simulate 4 idle Agents with distinct selected modelIDs polling Ping while gateway_models is locked:
	// all 4 Agents resolve their distinct effective models from the cached snapshot without hitting PostgreSQL.
	lockConn, err := pgx.Connect(ctx, u.String())
	if err != nil {
		t.Fatal(err)
	}
	defer lockConn.Close(ctx)
	lockTx, err := lockConn.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lockTx.Exec(ctx, `LOCK TABLE gateway_models IN ACCESS EXCLUSIVE MODE`); err != nil {
		_ = lockTx.Rollback(ctx)
		t.Fatal(err)
	}

	pingCtx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer cancel()
	for _, agentCase := range []struct {
		modelID string
		wantCW  int64
		wantID  string
	}{
		{modelID: "", wantCW: cwA, wantID: cfgID("https://1.1.1.1/v1", "gpt-a")},
		{modelID: "m-b1", wantCW: cwB1, wantID: cfgID("https://8.8.8.8/v1", "gpt-b1")},
		{modelID: "m-b2", wantCW: cwB2, wantID: cfgID("https://8.8.8.8/v1", "gpt-b2")},
		{modelID: "deleted-agent-model", wantCW: cwA, wantID: cfgID("https://1.1.1.1/v1", "gpt-a")},
	} {
		st := service.App.Ping(pingCtx, testToken, agentCase.modelID)
		if !st.Ready || !*st.Configured || !*st.Enabled || *st.ContextWindowTokens != agentCase.wantCW || st.ModelConfigID == nil || *st.ModelConfigID != agentCase.wantID {
			_ = lockTx.Rollback(ctx)
			t.Fatalf("cached Ping mismatch for agent model %q: %+v", agentCase.modelID, st)
		}
	}
	if err := lockTx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}

	// Disabling m-a and switching default routing must immediately update Ping without waiting for TTL.
	no := false
	if _, err := service.App.UpdateModelProfile(ctx, app.UpdateModelProfileInput{ID: "m-a", Enabled: &no}); err != nil {
		t.Fatal(err)
	}
	afterDisable := service.App.Ping(ctx, testToken, "m-a")
	if !afterDisable.Ready || *afterDisable.ContextWindowTokens != cwB1 || *afterDisable.ModelConfigID != cfgID("https://8.8.8.8/v1", "gpt-b1") {
		t.Fatalf("expected disabled model Ping to fall back immediately to m-b1, got %+v", afterDisable)
	}

	if _, err := service.App.UpdateRouting(ctx, "m-b2"); err != nil {
		t.Fatal(err)
	}
	afterRoute := service.App.Ping(ctx, testToken, "m-a")
	if !afterRoute.Ready || *afterRoute.ContextWindowTokens != cwB2 || *afterRoute.ModelConfigID != cfgID("https://8.8.8.8/v1", "gpt-b2") {
		t.Fatalf("expected routing switch to update Ping fallback immediately to m-b2, got %+v", afterRoute)
	}
}
