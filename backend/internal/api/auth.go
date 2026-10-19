package api

import (
	"context"
	"crypto/subtle"
	"go.uber.org/zap"
	"net/http"
	"strings"
	"teamie/backend/pkg/observability"
)

type Identity interface {
	Validate(context.Context, string) (Session, error)
	Users(context.Context, []string) ([]User, error)
	Ping(context.Context) error
}
type AuthedHandler func(http.ResponseWriter, *http.Request, Session) error
type PrincipalIdentity interface {
	ValidatePrincipal(context.Context, string, string) (Session, error)
}

func ValidServiceKey(expected, provided string) bool {
	return expected != "" && provided != "" && subtle.ConstantTimeCompare([]byte(expected), []byte(provided)) == 1
}
func HumanAuth(identity Identity, origin string, next AuthedHandler) http.HandlerFunc {
	return Auth(identity, origin, func(w http.ResponseWriter, r *http.Request, s Session) error {
		if s.User.Kind != "human" {
			return Err(403, "forbidden", "A human session is required")
		}
		return next(w, r, s)
	})
}

func Auth(identity Identity, origin string, next AuthedHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var s Session
		var err error
		if auth := r.Header.Get("Authorization"); auth != "" {
			p, ok := identity.(PrincipalIdentity)
			if !ok || !strings.HasPrefix(auth, "Bearer ") {
				Fail(w, Err(401, "unauthenticated", "Invalid participant credential"))
				return
			}
			// Machine credentials are never accepted from browser contexts.
			if r.Header.Get("Origin") != "" || r.Header.Get("Sec-Fetch-Site") != "" {
				Fail(w, Err(403, "forbidden", "Machine credentials are not browser sessions"))
				return
			}
			s, err = p.ValidatePrincipal(r.Context(), strings.TrimPrefix(auth, "Bearer "), r.Header.Get("X-Teamie-Service-Key"))
		} else {
			s, err = identity.Validate(r.Context(), Token(r))
		}
		if err != nil {
			Fail(w, err)
			return
		}
		observability.AnnotateHTTP(w, zap.String("user.id", s.User.ID), zap.String("custom.principal.kind", s.User.Kind))
		r = r.WithContext(observability.WithFields(r.Context(), zap.String("user.id", s.User.ID)))
		if !Mutating(r) && !s.ServicePrincipal && r.URL.Path != "/api/v1/auth/session" {
			if token := r.Header.Get("X-CSRF-Token"); token != "" && subtle.ConstantTimeCompare([]byte(token), []byte(s.CSRFToken)) != 1 {
				Fail(w, Err(401, "session_changed", "Browser session changed"))
				return
			}
		}
		if Mutating(r) && !s.ServicePrincipal {
			if err = CheckOrigin(r, origin); err != nil {
				Fail(w, err)
				return
			}
			token := r.Header.Get("X-CSRF-Token")
			if token == "" || subtle.ConstantTimeCompare([]byte(token), []byte(s.CSRFToken)) != 1 {
				Fail(w, Err(403, "csrf_failed", "A valid CSRF token is required"))
				return
			}
		}
		if err = next(w, r, s); err != nil {
			Fail(w, err)
		}
	}
}
