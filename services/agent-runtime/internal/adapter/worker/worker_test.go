package worker_test

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"teamie/services/agent-runtime/internal/adapter/worker"
	"teamie/services/agent-runtime/internal/app"
	"teamie/services/agent-runtime/internal/domain"
)

type stubControl struct {
	mu       sync.Mutex
	profiles []domain.AgentProfile
}

func (c *stubControl) GetAgentProfile(context.Context, string) (*domain.AgentProfile, error) {
	return nil, nil
}

func (c *stubControl) ListAgentProfiles(context.Context) ([]domain.AgentProfile, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]domain.AgentProfile, len(c.profiles))
	copy(out, c.profiles)
	return out, nil
}

func (c *stubControl) ReportRuntimeState(context.Context, string, string, string, bool) error {
	return nil
}

func (c *stubControl) Ping(context.Context) error { return nil }

type stubSessionRepo struct {
	domain.SessionRepository
	acquired atomic.Int32
	released atomic.Int32
}

func (r *stubSessionRepo) AcquireLease(_ context.Context, agentID, owner string) (*domain.Lease, error) {
	r.acquired.Add(1)
	return &domain.Lease{AgentID: agentID, Owner: owner, Fence: 1}, nil
}

func (r *stubSessionRepo) ReleaseLease(_ context.Context, _ *domain.Lease) {
	r.released.Add(1)
}

type stubEngine struct {
	domain.RuntimeEngine
	working      atomic.Int32
	canceledTask atomic.Int32
}

func (e *stubEngine) OwnerID() string { return "worker-owner-1" }

func (e *stubEngine) CancelAgentTasks(context.Context, string) {
	e.canceledTask.Add(1)
}

func (e *stubEngine) WorkAgent(ctx context.Context, _ domain.AgentProfile, _ *domain.Lease) {
	e.working.Add(1)
	<-ctx.Done()
}

func TestWorkerCoordinatorAcquiresAndReleasesLease(t *testing.T) {
	ctrl := &stubControl{
		profiles: []domain.AgentProfile{
			{ID: "11111111-1111-1111-1111-111111111111", Enabled: true, Revision: 1},
			{ID: "22222222-2222-2222-2222-222222222222", Enabled: false, Revision: 1},
		},
	}
	sessions := &stubSessionRepo{}
	eng := &stubEngine{}
	svc := &app.Service{
		Control:  ctrl,
		Sessions: sessions,
		Engine:   eng,
	}
	w := worker.New(svc)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		w.Run(ctx)
	}()

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if eng.working.Load() == 1 && eng.canceledTask.Load() >= 1 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if eng.working.Load() != 1 {
		t.Fatalf("expected 1 active worker, got %d", eng.working.Load())
	}
	if eng.canceledTask.Load() < 1 {
		t.Fatalf("expected disabled agent tasks to be canceled")
	}

	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("worker coordinator did not shut down cleanly")
	}
	if sessions.released.Load() != 1 {
		t.Fatalf("expected lease release on shutdown, got %d", sessions.released.Load())
	}
}
