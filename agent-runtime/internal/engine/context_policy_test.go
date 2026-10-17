package engine

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	"github.com/cloudwego/eino/schema"
	"github.com/google/uuid"
)

func TestRunContextPolicyIsFrozenAcrossFlagAndWindowChanges(t *testing.T) {
	s, lease, old := nativeModelRun(t)
	ctx := context.Background()
	budget, err := NewContextBudgetSnapshot(16384)
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := s.StartRunWithContextPolicy(ctx, lease, old.Inputs, contextProfileV1, budget)
	if err != nil || replayed.ID != old.ID || contextProfile(replayed) != contextProfileLegacy || string(replayed.ContextBudget) != "{}" {
		t.Fatal("new default rewrote an existing legacy Run", replayed, err)
	}
	inputs := []Input{{EventID: "2", Body: "new context request", ConversationID: "conversation", MessageID: "message"}}
	if err = s.Ingest(ctx, lease, "2", inputs); err != nil {
		t.Fatal(err)
	}
	fresh, err := s.StartRunWithContextPolicy(ctx, lease, inputs, contextProfileV1, budget)
	if err != nil {
		t.Fatal(err)
	}
	wantBudget, err := ParseContextBudgetSnapshot(fresh.ContextBudget)
	if err != nil {
		t.Fatal(err)
	}
	larger, _ := NewContextBudgetSnapshot(131072)
	for _, policy := range []struct {
		profile string
		budget  json.RawMessage
	}{
		{contextProfileLegacy, json.RawMessage(`{}`)},
		{contextProfileV1, larger},
	} {
		next, err := s.StartRunWithContextPolicy(ctx, lease, inputs, policy.profile, policy.budget)
		if err != nil {
			t.Fatal(err)
		}
		got, err := ParseContextBudgetSnapshot(next.ContextBudget)
		if err != nil || next.ID != fresh.ID || next.Attempt == fresh.Attempt || contextProfile(next) != contextProfileV1 || got != wantBudget {
			t.Fatal("recovery recalculated the policy/budget", next, err)
		}
	}
	var ledger int
	if err = s.DB.QueryRow(ctx, `SELECT max(version) FROM agent_runtime_schema_migrations`).Scan(&ledger); err != nil || ledger != runtimeSchemaVersion {
		t.Fatal("context migration ledger missing", ledger, err)
	}
	if _, err = s.DB.Exec(ctx, schemaV3SQL+contextArchiveSchemaSQL+contextRecoverySchemaSQL+schemaV4SQL+schemaV5SQL); err != nil {
		t.Fatal("context migration is not repeatable", err)
	}
}

func TestRestoreFixtureContainsTheCompleteSupportedContextBudget(t *testing.T) {
	raw, err := os.ReadFile("../../../ops/fixtures/runtime-context-budget-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	got, err := ParseContextBudgetSnapshot(raw)
	if err != nil {
		t.Fatal("restore fixture is not a supported frozen budget", err)
	}
	expected, err := makeContextBudgetSnapshotVersion(16384, 1)
	if err != nil || got != expected {
		t.Fatal("restore fixture differs from admitted Run policy", err)
	}
}

func TestRestoreFixtureContainsOutputRecoveryCapacity(t *testing.T) {
	raw, err := os.ReadFile("../../../ops/fixtures/runtime-context-budget-v2.json")
	if err != nil {
		t.Fatal(err)
	}
	got, err := ParseContextBudgetSnapshot(raw)
	expected, makeErr := makeContextBudgetSnapshotVersion(16384, 2)
	if err != nil || makeErr != nil || got != expected {
		t.Fatal("current restore fixture differs from the frozen output policy", err, makeErr)
	}
}

func TestRestoreFixtureContainsCurrentInputPolicy(t *testing.T) {
	raw, err := os.ReadFile("../../../ops/fixtures/runtime-context-budget-v3.json")
	if err != nil {
		t.Fatal(err)
	}
	got, err := ParseContextBudgetSnapshot(raw)
	expected, makeErr := makeContextBudgetSnapshotVersion(16384, 3)
	if err != nil || makeErr != nil || got != expected {
		t.Fatal("historical current-input fixture changed", err, makeErr)
	}
}

func TestRestoreFixtureContainsHistoryCoalescingPolicy(t *testing.T) {
	raw, err := os.ReadFile("../../../ops/fixtures/runtime-context-budget-v4.json")
	if err != nil {
		t.Fatal(err)
	}
	got, err := ParseContextBudgetSnapshot(raw)
	expected, makeErr := makeContextBudgetSnapshot(16384)
	if err != nil || makeErr != nil || got != expected {
		t.Fatal("current restore fixture differs from admission", err, makeErr)
	}
}

func TestNewContextCheckpointRequiresExactPolicyAndBudget(t *testing.T) {
	budget, err := NewContextBudgetSnapshot(16384)
	if err != nil {
		t.Fatal(err)
	}
	run := &Run{ID: uuid.NewString(), Attempt: uuid.NewString(), Revision: 1, toolProfile: currentToolProfile, ContextProfile: contextProfileV1, ContextBudget: budget}
	store := &SessionStore[*schema.AgenticMessage]{Revision: 1}
	store.bind(run, []string{"model-node"})
	var metadata checkpointMetadata
	if err = json.Unmarshal(store.metadata(), &metadata); err != nil || !resumeCompatibleForRun(&metadata, 1, run) {
		t.Fatal("new checkpoint failed its own policy check", metadata, err)
	}
	metadata.ContextBudgetHash = "other-budget"
	if resumeCompatibleForRun(&metadata, 1, run) {
		t.Fatal("checkpoint ignored a different frozen budget")
	}
	metadata.ContextProfile = ""
	metadata.ContextBudgetHash = ""
	if resumeCompatibleForRun(&metadata, 1, run) {
		t.Fatal("new Run accepted an unlabelled legacy checkpoint")
	}
}

func TestContextPolicyRejectsUnsupportedOrPartialBudget(t *testing.T) {
	for _, test := range []struct{ profile, budget string }{
		{"unknown", "{}"}, {contextProfileV1, "{}"}, {contextProfileV1, `{"version":1,"contextWindowTokens":16384}`},
		{contextProfileLegacy, `{"version":1}`}, {contextProfileLegacy, "null"},
	} {
		if _, _, err := canonicalContextPolicy(test.profile, json.RawMessage(test.budget)); err == nil {
			t.Fatal("unsupported context policy admitted", test)
		}
	}
}

func TestContextMigrationBackfillsLegacyWithoutChangingItsTape(t *testing.T) {
	s, lease, run := nativeModelRun(t)
	ctx := context.Background()
	response := []byte(`{"role":"assistant","content":"legacy immutable response"}`)
	if err := s.SaveModel(ctx, lease, run, 1, "legacy-request-hash", response); err != nil {
		t.Fatal(err)
	}
	if err := s.FinishRun(ctx, lease, run, "completed", "", false); err != nil {
		t.Fatal(err)
	}
	// Recreate the exact absence of schema-3 columns/tables around persisted
	// schema-2 Run/input/tape data, then execute the real forward DDL.
	if _, err := s.DB.Exec(ctx, `ALTER TABLE runtime_runs DROP COLUMN context_profile; ALTER TABLE runtime_runs DROP COLUMN context_budget; ALTER TABLE runtime_session_acceptances DROP COLUMN run_id; DROP TABLE runtime_context_artifacts; DROP TABLE runtime_context_recoveries; DELETE FROM agent_runtime_schema_migrations WHERE version>=3;`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec(ctx, schemaSQL+schemaV2SQL+schemaV3SQL+contextArchiveSchemaSQL+contextRecoverySchemaSQL+schemaV4SQL+schemaV5SQL); err != nil {
		t.Fatal(err)
	}
	var profile string
	var budget []byte
	if err := s.DB.QueryRow(ctx, `SELECT context_profile,context_budget FROM runtime_runs WHERE id=$1`, run.ID).Scan(&profile, &budget); err != nil || profile != contextProfileLegacy || string(budget) != "{}" {
		t.Fatal("migration retroactively selected a new Run policy", profile, string(budget), err)
	}
	cached, err := s.CachedModel(ctx, run, 1, "legacy-request-hash")
	if err != nil || string(cached) != string(response) || count(t, s, `SELECT count(*) FROM runtime_inputs`) != 1 {
		t.Fatal("migration rewrote the existing tape or input", err)
	}
	var acceptedRun string
	if err := s.DB.QueryRow(ctx, `SELECT run_id::text FROM runtime_session_acceptances WHERE attempt_id=$1`, run.Attempt).Scan(&acceptedRun); err != nil || acceptedRun != run.ID {
		t.Fatal("migration lost logical Run acceptance history", acceptedRun, err)
	}
}
