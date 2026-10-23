package gateway_test

import (
	"context"
	"github.com/jackc/pgx/v5"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

func TestPostgresEncryptedPersistenceUsageAndKeyPreservation(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL_TEST")
	if dsn == "" {
		t.Skip("DATABASE_URL_TEST is unset")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	admin, e := pgx.Connect(ctx, dsn)
	if e != nil {
		t.Fatal(e)
	}
	defer admin.Close(context.Background())
	schema := "gateway_test_" + time.Now().Format("150405000000000")
	if _, e = admin.Exec(ctx, "CREATE SCHEMA "+schema); e != nil {
		t.Fatal(e)
	}
	defer admin.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE")
	u, e := url.Parse(dsn)
	if e != nil {
		t.Fatal(e)
	}
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	store, e := Open(ctx, u.String())
	if e != nil {
		t.Fatal(e)
	}
	crypto := testCipher(t)
	key := "fixture-encrypted-key"
	webKey := "tvly-fixture-encrypted-key"
	c, e := store.Save(ctx, Config{BaseURL: "https://example.com/v1", Model: "model", Enabled: true, WebSearchEnabled: true, WebSearchEnabledSet: true, WebSearchAPIKey: &webKey}, &key, false, crypto)
	if e != nil {
		t.Fatal(e)
	}
	if strings.Contains(string(c.CipherKey), key) || strings.Contains(string(c.WebSearchCipherKey), webKey) {
		t.Fatal("plaintext credential persisted")
	}
	store.DB.Close()
	store, e = Open(ctx, u.String())
	if e != nil {
		t.Fatal(e)
	}
	defer store.DB.Close()
	c, e = store.Load(ctx)
	if e != nil {
		t.Fatal(e)
	}
	actual, e := crypto.Decrypt(c.CipherKey)
	if e != nil || actual != key {
		t.Fatal("credential not recoverable after restart", e)
	}
	actualWeb, e := crypto.Decrypt(c.WebSearchCipherKey)
	if e != nil || actualWeb != webKey || !c.WebSearchEnabled || !c.WebSearchConfigured() {
		t.Fatal("web search credential not recoverable after restart", e, c.WebSearchEnabled)
	}
	c.Model = "new-model"
	c.ContextWindowTokens = 131072
	c, e = store.Save(ctx, c, nil, false, crypto)
	if e != nil || !c.Configured() || !c.WebSearchConfigured() {
		t.Fatal(e)
	}
	c, e = store.Save(ctx, Config{BaseURL: c.BaseURL, Model: c.Model, Enabled: c.Enabled}, nil, false, crypto)
	if e != nil || c.ContextWindow() != 131072 || !c.WebSearchEnabled || !c.WebSearchConfigured() {
		t.Fatal("omitted context or web search metadata was not preserved", c.ContextWindow(), c.WebSearchEnabled, e)
	}
	if e = store.Record(ctx, Usage{OwnerID: "owner", AgentID: "agent", SessionID: "session", RunID: "run", Status: "success", PromptTokens: 12, CompletionTokens: 8, LatencyMS: 3}); e != nil {
		t.Fatal(e)
	}
	var owner, status string
	var version, count int
	_ = store.DB.QueryRow(ctx, "SELECT max(version) FROM gateway_schema_migrations").Scan(&version)
	_ = store.DB.QueryRow(ctx, "SELECT count(*) FROM gateway_usage").Scan(&count)
	_ = store.DB.QueryRow(ctx, "SELECT owner_id,status FROM gateway_usage LIMIT 1").Scan(&owner, &status)
	if version != 3 || count != 1 || owner != "owner" || status != "success" || c.ContextWindow() != 131072 {
		t.Fatal(version, count, owner, status)
	}
}
