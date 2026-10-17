package identity

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"teamie/backend/internal/api"
	"teamie/backend/internal/config"
	"teamie/backend/internal/database"
	"teamie/backend/internal/testdb"
)

func resetMember(t *testing.T, s *Store, admin api.Session, email, name string) (api.Session, string) {
	t.Helper()
	_, code, err := s.CreateRegistrationInvite(t.Context(), admin.User.ID)
	if err != nil {
		t.Fatal(err)
	}
	ss, token, err := s.Register(t.Context(), email, "initial-test-password", name, code)
	if err != nil {
		t.Fatal(err)
	}
	return ss, token
}

func issueReset(t *testing.T, s *Store, admin api.Session, adminToken, target string) (PasswordReset, string) {
	t.Helper()
	reset, token, err := s.CreatePasswordResetLink(t.Context(), admin.User.ID, adminToken, target)
	if err != nil {
		t.Fatal(err)
	}
	return reset, token
}

func TestPasswordResetSingleUsePolicyAndSessionRevocation(t *testing.T) {
	ctx := t.Context()
	s, admin, adminToken := passwordStore(t)
	member, memberToken := resetMember(t, s, admin, "recovery@example.invalid", "Recovery")
	_, secondToken, err := s.Login(ctx, member.User.Email, "initial-test-password")
	if err != nil {
		t.Fatal(err)
	}
	var oldHash string
	if err = s.DB.QueryRow(ctx, "SELECT password_hash FROM identity_users WHERE id=$1", member.User.ID).Scan(&oldHash); err != nil {
		t.Fatal(err)
	}
	reset, token := issueReset(t, s, admin, adminToken, member.User.ID)
	if len(token) != 64 || reset.ExpiresAt.Sub(reset.CreatedAt) != time.Hour {
		t.Fatal("wrong reset entropy or lifetime")
	}
	var storedHash string
	var creator *string
	if err = s.DB.QueryRow(ctx, "SELECT token_hash,created_by FROM identity_password_resets WHERE id=$1", reset.ID).Scan(&storedHash, &creator); err != nil || storedHash != api.Hash(token) || storedHash == token || creator == nil || *creator != admin.User.ID {
		t.Fatal("reset secret persistence or issuer", err)
	}
	for range 2 {
		info, err := s.InspectPasswordReset(ctx, token)
		if err != nil || info.Account.ID != member.User.ID || info.Account.DisplayName != "Recovery" || info.Account.EmailMasked != "r***@example.invalid" || info.ExpiresAt != reset.ExpiresAt {
			t.Fatal("inspection consumed the token or exposed wrong account", info, err)
		}
	}
	for _, password := range []string{"short", strings.Repeat("x", 73), strings.Repeat("字", 25)} {
		passwordError(t, s.ResetPassword(ctx, token, password), 400, "invalid_password")
	}
	for _, password := range []string{"initial-test-password", "initial-test-password\x00initial-test-password"} {
		passwordError(t, s.ResetPassword(ctx, token, password), 400, "password_unchanged")
	}
	for _, oldToken := range []string{memberToken, secondToken} {
		if _, err = s.Validate(ctx, oldToken); err != nil {
			t.Fatal("issuance/inspection/rejected password revoked a session", err)
		}
	}
	if err = s.ResetPassword(ctx, token, "replacement-test-password"); err != nil {
		t.Fatal(err)
	}
	for _, oldToken := range []string{memberToken, secondToken} {
		if _, err = s.Validate(ctx, oldToken); err == nil {
			t.Fatal("old session survived reset")
		}
	}
	if current, err := s.Validate(ctx, adminToken); err != nil || current.User.ID != admin.User.ID {
		t.Fatal("reset revoked unrelated administrator session", err)
	}
	if _, _, err = s.Login(ctx, member.User.Email, "initial-test-password"); err == nil {
		t.Fatal("previous password still logs in")
	}
	_, _, err = s.loginSession(ctx, member.User, oldHash)
	passwordError(t, err, 401, "invalid_credentials")
	current, _, err := s.Login(ctx, member.User.Email, "replacement-test-password")
	if err != nil || current.User.ID != member.User.ID || current.User.PlatformRole != "user" {
		t.Fatal("reset changed account or privilege", err)
	}
	_, err = s.InspectPasswordReset(ctx, token)
	passwordError(t, err, 400, "invalid_reset_link")
	passwordError(t, s.ResetPassword(ctx, token, "third-test-password"), 400, "invalid_reset_link")
	var used, revoked bool
	if err = s.DB.QueryRow(ctx, "SELECT used_at IS NOT NULL,revoked_at IS NOT NULL FROM identity_password_resets WHERE id=$1", reset.ID).Scan(&used, &revoked); err != nil || !used || revoked {
		t.Fatal("successful consumption not recorded", err)
	}
}

func TestPasswordResetReplacementPasswordChangeAndTerminalLinks(t *testing.T) {
	ctx := t.Context()
	s, admin, adminToken := passwordStore(t)
	first, firstToken := issueReset(t, s, admin, adminToken, admin.User.ID)
	_, secondToken := issueReset(t, s, admin, adminToken, admin.User.ID)
	_, err := s.InspectPasswordReset(ctx, firstToken)
	passwordError(t, err, 400, "invalid_reset_link")
	var revoked bool
	if err = s.DB.QueryRow(ctx, "SELECT revoked_at IS NOT NULL FROM identity_password_resets WHERE id=$1", first.ID).Scan(&revoked); err != nil || !revoked {
		t.Fatal("replacement did not retire previous link", err)
	}
	if err = s.ChangePassword(ctx, admin.User.ID, adminToken, "initial-test-password", "changed-via-settings-password"); err != nil {
		t.Fatal(err)
	}
	passwordError(t, s.ResetPassword(ctx, secondToken, "another-test-password"), 400, "invalid_reset_link")
	_, adminToken, err = s.Login(ctx, admin.User.Email, "changed-via-settings-password")
	if err != nil {
		t.Fatal(err)
	}
	for _, state := range []string{"expired", "revoked", "disabled", "deleted"} {
		t.Run(state, func(t *testing.T) {
			member, _ := resetMember(t, s, admin, state+"@example.invalid", state)
			reset, token := issueReset(t, s, admin, adminToken, member.User.ID)
			query := map[string]string{
				"expired":  "UPDATE identity_password_resets SET created_at=now()-interval '2 hours',expires_at=now()-interval '1 hour' WHERE id=$1",
				"revoked":  "UPDATE identity_password_resets SET revoked_at=now() WHERE id=$1",
				"disabled": "UPDATE identity_users SET enabled=false WHERE id=$1",
				"deleted":  "UPDATE identity_users SET deleted_at=now() WHERE id=$1",
			}[state]
			id := reset.ID
			if state == "disabled" || state == "deleted" {
				id = member.User.ID
			}
			if _, err := s.DB.Exec(ctx, query, id); err != nil {
				t.Fatal(err)
			}
			_, err := s.InspectPasswordReset(ctx, token)
			passwordError(t, err, 400, "invalid_reset_link")
			passwordError(t, s.ResetPassword(ctx, token, "replacement-test-password"), 400, "invalid_reset_link")
		})
	}
	for _, token := range []string{"", "bad-token", strings.Repeat("g", 64), strings.Repeat("a", 64), strings.Repeat("f", 1000)} {
		_, err := s.InspectPasswordReset(ctx, token)
		passwordError(t, err, 400, "invalid_reset_link")
		passwordError(t, s.ResetPassword(ctx, token, "short"), 400, "invalid_reset_link")
	}
}

func TestPasswordResetRollbackKeepsTokenPasswordAndSessions(t *testing.T) {
	ctx := t.Context()
	s, admin, adminToken := passwordStore(t)
	reset, token := issueReset(t, s, admin, adminToken, admin.User.ID)
	_, err := s.DB.Exec(ctx, `CREATE FUNCTION reject_reset_session_delete() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'disposable rollback fixture'; RETURN OLD; END $$;
		CREATE TRIGGER reject_reset_session_delete BEFORE DELETE ON identity_sessions FOR EACH ROW EXECUTE FUNCTION reject_reset_session_delete()`)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.ResetPassword(ctx, token, "replacement-test-password"); err == nil {
		t.Fatal("forced failure unexpectedly committed")
	}
	if _, err = s.InspectPasswordReset(ctx, token); err != nil {
		t.Fatal("rollback consumed token", err)
	}
	if _, err = s.Validate(ctx, adminToken); err != nil {
		t.Fatal("rollback deleted old session", err)
	}
	if _, _, err = s.Login(ctx, admin.User.Email, "initial-test-password"); err != nil {
		t.Fatal("rollback changed password", err)
	}
	if _, err = s.DB.Exec(ctx, "DROP TRIGGER reject_reset_session_delete ON identity_sessions; DROP FUNCTION reject_reset_session_delete()"); err != nil {
		t.Fatal(err)
	}
	if err = s.ResetPassword(ctx, token, "replacement-test-password"); err != nil {
		t.Fatal("rolled-back link was not reusable", reset.ID, err)
	}
}

func TestPasswordResetFlagGatesOnlyNewIssuance(t *testing.T) {
	ctx := t.Context()
	s, admin, adminToken := passwordStore(t)
	_, token := issueReset(t, s, admin, adminToken, admin.User.ID)
	path := filepath.Join(t.TempDir(), "flags.json")
	if err := os.WriteFile(path, []byte(`{"revision":"reset-off","flags":{"auth.password_reset":false}}`), 0600); err != nil {
		t.Fatal(err)
	}
	s.Flags = config.Flags{Path: path}
	_, _, err := s.CreatePasswordResetLink(ctx, admin.User.ID, adminToken, admin.User.ID)
	passwordError(t, err, 403, "feature_disabled")
	_, _, err = s.OperatorPasswordResetLink(ctx, admin.User.Email)
	passwordError(t, err, 403, "feature_disabled")
	if page, err := s.AdminUsers(ctx, admin.User.ID, adminToken, "", 0, ""); err != nil || len(page.Users) != 1 {
		t.Fatal("disabled issuance hid user list", err)
	}
	if _, err = s.InspectPasswordReset(ctx, token); err != nil {
		t.Fatal("disabled issuance hid issued link", err)
	}
	if err = s.ResetPassword(ctx, token, "replacement-test-password"); err != nil {
		t.Fatal("disabled issuance blocked redemption", err)
	}
}

func TestConcurrentPasswordResetConsumesExactlyOnce(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	s, admin, adminToken := passwordStore(t)
	_, token := issueReset(t, s, admin, adminToken, admin.User.ID)
	start := make(chan struct{})
	results := make(chan error, 4)
	for range 4 {
		go func() { <-start; results <- s.ResetPassword(ctx, token, "replacement-test-password") }()
	}
	close(start)
	successes := 0
	for range 4 {
		if err := <-results; err == nil {
			successes++
		} else {
			passwordError(t, err, 400, "invalid_reset_link")
		}
	}
	if successes != 1 {
		t.Fatal("reset committed multiple times", successes)
	}
	var sessions int
	if err := s.DB.QueryRow(ctx, "SELECT count(*) FROM identity_sessions WHERE user_id=$1", admin.User.ID).Scan(&sessions); err != nil || sessions != 0 {
		t.Fatal("reset created or retained a session", sessions, err)
	}
}

func lockResetUser(t *testing.T, s *Store, id string) pgx.Tx {
	t.Helper()
	tx, err := s.DB.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(t.Context(), "SELECT id FROM identity_users WHERE id=$1 FOR UPDATE", id); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tx.Rollback(context.Background()) })
	return tx
}

func waitForResetWaiters(t *testing.T, s *Store, lock pgx.Tx, count int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	var blocker int32
	if err := lock.QueryRow(ctx, "SELECT pg_backend_pid()").Scan(&blocker); err != nil {
		t.Fatal(err)
	}
	tick := time.NewTicker(5 * time.Millisecond)
	defer tick.Stop()
	for {
		var waiting int
		// PostgreSQL may queue a second tuple waiter behind the first waiter,
		// so include the complete blocker chain rather than only direct waits.
		if err := s.DB.QueryRow(ctx, `WITH RECURSIVE blocked(pid) AS (
			SELECT pid FROM pg_stat_activity WHERE $1=ANY(pg_blocking_pids(pid))
			UNION SELECT a.pid FROM pg_stat_activity a JOIN blocked b ON b.pid=ANY(pg_blocking_pids(a.pid))
		) SELECT count(*) FROM blocked`, blocker).Scan(&waiting); err != nil {
			t.Fatal("could not observe competing row locks", err)
		}
		if waiting >= count {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatal("credential operations did not overlap while the user row was locked")
		case <-tick.C:
		}
	}
}

func TestPasswordResetSerializesWithIssuancePasswordChangeAndLogin(t *testing.T) {
	for _, competing := range []string{"issuance", "password-change", "login"} {
		t.Run(competing, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
			defer cancel()
			s, admin, adminToken := passwordStore(t)
			member, memberToken := resetMember(t, s, admin, competing+"@example.invalid", competing)
			_, resetToken := issueReset(t, s, admin, adminToken, member.User.ID)
			var oldHash string
			if err := s.DB.QueryRow(ctx, "SELECT password_hash FROM identity_users WHERE id=$1", member.User.ID).Scan(&oldHash); err != nil {
				t.Fatal(err)
			}
			lock := lockResetUser(t, s, member.User.ID)
			resetResult, otherResult := make(chan error, 1), make(chan error, 1)
			var otherToken string
			var started sync.WaitGroup
			started.Add(2)
			go func() {
				started.Done()
				resetResult <- s.ResetPassword(ctx, resetToken, "reset-winning-password")
			}()
			go func() {
				started.Done()
				var err error
				switch competing {
				case "issuance":
					_, otherToken, err = s.CreatePasswordResetLink(ctx, admin.User.ID, adminToken, member.User.ID)
				case "password-change":
					err = s.ChangePassword(ctx, member.User.ID, memberToken, "initial-test-password", "settings-winning-password")
				case "login":
					_, otherToken, err = s.loginSession(ctx, member.User, oldHash)
				}
				otherResult <- err
			}()
			started.Wait()
			waitForResetWaiters(t, s, lock, 2)
			if err := lock.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			resetErr, otherErr := <-resetResult, <-otherResult
			switch competing {
			case "issuance":
				if otherErr != nil {
					t.Fatal("issuance could not serialize", otherErr)
				}
				if resetErr != nil {
					passwordError(t, resetErr, 400, "invalid_reset_link")
				}
				if _, err := s.InspectPasswordReset(ctx, otherToken); err != nil {
					t.Fatal("new issuance lost its active link", err)
				}
			case "password-change":
				if (resetErr == nil) == (otherErr == nil) {
					t.Fatal("exactly one credential replacement should commit", resetErr, otherErr)
				}
				if resetErr == nil {
					passwordError(t, otherErr, 401, "unauthenticated")
				} else {
					passwordError(t, resetErr, 400, "invalid_reset_link")
				}
			case "login":
				if resetErr != nil {
					t.Fatal("reset failed racing login", resetErr)
				}
				if otherErr != nil {
					passwordError(t, otherErr, 401, "invalid_credentials")
				} else if _, err := s.Validate(ctx, otherToken); err == nil {
					t.Fatal("login checked before reset retained an old-password session")
				}
			}
			_, err := s.InspectPasswordReset(ctx, resetToken)
			passwordError(t, err, 400, "invalid_reset_link")
		})
	}
}

func TestTwoAdministratorsCanIssueForEachOtherWithoutDeadlock(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	s, first, firstToken := passwordStore(t)
	second, secondToken := resetMember(t, s, first, "second-admin@example.invalid", "Second admin")
	if _, err := s.DB.Exec(ctx, "UPDATE identity_users SET platform_role='admin' WHERE id=$1", second.User.ID); err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	results := make(chan error, 2)
	go func() {
		<-start
		_, _, err := s.CreatePasswordResetLink(ctx, first.User.ID, firstToken, second.User.ID)
		results <- err
	}()
	go func() {
		<-start
		_, _, err := s.CreatePasswordResetLink(ctx, second.User.ID, secondToken, first.User.ID)
		results <- err
	}()
	close(start)
	for range 2 {
		if err := <-results; err != nil {
			t.Fatal("opposite admin targets did not serialize", err)
		}
	}
	issueReset(t, s, first, firstToken, first.User.ID)
}

func TestPasswordResetAdminUsersPaginationAndLiveAuthorization(t *testing.T) {
	ctx := t.Context()
	s, admin, adminToken := passwordStore(t)
	member, memberToken := resetMember(t, s, admin, "search@example.invalid", "Literal_% Name")
	disabled, _ := resetMember(t, s, admin, "hidden-disabled@example.invalid", "Disabled")
	deleted, _ := resetMember(t, s, admin, "hidden-deleted@example.invalid", "Deleted")
	if _, err := s.DB.Exec(ctx, "UPDATE identity_users SET enabled=false WHERE id=$1", disabled.User.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec(ctx, "UPDATE identity_users SET deleted_at=now() WHERE id=$1", deleted.User.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ProvisionPrincipal(ctx, "hidden-agent", "Hidden agent", "agent", true, false, 1); err != nil {
		t.Fatal(err)
	}
	first, err := s.AdminUsers(ctx, admin.User.ID, adminToken, "", 1, "")
	if err != nil || len(first.Users) != 1 || first.Cursor == "" {
		t.Fatal("first keyset page", err)
	}
	last, err := s.AdminUsers(ctx, admin.User.ID, adminToken, first.Cursor, 1, "")
	if err != nil || len(last.Users) != 1 || last.Cursor != "" || first.Users[0].ID == last.Users[0].ID {
		t.Fatal("last keyset page skipped/duplicated a human", err)
	}
	for _, query := range []string{"SEARCH@EXAMPLE", "literal_%"} {
		page, err := s.AdminUsers(ctx, admin.User.ID, adminToken, "", 50, query)
		if err != nil || len(page.Users) != 1 || page.Users[0].ID != member.User.ID {
			t.Fatal("literal case-insensitive search", query, err)
		}
	}
	for _, target := range []string{disabled.User.ID, deleted.User.ID, "hidden-agent", "unknown"} {
		_, _, err := s.CreatePasswordResetLink(ctx, admin.User.ID, adminToken, target)
		passwordError(t, err, 404, "user_not_found")
	}
	_, err = s.AdminUsers(ctx, member.User.ID, memberToken, "", 10, "")
	passwordError(t, err, 403, "forbidden")
	_, _, err = s.CreatePasswordResetLink(ctx, member.User.ID, memberToken, admin.User.ID)
	passwordError(t, err, 403, "forbidden")
	_, _, err = s.CreatePasswordResetLink(ctx, admin.User.ID, memberToken, admin.User.ID)
	passwordError(t, err, 401, "unauthenticated")
	for _, request := range []struct {
		cursor, query string
		limit         int
	}{{cursor: "!"}, {cursor: strings.Repeat("a", 1000)}, {limit: 101}, {limit: -1}, {query: strings.Repeat("字", 201)}, {query: "invalid\x00query"}, {query: string([]byte{0xff})}} {
		_, err := s.AdminUsers(ctx, admin.User.ID, adminToken, request.cursor, request.limit, request.query)
		if err == nil {
			t.Fatal("invalid list bound accepted")
		}
	}
	if err := s.Logout(ctx, adminToken); err != nil {
		t.Fatal(err)
	}
	_, err = s.AdminUsers(ctx, admin.User.ID, adminToken, "", 10, "")
	passwordError(t, err, 401, "unauthenticated")
	_, _, err = s.CreatePasswordResetLink(ctx, admin.User.ID, adminToken, member.User.ID)
	passwordError(t, err, 401, "unauthenticated")
}

func TestPasswordResetHTTPAuthorizationPrivacyAndCapability(t *testing.T) {
	ctx := t.Context()
	s, admin, adminToken := passwordStore(t)
	member, memberToken := resetMember(t, s, admin, "http-recovery@example.invalid", "HTTP recovery")
	h := (&Handler{Store: s, Origin: "https://teamie.example", Secure: true}).Routes()
	call := func(method, path, body, token, csrf, origin, contentType string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Content-Type", contentType)
		r.Header.Set("Origin", origin)
		r.Header.Set("X-CSRF-Token", csrf)
		if token != "" {
			r.AddCookie(&http.Cookie{Name: api.CookieName, Value: token})
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if len(w.Result().Cookies()) != 0 {
			t.Fatal("reset endpoints must never overwrite browser cookies", method, path)
		}
		return w
	}
	issuePath := "/api/v1/auth/admin/users/" + member.User.ID + "/password-reset-links"
	for _, request := range []struct {
		method, path, body, token, csrf, origin, mime string
		status                                        int
	}{
		{"GET", "/api/v1/auth/admin/users", "", "", "", "", "", 401},
		{"GET", "/api/v1/auth/admin/users", "", memberToken, "", "", "", 403},
		{"POST", issuePath, `{}`, memberToken, member.CSRFToken, "https://teamie.example", "application/json", 403},
		{"POST", issuePath, `{}`, adminToken, "", "https://teamie.example", "application/json", 403},
		{"POST", issuePath, `{}`, adminToken, admin.CSRFToken, "https://evil.example", "application/json", 403},
		{"POST", issuePath, `{}`, adminToken, admin.CSRFToken, "https://teamie.example", "text/plain", 415},
		{"POST", issuePath, `{"newPassword":"forged-password"}`, adminToken, admin.CSRFToken, "https://teamie.example", "application/json", 400},
		{"GET", "/api/v1/auth/admin/users?limit=0", "", adminToken, "", "", "", 400},
		{"GET", "/api/v1/auth/admin/users?limit=101", "", adminToken, "", "", "", 400},
	} {
		if w := call(request.method, request.path, request.body, request.token, request.csrf, request.origin, request.mime); w.Code != request.status {
			t.Fatal("management authorization/request validation", request.path, w.Code, w.Body.String())
		}
	}
	w := call("POST", issuePath, `{}`, adminToken, admin.CSRFToken, "https://teamie.example", "application/json")
	if w.Code != 201 || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("reset creation response", w.Code, w.Body.String())
	}
	var issued struct {
		Reset PasswordReset `json:"reset"`
		URL   string        `json:"url"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &issued); err != nil || !strings.HasPrefix(issued.URL, "/reset-password#token=") {
		t.Fatal("wrong reset creation DTO", err)
	}
	token := strings.TrimPrefix(issued.URL, "/reset-password#token=")
	if len(token) != 64 || strings.Contains(w.Body.String(), "tokenHash") || strings.Contains(w.Body.String(), "\"token\"") {
		t.Fatal("reset response leaked unexpected material")
	}
	w = call("GET", "/api/v1/auth/admin/users", "", adminToken, "", "", "")
	if w.Code != 200 || w.Header().Get("Cache-Control") != "no-store" || strings.Contains(w.Body.String(), token) || strings.Contains(w.Body.String(), "hash") || strings.Contains(w.Body.String(), "csrf") || strings.Contains(w.Body.String(), "session") {
		t.Fatal("public user list secret disclosure", w.Code, w.Body.String())
	}
	for _, path := range []string{"/api/v1/auth/password-reset/inspect", "/api/v1/auth/password-reset"} {
		body := fmt.Sprintf(`{"token":%q}`, token)
		if path == "/api/v1/auth/password-reset" {
			body = fmt.Sprintf(`{"token":%q,"newPassword":"replacement-test-password"}`, token)
		}
		if w := call("POST", path, body, "", "", "https://evil.example", "application/json"); w.Code != 403 {
			t.Fatal("foreign capability origin accepted", path, w.Code)
		}
		if w := call("POST", path, body, "", "", "https://teamie.example", "application/x-www-form-urlencoded"); w.Code != 415 {
			t.Fatal("form capability request accepted", path, w.Code)
		}
		if w := call("POST", path, strings.TrimSuffix(body, "}")+`,"confirmPassword":"unexpected"}`, "", "", "https://teamie.example", "application/json"); w.Code != 400 {
			t.Fatal("unknown credential field accepted", path, w.Code)
		}
	}
	w = call("POST", "/api/v1/auth/password-reset/inspect", fmt.Sprintf(`{"token":%q}`, token), "", "", "", "application/json; charset=utf-8")
	if w.Code != 200 || w.Header().Get("Cache-Control") != "no-store" || strings.Contains(w.Body.String(), member.User.Email) || strings.Contains(w.Body.String(), "platformRole") || strings.Contains(w.Body.String(), token) {
		t.Fatal("capability inspection privacy/nonbrowser origin", w.Code, w.Body.String())
	}
	// A stale or unrelated cookie is ignored; the capability alone selects A.
	w = call("POST", "/api/v1/auth/password-reset", fmt.Sprintf(`{"token":%q,"newPassword":"replacement-test-password"}`, token), adminToken, "", "https://teamie.example", "application/json")
	if w.Code != 204 || w.Body.Len() != 0 || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("capability redemption", w.Code, w.Body.String())
	}
	if _, err := s.Validate(ctx, memberToken); err == nil {
		t.Fatal("target session survived public capability reset")
	}
	if _, err := s.Validate(ctx, adminToken); err != nil {
		t.Fatal("unrelated current cookie was revoked", err)
	}
	w = call("POST", "/api/v1/auth/password-reset", fmt.Sprintf(`{"token":%q,"newPassword":"replacement-test-password"}`, token), "", "", "https://teamie.example", "application/json")
	if w.Code != 400 || !strings.Contains(w.Body.String(), `"code":"invalid_reset_link"`) {
		t.Fatal("public terminal error", w.Code)
	}
	if _, err := s.ProvisionPrincipal(ctx, "reset-agent", "Agent", "agent", true, false, 1); err != nil {
		t.Fatal(err)
	}
	s.RuntimeToken = "disposable-runtime-service-key"
	agentToken, _, err := s.IssuePrincipalToken(ctx, "reset-agent")
	if err != nil {
		t.Fatal(err)
	}
	for _, method := range []string{"GET", "POST"} {
		path := "/api/v1/auth/admin/users"
		if method == "POST" {
			path = issuePath
		}
		r := httptest.NewRequest(method, path, strings.NewReader(`{}`))
		r.Header.Set("Authorization", "Bearer "+agentToken)
		r.Header.Set("X-Teamie-Service-Key", s.RuntimeToken)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 403 {
			t.Fatal("agent management access", method, w.Code)
		}
	}
}

func TestDelayedPasswordResetResponsePreservesNewerAccountCookie(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	s, admin, adminToken := passwordStore(t)
	other, _ := resetMember(t, s, admin, "newer-reset-login@example.invalid", "Other account")
	_, token := issueReset(t, s, admin, adminToken, admin.User.ID)
	committed, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	h := &Handler{Store: s}
	var routes http.Handler
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/auth/password-reset" {
			routes.ServeHTTP(w, r)
			return
		}
		response := httptest.NewRecorder()
		routes.ServeHTTP(response, r)
		close(committed)
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
	base, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	jar.SetCookies(base, []*http.Cookie{{Name: api.CookieName, Value: adminToken, Path: "/"}})
	client := &http.Client{Jar: jar}
	post := func(path, body string) (*http.Response, error) {
		request, err := http.NewRequestWithContext(ctx, "POST", server.URL+path, strings.NewReader(body))
		if err != nil {
			return nil, err
		}
		request.Header.Set("Origin", server.URL)
		request.Header.Set("Content-Type", "application/json")
		return client.Do(request)
	}
	resetResult := make(chan error, 1)
	go func() {
		response, err := post("/api/v1/auth/password-reset", fmt.Sprintf(`{"token":%q,"newPassword":"replacement-test-password"}`, token))
		if err == nil {
			response.Body.Close()
			if response.StatusCode != 204 {
				err = fmt.Errorf("reset status %d", response.StatusCode)
			}
		}
		resetResult <- err
	}()
	select {
	case <-committed:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	response, err := post("/api/v1/auth/login", `{"email":"newer-reset-login@example.invalid","password":"initial-test-password"}`)
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
	case err = <-resetResult:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	response, err = client.Get(server.URL + "/api/v1/auth/session")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var current api.Session
	if err = json.NewDecoder(response.Body).Decode(&current); err != nil || response.StatusCode != 200 || current.User.ID != other.User.ID {
		t.Fatal("delayed reset replaced newer account cookie", response.StatusCode, err)
	}
}

func TestIdentityV2PasswordResetMigrationPreservesDataAndGatesNewerSchema(t *testing.T) {
	ctx := t.Context()
	url := testdb.URL(t)
	db, err := database.Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err = db.Exec(ctx, schema+schemaV2+`; INSERT INTO identity_schema_migrations(version) VALUES(2);
		INSERT INTO identity_users(id,email,display_name,password_hash,platform_role) VALUES('preserved-human','preserved@example.invalid','Preserved human','preserved-password-hash','admin');
		INSERT INTO identity_sessions(token_hash,user_id,csrf_token,expires_at) VALUES('preserved-session','preserved-human','preserved-csrf',now()+interval '1 day');
		INSERT INTO registration_invites(id,code_hash,created_by,expires_at) VALUES('preserved-invite','preserved-code-hash','preserved-human',now()+interval '1 day');
		INSERT INTO identity_users(id,display_name,kind,enabled) VALUES('preserved-agent','Agent','agent',true);
		INSERT INTO principal_tokens(principal_id,token_hash,expires_at) VALUES('preserved-agent','preserved-principal-hash',now()+interval '1 hour');`); err != nil {
		t.Fatal(err)
	}
	s, err := Open(ctx, url, config.Flags{}, "")
	if err != nil {
		t.Fatal(err)
	}
	defer s.DB.Close()
	if err = s.Migrate(ctx); err != nil {
		t.Fatal("migration not idempotent", err)
	}
	var version, sessions, invites, principals int
	var role, hash string
	if err = s.DB.QueryRow(ctx, `SELECT (SELECT max(version) FROM identity_schema_migrations),platform_role,password_hash,
		(SELECT count(*) FROM identity_sessions WHERE token_hash='preserved-session' AND csrf_token='preserved-csrf'),
		(SELECT count(*) FROM registration_invites WHERE code_hash='preserved-code-hash'),
		(SELECT count(*) FROM principal_tokens WHERE token_hash='preserved-principal-hash')
		FROM identity_users WHERE id='preserved-human'`).Scan(&version, &role, &hash, &sessions, &invites, &principals); err != nil || version != 3 || role != "admin" || hash != "preserved-password-hash" || sessions != 1 || invites != 1 || principals != 1 {
		t.Fatal("schema2 data changed during schema3 upgrade", version, sessions, invites, principals, err)
	}
	if _, err = s.DB.Exec(ctx, "INSERT INTO identity_schema_migrations(version) VALUES(4)"); err != nil {
		t.Fatal(err)
	}
	if err = s.Migrate(ctx); err == nil || !strings.Contains(err.Error(), "unsupported identity_schema_migrations schema version 4") {
		t.Fatal("older binary accepted newer database", err)
	}
}
