package pg_test

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"teamie/pkg/postgres/testdb"
	"teamie/services/llm-gateway/internal/domain"
	"teamie/services/llm-gateway/internal/infra/pg"
)

func TestLoadInterleavedModelReplaceAndRoutingNeverReturnsMixedSnapshot(t *testing.T) {
	ctx := context.Background()
	dsn := testdb.URL(t)
	readerStore, err := pg.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer readerStore.DB.Close()

	writerStore, err := pg.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer writerStore.DB.Close()

	if _, err := writerStore.UpdateCatalog(ctx, func(_ []domain.ProviderProfile, _ []domain.ModelProfile) ([]domain.ProviderProfile, []domain.ModelProfile, error) {
		return []domain.ProviderProfile{
				{
					ID:          "prov-empty",
					DisplayName: "Empty Provider",
					BaseURL:     "https://8.8.8.8/v1",
					CipherKey:   []byte("cipher-empty"),
					Enabled:     true,
				},
				{
					ID:          "prov-0",
					DisplayName: "Provider 0",
					BaseURL:     "https://1.1.1.1/v1",
					CipherKey:   []byte("cipher-0"),
					Enabled:     true,
				},
			}, []domain.ModelProfile{{
				ID:                  "model-0",
				ProviderID:          "prov-0",
				DisplayName:         "Model 0",
				Model:               "gpt-0",
				ContextWindowTokens: 16384,
				Enabled:             true,
			}}, nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := writerStore.UpdateRouting(ctx, domain.ModelRoutingConfig{DefaultModelID: "model-0"}); err != nil {
		t.Fatal(err)
	}

	type loadResult struct {
		cfg domain.Config
		err error
	}
	loadDone := make(chan loadResult, 1)

	if err := writerStore.WithinTx(ctx, func(txCtx context.Context) error {
		// Hold an exclusive lock on gateway_models so a multi-query Load would read
		// gateway_model_routing + gateway_web_search_config before this transaction
		// commits, and then read gateway_models after this transaction commits.
		if _, err := writerStore.DB.Conn(txCtx).Exec(txCtx, `LOCK TABLE gateway_models IN ACCESS EXCLUSIVE MODE`); err != nil {
			return err
		}

		go func() {
			c, err := readerStore.Load(ctx)
			loadDone <- loadResult{cfg: c, err: err}
		}()

		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			var waiting bool
			_ = readerStore.DB.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_locks WHERE relation = 'gateway_models'::regclass AND NOT granted)`).Scan(&waiting)
			if waiting {
				break
			}
			time.Sleep(5 * time.Millisecond)
		}

		if _, err := writerStore.DB.Conn(txCtx).Exec(txCtx, `DELETE FROM gateway_models WHERE id = 'model-0'`); err != nil {
			return err
		}
		if _, err := writerStore.DB.Conn(txCtx).Exec(txCtx, `DELETE FROM gateway_providers WHERE id = 'prov-0'`); err != nil {
			return err
		}
		if _, err := writerStore.DB.Conn(txCtx).Exec(txCtx, `INSERT INTO gateway_providers(id,display_name,base_url,encrypted_key,enabled) VALUES('prov-1','Provider 1','https://1.1.1.1/v1',$1,true)`, []byte("cipher-1")); err != nil {
			return err
		}
		if _, err := writerStore.DB.Conn(txCtx).Exec(txCtx, `INSERT INTO gateway_models(id,provider_id,display_name,description,model,context_window_tokens,enabled) VALUES('model-1','prov-1','Model 1','rev-1','gpt-1',32768,true)`); err != nil {
			return err
		}
		if _, err := writerStore.DB.Conn(txCtx).Exec(txCtx, `UPDATE gateway_model_routing SET default_model_id = 'model-1', updated_at = now() WHERE id`); err != nil {
			return err
		}
		if _, err := writerStore.DB.Conn(txCtx).Exec(txCtx, `UPDATE gateway_web_search_config SET encrypted_key = $1, enabled = true, updated_at = now() WHERE id`, []byte("web-cipher-1")); err != nil {
			return err
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	res := <-loadDone
	if res.err != nil {
		t.Fatalf("Load failed: %v", res.err)
	}
	assertConsistentSnapshot(t, res.cfg)
	foundEmptyProvider := false
	for _, p := range res.cfg.Providers {
		if p.ID == "prov-empty" {
			foundEmptyProvider = true
		}
	}
	if !foundEmptyProvider {
		t.Fatalf("expected empty provider prov-empty to remain visible in single-SQL snapshot, got %+v", res.cfg.Providers)
	}
}

func assertConsistentSnapshot(t *testing.T, c domain.Config) {
	t.Helper()
	if c.Routing.DefaultModelID == "" {
		t.Fatalf("expected non-empty routing defaultModelId in snapshot, got empty (models=%v)", c.Models)
	}
	if len(c.Models) < 1 || len(c.Models) > 2 {
		t.Fatalf("expected 1 or 2 models in snapshot, got %d: %+v", len(c.Models), c.Models)
	}
	foundDefault := false
	for _, m := range c.Models {
		if _, ok := c.ProviderByID(m.ProviderID); !ok {
			t.Fatalf("model %q references missing provider %q in snapshot (providers=%+v)", m.ID, m.ProviderID, c.Providers)
		}
		if m.ID == c.Routing.DefaultModelID {
			foundDefault = true
		}
	}
	if !foundDefault {
		ids := make([]string, 0, len(c.Models))
		for _, m := range c.Models {
			ids = append(ids, m.ID)
		}
		t.Fatalf("inconsistent config snapshot: routing.DefaultModelID=%q is absent from models=%v", c.Routing.DefaultModelID, ids)
	}
	if got := c.EffectiveDefaultModelID(); got != c.Routing.DefaultModelID {
		t.Fatalf("inconsistent effective default model: got %q, want %q", got, c.Routing.DefaultModelID)
	}
	if c.WebSearch.Enabled != c.WebSearch.Configured() {
		t.Fatalf("inconsistent web search snapshot: enabled=%v configured=%v", c.WebSearch.Enabled, c.WebSearch.Configured())
	}
}

func TestLoadSingleConsistentSnapshotUnderConcurrentUpdates(t *testing.T) {
	ctx := context.Background()
	dsn := testdb.URL(t)
	readerStore, err := pg.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer readerStore.DB.Close()

	writerStore, err := pg.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer writerStore.DB.Close()

	sharedProvider := domain.ProviderProfile{
		ID:          "shared-provider",
		DisplayName: "Shared Provider",
		BaseURL:     "https://1.1.1.1/v1",
		CipherKey:   []byte("cipher-shared"),
		Enabled:     true,
	}
	makeModel := func(id string, rev int) domain.ModelProfile {
		return domain.ModelProfile{
			ID:                  id,
			ProviderID:          sharedProvider.ID,
			DisplayName:         "Model " + id,
			Description:         fmt.Sprintf("rev-%d", rev),
			Model:               "gpt-" + id,
			ContextWindowTokens: 32768,
			Enabled:             true,
		}
	}

	initialID := "model-0"
	if _, err := writerStore.UpdateCatalog(ctx, func(_ []domain.ProviderProfile, _ []domain.ModelProfile) ([]domain.ProviderProfile, []domain.ModelProfile, error) {
		return []domain.ProviderProfile{sharedProvider}, []domain.ModelProfile{makeModel(initialID, 0)}, nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := writerStore.UpdateRouting(ctx, domain.ModelRoutingConfig{DefaultModelID: initialID}); err != nil {
		t.Fatal(err)
	}

	stop := make(chan struct{})
	var readersWG sync.WaitGroup
	for r := 0; r < 4; r++ {
		readersWG.Add(1)
		go func() {
			defer readersWG.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				snap, err := readerStore.Load(ctx)
				if err != nil {
					t.Errorf("Load failed: %v", err)
					return
				}
				assertConsistentSnapshot(t, snap)
			}
		}()
	}

	var writersWG sync.WaitGroup
	writersWG.Add(2)

	const iterations = 100
	go func() {
		defer writersWG.Done()
		for i := 1; i <= iterations; i++ {
			prevID := fmt.Sprintf("model-%d", i-1)
			nextID := fmt.Sprintf("model-%d", i)
			rev := i

			// 1. Insert nextID alongside prevID under sharedProvider while routing still points to prevID.
			c1, err := writerStore.UpdateCatalog(ctx, func(providers []domain.ProviderProfile, models []domain.ModelProfile) ([]domain.ProviderProfile, []domain.ModelProfile, error) {
				providers[0].CipherKey = []byte(fmt.Sprintf("cipher-shared-%d", rev))
				return providers, append(models, makeModel(nextID, rev)), nil
			})
			if err != nil {
				t.Errorf("UpdateCatalog insert %s: %v", nextID, err)
				return
			}
			assertConsistentSnapshot(t, c1)

			// 2. Switch routing default to nextID while both prevID and nextID exist.
			c2, err := writerStore.UpdateRouting(ctx, domain.ModelRoutingConfig{DefaultModelID: nextID})
			if err != nil {
				t.Errorf("UpdateRouting %s: %v", nextID, err)
				return
			}
			assertConsistentSnapshot(t, c2)

			// 3. Delete prevID now that routing points to nextID.
			c3, err := writerStore.UpdateCatalog(ctx, func(providers []domain.ProviderProfile, models []domain.ModelProfile) ([]domain.ProviderProfile, []domain.ModelProfile, error) {
				filtered := make([]domain.ModelProfile, 0, len(models))
				for _, m := range models {
					if m.ID != prevID {
						filtered = append(filtered, m)
					}
				}
				return providers, filtered, nil
			})
			if err != nil {
				t.Errorf("UpdateCatalog delete %s: %v", prevID, err)
				return
			}
			assertConsistentSnapshot(t, c3)
		}
	}()

	go func() {
		defer writersWG.Done()
		for i := 1; i <= iterations; i++ {
			enable := i%2 == 1
			iter := i
			c, err := writerStore.UpdateWebSearch(ctx, func(old domain.WebSearchConfig) (domain.WebSearchConfig, error) {
				old.Enabled = enable
				if enable {
					old.CipherKey = []byte(fmt.Sprintf("web-cipher-%d", iter))
				} else {
					old.CipherKey = nil
				}
				return old, nil
			})
			if err != nil {
				t.Errorf("UpdateWebSearch %d: %v", i, err)
				return
			}
			assertConsistentSnapshot(t, c)
		}
	}()

	writersWG.Wait()
	close(stop)
	readersWG.Wait()
}

func TestSchemaVersionFenceRejectsNewerAndIncompleteLedgers(t *testing.T) {
	ctx := context.Background()
	baseDSN := testdb.URL(t)

	for _, tc := range []struct {
		name     string
		versions []int
	}{
		{name: "empty_ledger", versions: nil},
		{name: "gapped_ledger", versions: []int{1, 2, 4}},
		{name: "future_schema_v7", versions: []int{1, 2, 3, 4, 5, 6, 7}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			admin, err := pgx.Connect(ctx, baseDSN)
			if err != nil {
				t.Fatal(err)
			}
			defer admin.Close(ctx)
			schema := fmt.Sprintf("gateway_fence_%s_%d", tc.name, time.Now().UnixNano())
			if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
				t.Fatal(err)
			}
			defer admin.Exec(ctx, "DROP SCHEMA "+schema+" CASCADE")

			u, _ := url.Parse(baseDSN)
			q := u.Query()
			q.Set("search_path", schema)
			u.RawQuery = q.Encode()

			conn, err := pgx.Connect(ctx, u.String())
			if err != nil {
				t.Fatal(err)
			}
			if _, err := conn.Exec(ctx, `CREATE TABLE gateway_schema_migrations(version INTEGER PRIMARY KEY, applied_at TIMESTAMPTZ NOT NULL DEFAULT now())`); err != nil {
				conn.Close(ctx)
				t.Fatal(err)
			}
			for _, v := range tc.versions {
				if _, err := conn.Exec(ctx, `INSERT INTO gateway_schema_migrations(version) VALUES($1)`, v); err != nil {
					conn.Close(ctx)
					t.Fatal(err)
				}
			}
			conn.Close(ctx)

			store, err := pg.Open(ctx, u.String())
			if err == nil {
				store.DB.Close()
				t.Fatalf("expected Open to reject %s, but succeeded", tc.name)
			}
			if !errors.Is(err, pg.ErrUnsupportedGatewaySchema) {
				t.Fatalf("expected ErrUnsupportedGatewaySchema, got %v", err)
			}
		})
	}
}
