// Package rpcclient implements the outbound Identity RPC session validator for
// administrator authentication on the Model Gateway.
package rpcclient

import (
	"context"
	"net/http"
	"time"

	"github.com/cloudwego/kitex/client/callopt"
	"teamie/contracts/gen/im/kitex_gen/im"
	"teamie/contracts/gen/im/kitex_gen/im/identityservice"
	"teamie/pkg/httpx"
	"teamie/pkg/rpcx"
)

// SessionValidator is the subset of IdentityService used by AdminAuth.
type SessionValidator interface {
	ValidateSession(context.Context, *im.SessionRequest, ...callopt.Option) (*im.SessionResponse, error)
}

// AdminAuth authenticates human platform administrators via Identity RPC with CSRF and Origin protection.
type AdminAuth struct {
	Identity SessionValidator
	Origin   string
}

// NewAdminAuth dials a new Identity RPC client at addr and configures Origin protection.
func NewAdminAuth(addr, origin string) (AdminAuth, error) {
	c, err := identityservice.NewClient("teamie.identity", rpcx.ClientOptions(addr, 3*time.Second, time.Second)...)
	return AdminAuth{Identity: c, Origin: origin}, err
}

func (a AdminAuth) Require(r *http.Request) (*im.User, error) {
	cookie, err := r.Cookie(httpx.CookieName)
	if err != nil || len(cookie.Value) != 64 {
		return nil, httpx.Err(401, "unauthorized", "Authentication required")
	}
	s, err := a.Identity.ValidateSession(r.Context(), &im.SessionRequest{Token: cookie.Value})
	if err != nil {
		return nil, httpx.Err(503, "identity_unavailable", "Authentication is temporarily unavailable")
	}
	if s == nil || !s.Valid || s.User == nil {
		return nil, httpx.Err(401, "unauthorized", "Authentication required")
	}
	if r.Method == "GET" || r.Method == "HEAD" {
		if provided := r.Header.Get("X-CSRF-Token"); provided != "" && (s.CsrfToken == nil || !httpx.EqualSecret(provided, *s.CsrfToken)) {
			return nil, httpx.Err(401, "session_changed", "Browser session changed")
		}
	}
	if s.User.Kind != "human" || s.User.PlatformRole != "admin" {
		return nil, httpx.Err(403, "forbidden", "Administrator access required")
	}
	if r.Method != "GET" && r.Method != "HEAD" {
		if r.Header.Get("Origin") != a.Origin {
			return nil, httpx.Err(403, "untrusted_origin", "Untrusted request origin")
		}
		if s.CsrfToken == nil || !httpx.EqualSecret(r.Header.Get("X-CSRF-Token"), *s.CsrfToken) {
			return nil, httpx.Err(403, "invalid_csrf", "Invalid CSRF token")
		}
	}
	return s.User, nil
}
