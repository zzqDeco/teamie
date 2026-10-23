package eino

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cloudwego/eino/adk"
	"github.com/google/uuid"
)

func enableContextFixture(t *testing.T, e *Engine) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "flags.json")
	if err := os.WriteFile(path, []byte(`{"revision":"context-fixture-v1","flags":{"agents.runtime.v3":true,"agents.tasks.execute":true,"agents.context.v1":true}}`), 0600); err != nil {
		t.Fatal(err)
	}
	e.Config.FlagsFile = path
}

func TestNewContextPolicyWorkerUsesOneSessionAndExplicitIMSends(t *testing.T) {
	s := testStore(t)
	e, f, gw, _ := fixtureEngine(t, s)
	enableContextFixture(t, e)
	f.add("MEMO-42", "")
	cancel, done := startFixtureWorker(t, e, f)
	waitFor(t, func() bool { return count(t, s, `SELECT count(*) FROM runtime_runs WHERE state='completed'`) == 1 })
	f.add("silent in second conversation", uuid.NewString())
	waitFor(t, func() bool { return count(t, s, `SELECT count(*) FROM runtime_runs WHERE state='completed'`) == 2 })
	cancel()
	<-done
	if count(t, s, `SELECT count(*) FROM runtime_runs WHERE context_profile=$1`, contextProfileV1) != 2 || count(t, s, `SELECT count(DISTINCT session_id) FROM runtime_session_events`) != 1 {
		t.Fatal("new policy introduced multiple Sessions or lost admission")
	}
	f.mu.Lock()
	if len(f.sent) != 1 {
		f.mu.Unlock()
		t.Fatal("new policy duplicated an explicit send or published private final text")
	}
	for _, msg := range f.sent {
		if strings.Contains(msg.Body, "PRIVATE_FINAL") {
			f.mu.Unlock()
			t.Fatal("model final text leaked into IM")
		}
	}
	f.mu.Unlock()
	gw.mu.Lock()
	requests := append([][]byte(nil), gw.requests...)
	gw.mu.Unlock()
	if len(requests) == 0 {
		t.Fatal("new worker never reached the model")
	}
	firstMeasurement, err := measureContextRequest(requests[0])
	if err != nil {
		t.Fatal(err)
	}
	defaultBudget, _ := makeContextBudgetSnapshot(16384)
	if firstMeasurement.EstimatedTokens >= defaultBudget.TriggerTokens || !defaultBudget.fits(firstMeasurement) {
		t.Fatal("ordinary first turn has no useful default context capacity", firstMeasurement)
	}
	t.Logf("default 16K first model input: estimated=%d tokens, transport=%d bytes", firstMeasurement.EstimatedTokens, firstMeasurement.TransportBytes)
	var last toolPathRequest
	if err := decodeFixtureRequest(string(requests[len(requests)-1]), &last); err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, item := range last.Tools {
		seen[item.Function.Name] = true
	}
	for _, name := range []string{"im_send_message", "context_read", "context_search", "workspace_read_page"} {
		if !seen[name] {
			t.Fatal("new policy did not advertise required tool", name)
		}
	}
	if !strings.Contains(string(requests[len(requests)-1]), "MEMO-42") || !strings.Contains(string(requests[len(requests)-1]), "second conversation") {
		t.Fatal("new context policy lost continuous cross-conversation memory")
	}
	rows, err := s.DB.Query(context.Background(), `SELECT context_budget FROM runtime_runs`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var raw json.RawMessage
		if err = rows.Scan(&raw); err != nil {
			t.Fatal(err)
		}
		if _, err = ParseContextBudgetSnapshot(raw); err != nil {
			t.Fatal("worker did not persist its full budget", err)
		}
	}
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
}

func TestWillRetryIsObservationAndTerminalErrorsStillFail(t *testing.T) {
	if !modelRetryObservation(&adk.WillRetryError{ErrStr: "context retry", RetryAttempt: 1}) || modelRetryObservation(&ModelError{Code: "context_unrecoverable"}) || modelRetryObservation(nil) {
		t.Fatal("native retry/terminal error classification is incorrect")
	}
}
