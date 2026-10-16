// Package testdb provisions disposable, isolated PostgreSQL schemas for tests.
package testdb

import (
	"context"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"net/url"
	"os"
	"strings"
	"teamie/backend/internal/api"
	"testing"
)

func URL(t *testing.T) string {
	t.Helper()
	raw := os.Getenv("DATABASE_URL_TEST")
	if raw == "" {
		t.Skip("DATABASE_URL_TEST is required for PostgreSQL integration tests")
	}
	ctx := context.Background()
	admin, e := pgxpool.New(ctx, raw)
	if e != nil {
		t.Fatal(e)
	}
	schema := "test_" + strings.ReplaceAll(api.ID(), "-", "")
	if _, e = admin.Exec(ctx, "CREATE SCHEMA "+pgx.Identifier{schema}.Sanitize()); e != nil {
		admin.Close()
		t.Fatal(e)
	}
	t.Cleanup(func() {
		_, _ = admin.Exec(ctx, "DROP SCHEMA "+pgx.Identifier{schema}.Sanitize()+" CASCADE")
		admin.Close()
	})
	u, e := url.Parse(raw)
	if e != nil {
		t.Fatal(e)
	}
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	return u.String()
}
