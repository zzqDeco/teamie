// Package worker implements the inbound background polling and schedule wakeup
// worker adapter for the Agent Runtime service.
package worker

import (
	"context"
	"time"

	"teamie/services/agent-runtime/internal/app"
	"teamie/services/agent-runtime/internal/domain"
)

type workerHandle struct {
	revision int64
	cancel   context.CancelFunc
	done     chan struct{}
}

// Worker drives the Agent Runtime background polling, lease acquisition,
// revision preemption, and per-Agent TurnLoop worker lifecycle.
type Worker struct {
	App *app.Service
}

// New creates a new background runtime Worker bound to the application service.
func New(appSvc *app.Service) *Worker {
	return &Worker{App: appSvc}
}

// Run starts the background runtime coordinator loop until ctx is canceled.
func (w *Worker) Run(ctx context.Context) {
	if w == nil || w.App == nil {
		return
	}
	workers := map[string]*workerHandle{}
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	defer func() {
		for _, handle := range workers {
			handle.cancel()
		}
		for _, handle := range workers {
			<-handle.done
		}
	}()
	refresh := func() {
		profiles, err := w.App.ListAgentProfiles(ctx)
		if err != nil {
			return
		}
		defs := make(map[string]domain.AgentProfile, len(profiles))
		for _, p := range profiles {
			defs[p.ID] = p
		}
		for id, handle := range workers {
			d, ok := defs[id]
			select {
			case <-handle.done:
				delete(workers, id)
				continue
			default:
			}
			if !ok || w.App.CheckRevisionPreemption(d.Enabled, d.Deleted, d.Revision, handle.revision) {
				handle.cancel()
			}
		}
		for _, d := range profiles {
			if !d.Enabled || d.Deleted {
				pc, cancel := context.WithTimeout(ctx, 3*time.Second)
				w.App.CancelAgentTasks(pc, d.ID)
				cancel()
				continue
			}
			if _, exists := workers[d.ID]; exists {
				continue
			}
			lease, err := w.App.AcquireWorkerLease(ctx, d.ID)
			if err != nil || lease == nil {
				continue
			}
			wc, cancel := context.WithCancel(ctx)
			handle := &workerHandle{revision: d.Revision, cancel: cancel, done: make(chan struct{})}
			workers[d.ID] = handle
			go func(profile domain.AgentProfile, l *domain.Lease, h *workerHandle) {
				defer close(h.done)
				defer func() {
					releaseCtx, done := context.WithTimeout(context.Background(), 5*time.Second)
					defer done()
					w.App.ReleaseWorkerLease(releaseCtx, l)
				}()
				w.App.WorkAgent(wc, profile, l)
			}(d, lease, handle)
		}
	}
	refresh()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			refresh()
		}
	}
}
