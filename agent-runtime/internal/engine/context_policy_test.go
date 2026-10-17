package engine

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/cloudwego/eino/schema"
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
	if err = s.DB.QueryRow(ctx, `SELECT max(version) FROM agent_runtime_schema_migrations`).Scan(&ledger); err != nil || ledger != 3 {
		t.Fatal("context migration ledger missing", ledger, err)
	}
	if _, err = s.DB.Exec(ctx, schemaV3SQL+contextArchiveSchemaSQL+contextRecoverySchemaSQL); err != nil {
		t.Fatal("context migration is not repeatable", err)
	}
}

func TestNewContextCheckpointRequiresExactPolicyAndBudget(t *testing.T) {
	budget, err := NewContextBudgetSnapshot(16384)
	if err != nil {
		t.Fatal(err)
	}
	run := &Run{ID: "run", Attempt: "attempt", Revision: 1, toolProfile: currentToolProfile, ContextProfile: contextProfileV1, ContextBudget: budget}
	store := &SessionStore[*schema.AgenticMessage]{Revision: 1}
	store.bind(run, nil)
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
