package gateway_test

import (
	"context"
	"net/url"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"teamie/services/llm-gateway/internal/app"
	"teamie/services/llm-gateway/internal/infra/pg"
)

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
	if len(c.Models) != 1 || c.Routing.DefaultModelID != "default" || c.ResolveModel("").ContextWindow() != 131072 || !c.WebSearch.Enabled {
		t.Fatal("legacy migration did not preserve configuration")
	}
	actual, _ := crypto.Decrypt(c.Models[0].CipherKey)
	actualWeb, _ := crypto.Decrypt(c.WebSearch.CipherKey)
	if actual != modelKey || actualWeb != webKey {
		t.Fatal("migration lost encrypted credentials")
	}
	if strings.Contains(string(c.Models[0].CipherKey), modelKey) || strings.Contains(string(c.WebSearch.CipherKey), webKey) {
		t.Fatal("plaintext credential persisted")
	}
	keyB := "fixture-model-b"
	if _, err = service.App.CreateModelProfile(ctx, app.CreateModelProfileInput{ID: "b", BaseURL: "https://1.1.1.1/v1", Model: "model-b", APIKey: &keyB, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	disabled := false
	c, err = service.App.UpdateModelProfile(ctx, app.UpdateModelProfileInput{ID: "default", Enabled: &disabled})
	if err != nil {
		t.Fatal(err)
	}
	if c.ResolveModel("").ID != "b" || c.Routing.DefaultModelID != "default" {
		t.Fatal("fallback must retain default intent")
	}
	beforeModels, beforeRouting := c.Models, c.Routing
	// This was the destructive scenario: only change search while default A is disabled.
	nextWebKey := "fixture-new-web-key"
	c, err = service.App.UpdateWebSearch(ctx, app.UpdateWebSearchInput{Enabled: &disabled, APIKey: &nextWebKey})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(c.Models, beforeModels) || c.Routing != beforeRouting {
		t.Fatal("search update changed model rows or routing")
	}
	actualWeb, _ = crypto.Decrypt(c.WebSearch.CipherKey)
	if actualWeb != nextWebKey || c.WebSearch.Enabled {
		t.Fatal("search update did not apply")
	}
	beforeSearch := c.WebSearch
	if _, err = service.App.UpdateRouting(ctx, "b"); err != nil {
		t.Fatal(err)
	}
	c, _ = store.Load(ctx)
	if !reflect.DeepEqual(c.Models, beforeModels) || !reflect.DeepEqual(c.WebSearch, beforeSearch) {
		t.Fatal("routing update changed model or search rows")
	}
	// Concurrent independent commands must preserve both writes, including keys.
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	wg.Add(2)
	go func() {
		defer wg.Done()
		name := "Updated B"
		_, e := service.App.UpdateModelProfile(ctx, app.UpdateModelProfileInput{ID: "b", DisplayName: &name})
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
	b := c.ResolveModel("b")
	actualB, _ := crypto.Decrypt(b.CipherKey)
	actualWeb, _ = crypto.Decrypt(c.WebSearch.CipherKey)
	if b.DisplayName != "Updated B" || actualB != keyB || actualWeb != nextWebKey || !c.WebSearch.Enabled {
		t.Fatal("concurrent writes lost data")
	}
	if _, err = service.App.DeleteModelProfile(ctx, "default"); err != nil {
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
		if len(c.Models) != 1 || c.Models[0].ID != "b" {
			t.Fatal("deleted default resurrected after restart")
		}
	}
	service = New(store, crypto, testToken)
	beforeSearch = c.WebSearch
	if _, err = service.App.UpdateModelProfile(ctx, app.UpdateModelProfileInput{ID: "b", ClearAPIKey: true}); err != nil {
		t.Fatal(err)
	}
	c, _ = store.Load(ctx)
	if c.ResolveModel("b").Configured() || !reflect.DeepEqual(c.WebSearch, beforeSearch) {
		t.Fatal("model key clear changed search")
	}
	if _, err = service.App.DeleteModelProfile(ctx, "b"); err != nil {
		t.Fatal(err)
	}
	store.DB.Close()
	store, err = Open(ctx, u.String())
	if err != nil {
		t.Fatal(err)
	}
	c, _ = store.Load(ctx)
	if len(c.Models) != 0 || !reflect.DeepEqual(c.WebSearch, beforeSearch) {
		t.Fatal("empty catalog resurrected or changed search")
	}
	if err = store.Record(ctx, Usage{OwnerID: "owner", AgentID: "agent", SessionID: "session", RunID: "run", ModelID: "b", Status: "success", PromptTokens: 12, CompletionTokens: 8, LatencyMS: 3}); err != nil {
		t.Fatal(err)
	}
	var version, count int
	var modelID string
	_ = store.DB.QueryRow(ctx, "SELECT max(version) FROM gateway_schema_migrations").Scan(&version)
	_ = store.DB.QueryRow(ctx, "SELECT count(*) FROM gateway_usage").Scan(&count)
	_ = store.DB.QueryRow(ctx, "SELECT model_id FROM gateway_usage LIMIT 1").Scan(&modelID)
	if version != 5 || count != 1 || modelID != "b" {
		t.Fatal(version, count, modelID)
	}
}
