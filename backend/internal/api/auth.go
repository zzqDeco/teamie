package api

import (
	"context"
	"crypto/subtle"
	"net/http"
)

type Identity interface {
	Validate(context.Context, string) (Session, error)
	Users(context.Context, []string) ([]User, error)
	Ping(context.Context) error
}
type AuthedHandler func(http.ResponseWriter, *http.Request, Session) error

func Auth(identity Identity, origin string, next AuthedHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s, err := identity.Validate(r.Context(), Token(r))
		if err != nil {
			Fail(w, err)
			return
		}
		if Mutating(r) {
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
