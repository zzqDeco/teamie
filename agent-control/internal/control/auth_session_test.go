package control

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"teamie/backend/kitex_gen/im"
)

func TestAuthenticatedReadsBindOptionalNonceBeforeControlHandler(t *testing.T) {
	f := &authority{users: map[string]*im.User{"session": {Id: "owner", Kind: "human"}}}
	h := &Handler{Store: &Store{IM: f}, Origin: "https://teamie.example"}
	for _, method := range []string{http.MethodGet, http.MethodHead, http.MethodPatch} {
		for _, csrf := range []string{"", "csrf", "previous-account"} {
			t.Run(method+"/"+csrf, func(t *testing.T) {
				called := false
				r := httptest.NewRequest(method, "/api/v1/agents", nil)
				r.AddCookie(&http.Cookie{Name: "teamie_session", Value: "session"})
				r.Header.Set("X-CSRF-Token", csrf)
				w := httptest.NewRecorder()
				h.auth(func(w http.ResponseWriter, _ *http.Request, _ *im.User) error {
					called = true
					w.WriteHeader(204)
					return nil
				})(w, r)
				want := 204
				if method == http.MethodPatch && csrf != "csrf" {
					want = 403
				} else if method != http.MethodPatch && csrf == "previous-account" {
					want = 401
				}
				if w.Code != want || called != (want == 204) {
					t.Fatalf("status=%d called=%t body=%s want=%d", w.Code, called, w.Body.String(), want)
				}
			})
		}
	}
}
