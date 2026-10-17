package identity

import (
	"mime"
	"net/http"
	"strconv"

	"teamie/backend/internal/api"
)

func (h *Handler) resetJSON(w http.ResponseWriter, r *http.Request, input any) error {
	w.Header().Set("Cache-Control", "no-store")
	if err := api.CheckOrigin(r, h.Origin); err != nil {
		return err
	}
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		return api.Err(415, "unsupported_media_type", "Use an application/json request")
	}
	return api.Decode(w, r, input)
}

func (h *Handler) passwordResetRoutes(m *http.ServeMux) {
	m.HandleFunc("GET /api/v1/auth/admin/users", api.HumanAuth(h.Store, h.Origin, func(w http.ResponseWriter, r *http.Request, session api.Session) error {
		w.Header().Set("Cache-Control", "no-store")
		limit := 0
		if raw := r.URL.Query().Get("limit"); raw != "" {
			var err error
			limit, err = strconv.Atoi(raw)
			if err != nil || limit < 1 {
				return api.Err(400, "invalid_request", "Use a limit from 1 to 100")
			}
		}
		page, err := h.Store.AdminUsers(r.Context(), session.User.ID, api.Token(r), r.URL.Query().Get("cursor"), limit, r.URL.Query().Get("q"))
		if err != nil {
			return err
		}
		api.Write(w, 200, page)
		return nil
	}))
	m.HandleFunc("POST /api/v1/auth/admin/users/{id}/password-reset-links", api.HumanAuth(h.Store, h.Origin, func(w http.ResponseWriter, r *http.Request, session api.Session) error {
		w.Header().Set("Cache-Control", "no-store")
		if err := h.Store.requireAdmin(r.Context(), session.User.ID); err != nil {
			return err
		}
		if err := h.resetJSON(w, r, &struct{}{}); err != nil {
			return err
		}
		if !h.limit("password-reset-issue:" + session.User.ID) {
			return api.Err(429, "rate_limited", "Try again in a minute")
		}
		reset, token, err := h.Store.CreatePasswordResetLink(r.Context(), session.User.ID, api.Token(r), r.PathValue("id"))
		if err != nil {
			return err
		}
		api.Write(w, 201, map[string]any{"reset": reset, "url": "/reset-password#token=" + token})
		return nil
	}))
	m.HandleFunc("POST /api/v1/auth/password-reset/inspect", func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Token string `json:"token"`
		}
		if err := h.resetJSON(w, r, &input); err != nil {
			api.Fail(w, err)
			return
		}
		if !h.limit("password-reset:" + input.Token) {
			api.Fail(w, api.Err(429, "rate_limited", "Try again in a minute"))
			return
		}
		result, err := h.Store.InspectPasswordReset(r.Context(), input.Token)
		if err != nil {
			api.Fail(w, err)
			return
		}
		api.Write(w, 200, result)
	})
	m.HandleFunc("POST /api/v1/auth/password-reset", func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Token       string `json:"token"`
			NewPassword string `json:"newPassword"`
		}
		if err := h.resetJSON(w, r, &input); err != nil {
			api.Fail(w, err)
			return
		}
		if !h.limit("password-reset:" + input.Token) {
			api.Fail(w, api.Err(429, "rate_limited", "Try again in a minute"))
			return
		}
		if err := h.Store.ResetPassword(r.Context(), input.Token, input.NewPassword); err != nil {
			api.Fail(w, err)
			return
		}
		// Capability redemption never authenticates or changes the browser cookie.
		// A delayed response must preserve a newer login to any other account.
		api.Write(w, 204, nil)
	})
}
