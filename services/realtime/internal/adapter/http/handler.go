// Package httpadapter registers health and readiness HTTP endpoints for Realtime.
package httpadapter

import (
	"net/http"

	"teamie/pkg/httpx"
)

// RegisterHealthRoutes registers /healthz and /readyz on the provided ServeMux.
func RegisterHealthRoutes(m *http.ServeMux, ready func(*http.Request) error) {
	httpx.Operations(m, ready)
}
