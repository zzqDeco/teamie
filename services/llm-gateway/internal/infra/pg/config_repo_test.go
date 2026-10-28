package pg_test

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

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

	if _, err := writerStore.UpdateModels(ctx, func(_ []domain.ModelProfile) ([]domain.ModelProfile, error) {
		return []domain.ModelProfile{{
			ID:                  "model-0",
			DisplayName:         "Model 0",
			BaseURL:             "https://1.1.1.1/v1",
			Model:               "gpt-0",
			CipherKey:           []byte("cipher-0"),
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
		// Hold an exclusive lock on gateway_models so a two-query Load would read
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
		if _, err := writerStore.DB.Conn(txCtx).Exec(txCtx, `INSERT INTO gateway_models(id,display_name,description,base_url,model,encrypted_key,context_window_tokens,enabled) VALUES('model-1','Model 1','rev-1','https://1.1.1.1/v1','gpt-1',$1,32768,true)`, []byte("cipher-1")); err != nil {
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
		if m.ID == c.Routing.DefaultModelID {
			foundDefault = true
			break
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

	makeModel := func(id string, rev int) domain.ModelProfile {
		return domain.ModelProfile{
			ID:                  id,
			DisplayName:         "Model " + id,
			Description:         fmt.Sprintf("rev-%d", rev),
			BaseURL:             "https://1.1.1.1/v1",
			Model:               "gpt-" + id,
			CipherKey:           []byte("cipher-" + id),
			ContextWindowTokens: 32768,
			Enabled:             true,
		}
	}

	initialID := "model-0"
	if _, err := writerStore.UpdateModels(ctx, func(_ []domain.ModelProfile) ([]domain.ModelProfile, error) {
		return []domain.ModelProfile{makeModel(initialID, 0)}, nil
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

			// 1. Insert nextID alongside prevID while routing still points to prevID.
			c1, err := writerStore.UpdateModels(ctx, func(models []domain.ModelProfile) ([]domain.ModelProfile, error) {
				return append(models, makeModel(nextID, rev)), nil
			})
			if err != nil {
				t.Errorf("UpdateModels insert %s: %v", nextID, err)
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
			c3, err := writerStore.UpdateModels(ctx, func(models []domain.ModelProfile) ([]domain.ModelProfile, error) {
				filtered := make([]domain.ModelProfile, 0, len(models))
				for _, m := range models {
					if m.ID != prevID {
						filtered = append(filtered, m)
					}
				}
				return filtered, nil
			})
			if err != nil {
				t.Errorf("UpdateModels delete %s: %v", prevID, err)
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
