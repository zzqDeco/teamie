// Package api keeps service transport and authentication separate from execution.
package api

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/cloudwego/hertz/pkg/common/adaptor"
	"github.com/cloudwego/kitex/client"
	"github.com/cloudwego/kitex/client/callopt"
	"teamie/backend/kitex_gen/im"
	"teamie/backend/kitex_gen/im/identityservice"
)

var BuildVersion = "unreleased"
var BuildSHA = "unknown"

type Error struct {
	Status        int
	Code, Message string
}

func (e *Error) Error() string                   { return e.Code }
func Err(status int, code, message string) error { return &Error{status, code, message} }
func Write(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func Fail(w http.ResponseWriter, err error) {
	var ae *Error
	if !errors.As(err, &ae) {
		ae = &Error{500, "internal_error", "The request could not be completed"}
	}
	Write(w, ae.Status, map[string]any{"error": map[string]string{"code": ae.Code, "message": ae.Message}})
}
func Decode(w http.ResponseWriter, r *http.Request, v any) error {
	r.Body = http.MaxBytesReader(w, r.Body, 262144)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if d.Decode(v) != nil || d.Decode(new(any)) != io.EOF {
		return Err(400, "invalid_request", "Expected one valid JSON object")
	}
	return nil
}
func Env(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}
func EqualSecret(a, b string) bool {
	x, y := sha256.Sum256([]byte(a)), sha256.Sum256([]byte(b))
	return len(a) >= 24 && len(b) >= 24 && subtle.ConstantTimeCompare(x[:], y[:]) == 1
}
func Operations(m *http.ServeMux, ready func(context.Context) error) {
	m.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { Write(w, 200, map[string]string{"status": "ok"}) })
	m.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if ready(ctx) != nil {
			Write(w, 503, map[string]string{"status": "unavailable"})
			return
		}
		Write(w, 200, map[string]string{"status": "ready"})
	})
	m.HandleFunc("GET /version", func(w http.ResponseWriter, r *http.Request) {
		Write(w, 200, map[string]string{"version": BuildVersion, "sha": BuildSHA})
	})
}
func Serve(addr string, handler http.Handler) {
	h := server.Default(server.WithHostPorts(addr), server.WithMaxRequestBodySize(262144), server.WithReadTimeout(15*time.Second), server.WithWriteTimeout(65*time.Second))
	h.Any("/*path", adaptor.HertzHandler(handler))
	h.Spin()
}

type SessionValidator interface {
	ValidateSession(context.Context, *im.SessionRequest, ...callopt.Option) (*im.SessionResponse, error)
}
type AdminAuth struct {
	Identity SessionValidator
	Origin   string
}

func NewAdminAuth(addr, origin string) (AdminAuth, error) {
	c, err := identityservice.NewClient("teamie.identity", client.WithHostPorts(addr), client.WithRPCTimeout(3*time.Second), client.WithConnectTimeout(time.Second))
	return AdminAuth{Identity: c, Origin: origin}, err
}
func (a AdminAuth) Require(r *http.Request) (*im.User, error) {
	cookie, err := r.Cookie("teamie_session")
	if err != nil || len(cookie.Value) != 64 {
		return nil, Err(401, "unauthorized", "Authentication required")
	}
	s, err := a.Identity.ValidateSession(r.Context(), &im.SessionRequest{Token: cookie.Value})
	if err != nil {
		return nil, Err(503, "identity_unavailable", "Authentication is temporarily unavailable")
	}
	if s == nil || !s.Valid || s.User == nil {
		return nil, Err(401, "unauthorized", "Authentication required")
	}
	if s.User.Kind != "human" || s.User.PlatformRole != "admin" {
		return nil, Err(403, "forbidden", "Administrator access required")
	}
	if r.Method != "GET" && r.Method != "HEAD" {
		if r.Header.Get("Origin") != a.Origin {
			return nil, Err(403, "untrusted_origin", "Untrusted request origin")
		}
		if s.CsrfToken == nil || !EqualSecret(r.Header.Get("X-CSRF-Token"), *s.CsrfToken) {
			return nil, Err(403, "invalid_csrf", "Invalid CSRF token")
		}
	}
	return s.User, nil
}
