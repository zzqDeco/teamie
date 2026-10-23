// Package worker implements the inbound background polling and schedule wakeup
// worker adapter for the Agent Runtime service.
package worker

import (
	"context"

	"teamie/services/agent-runtime/internal/app"
)

// Worker drives the Agent Runtime background polling and schedule wakeup loop.
type Worker struct {
	App *app.Service
}

// New creates a new background runtime Worker bound to the application service.
func New(appSvc *app.Service) *Worker {
	return &Worker{App: appSvc}
}

// Run starts the background runtime loop until ctx is canceled.
func (w *Worker) Run(ctx context.Context) {
	if w != nil && w.App != nil {
		w.App.Run(ctx)
	}
}
