package identity

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"teamie/pkg/httpx"
	"teamie/pkg/postgres/testdb"
	"testing"
	"time"
)

func newIdentity(t *testing.T, code string) *Store {
	t.Helper()
	s, e := Open(context.Background(), testdb.URL(t), httpx.Flags{}, code)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(s.DB.Close)
	return s
}
func bootstrap(t *testing.T, s *Store) (httpx.Session, string) {
	t.Helper()
	ss, token, e := s.Register(context.Background(), "admin@example.invalid", "long-test-password", "Administrator", s.BootstrapInviteCode)
	if e != nil {
		t.Fatal(e)
	}
	if ss.User.PlatformRole != "admin" {
		t.Fatal("first account did not become platform admin")
	}
	return ss, token
}
func wantInviteError(t *testing.T, e error) {
	t.Helper()
	var ae *httpx.Error
	if !errors.As(e, &ae) || ae.Status != 403 || ae.Code != "invalid_invite" {
		t.Fatalf("expected consistent invalid_invite403, got %v", e)
	}
}
func TestBootstrapDefaultConfiguredAndFirstOnly(t *testing.T) {
	for _, code := range []string{"", "deployment-custom-bootstrap"} {
		t.Run(code, func(t *testing.T) {
			ctx := context.Background()
			s := newIdentity(t, code)
			if code == "" && s.BootstrapInviteCode != "teamie-bootstrap" {
				t.Fatal("wrong documented default")
			}
			required, e := s.BootstrapRequired(ctx)
			if e != nil || !required {
				t.Fatal("fresh database must require initialization", e)
			}
			bad := "wrong"
			if code != "" {
				bad = DefaultBootstrapInviteCode
			}
			_, _, e = s.Register(ctx, "wrong@example.invalid", "long-test-password", "Wrong", bad)
			wantInviteError(t, e)
			ss, token := bootstrap(t, s)
			if _, e = s.Validate(ctx, token); e != nil {
				t.Fatal(e)
			}
			required, e = s.BootstrapRequired(ctx)
			if e != nil || required {
				t.Fatal("initialized database still requires bootstrap", e)
			}
			_, _, e = s.Register(ctx, "second@example.invalid", "long-test-password", "Second", s.BootstrapInviteCode)
			wantInviteError(t, e)
			h := (&Handler{Store: s, Origin: "https://teamie.example", Secure: true}).Routes()
			w := httptest.NewRecorder()
			h.ServeHTTP(w, httptest.NewRequest("GET", "/api/v1/auth/registration", nil))
			if w.Code != 200 || w.Header().Get("Cache-Control") != "no-store" {
				t.Fatal("public registration metadata", w.Code)
			}
			var metadata map[string]any
			if e = json.Unmarshal(w.Body.Bytes(), &metadata); e != nil || len(metadata) != 2 || metadata["mode"] != "invite" || metadata["bootstrapRequired"] != false {
				t.Fatal("metadata leaked secrets or wrong state", metadata, e)
			}
			current, e := s.Validate(ctx, token)
			if e != nil || current.User.PlatformRole != ss.User.PlatformRole {
				t.Fatal("session lost platform role", e)
			}
		})
	}
}
func TestConcurrentBootstrapCreatesExactlyOneAdmin(t *testing.T) {
	ctx := context.Background()
	s := newIdentity(t, "")
	const workers = 4
	var wg sync.WaitGroup
	start := make(chan struct{})
	results := make(chan httpx.Session, workers)
	failures := make(chan error, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			ss, _, e := s.Register(ctx, fmt.Sprintf("bootstrap-%d@example.invalid", i), "long-test-password", "First", DefaultBootstrapInviteCode)
			if e != nil {
				failures <- e
			} else {
				results <- ss
			}
		}(i)
	}
	close(start)
	wg.Wait()
	close(results)
	close(failures)
	successes := 0
	for ss := range results {
		successes++
		if ss.User.PlatformRole != "admin" {
			t.Fatal("bootstrap winner is not admin")
		}
	}
	if successes != 1 {
		t.Fatalf("bootstrap successes=%d", successes)
	}
	for e := range failures {
		wantInviteError(t, e)
	}
	var users, admins, sessions int
	if e := s.DB.QueryRow(ctx, "SELECT count(*),count(*) FILTER (WHERE platform_role='admin') FROM identity_users").Scan(&users, &admins); e != nil {
		t.Fatal(e)
	}
	if e := s.DB.QueryRow(ctx, "SELECT count(*) FROM identity_sessions").Scan(&sessions); e != nil {
		t.Fatal(e)
	}
	if users != 1 || admins != 1 || sessions != 1 {
		t.Fatal("bootstrap transaction created extra identities", users, admins, sessions)
	}
}
func TestSingleUseInvitationFailureAtomicityAndTerminalStates(t *testing.T) {
	ctx := context.Background()
	s := newIdentity(t, "")
	admin, _ := bootstrap(t, s)
	invite, code, e := s.CreateRegistrationInvite(ctx, admin.User.ID)
	if e != nil {
		t.Fatal(e)
	}
	if len(code) != 64 || invite.ExpiresAt.Sub(invite.CreatedAt) != 7*24*time.Hour {
		t.Fatal("invite entropy/expiry invalid")
	}
	var hash string
	if e = s.DB.QueryRow(ctx, "SELECT code_hash FROM registration_invites WHERE id=$1", invite.ID).Scan(&hash); e != nil || hash == code || hash != httpx.Hash(code) {
		t.Fatal("raw invitation was persisted", e)
	}
	_, _, e = s.Register(ctx, "short@example.invalid", "short", "Short", code)
	if e == nil {
		t.Fatal("invalid registration succeeded")
	}
	_, _, e = s.Register(ctx, "ADMIN@example.invalid", "long-test-password", "Duplicate", code)
	var ae *httpx.Error
	if !errors.As(e, &ae) || ae.Code != "email_in_use" {
		t.Fatal("duplicate email did not fail", e)
	}
	// Force session creation to fail after account insert. No partial user or
	// consumed invitation may survive the transaction rollback.
	if _, e = s.DB.Exec(ctx, "ALTER TABLE identity_sessions ADD CONSTRAINT reject_new_sessions CHECK(false) NOT VALID"); e != nil {
		t.Fatal(e)
	}
	_, _, e = s.Register(ctx, "rolled-back@example.invalid", "long-test-password", "Rollback", code)
	if e == nil {
		t.Fatal("forced session failure was ignored")
	}
	if _, e = s.DB.Exec(ctx, "ALTER TABLE identity_sessions DROP CONSTRAINT reject_new_sessions"); e != nil {
		t.Fatal(e)
	}
	var count int
	if e = s.DB.QueryRow(ctx, "SELECT count(*) FROM identity_users WHERE email='rolled-back@example.invalid'").Scan(&count); e != nil || count != 0 {
		t.Fatal("partial account survived", e)
	}
	list, e := s.RegistrationInvites(ctx, admin.User.ID)
	if e != nil || len(list) != 1 || list[0].UsedAt != nil {
		t.Fatal("failed registration consumed invitation", e)
	}
	member, _, e := s.Register(ctx, "member@example.invalid", "long-test-password", "Member", code)
	if e != nil || member.User.PlatformRole != "user" {
		t.Fatal("ordinary registration", e)
	}
	_, _, e = s.Register(ctx, "reused@example.invalid", "long-test-password", "Reuse", code)
	wantInviteError(t, e)
	if e = s.RevokeRegistrationInvite(ctx, admin.User.ID, invite.ID); e != nil {
		t.Fatal(e)
	}
	list, e = s.RegistrationInvites(ctx, admin.User.ID)
	if e != nil || list[0].UsedAt == nil || list[0].UsedBy == nil || *list[0].UsedBy != member.User.ID || list[0].RevokedAt != nil {
		t.Fatal("used invitation became reusable/revoked", e)
	}
	for _, terminal := range []string{"expired", "revoked"} {
		i, c, e := s.CreateRegistrationInvite(ctx, admin.User.ID)
		if e != nil {
			t.Fatal(e)
		}
		if terminal == "expired" {
			_, e = s.DB.Exec(ctx, "UPDATE registration_invites SET expires_at=now()-interval '1 second' WHERE id=$1", i.ID)
		} else {
			e = s.RevokeRegistrationInvite(ctx, admin.User.ID, i.ID)
			if e == nil {
				e = s.RevokeRegistrationInvite(ctx, admin.User.ID, i.ID)
			}
		}
		if e != nil {
			t.Fatal(e)
		}
		_, _, e = s.Register(ctx, terminal+"@example.invalid", "long-test-password", "Terminal", c)
		wantInviteError(t, e)
	}
	_, _, e = s.Register(ctx, "invalid@example.invalid", "long-test-password", "Invalid", strings.Repeat("f", 64))
	wantInviteError(t, e)
}
func TestConcurrentRegistrationSpendsOneInvitationOnce(t *testing.T) {
	ctx := context.Background()
	s := newIdentity(t, "")
	admin, _ := bootstrap(t, s)
	invite, code, e := s.CreateRegistrationInvite(ctx, admin.User.ID)
	if e != nil {
		t.Fatal(e)
	}
	const workers = 4
	start := make(chan struct{})
	var wg sync.WaitGroup
	results := make(chan httpx.Session, workers)
	failures := make(chan error, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			ss, _, e := s.Register(ctx, fmt.Sprintf("invited-%d@example.invalid", i), "long-test-password", "Invited", code)
			if e != nil {
				failures <- e
			} else {
				results <- ss
			}
		}(i)
	}
	close(start)
	wg.Wait()
	close(results)
	close(failures)
	successes := 0
	winner := ""
	for ss := range results {
		successes++
		winner = ss.User.ID
		if ss.User.PlatformRole != "user" {
			t.Fatal("ordinary invite elevated role")
		}
	}
	if successes != 1 {
		t.Fatalf("single-use invitation successes=%d", successes)
	}
	for e := range failures {
		wantInviteError(t, e)
	}
	var usedBy string
	if e = s.DB.QueryRow(ctx, "SELECT used_by FROM registration_invites WHERE id=$1", invite.ID).Scan(&usedBy); e != nil || usedBy != winner {
		t.Fatal("invitation consumption differs from committed user", e)
	}
	var users, sessions int
	if e = s.DB.QueryRow(ctx, "SELECT count(*) FROM identity_users").Scan(&users); e != nil {
		t.Fatal(e)
	}
	if e = s.DB.QueryRow(ctx, "SELECT count(*) FROM identity_sessions").Scan(&sessions); e != nil {
		t.Fatal(e)
	}
	if users != 2 || sessions != 2 {
		t.Fatal("partial concurrent registration survived", users, sessions)
	}
}
func TestAdminInvitationHTTPAuthorizationAndSecretDisclosure(t *testing.T) {
	ctx := context.Background()
	s := newIdentity(t, "")
	admin, adminToken := bootstrap(t, s)
	_, code, e := s.CreateRegistrationInvite(ctx, admin.User.ID)
	if e != nil {
		t.Fatal(e)
	}
	member, memberToken, e := s.Register(ctx, "member@example.invalid", "long-test-password", "Member", code)
	if e != nil {
		t.Fatal(e)
	}
	h := (&Handler{Store: s, Origin: "https://teamie.example", Secure: true}).Routes()
	call := func(method, path, body, token, csrf, origin string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Origin", origin)
		r.Header.Set("X-CSRF-Token", csrf)
		r.AddCookie(&http.Cookie{Name: httpx.CookieName, Value: token})
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	for _, method := range []string{"GET", "POST", "DELETE"} {
		path := "/api/v1/auth/admin/invites"
		if method == "DELETE" {
			path += "/any-id"
		}
		w := call(method, path, `{}`, memberToken, member.CSRFToken, "https://teamie.example")
		if w.Code != 403 {
			t.Fatalf("non-admin %s allowed: %d %s", method, w.Code, w.Body.String())
		}
	}
	if w := call("POST", "/api/v1/auth/admin/invites", `{}`, adminToken, "", "https://teamie.example"); w.Code != 403 {
		t.Fatal("admin write bypassed CSRF")
	}
	if w := call("POST", "/api/v1/auth/admin/invites", `{}`, adminToken, admin.CSRFToken, "https://evil.example"); w.Code != 403 {
		t.Fatal("admin write bypassed Origin")
	}
	w := call("POST", "/api/v1/auth/admin/invites", `{}`, adminToken, admin.CSRFToken, "https://teamie.example")
	if w.Code != 201 || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("admin create", w.Code, w.Body.String())
	}
	var created struct {
		Invite struct {
			ID   string `json:"id"`
			Code string `json:"code"`
		}
		URL string `json:"url"`
	}
	if e = json.Unmarshal(w.Body.Bytes(), &created); e != nil || len(created.Invite.Code) != 64 || created.URL != "/login?mode=register&inviteCode="+created.Invite.Code {
		t.Fatal("created code/link", e)
	}
	w = call("GET", "/api/v1/auth/admin/invites", "", adminToken, "", "https://teamie.example")
	if w.Code != 200 || w.Header().Get("Cache-Control") != "no-store" || strings.Contains(w.Body.String(), "\"code\"") || strings.Contains(w.Body.String(), "code_hash") || strings.Contains(w.Body.String(), created.Invite.Code) {
		t.Fatal("list leaked invitation secret", w.Body.String())
	}
	w = call("POST", "/api/v1/auth/register", fmt.Sprintf(`{"email":"forged@example.invalid","password":"long-test-password","displayName":"Forged","inviteCode":%q,"platformRole":"admin"}`, created.Invite.Code), "", "", "https://teamie.example")
	if w.Code != 400 {
		t.Fatal("registration accepted caller-selected platform role", w.Code)
	}
	for i := 0; i < 2; i++ {
		w = call("DELETE", "/api/v1/auth/admin/invites/"+created.Invite.ID, "", adminToken, admin.CSRFToken, "https://teamie.example")
		if w.Code != 204 {
			t.Fatal("revocation is not idempotent", w.Code)
		}
	}
	_, _, e = s.Register(ctx, "revoked@example.invalid", "long-test-password", "Revoked", created.Invite.Code)
	wantInviteError(t, e)
}
