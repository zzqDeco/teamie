package chat

import (
	"net/http"

	"teamie/backend/internal/api"
	httpadapter "teamie/backend/internal/chat/adapter/http"
)

// Handler delegates HTTP routing and DTO binding to the Chat HTTP inbound adapter.
type Handler struct {
	Store    *Store
	Identity api.Identity
	Origin   string
}

func (h *Handler) Routes() http.Handler {
	adapter := &httpadapter.Adapter{
		App:       h.Store.App(h.Identity, ""),
		Auth:      h.Identity,
		Origin:    h.Origin,
		Transfers: attachmentTransfers,
	}
	return adapter.Routes()
}
