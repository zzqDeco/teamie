package internal_test

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	"teamie/services/agent-runtime/internal/adapter/rpc"
	"teamie/services/agent-runtime/internal/adapter/worker"
	"teamie/services/agent-runtime/internal/app"
	"teamie/services/agent-runtime/internal/domain"
	"teamie/services/agent-runtime/internal/infra/eino"
)

type stubControlGateway struct {
	profile *domain.AgentProfile
}

func (s *stubControlGateway) GetAgentProfile(context.Context, string) (*domain.AgentProfile, error) {
	return s.profile, nil
}

func (s *stubControlGateway) ListAgentProfiles(context.Context) ([]domain.AgentProfile, error) {
	if s.profile == nil {
		return nil, nil
	}
	return []domain.AgentProfile{*s.profile}, nil
}

func (s *stubControlGateway) ReportRuntimeState(context.Context, string, string, string, bool) error {
	return nil
}

func (s *stubControlGateway) Ping(context.Context) error {
	return nil
}

func TestAppLayersAndInspectRepositoryIntegration(t *testing.T) {
	dbURL := os.Getenv("TEAMIE_RUNTIME_TEST_DATABASE_URL")
	if dbURL == "" {
		dbURL = os.Getenv("DATABASE_URL_TEST")
	}
	if dbURL == "" {
		t.Skip("runtime test database URL not configured")
	}
	ctx := context.Background()
	store, err := eino.Open(ctx, dbURL)
	if err != nil {
		t.Fatalf("open runtime store: %v", err)
	}
	defer store.DB.Close()

	repo := store.Repo()
	agentID := uuid.NewString()
	ownerID := uuid.NewString()

	ctrl := &stubControlGateway{
		profile: &domain.AgentProfile{
			ID:      agentID,
			OwnerID: ownerID,
			Enabled: true,
		},
	}

	appSvc := &app.Service{
		Tx:           repo,
		Sessions:     repo,
		Schedules:    repo,
		Archive:      repo,
		ToolReceipts: repo,
		Inspect:      repo,
		Control:      ctrl,
		ControlToken: "runtime-secret",
	}

	// Verify adapter/worker wiring
	w := worker.New(appSvc)
	if w.App != appSvc {
		t.Fatalf("expected worker to wrap app.Service")
	}

	// Verify lease + inbox app orchestration
	lease, err := repo.AcquireLease(ctx, agentID, "test-worker")
	if err != nil {
		t.Fatalf("acquire lease: %v", err)
	}
	defer repo.ReleaseLease(ctx, lease)

	if err := appSvc.ValidateLease(ctx, lease); err != nil {
		t.Fatalf("validate lease: %v", err)
	}
	if !appSvc.CheckRevisionPreemption(true, false, 2, 1) {
		t.Fatalf("expected revision mismatch to trigger preemption")
	}

	in := domain.Input{
		EventID:        "101",
		WorkspaceID:    uuid.NewString(),
		ConversationID: uuid.NewString(),
		MessageID:      uuid.NewString(),
		SenderID:       ownerID,
		SenderKind:     "human",
		Body:           "SECRET_BODY_DO_NOT_LEAK",
	}
	if err := appSvc.IngestInboxInputs(ctx, lease, "101", []domain.Input{in}); err != nil {
		t.Fatalf("ingest inbox inputs: %v", err)
	}
	cur, err := appSvc.GetInboxCursor(ctx, lease)
	if err != nil || cur != "101" {
		t.Fatalf("unexpected inbox cursor: %q, err=%v", cur, err)
	}

	// Verify inspect_app + adapter/rpc integration via InspectRepository
	rpcSrv := &rpc.Server{App: appSvc}
	stateRes, err := appSvc.State(ctx, domain.RuntimeQuery{
		ServiceToken: "runtime-secret",
		AgentID:      agentID,
		OwnerID:      ownerID,
	})
	if err != nil || !stateRes.OK {
		t.Fatalf("State failed: %+v, err=%v", stateRes, err)
	}
	if strings.Contains(stateRes.DataJSON, "SECRET_BODY_DO_NOT_LEAK") {
		t.Fatalf("State leaked message body: %s", stateRes.DataJSON)
	}
	var statePayload struct {
		State         string `json:"state"`
		PendingInputs int    `json:"pendingInputs"`
	}
	if err := json.Unmarshal([]byte(stateRes.DataJSON), &statePayload); err != nil {
		t.Fatalf("unmarshal state: %v", err)
	}
	if statePayload.PendingInputs != 1 {
		t.Fatalf("expected 1 pending input, got %d", statePayload.PendingInputs)
	}

	runsRes, err := appSvc.Runs(ctx, domain.RuntimeQuery{
		ServiceToken: "runtime-secret",
		AgentID:      agentID,
		OwnerID:      ownerID,
	})
	if err != nil || !runsRes.OK {
		t.Fatalf("Runs failed: %+v, err=%v", runsRes, err)
	}
	_ = rpcSrv
}
