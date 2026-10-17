package engine

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	bg "github.com/cloudwego/eino/adk/backgroundtask"
	"github.com/google/uuid"
)

func TestTaskNotificationKeepsParentSourcesWithoutChoosingDestination(t *testing.T) {
	s, lease, parent := nativeModelRun(t)
	ctx := WithRun(context.Background(), parent)
	parent.Inputs = append(parent.Inputs, Input{EventID: "2", WorkspaceID: "other-workspace", ConversationID: "other-conversation", MessageID: "other-message", SenderID: "other-sender"})
	b, _ := json.Marshal(parent.Inputs)
	if _, err := s.DB.Exec(ctx, `UPDATE runtime_runs SET input_json=$2 WHERE id=$1`, parent.ID, b); err != nil {
		t.Fatal(err)
	}
	ts := &TaskStore{Store: s, Lease: lease, AgentID: lease.AgentID}
	task, err := ts.Create(ctx, &bg.CreateTaskRequest{Spec: bg.Spec{ID: uuid.NewString(), ExecutorKey: "fixture", SessionID: "agent:" + lease.AgentID}, LeaseExpiryPolicy: bg.LeaseExpiryRetry})
	if err != nil {
		t.Fatal(err)
	}
	w := &workerTasks{store: ts}
	if err = w.notifications(ctx, lease); err != nil {
		t.Fatal(err)
	}
	var raw []byte
	if err = s.DB.QueryRow(ctx, `SELECT payload FROM runtime_inputs WHERE agent_id=$1 AND notification_id IS NOT NULL`, lease.AgentID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var in Input
	if err = json.Unmarshal(raw, &in); err != nil || in.ParentRunID != parent.ID || in.TaskID != task.Spec.ID || len(in.SourceRefs) != 2 || in.ConversationID != "" || in.MessageID != "" {
		t.Fatal("notification guessed a destination or lost sources", string(raw), err)
	}
	if in.SourceRefs[0].MessageID != parent.Inputs[0].MessageID || in.SourceRefs[1].MessageID != "other-message" {
		t.Fatal("notification origin changed order or selected latest message")
	}
	if err = w.notifications(ctx, lease); err != nil || count(t, s, `SELECT count(*) FROM runtime_inputs WHERE notification_id IS NOT NULL`) != 1 {
		t.Fatal("notification retry duplicated the input", err)
	}
}

func TestTaskSnapshotPreservesNewPolicyAndLegacyBytes(t *testing.T) {
	run := &Run{AgentID: "agent", ID: "run", Attempt: "attempt", Revision: 2}
	snapshotter := taskContexts{agentID: run.AgentID, revision: 2}
	legacy, err := snapshotter.CaptureContext(WithRun(context.Background(), run))
	if err != nil || string(legacy) != `{"AgentID":"agent","RunID":"run","Attempt":"attempt","Revision":2}` {
		t.Fatal("old task snapshot changed", string(legacy), err)
	}
	run.ContextProfile = contextProfileV1
	run.ContextBudget = json.RawMessage(`{"version":1,"contextWindowTokens":16384}`)
	run.Inputs = []Input{{EventID: "1", WorkspaceID: "w", ConversationID: "c", MessageID: "m", Body: "PRIVATE_BODY"}}
	encoded, err := snapshotter.CaptureContext(WithRun(context.Background(), run))
	if err != nil || strings.Contains(string(encoded), "PRIVATE_BODY") {
		t.Fatal("snapshot copied private message body", err)
	}
	ctx, err := snapshotter.RestoreContext(context.Background(), encoded)
	if err != nil {
		t.Fatal(err)
	}
	restored, _ := CurrentRun(ctx)
	refs, overflow := inputSources(restored.Inputs)
	want, _ := inputSources(run.Inputs)
	_, wantBudget, _ := canonicalContextPolicy(run.ContextProfile, run.ContextBudget)
	if contextProfile(restored) != contextProfileV1 || overflow || !reflect.DeepEqual(refs, want) || string(restored.ContextBudget) != string(wantBudget) {
		t.Fatal("restored task lost original policy/source", restored)
	}
}

func TestInputSourcesBoundedAndExplicitlyIncomplete(t *testing.T) {
	inputs := []Input{}
	for i := 0; i < 140; i++ {
		inputs = append(inputs, Input{EventID: uuid.NewString(), ConversationID: "conversation", MessageID: uuid.NewString()})
	}
	refs, overflow := inputSources(inputs)
	if len(refs) != 128 || !overflow {
		t.Fatal("source truncation was silent")
	}
	refs, overflow = inputSources([]Input{{Kind: "task.notification", SourceRefs: refs, SourceOverflow: overflow}})
	if len(refs) != 128 || !overflow {
		t.Fatal("nested task erased incomplete source marker")
	}
}
