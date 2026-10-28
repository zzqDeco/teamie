package identity

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"teamie/pkg/httpx"
	"teamie/pkg/postgres/testdb"
	"testing"
	"time"
)

func TestInvitationSessionCSRFAndRevocation(t *testing.T) {
	ctx := context.Background()
	s, e := Open(ctx, testdb.URL(t), httpx.Flags{}, "test-bootstrap-code")
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(s.DB.Close)
	if _, _, e = s.Register(ctx, "alice@example.invalid", "very-long-password", "Alice", "wrong"); e == nil {
		t.Fatal("registration bypassed invitation")
	}
	if _, _, e = s.Register(ctx, "alice@example.invalid", "short", "Alice", "test-bootstrap-code"); e == nil {
		t.Fatal("short password accepted")
	}
	ss, token, e := s.Register(ctx, "ALICE@example.invalid", "very-long-password", "Alice", "test-bootstrap-code")
	if e != nil {
		t.Fatal(e)
	}
	if ss.User.Email != "alice@example.invalid" {
		t.Fatal("email not normalized")
	}
	if ss.User.CreatedAt.Location() != time.UTC {
		t.Fatal("wire timestamps must be UTC")
	}
	if _, _, e = s.Register(ctx, "alice@example.invalid", "very-long-password", "Another", "test-bootstrap-code"); e == nil {
		t.Fatal("case-insensitive duplicate email accepted")
	}
	if _, _, e = s.Login(ctx, "alice@example.invalid", "wrong-password"); e == nil {
		t.Fatal("wrong password accepted")
	}
	logged, newToken, e := s.Login(ctx, "ALICE@example.invalid", "very-long-password")
	if e != nil || logged.User.ID != ss.User.ID || newToken == token {
		t.Fatal("login must establish a fresh opaque session", e)
	}
	h := (&Handler{Store: s, Origin: "https://teamie.example", Secure: true}).Routes()
	call := func(method, path, body, origin, csrf string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Origin", origin)
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("X-CSRF-Token", csrf)
		r.AddCookie(&http.Cookie{Name: httpx.CookieName, Value: token})
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	if w := call("POST", "/api/v1/auth/logout", "", "https://teamie.example", ""); w.Code != 403 {
		t.Fatalf("logout without CSRF: %d", w.Code)
	}
	if w := call("PATCH", "/api/v1/auth/profile", `{"displayName":"Mallory"}`, "https://evil.example", ss.CSRFToken); w.Code != 403 {
		t.Fatalf("foreign origin accepted: %d", w.Code)
	}
	if w := call("PATCH", "/api/v1/auth/profile", `{"displayName":"Alice Updated"}`, "https://teamie.example", ss.CSRFToken); w.Code != 200 {
		t.Fatalf("profile: %d %s", w.Code, w.Body.String())
	}
	current, e := s.Validate(ctx, token)
	if e != nil || current.User.DisplayName != "Alice Updated" {
		t.Fatal("profile was not re-read", e)
	}
	if w := call("POST", "/api/v1/auth/logout", "", "https://teamie.example", ss.CSRFToken); w.Code != 204 {
		t.Fatalf("logout: %d", w.Code)
	}
	if _, e = s.Validate(ctx, token); e == nil {
		t.Fatal("revoked session still valid")
	}
	if _, e = s.Validate(ctx, newToken); e != nil {
		t.Fatal("logout revoked unrelated session", e)
	}
	r := httptest.NewRequest("POST", "/api/v1/auth/login", strings.NewReader(`{"email":"alice@example.invalid","password":"very-long-password"}`))
	r.Header.Set("Origin", "https://teamie.example")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	cookies := w.Result().Cookies()
	if len(cookies) != 1 || !cookies[0].HttpOnly || !cookies[0].Secure || cookies[0].SameSite != http.SameSiteLaxMode {
		t.Fatal("unsafe session cookie")
	}
	var decoded httpx.Session
	if e = json.Unmarshal(w.Body.Bytes(), &decoded); e != nil || decoded.CSRFToken == "" {
		t.Fatal("login CSRF missing")
	}
}

func TestRateLimiterSharesNormalizedEmailBucketAndSkipsInvalidEmailBuckets(t *testing.T) {
	handler := &Handler{Store: &Store{}, Origin: "https://teamie.example", Secure: true}
	routes := handler.Routes()

	postJSON := func(path, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		r.Header.Set("Origin", "https://teamie.example")
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		routes.ServeHTTP(w, r)
		return w
	}

	// Invalid emails fail without creating per-email rate-limit buckets.
	for _, invalid := range []string{"", "   ", "not-an-email", "Alice <a@x.com>", "a@b@c.com"} {
		w := postJSON("/api/v1/auth/register", `{"email":`+strconvQuote(invalid)+`,"password":"very-long-password","displayName":"Alice","inviteCode":"test-bootstrap-code"}`)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("register invalid email %q: got %d, want 400", invalid, w.Code)
		}
		wLogin := postJSON("/api/v1/auth/login", `{"email":`+strconvQuote(invalid)+`,"password":"very-long-password"}`)
		if wLogin.Code != http.StatusUnauthorized {
			t.Fatalf("login invalid email %q: got %d, want 401", invalid, wLogin.Code)
		}
	}
	if got := handler.BucketCount(); got != 0 {
		t.Fatalf("invalid emails created %d rate-limit buckets, want 0", got)
	}

	// Case and surrounding-whitespace variants share one 20/min bucket.
	variants := []string{"A@x.com", "a@x.com", " a@x.com "}
	for i := 0; i < 20; i++ {
		email := variants[i%len(variants)]
		w := postJSON("/api/v1/auth/register", `{"email":`+strconvQuote(email)+`,"password":"short","displayName":"Alice","inviteCode":"wrong"}`)
		if w.Code == http.StatusTooManyRequests {
			t.Fatalf("request %d (%q) throttled early: %d", i+1, email, w.Code)
		}
	}
	if got := handler.BucketCount(); got != 1 {
		t.Fatalf("expected 1 shared email bucket across variants, got %d", got)
	}
	for _, email := range variants {
		w := postJSON("/api/v1/auth/register", `{"email":`+strconvQuote(email)+`,"password":"very-long-password","displayName":"Alice","inviteCode":"test-bootstrap-code"}`)
		if w.Code != http.StatusTooManyRequests {
			t.Fatalf("21st+ register for %q was not rate-limited: %d %s", email, w.Code, w.Body.String())
		}
		wLogin := postJSON("/api/v1/auth/login", `{"email":`+strconvQuote(email)+`,"password":"very-long-password"}`)
		if wLogin.Code != http.StatusTooManyRequests {
			t.Fatalf("21st+ login for %q was not rate-limited: %d %s", email, wLogin.Code, wLogin.Body.String())
		}
	}
}

func strconvQuote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
