package identity

import (
	"net/http"
	"strings"
	"sync"
	"teamie/backend/internal/api"
	"time"
)

type rateEntry struct {
	Start time.Time
	Count int
}
type Handler struct {
	Store  *Store
	Origin string
	Secure bool
	mu     sync.Mutex
	rates  map[string]rateEntry
}

func (h *Handler) limit(email string) bool {
	// The edge is a shared peer: account buckets avoid throttling every user as
	// one IP. A bounded global bucket limits account-rotation attempts without
	// trusting client-supplied forwarding headers.
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.rates == nil {
		h.rates = map[string]rateEntry{}
	}
	now := time.Now()
	for k, v := range h.rates {
		if now.Sub(v.Start) > time.Minute {
			delete(h.rates, k)
		}
	}
	if len(h.rates) > 10000 {
		return false
	}
	allowed := true
	for key, max := range map[string]int{"*": 600, api.Hash(strings.ToLower(strings.TrimSpace(email))): 20} {
		v := h.rates[key]
		if now.Sub(v.Start) > time.Minute {
			v = rateEntry{Start: now}
		}
		v.Count++
		h.rates[key] = v
		allowed = allowed && v.Count <= max
	}
	return allowed
}
func (h *Handler) cookie(w http.ResponseWriter, token string, maxAge int) {
	http.SetCookie(w, &http.Cookie{Name: api.CookieName, Value: token, Path: "/", HttpOnly: true, Secure: h.Secure, SameSite: http.SameSiteLaxMode, MaxAge: maxAge})
}
func (h *Handler) Routes() http.Handler {
	m := http.NewServeMux()
	api.Operations(m, func(r *http.Request) error { return h.Store.Ping(r.Context()) })
	m.HandleFunc("GET /api/v1/auth/registration", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		required, e := h.Store.BootstrapRequired(r.Context())
		if e != nil {
			api.Fail(w, e)
			return
		}
		api.Write(w, 200, map[string]any{"mode": "invite", "bootstrapRequired": required})
	})
	login := func(register bool) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if e := api.CheckOrigin(r, h.Origin); e != nil {
				api.Fail(w, e)
				return
			}
			var in struct {
				Email       string `json:"email"`
				Password    string `json:"password"`
				DisplayName string `json:"displayName"`
				InviteCode  string `json:"inviteCode"`
			}
			if e := api.Decode(w, r, &in); e != nil {
				api.Fail(w, e)
				return
			}
			if !h.limit(in.Email) {
				api.Fail(w, api.Err(429, "rate_limited", "Try again in a minute"))
				return
			}
			var ss api.Session
			var token string
			var e error
			status := 200
			if register {
				ss, token, e = h.Store.Register(r.Context(), in.Email, in.Password, in.DisplayName, in.InviteCode)
				status = 201
			} else {
				ss, token, e = h.Store.Login(r.Context(), in.Email, in.Password)
			}
			if e != nil {
				api.Fail(w, e)
				return
			}
			h.cookie(w, token, 7*86400)
			w.Header().Set("Cache-Control", "no-store")
			api.Write(w, status, ss)
		}
	}
	m.HandleFunc("POST /api/v1/auth/register", login(true))
	m.HandleFunc("POST /api/v1/auth/login", login(false))
	m.HandleFunc("GET /api/v1/auth/session", api.HumanAuth(h.Store, h.Origin, func(w http.ResponseWriter, r *http.Request, s api.Session) error {
		w.Header().Set("Cache-Control", "no-store")
		api.Write(w, 200, s)
		return nil
	}))
	m.HandleFunc("POST /api/v1/auth/logout", api.HumanAuth(h.Store, h.Origin, func(w http.ResponseWriter, r *http.Request, s api.Session) error {
		if e := h.Store.Logout(r.Context(), api.Token(r)); e != nil {
			return e
		}
		h.cookie(w, "", -1)
		api.Write(w, 204, nil)
		return nil
	}))
	m.HandleFunc("POST /api/v1/auth/password", api.HumanAuth(h.Store, h.Origin, func(w http.ResponseWriter, r *http.Request, s api.Session) error {
		w.Header().Set("Cache-Control", "no-store")
		if s.ServicePrincipal {
			return api.Err(403, "forbidden", "A human login session is required")
		}
		if !h.limit(s.User.Email) {
			return api.Err(429, "rate_limited", "Try again in a minute")
		}
		var in struct {
			CurrentPassword string `json:"currentPassword"`
			NewPassword     string `json:"newPassword"`
		}
		if err := api.Decode(w, r, &in); err != nil {
			return err
		}
		if err := h.Store.ChangePassword(r.Context(), s.User.ID, api.Token(r), in.CurrentPassword, in.NewPassword); err != nil {
			return err
		}
		h.cookie(w, "", -1)
		api.Write(w, 204, nil)
		return nil
	}))
	m.HandleFunc("PATCH /api/v1/auth/profile", api.HumanAuth(h.Store, h.Origin, func(w http.ResponseWriter, r *http.Request, s api.Session) error {
		var in struct {
			DisplayName string `json:"displayName"`
		}
		if e := api.Decode(w, r, &in); e != nil {
			return e
		}
		u, e := h.Store.Profile(r.Context(), s.User.ID, in.DisplayName)
		if e != nil {
			return e
		}
		api.Write(w, 200, map[string]any{"user": u})
		return nil
	}))
	m.HandleFunc("GET /api/v1/auth/admin/invites", api.HumanAuth(h.Store, h.Origin, func(w http.ResponseWriter, r *http.Request, s api.Session) error {
		w.Header().Set("Cache-Control", "no-store")
		invites, e := h.Store.RegistrationInvites(r.Context(), s.User.ID)
		if e != nil {
			return e
		}
		api.Write(w, 200, map[string]any{"invites": invites})
		return nil
	}))
	m.HandleFunc("POST /api/v1/auth/admin/invites", api.HumanAuth(h.Store, h.Origin, func(w http.ResponseWriter, r *http.Request, s api.Session) error {
		w.Header().Set("Cache-Control", "no-store")
		// Check permission before parsing a management request.
		if e := h.Store.requireAdmin(r.Context(), s.User.ID); e != nil {
			return e
		}
		if e := api.Decode(w, r, &struct{}{}); e != nil {
			return e
		}
		invite, code, e := h.Store.CreateRegistrationInvite(r.Context(), s.User.ID)
		if e != nil {
			return e
		}
		api.Write(w, 201, map[string]any{"invite": struct {
			RegistrationInvite
			Code string `json:"code"`
		}{invite, code}, "url": "/login?mode=register&inviteCode=" + code})
		return nil
	}))
	m.HandleFunc("DELETE /api/v1/auth/admin/invites/{id}", api.HumanAuth(h.Store, h.Origin, func(w http.ResponseWriter, r *http.Request, s api.Session) error {
		w.Header().Set("Cache-Control", "no-store")
		if e := h.Store.RevokeRegistrationInvite(r.Context(), s.User.ID, r.PathValue("id")); e != nil {
			return e
		}
		api.Write(w, 204, nil)
		return nil
	}))
	return m
}
