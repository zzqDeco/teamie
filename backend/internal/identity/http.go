package identity

import (
	"net/http"
	"sync"

	httpadapter "teamie/backend/internal/identity/adapter/http"
)

// Handler delegates HTTP routing and DTO binding to the Identity HTTP inbound adapter.
type Handler struct {
	Store   *Store
	Origin  string
	Secure  bool
	once    sync.Once
	adapter *httpadapter.Adapter
}

func (h *Handler) httpAdapter() *httpadapter.Adapter {
	h.once.Do(func() {
		h.adapter = &httpadapter.Adapter{
			App:    h.Store.App(),
			Auth:   h.Store,
			Origin: h.Origin,
			Secure: h.Secure,
		}
	})
	// Refresh App in case Store configuration fields (such as tokens or flags) were updated after initialization.
	h.adapter.App = h.Store.App()
	h.adapter.Auth = h.Store
	h.adapter.Origin = h.Origin
	h.adapter.Secure = h.Secure
	return h.adapter
}

func (h *Handler) Routes() http.Handler {
	return h.httpAdapter().Routes()
}
