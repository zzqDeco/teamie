package identity

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

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

func TestPasswordHTTPOriginCSRFValidationAndCookieRevocation(t *testing.T) {
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
	if len(cookies) != 1 || cookies[0].Value != "" || cookies[0].MaxAge >= 0 || !cookies[0].Secure || !cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteLaxMode {
		t.Fatal("session cookie not cleared safely")
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
