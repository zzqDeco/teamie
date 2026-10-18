package control

import (
	"crypto/subtle"
	"encoding/json"
	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/cloudwego/hertz/pkg/common/adaptor"
	"go.uber.org/zap"
	"io"
	"net/http"
	"teamie/backend/kitex_gen/im"
	"teamie/backend/pkg/observability"
)

type Handler struct {
	Store  *Store
	Origin string
}
type handler func(http.ResponseWriter, *http.Request, *im.User) error

func (h *Handler) auth(next handler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			fail(w, errAPI(403, "forbidden", "Human browser session required"))
			return
		}
		cookie, e := r.Cookie("teamie_session")
		if e != nil {
			fail(w, errAPI(401, "unauthenticated", "Sign in to continue"))
			return
		}
		user, csrf, e := h.Store.IM.Validate(r.Context(), cookie.Value)
		if e != nil {
			fail(w, e)
			return
		}
		if user == nil || user.Kind != "human" {
			fail(w, errAPI(403, "forbidden", "A human browser session is required"))
			return
		}
		observability.AnnotateHTTP(w, zap.String("user.id", user.Id))
		r = r.WithContext(observability.WithFields(r.Context(), zap.String("user.id", user.Id)))
		if r.Method == "GET" || r.Method == "HEAD" {
			if provided := r.Header.Get("X-CSRF-Token"); provided != "" && subtle.ConstantTimeCompare([]byte(csrf), []byte(provided)) != 1 {
				fail(w, errAPI(401, "session_changed", "Browser session changed"))
				return
			}
		}
		if r.Method != "GET" && r.Method != "HEAD" {
			origin := r.Header.Get("Origin")
			if origin != "" && origin != h.Origin || origin == "" && r.Header.Get("Sec-Fetch-Site") == "cross-site" {
				fail(w, errAPI(403, "untrusted_origin", "Untrusted origin"))
				return
			}
			provided := r.Header.Get("X-CSRF-Token")
			if provided == "" || subtle.ConstantTimeCompare([]byte(csrf), []byte(provided)) != 1 {
				fail(w, errAPI(403, "csrf_failed", "A valid CSRF token is required"))
				return
			}
		}
		if e = next(w, r, user); e != nil {
			fail(w, e)
		}
	}
}
func decode(w http.ResponseWriter, r *http.Request, v any) error {
	r.Body = http.MaxBytesReader(w, r.Body, 65536)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if e := d.Decode(v); e != nil {
		return errAPI(400, "invalid_request", "Invalid Agent request")
	}
	if e := d.Decode(new(any)); e != io.EOF {
		return errAPI(400, "invalid_request", "Expected one JSON object")
	}
	return nil
}
func (h *Handler) one(w http.ResponseWriter, r *http.Request, a Agent, status int) error {
	out, e := h.Store.decorate(r.Context(), []Agent{a})
	if e != nil {
		return e
	}
	write(w, status, map[string]any{"agent": publicProfile(out[0])})
	return nil
}
func (h *Handler) Routes() http.Handler {
	m := http.NewServeMux()
	m.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		fail(w, errAPI(404, "not_found", "Resource is unavailable"))
	})
	m.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { write(w, 200, map[string]string{"status": "ok"}) })
	m.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		if h.Store.DB.Ping(r.Context()) != nil || h.Store.IM.Ping(r.Context()) != nil {
			write(w, 503, map[string]string{"status": "unavailable"})
			return
		}
		write(w, 200, map[string]string{"status": "ready"})
	})
	m.HandleFunc("GET /version", func(w http.ResponseWriter, r *http.Request) {
		write(w, 200, map[string]string{"version": Version, "sha": SHA})
	})
	m.HandleFunc("GET /api/v1/agents", h.auth(func(w http.ResponseWriter, r *http.Request, u *im.User) error {
		a, e := h.Store.ListOwned(r.Context(), u.Id)
		if e != nil {
			return e
		}
		a, e = h.Store.decorate(r.Context(), a)
		if e != nil {
			return e
		}
		write(w, 200, map[string]any{"agents": publicProfiles(a, "")})
		return nil
	}))
	m.HandleFunc("POST /api/v1/agents", h.auth(func(w http.ResponseWriter, r *http.Request, u *im.User) error {
		var in Changes
		if e := decode(w, r, &in); e != nil {
			return e
		}
		a, e := h.Store.Create(r.Context(), u.Id, in)
		if e != nil {
			return e
		}
		return h.one(w, r, a, 201)
	}))
	m.HandleFunc("GET /api/v1/agents/{id}", h.auth(func(w http.ResponseWriter, r *http.Request, u *im.User) error {
		a, e := h.Store.Owned(r.Context(), u.Id, r.PathValue("id"))
		if e != nil {
			return e
		}
		return h.one(w, r, a, 200)
	}))
	m.HandleFunc("PATCH /api/v1/agents/{id}", h.auth(func(w http.ResponseWriter, r *http.Request, u *im.User) error {
		var in Changes
		if e := decode(w, r, &in); e != nil {
			return e
		}
		a, e := h.Store.Update(r.Context(), u.Id, r.PathValue("id"), in)
		if e != nil {
			return e
		}
		return h.one(w, r, a, 200)
	}))
	m.HandleFunc("DELETE /api/v1/agents/{id}", h.auth(func(w http.ResponseWriter, r *http.Request, u *im.User) error {
		if e := h.Store.Delete(r.Context(), u.Id, r.PathValue("id")); e != nil {
			return e
		}
		write(w, 204, nil)
		return nil
	}))
	m.HandleFunc("GET /api/v1/workspaces/{id}/agents", h.auth(func(w http.ResponseWriter, r *http.Request, u *im.User) error {
		a, e := h.Store.Discover(r.Context(), r.PathValue("id"), u.Id)
		if e != nil {
			return e
		}
		a, e = h.Store.decorate(r.Context(), a)
		if e != nil {
			return e
		}
		write(w, 200, map[string]any{"agents": publicProfiles(a, r.PathValue("id"))})
		return nil
	}))
	return m
}
func Serve(addr string, h http.Handler) {
	s := server.Default(server.WithHostPorts(addr), server.WithMaxRequestBodySize(65536))
	s.Any("/*path", adaptor.HertzHandler(observability.HTTP(h)))
	s.Spin()
}
