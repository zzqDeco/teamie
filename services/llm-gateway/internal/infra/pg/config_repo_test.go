package pg_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"teamie/pkg/postgres"
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

func newIsolatedSchemaDSN(t *testing.T, baseDSN, prefix string) (string, string) {
	t.Helper()
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, baseDSN)
	if err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("%s_%d", prefix, time.Now().UnixNano())
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		admin.Close(ctx)
		t.Fatal(err)
	}
	admin.Close(ctx)

	t.Cleanup(func() {
		cleanCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		c, err := pgx.Connect(cleanCtx, baseDSN)
		if err == nil {
			_, _ = c.Exec(cleanCtx, "DROP SCHEMA "+schema+" CASCADE")
			_ = c.Close(cleanCtx)
		}
	})

	u, err := url.Parse(baseDSN)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	return schema, u.String()
}

type explainPlanNode struct {
	NodeType   string            `json:"Node Type"`
	TotalCost  float64           `json:"Total Cost"`
	PlanRows   float64           `json:"Plan Rows"`
	ActualRows float64           `json:"Actual Rows"`
	Plans      []explainPlanNode `json:"Plans"`
}

type explainJSONEntry struct {
	Plan          explainPlanNode `json:"Plan"`
	PlanningTime  float64         `json:"Planning Time"`
	ExecutionTime float64         `json:"Execution Time"`
	JIT           map[string]any  `json:"JIT"`
}

func assertNoJoinOrSortNodes(t *testing.T, node explainPlanNode) {
	t.Helper()
	if strings.Contains(node.NodeType, "Join") || strings.Contains(node.NodeType, "Nested Loop") || node.NodeType == "Sort" {
		t.Fatalf("forbidden join/sort node %q found in LoadConfigSQL plan: %+v", node.NodeType, node)
	}
	for _, child := range node.Plans {
		assertNoJoinOrSortNodes(t, child)
	}
}

func TestLoadConfigExplainPlanAvoidsJITAndOpenInitializesStats(t *testing.T) {
	ctx := context.Background()
	baseDSN := testdb.URL(t)
	_, schemaDSN := newIsolatedSchemaDSN(t, baseDSN, "gateway_explain")

	conn, err := pgx.Connect(ctx, schemaDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)

	// Apply DDL directly before pg.Open so pg_class.reltuples starts at -1 (missing statistics).
	if _, err := conn.Exec(ctx, pg.SchemaSQL); err != nil {
		t.Fatal(err)
	}
	t0 := time.Date(2026, 10, 30, 8, 0, 0, 0, time.UTC)
	if _, err := conn.Exec(ctx, `INSERT INTO gateway_providers(id,display_name,base_url,encrypted_key,enabled,created_at,updated_at) VALUES
		('p-alpha','Alpha','https://1.1.1.1/v1',$1,true,$4,$4),
		('p-beta','Beta','https://8.8.8.8/v1',$2,true,$5,$5),
		('p-empty','Empty','https://9.9.9.9/v1',$3,true,$6,$6)`,
		[]byte("enc-alpha"), []byte("enc-beta"), []byte("enc-empty"),
		t0, t0.Add(time.Second), t0.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(ctx, `INSERT INTO gateway_models(id,provider_id,display_name,description,model,context_window_tokens,enabled,created_at,updated_at) VALUES
		('m-alpha','p-alpha','Alpha GPT','primary','gpt-4o',65536,true,$1,$1),
		('m-beta-1','p-beta','Beta One','secondary','gpt-4o-mini',131072,true,$2,$2),
		('m-beta-2','p-beta','Beta Two','disabled','gpt-4.1',32768,false,$3,$3)`,
		t0, t0.Add(time.Second), t0.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(ctx, `UPDATE gateway_model_routing SET default_model_id='m-alpha', updated_at=$1 WHERE id`, t0); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(ctx, `UPDATE gateway_web_search_config SET encrypted_key=$1, enabled=true, updated_at=$2 WHERE id`, []byte("enc-web"), t0); err != nil {
		t.Fatal(err)
	}

	var unanalyzedCount int
	if err := conn.QueryRow(ctx, `
		SELECT count(*)
		FROM pg_class
		WHERE oid IN (
			'gateway_model_routing'::regclass,
			'gateway_web_search_config'::regclass,
			'gateway_providers'::regclass,
			'gateway_models'::regclass
		) AND reltuples < 0`).Scan(&unanalyzedCount); err != nil {
		t.Fatal(err)
	}
	if unanalyzedCount != 4 {
		t.Fatalf("expected all 4 tables to have missing stats (reltuples = -1) before ANALYZE, got %d", unanalyzedCount)
	}

	runExplain := func(phase string, maxTotalCost, maxPlanRows float64) explainJSONEntry {
		t.Helper()
		var rawJSON string
		if err := conn.QueryRow(ctx, "EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON) "+pg.LoadConfigSQL).Scan(&rawJSON); err != nil {
			t.Fatalf("%s EXPLAIN failed: %v", phase, err)
		}
		var entries []explainJSONEntry
		if err := json.Unmarshal([]byte(rawJSON), &entries); err != nil || len(entries) != 1 {
			t.Fatalf("%s EXPLAIN JSON parse failed (%v): %s", phase, err, rawJSON)
		}
		e := entries[0]
		t.Logf("%s EXPLAIN: node=%s total_cost=%.2f plan_rows=%.0f actual_rows=%.0f planning_ms=%.3f exec_ms=%.3f jit=%v",
			phase, e.Plan.NodeType, e.Plan.TotalCost, e.Plan.PlanRows, e.Plan.ActualRows, e.PlanningTime, e.ExecutionTime, e.JIT != nil)
		assertNoJoinOrSortNodes(t, e.Plan)
		if e.JIT != nil {
			t.Fatalf("%s unexpectedly triggered PostgreSQL LLVM JIT: %+v", phase, e.JIT)
		}
		if e.Plan.TotalCost > maxTotalCost {
			t.Fatalf("%s Total Cost %.2f exceeded threshold %.2f (jit_above_cost=100000)", phase, e.Plan.TotalCost, maxTotalCost)
		}
		if e.Plan.PlanRows > maxPlanRows {
			t.Fatalf("%s Plan Rows %.0f exceeded threshold %.0f", phase, e.Plan.PlanRows, maxPlanRows)
		}
		if e.Plan.ActualRows != 8 {
			t.Fatalf("%s expected Actual Rows = 8 (1 routing + 1 web_search + 3 providers + 3 models), got %.0f", phase, e.Plan.ActualRows)
		}
		return e
	}

	// Phase 1: Missing statistics (reltuples = -1 on all 4 tables).
	runExplain("missing_stats", 100.0, 1000.0)

	rawDB, err := postgres.OpenDB(ctx, schemaDSN)
	if err != nil {
		t.Fatal(err)
	}
	rawStore := &pg.PGStore{DB: rawDB}
	for i := 0; i < 20; i++ {
		start := time.Now()
		cfg, err := rawStore.Load(ctx)
		elapsed := time.Since(start)
		if err != nil {
			rawDB.Close()
			t.Fatalf("unanalyzed Load(%d) failed: %v", i, err)
		}
		if len(cfg.Providers) != 3 || len(cfg.Models) != 3 || cfg.Routing.DefaultModelID != "m-alpha" || !cfg.WebSearch.Enabled {
			rawDB.Close()
			t.Fatalf("unexpected config snapshot under missing stats: %+v", cfg)
		}
		if elapsed > 100*time.Millisecond {
			rawDB.Close()
			t.Fatalf("unanalyzed Load(%d) took %s (> 100ms)", i, elapsed)
		}
	}
	rawDB.Close()

	// Phase 2: pg.Open automatically initializes table statistics via ANALYZE when reltuples < 0.
	openedStore, err := pg.Open(ctx, schemaDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer openedStore.DB.Close()

	if err := conn.QueryRow(ctx, `
		SELECT count(*)
		FROM pg_class
		WHERE oid IN (
			'gateway_model_routing'::regclass,
			'gateway_web_search_config'::regclass,
			'gateway_providers'::regclass,
			'gateway_models'::regclass
		) AND reltuples < 0`).Scan(&unanalyzedCount); err != nil {
		t.Fatal(err)
	}
	if unanalyzedCount != 0 {
		t.Fatalf("expected pg.Open to initialize stats on all 4 config tables, still have %d with reltuples < 0", unanalyzedCount)
	}

	analyzed := runExplain("analyzed", 15.0, 8.0)
	if analyzed.Plan.PlanRows != 8 {
		t.Fatalf("expected analyzed Plan Rows = 8, got %.0f", analyzed.Plan.PlanRows)
	}
}

func TestLoadConfigEdgeCasesAndOrdering(t *testing.T) {
	ctx := context.Background()
	baseDSN := testdb.URL(t)
	_, schemaDSN := newIsolatedSchemaDSN(t, baseDSN, "gateway_edges")

	store, err := pg.Open(ctx, schemaDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer store.DB.Close()

	// 1. Empty catalog (0 providers, 0 models).
	emptyCfg, err := store.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if emptyCfg.Providers != nil || emptyCfg.Models != nil {
		t.Fatalf("expected nil Providers and Models slices for empty catalog, got providers=%v models=%v", emptyCfg.Providers, emptyCfg.Models)
	}
	if emptyCfg.Routing.DefaultModelID != "default" || emptyCfg.WebSearch.Enabled || emptyCfg.WebSearch.Configured() {
		t.Fatalf("unexpected default singleton state: routing=%+v webSearch=%+v", emptyCfg.Routing, emptyCfg.WebSearch)
	}
	if emptyCfg.ResolveModel("").Configured() {
		t.Fatal("empty catalog must resolve as unconfigured")
	}
	if emptyAuto, err := store.UpdateRouting(ctx, domain.ModelRoutingConfig{DefaultModelID: ""}); err != nil || emptyAuto.Routing.DefaultModelID != "" {
		t.Fatalf("expected UpdateRouting empty default to persist empty DefaultModelID, got %+v (%v)", emptyAuto.Routing, err)
	}

	// 2. Mixed catalog states & deterministic (CreatedAt ASC, ID ASC) ordering.
	tBase := time.Date(2026, 10, 30, 9, 0, 0, 0, time.UTC)
	if _, err := store.DB.Exec(ctx, `INSERT INTO gateway_providers(id,display_name,base_url,encrypted_key,enabled,created_at,updated_at) VALUES
		('prov-later','Later Provider','https://1.1.1.1/v1',$1,true,$5,$5),
		('prov-empty-b','Empty Provider B','https://8.8.8.8/v1',$2,true,$4,$4),
		('prov-disabled-a','Disabled Provider A','https://8.8.4.4/v1',$3,false,$4,$4),
		('prov-nokey-c','NoKey Provider C','https://9.9.9.9/v1',NULL,true,$4,$4)`,
		[]byte("key-later"), []byte("key-empty"), []byte("key-disabled"),
		tBase, tBase.Add(5*time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB.Exec(ctx, `INSERT INTO gateway_models(id,provider_id,display_name,description,model,context_window_tokens,enabled,created_at,updated_at) VALUES
		('mod-later','prov-later','Later Model','later','gpt-later',65536,true,$3,$3),
		('mod-same-z','prov-later','Same Time Z','disabled-model','gpt-z',32768,false,$1,$1),
		('mod-same-a','prov-disabled-a','Same Time A','disabled-provider','gpt-a',16384,true,$1,$1),
		('mod-nokey','prov-nokey-c','NoKey Model','missing-key','gpt-nokey',16384,true,$2,$2)`,
		tBase, tBase.Add(time.Second), tBase.Add(3*time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpdateRouting(ctx, domain.ModelRoutingConfig{DefaultModelID: "mod-same-z"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpdateWebSearch(ctx, func(old domain.WebSearchConfig) (domain.WebSearchConfig, error) {
		old.Enabled = true
		old.CipherKey = []byte("web-secret")
		return old, nil
	}); err != nil {
		t.Fatal(err)
	}

	cfg, err := store.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	gotProviderIDs := []string{cfg.Providers[0].ID, cfg.Providers[1].ID, cfg.Providers[2].ID, cfg.Providers[3].ID}
	wantProviderIDs := []string{"prov-disabled-a", "prov-empty-b", "prov-nokey-c", "prov-later"}
	if !reflect.DeepEqual(gotProviderIDs, wantProviderIDs) {
		t.Fatalf("providers order mismatch: got %v, want %v", gotProviderIDs, wantProviderIDs)
	}
	gotModelIDs := []string{cfg.Models[0].ID, cfg.Models[1].ID, cfg.Models[2].ID, cfg.Models[3].ID}
	wantModelIDs := []string{"mod-same-a", "mod-same-z", "mod-nokey", "mod-later"}
	if !reflect.DeepEqual(gotModelIDs, wantModelIDs) {
		t.Fatalf("models order mismatch: got %v, want %v", gotModelIDs, wantModelIDs)
	}

	// Disabled selected default preserves intent while fallback picks the only available model (mod-later).
	if cfg.Routing.DefaultModelID != "mod-same-z" || cfg.EffectiveDefaultModelID() != "mod-later" {
		t.Fatalf("unexpected routing/fallback: routing=%q effective=%q", cfg.Routing.DefaultModelID, cfg.EffectiveDefaultModelID())
	}
	rmDisabledProv, _ := cfg.ResolveExactModel("mod-same-a")
	if rmDisabledProv.ProviderEnabled || !rmDisabledProv.ModelEnabled || rmDisabledProv.Available() {
		t.Fatalf("expected disabled provider to disable resolved model: %+v", rmDisabledProv)
	}
	rmDisabledModel, _ := cfg.ResolveExactModel("mod-same-z")
	if !rmDisabledModel.ProviderEnabled || rmDisabledModel.ModelEnabled || rmDisabledModel.Available() {
		t.Fatalf("expected disabled model to be unavailable: %+v", rmDisabledModel)
	}
	rmNoKey, _ := cfg.ResolveExactModel("mod-nokey")
	if rmNoKey.Configured() || rmNoKey.Available() || rmNoKey.CipherKey != nil {
		t.Fatalf("expected keyless provider model to be unconfigured with nil CipherKey: %+v", rmNoKey)
	}
	if !cfg.WebSearch.Enabled || string(cfg.WebSearch.CipherKey) != "web-secret" {
		t.Fatalf("unexpected web search state: %+v", cfg.WebSearch)
	}

	// 3. Missing singleton row fails closed rather than inventing empty defaults.
	if _, err := store.DB.Exec(ctx, `DELETE FROM gateway_web_search_config`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(ctx); err == nil || !strings.Contains(err.Error(), "gateway configuration not initialized") {
		t.Fatalf("expected missing singleton error, got %v", err)
	}
}

func TestLoadForPingCoalescesIdleReadsAndInvalidatesImmediatelyOnWrites(t *testing.T) {
	ctx := context.Background()
	baseDSN := testdb.URL(t)
	_, schemaDSN := newIsolatedSchemaDSN(t, baseDSN, "gateway_ping_cache")

	store, err := pg.Open(ctx, schemaDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer store.DB.Close()

	var clockMu sync.Mutex
	now := time.Date(2026, 10, 30, 10, 0, 0, 0, time.UTC)
	store.PingCacheTTL = 5 * time.Second
	store.Now = func() time.Time {
		clockMu.Lock()
		defer clockMu.Unlock()
		return now
	}
	advanceClock := func(d time.Duration) {
		clockMu.Lock()
		now = now.Add(d)
		clockMu.Unlock()
	}

	if _, err := store.UpdateCatalog(ctx, func(_ []domain.ProviderProfile, _ []domain.ModelProfile) ([]domain.ProviderProfile, []domain.ModelProfile, error) {
		return []domain.ProviderProfile{{
				ID:          "prov-1",
				DisplayName: "Provider 1",
				BaseURL:     "https://1.1.1.1/v1",
				CipherKey:   []byte("cipher-1"),
				Enabled:     true,
			}}, []domain.ModelProfile{
				{
					ID:                  "model-a",
					ProviderID:          "prov-1",
					DisplayName:         "Model A",
					Model:               "gpt-a",
					ContextWindowTokens: 32768,
					Enabled:             true,
				},
				{
					ID:                  "model-b",
					ProviderID:          "prov-1",
					DisplayName:         "Model B",
					Model:               "gpt-b",
					ContextWindowTokens: 65536,
					Enabled:             true,
				},
			}, nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpdateRouting(ctx, domain.ModelRoutingConfig{DefaultModelID: "model-a"}); err != nil {
		t.Fatal(err)
	}

	lockDB, err := postgres.OpenDB(ctx, schemaDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer lockDB.Close()
	checkConn, err := pgx.Connect(ctx, schemaDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer checkConn.Close(ctx)

	// 1. Coalesce 8 concurrent misses + 1 short-deadline caller into a single PostgreSQL query.
	const healthyCallers = 8
	results := make(chan error, healthyCallers)
	if err := lockDB.WithinTx(ctx, func(txCtx context.Context) error {
		if _, err := lockDB.Conn(txCtx).Exec(txCtx, `LOCK TABLE gateway_models IN ACCESS EXCLUSIVE MODE`); err != nil {
			return err
		}

		for i := 0; i < healthyCallers; i++ {
			go func() {
				c, err := store.LoadForPing(ctx)
				if err != nil {
					results <- err
					return
				}
				if c.Routing.DefaultModelID != "model-a" || len(c.Models) != 2 {
					results <- fmt.Errorf("unexpected coalesced config: %+v", c)
					return
				}
				// Mutating caller slice must never corrupt cached snapshot.
				c.Models[0].ID = "corrupted-by-caller"
				c.Providers[0].CipherKey[0] = 'X'
				results <- nil
			}()
		}

		cancelCtx, cancel := context.WithTimeout(ctx, 30*time.Millisecond)
		defer cancel()
		if _, err := store.LoadForPing(cancelCtx); !errors.Is(err, context.DeadlineExceeded) {
			return fmt.Errorf("expected canceled caller to return DeadlineExceeded while DB locked, got %v", err)
		}

		var waitingLocks int
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			_ = checkConn.QueryRow(ctx, `SELECT count(*) FROM pg_locks WHERE relation = 'gateway_models'::regclass AND NOT granted`).Scan(&waitingLocks)
			if waitingLocks > 0 {
				break
			}
			time.Sleep(5 * time.Millisecond)
		}
		if waitingLocks != 1 {
			return fmt.Errorf("expected 8 concurrent LoadForPing misses to coalesce into 1 SQL query, saw %d waiting locks", waitingLocks)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < healthyCallers; i++ {
		if err := <-results; err != nil {
			t.Fatalf("coalesced LoadForPing caller %d failed: %v", i, err)
		}
	}

	// 2. Within TTL, LoadForPing serves from memory with ZERO PostgreSQL queries
	// (verified deterministically by holding ACCESS EXCLUSIVE lock on gateway_models).
	advanceClock(2 * time.Second)
	if err := lockDB.WithinTx(ctx, func(txCtx context.Context) error {
		if _, err := lockDB.Conn(txCtx).Exec(txCtx, `LOCK TABLE gateway_models IN ACCESS EXCLUSIVE MODE`); err != nil {
			return err
		}
		hitCtx, hitCancel := context.WithTimeout(ctx, 500*time.Millisecond)
		defer hitCancel()
		for i := 0; i < 16; i++ {
			c, err := store.LoadForPing(hitCtx)
			if err != nil {
				return fmt.Errorf("expected cached LoadForPing hit without touching locked table, got err: %w", err)
			}
			if c.Models[0].ID != "model-a" || string(c.Providers[0].CipherKey) != "cipher-1" {
				return fmt.Errorf("cached snapshot was mutated or invalid: %+v", c)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	// 3. Advancing clock past TTL reloads fresh out-of-band DB state.
	if _, err := checkConn.Exec(ctx, `UPDATE gateway_model_routing SET default_model_id = 'model-b' WHERE id`); err != nil {
		t.Fatal(err)
	}
	stillCached, err := store.LoadForPing(ctx)
	if err != nil || stillCached.Routing.DefaultModelID != "model-a" {
		t.Fatalf("expected cached value before TTL expiry, got %+v (%v)", stillCached.Routing, err)
	}
	advanceClock(4 * time.Second) // total +6s > 5s TTL
	reloaded, err := store.LoadForPing(ctx)
	if err != nil || reloaded.Routing.DefaultModelID != "model-b" {
		t.Fatalf("expected TTL expiry to reload model-b, got %+v (%v)", reloaded.Routing, err)
	}

	// 4. Every PGStore write method invalidates the cache immediately (0ms, no clock advance).
	if _, err := store.UpdateRouting(ctx, domain.ModelRoutingConfig{DefaultModelID: "model-a"}); err != nil {
		t.Fatal(err)
	}
	afterRouting, err := store.LoadForPing(ctx)
	if err != nil || afterRouting.Routing.DefaultModelID != "model-a" {
		t.Fatalf("UpdateRouting did not immediately invalidate Ping cache: %+v (%v)", afterRouting.Routing, err)
	}

	if _, err := store.UpdateCatalog(ctx, func(providers []domain.ProviderProfile, models []domain.ModelProfile) ([]domain.ProviderProfile, []domain.ModelProfile, error) {
		for i := range models {
			if models[i].ID == "model-a" {
				models[i].Enabled = false
			}
		}
		return providers, models, nil
	}); err != nil {
		t.Fatal(err)
	}
	afterCatalog, err := store.LoadForPing(ctx)
	if err != nil || afterCatalog.ResolveModel("").ID != "model-b" {
		t.Fatalf("UpdateCatalog did not immediately invalidate Ping cache: resolved=%q err=%v", afterCatalog.ResolveModel("").ID, err)
	}

	if _, err := store.UpdateWebSearch(ctx, func(old domain.WebSearchConfig) (domain.WebSearchConfig, error) {
		old.Enabled = true
		old.CipherKey = []byte("web-key-now")
		return old, nil
	}); err != nil {
		t.Fatal(err)
	}
	afterWeb, err := store.LoadForPing(ctx)
	if err != nil || !afterWeb.WebSearch.Enabled || string(afterWeb.WebSearch.CipherKey) != "web-key-now" {
		t.Fatalf("UpdateWebSearch did not immediately invalidate Ping cache: %+v (%v)", afterWeb.WebSearch, err)
	}
}
