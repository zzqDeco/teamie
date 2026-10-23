package rpcclient

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cloudwego/kitex/client/callopt"
	"teamie/contracts/gen/im/kitex_gen/im"
)

type identityStub struct{ s *im.SessionResponse }

func (i identityStub) ValidateSession(context.Context, *im.SessionRequest, ...callopt.Option) (*im.SessionResponse, error) {
	return i.s, nil
}

func TestAdminAuthenticationAndCSRF(t *testing.T) {
	csrf := strings.Repeat("a", 64)
	session := &im.SessionResponse{Valid: true, User: &im.User{Id: "admin", Kind: "human", PlatformRole: "admin"}, CsrfToken: &csrf}
	auth := AdminAuth{Identity: identityStub{session}, Origin: "https://example.com"}
	request := func(method string) *http.Request {
		r := httptest.NewRequest(method, "https://example.com/api/v1/admin/model-gateway", nil)
		r.AddCookie(&http.Cookie{Name: "teamie_session", Value: strings.Repeat("b", 64)})
		return r
	}
	if _, e := auth.Require(httptest.NewRequest("GET", "/", nil)); e == nil {
		t.Fatal("anonymous accepted")
	}
	if _, e := auth.Require(request("GET")); e != nil {
		t.Fatal(e)
	}
	for _, method := range []string{"GET", "HEAD"} {
		r := request(method)
		r.Header.Set("X-CSRF-Token", "previous-browser-session")
		if _, e := auth.Require(r); e == nil || !strings.Contains(e.Error(), "Browser session changed") {
			t.Fatalf("changed browser session accepted: %v", e)
		}
		r.Header.Set("X-CSRF-Token", csrf)
		if _, e := auth.Require(r); e != nil {
			t.Fatal(e)
		}
	}
	session.User.PlatformRole = "user"
	if _, e := auth.Require(request("GET")); e == nil {
		t.Fatal("member accepted")
	}
	session.User.PlatformRole = "admin"
	session.User.Kind = "agent"
	if _, e := auth.Require(request("GET")); e == nil {
		t.Fatal("agent principal accepted")
	}
	session.User.Kind = "human"
	r := request("PUT")
	r.Header.Set("Origin", "https://evil.example")
	r.Header.Set("X-CSRF-Token", csrf)
	if _, e := auth.Require(r); e == nil {
		t.Fatal("wrong origin accepted")
	}
	r.Header.Set("Origin", auth.Origin)
	r.Header.Del("X-CSRF-Token")
	if _, e := auth.Require(r); e == nil {
		t.Fatal("missing CSRF accepted")
	}
	r.Header.Set("X-CSRF-Token", csrf)
	if _, e := auth.Require(r); e != nil {
		t.Fatal(e)
	}
}
