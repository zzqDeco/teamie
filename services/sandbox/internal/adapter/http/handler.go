// Package http implements the inbound HTTP operational routes for the Sandbox bounded context.
package http

import (
	"net/http"

	"teamie/pkg/httpx"
	"teamie/services/sandbox/internal/app"
)

// Adapter exposes operational HTTP routes (/healthz, /readyz) for the Sandbox service.
type Adapter struct {
	App *app.Service
}

// New constructs a new HTTP Adapter for the Sandbox service.
func New(appSvc *app.Service) *Adapter {
	return &Adapter{App: appSvc}
}

// Routes returns an http.Handler serving /healthz and /readyz.
func (a *Adapter) Routes() http.Handler {
	m := http.NewServeMux()
	httpx.OperationsContext(m, a.App.Ready)
	return m
}
