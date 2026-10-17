package identity

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"
	"teamie/backend/internal/api"
	"teamie/backend/internal/config"
	"teamie/backend/internal/testdb"
)

func passwordStore(t *testing.T) (*Store, api.Session, string) {
	t.Helper()
	s, err := Open(context.Background(), testdb.URL(t), config.Flags{}, "password-test-invite")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.DB.Close)
	ss, token, err := s.Register(context.Background(), "password-test@example.invalid", "initial-test-password", "Password Tester", "password-test-invite")
	if err != nil {
		t.Fatal(err)
	}
	return s, ss, token
}

func passwordError(t *testing.T, err error, status int, code string) {
	t.Helper()
	var typed *api.Error
	if !errors.As(err, &typed) || typed.Status != status || typed.Code != code {
		t.Fatalf("expected %d %s, got %v", status, code, err)
	}
}

func TestPasswordPolicyMatchesRegistrationLimits(t *testing.T) {
	for _, tt := range []struct {
		value string
		valid bool
	}{
		{strings.Repeat("a", 11), false}, {strings.Repeat("a", 12), true},
		{strings.Repeat("a", 72), true}, {strings.Repeat("a", 73), false},
		{strings.Repeat("字", 24), true}, {strings.Repeat("字", 25), false},
	} {
		if (validatePassword(tt.value) == nil) != tt.valid {
			t.Fatalf("unexpected policy for %d bytes", len(tt.value))
		}
	}
}

func TestPasswordChangeRevokesSessionsAndRejectsStaleLogin(t *testing.T) {
	ctx := context.Background()
	s, ss, token := passwordStore(t)
	_, otherToken, err := s.Login(ctx, ss.User.Email, "initial-test-password")
	if err != nil {
		t.Fatal(err)
	}
	var oldHash string
	if err = s.DB.QueryRow(ctx, "SELECT password_hash FROM identity_users WHERE id=$1", ss.User.ID).Scan(&oldHash); err != nil {
		t.Fatal(err)
	}
	passwordError(t, s.ChangePassword(ctx, ss.User.ID, token, "wrong-password", "different-test-password"), 400, "invalid_current_password")
	passwordError(t, s.ChangePassword(ctx, ss.User.ID, token, "initial-test-password", "short"), 400, "invalid_password")
	passwordError(t, s.ChangePassword(ctx, ss.User.ID, token, "initial-test-password", "initial-test-password"), 400, "password_unchanged")
	for _, current := range []string{token, otherToken} {
		if _, err = s.Validate(ctx, current); err != nil {
			t.Fatal("failed change revoked a session", err)
		}
	}
	if err = s.ChangePassword(ctx, ss.User.ID, token, "initial-test-password", "different-test-password"); err != nil {
		t.Fatal(err)
	}
	for _, current := range []string{token, otherToken} {
		if _, err = s.Validate(ctx, current); err == nil {
			t.Fatal("previous session survived password change")
		}
	}
	if _, _, err = s.Login(ctx, ss.User.Email, "initial-test-password"); err == nil {
		t.Fatal("old password still logs in")
	}
	// Simulate a login that verified oldHash before the password transaction.
	// The production Login path uses this same locked session creation step.
	_, _, err = s.loginSession(ctx, ss.User, oldHash)
	passwordError(t, err, 401, "invalid_credentials")
	logged, newToken, err := s.Login(ctx, ss.User.Email, "different-test-password")
	if err != nil || logged.User.ID != ss.User.ID || logged.User.PlatformRole != ss.User.PlatformRole || newToken == token {
		t.Fatal("new password cannot establish the same account", err)
	}
	var newHash string
	if err = s.DB.QueryRow(ctx, "SELECT password_hash FROM identity_users WHERE id=$1", ss.User.ID).Scan(&newHash); err != nil {
		t.Fatal(err)
	}
	if cost, err := bcrypt.Cost([]byte(newHash)); err != nil || cost != 12 || newHash == oldHash {
		t.Fatal("password hash not replaced with required cost", err)
	}
	passwordError(t, s.ChangePassword(ctx, ss.User.ID, token, "different-test-password", "third-test-password"), 401, "unauthenticated")
	passwordError(t, s.ChangePassword(ctx, ss.User.ID, token, "different-test-password", "short"), 401, "unauthenticated")
}

func TestPasswordChangeIsAtomicAndBoundToCurrentHumanSession(t *testing.T) {
	ctx := context.Background()
	s, ss, token := passwordStore(t)
	_, code, err := s.CreateRegistrationInvite(ctx, ss.User.ID)
	if err != nil {
		t.Fatal(err)
	}
	other, otherToken, err := s.Register(ctx, "other-password-test@example.invalid", "other-test-password", "Other", code)
	if err != nil {
		t.Fatal(err)
	}
	passwordError(t, s.ChangePassword(ctx, ss.User.ID, otherToken, "initial-test-password", "different-test-password"), 401, "unauthenticated")
	_, err = s.DB.Exec(ctx, `CREATE FUNCTION reject_password_session_delete() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'disposable rollback fixture'; RETURN OLD; END $$;
		CREATE TRIGGER reject_password_session_delete BEFORE DELETE ON identity_sessions FOR EACH ROW EXECUTE FUNCTION reject_password_session_delete();`)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.ChangePassword(ctx, ss.User.ID, token, "initial-test-password", "different-test-password"); err == nil {
		t.Fatal("expected transaction rollback")
	}
	if _, err = s.Validate(ctx, token); err != nil {
		t.Fatal("rollback revoked session", err)
	}
	if _, _, err = s.Login(ctx, ss.User.Email, "initial-test-password"); err != nil {
		t.Fatal("rollback changed password", err)
	}
	if _, err = s.DB.Exec(ctx, "DROP TRIGGER reject_password_session_delete ON identity_sessions; DROP FUNCTION reject_password_session_delete()"); err != nil {
		t.Fatal(err)
	}
	if err = s.ChangePassword(ctx, ss.User.ID, token, "initial-test-password", "different-test-password"); err != nil {
		t.Fatal(err)
	}
	if current, err := s.Validate(ctx, otherToken); err != nil || current.User.ID != other.User.ID {
		t.Fatal("unrelated account session changed", err)
	}
	if _, _, err = s.Login(ctx, other.User.Email, "other-test-password"); err != nil {
		t.Fatal("unrelated password changed", err)
	}
}

func TestPasswordChangeAndLoginRejectOversizedBcryptAliases(t *testing.T) {
	ctx := context.Background()
	s, ss, token := passwordStore(t)
	password := strings.Repeat("a", 72)
	if err := s.ChangePassword(ctx, ss.User.ID, token, "initial-test-password", password); err != nil {
		t.Fatal(err)
	}
	_, token, err := s.Login(ctx, ss.User.Email, password)
	if err != nil {
		t.Fatal(err)
	}
	var before string
	if err = s.DB.QueryRow(ctx, "SELECT password_hash FROM identity_users WHERE id=$1", ss.User.ID).Scan(&before); err != nil {
		t.Fatal(err)
	}
	// bcrypt itself ignores bytes past 72 during comparison. The application
	// must reject the alias rather than accept an effectively unchanged secret.
	_, _, err = s.Login(ctx, ss.User.Email, password+"x")
	passwordError(t, err, 401, "invalid_credentials")
	passwordError(t, s.ChangePassword(ctx, ss.User.ID, token, password+"x", password), 400, "invalid_current_password")
	passwordError(t, s.ChangePassword(ctx, ss.User.ID, token, password, password), 400, "password_unchanged")
	var after string
	if err = s.DB.QueryRow(ctx, "SELECT password_hash FROM identity_users WHERE id=$1", ss.User.ID).Scan(&after); err != nil || after != before {
		t.Fatal("rejected alias modified stored credential", err)
	}
	if _, err = s.Validate(ctx, token); err != nil {
		t.Fatal("rejected alias revoked a valid session", err)
	}
}

func TestPasswordChangeRejectsEquivalentBcryptKeys(t *testing.T) {
	ctx := context.Background()
	s, ss, token := passwordStore(t)
	old := "initial-test-password"
	alias := old + "\x00" + old
	if err := validatePassword(alias); err != nil {
		t.Fatal("fixture must meet normal byte/codepoint limits", err)
	}
	var before string
	if err := s.DB.QueryRow(ctx, "SELECT password_hash FROM identity_users WHERE id=$1", ss.User.ID).Scan(&before); err != nil {
		t.Fatal(err)
	}
	passwordError(t, s.ChangePassword(ctx, ss.User.ID, token, old, alias), 400, "password_unchanged")
	var after string
	if err := s.DB.QueryRow(ctx, "SELECT password_hash FROM identity_users WHERE id=$1", ss.User.ID).Scan(&after); err != nil || after != before {
		t.Fatal("equivalent key changed stored credential", err)
	}
	if _, err := s.Validate(ctx, token); err != nil {
		t.Fatal("equivalent key revoked session", err)
	}
}

func TestPasswordHTTPOriginCSRFValidationAndSessionRevocation(t *testing.T) {
	s, ss, token := passwordStore(t)
	h := (&Handler{Store: s, Origin: "https://teamie.example", Secure: true}).Routes()
	call := func(body, origin, csrf string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, "/api/v1/auth/password", strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Origin", origin)
		r.Header.Set("X-CSRF-Token", csrf)
		r.AddCookie(&http.Cookie{Name: api.CookieName, Value: token})
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	body := `{"currentPassword":"initial-test-password","newPassword":"different-test-password"}`
	if w := call(body, "https://evil.example", ss.CSRFToken); w.Code != 403 {
		t.Fatal("foreign origin allowed", w.Code)
	}
	if w := call(body, "https://teamie.example", ""); w.Code != 403 {
		t.Fatal("missing CSRF allowed", w.Code)
	}
	if w := call(`{"currentPassword":"wrong-password","newPassword":"different-test-password"}`, "https://teamie.example", ss.CSRFToken); w.Code != 400 {
		t.Fatal("wrong password must remain a recoverable form error", w.Code)
	}
	if _, err := s.Validate(context.Background(), token); err != nil {
		t.Fatal(err)
	}
	w := call(body, "https://teamie.example", ss.CSRFToken)
	if w.Code != 204 || w.Body.Len() != 0 || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("password response", w.Code, w.Body.String())
	}
	cookies := w.Result().Cookies()
	if len(cookies) != 0 {
		t.Fatal("password response must not overwrite a newer login cookie")
	}
	w = call(body, "https://teamie.example", ss.CSRFToken)
	var result struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil || w.Code != 401 || result.Error.Code != "unauthenticated" {
		t.Fatal("revoked session reused", w.Code)
	}
}

func TestDelayedPasswordResponsePreservesNewerLoginCookie(t *testing.T) {
	testDelayedSessionRevocation(t, "/api/v1/auth/password", `{"currentPassword":"initial-test-password","newPassword":"different-test-password"}`)
}

func TestDelayedLogoutResponsePreservesNewerLoginCookie(t *testing.T) {
	testDelayedSessionRevocation(t, "/api/v1/auth/logout", `{}`)
}

func testDelayedSessionRevocation(t *testing.T, path, body string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	s, ss, token := passwordStore(t)
	_, code, err := s.CreateRegistrationInvite(ctx, ss.User.ID)
	if err != nil {
		t.Fatal(err)
	}
	other, _, err := s.Register(ctx, "later-login@example.invalid", "other-test-password", "Other", code)
	if err != nil {
		t.Fatal(err)
	}
	changed, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	h := &Handler{Store: s}
	var routes http.Handler
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != path {
			routes.ServeHTTP(w, r)
			return
		}
		response := httptest.NewRecorder()
		routes.ServeHTTP(response, r)
		close(changed)
		select {
		case <-release:
		case <-ctx.Done():
			return
		}
		for key, values := range response.Header() {
			for _, value := range values {
				w.Header().Add(key, value)
			}
		}
		w.WriteHeader(response.Code)
		_, _ = w.Write(response.Body.Bytes())
	}))
	defer func() { once.Do(func() { close(release) }); server.Close() }()
	h.Origin = "http://" + server.Listener.Addr().String()
	routes = h.Routes()
	server.Start()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	jar.SetCookies(u, []*http.Cookie{{Name: api.CookieName, Value: token, Path: "/"}})
	client := &http.Client{Jar: jar}
	post := func(path, body, csrf string) (*http.Response, error) {
		r, err := http.NewRequestWithContext(ctx, http.MethodPost, server.URL+path, strings.NewReader(body))
		if err != nil {
			return nil, err
		}
		r.Header.Set("Origin", server.URL)
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("X-CSRF-Token", csrf)
		return client.Do(r)
	}
	passwordResult := make(chan error, 1)
	go func() {
		response, err := post(path, body, ss.CSRFToken)
		if err == nil {
			response.Body.Close()
			if response.StatusCode != 204 {
				err = errors.New("session revocation did not succeed: " + response.Status)
			} else if len(response.Cookies()) != 0 {
				err = errors.New("delayed response rewrote the shared login cookie")
			}
		}
		passwordResult <- err
	}()
	select {
	case <-changed:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	// Another tab signs in while the earlier revocation response is delayed.
	response, err := post("/api/v1/auth/login", `{"email":"later-login@example.invalid","password":"other-test-password"}`, "")
	if err != nil {
		t.Fatal(err)
	}
	var signedIn api.Session
	err = json.NewDecoder(response.Body).Decode(&signedIn)
	response.Body.Close()
	if err != nil || response.StatusCode != 200 || signedIn.User.ID != other.User.ID {
		t.Fatal("new login failed", err)
	}
	once.Do(func() { close(release) })
	select {
	case err = <-passwordResult:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"/api/v1/auth/session", nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err = client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var current api.Session
	if err = json.NewDecoder(response.Body).Decode(&current); err != nil || response.StatusCode != 200 || current.User.ID != other.User.ID {
		t.Fatal("delayed revocation response removed the newer session cookie", response.StatusCode, err)
	}
	if _, err = s.Validate(ctx, token); err == nil {
		t.Fatal("old account session survived revocation")
	}
}
