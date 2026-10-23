// Package http implements the operational HTTP endpoints (/healthz, /readyz, /version)
// for the Agent Runtime service.
package http

import (
	"context"
	"net/http"
	"time"

	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/cloudwego/hertz/pkg/common/adaptor"
	"teamie/pkg/httpx"
	"teamie/pkg/observability"
	"teamie/services/agent-runtime/internal/app"
)

// Handler registers operational HTTP routes for the Agent Runtime service.
type Handler struct {
	App     *app.Service
	Version string
	SHA     string
}

// RegisterRoutes wires /healthz, /readyz, and /version onto the Hertz server.
func (h *Handler) RegisterRoutes(srv *server.Hertz) {
	if h.Version != "" {
		httpx.BuildVersion = h.Version
	}
	if h.SHA != "" {
		httpx.BuildSHA = h.SHA
	}
	mux := http.NewServeMux()
	httpx.OperationsContext(mux, func(ctx context.Context) error {
		pc, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		return h.App.Ready(pc)
	})
	srv.Any("/*path", adaptor.HertzHandler(observability.HTTP(mux)))
}
