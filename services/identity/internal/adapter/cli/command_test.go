package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"teamie/pkg/httpx"
	"teamie/pkg/postgres"
	"teamie/pkg/postgres/testdb"
	"teamie/services/identity/internal/app"
	"teamie/services/identity/internal/domain"
	"teamie/services/identity/internal/infra/crypto"
	"teamie/services/identity/internal/infra/flags"
	"teamie/services/identity/internal/infra/pg"
)

func TestCanonicalRecoveryOrigin(t *testing.T) {
	for _, origin := range []string{"https://teamie.example", "http://127.0.0.1:8080", "https://[::1]:8443"} {
		if got, err := canonicalOrigin(origin); err != nil || got != origin {
			t.Fatal("canonical origin rejected", origin, err)
		}
	}
	for _, origin := range []string{"", "teamie.example", "ftp://teamie.example", "https://", "https://teamie.example/", "https://teamie.example/path", "https://teamie.example?query=1", "https://teamie.example?", "https://teamie.example#token=secret", "https://user:private@teamie.example", "https://TEAMIE.example", " https://teamie.example", "https://teamie.example:0", "https://teamie.example:65536"} {
		if _, err := canonicalOrigin(origin); err == nil {
			t.Fatal("unsafe/noncanonical origin accepted", origin)
		}
	}
}

func TestOperatorResetCommandRejectsBadInputsAndSanitizesErrors(t *testing.T) {
	for _, args := range [][]string{nil, {"password-set"}, {"password-reset-link"}, {"password-reset-link", "--email", "user@example.invalid", "--password", "private-fixture"}, {"password-reset-link", "--email", "user@example.invalid", "extra"}} {
		var out, errOut bytes.Buffer
		if code := Run(t.Context(), args, func(string) string { return "private-fixture" }, &out, &errOut); code != 2 || out.Len() != 0 || strings.Contains(errOut.String(), "private-fixture") {
			t.Fatal("invalid command accepted or echoed input", code, errOut.String())
		}
	}
	for _, fixture := range []struct {
		origin, database, flags string
		message                 string
	}{
		{"https://user:private-fixture@teamie.example", "", "", "APP_ORIGIN"},
		{"https://teamie.example", "", "", "DATABASE_URL is required"},
		{"https://teamie.example", "postgres://operator:private-fixture@[invalid", "", "Identity database could not be opened"},
		{"https://teamie.example", "not-connected", filepath.Join(t.TempDir(), "missing"), "FEATURE_FLAGS_FILE is missing or invalid"},
	} {
		env := map[string]string{"APP_ORIGIN": fixture.origin, "DATABASE_URL": fixture.database, "FEATURE_FLAGS_FILE": fixture.flags}
		var out, errOut bytes.Buffer
		if code := Run(t.Context(), []string{"password-reset-link", "--email", "user@example.invalid"}, func(key string) string { return env[key] }, &out, &errOut); code != 1 || out.Len() != 0 || strings.Contains(errOut.String(), "private-fixture") || !strings.Contains(errOut.String(), fixture.message) {
			t.Fatal("operator errors disclosed connection material", code, errOut.String())
		}
	}
}

func TestOperatorResetCommandRecoversHumanAndAdminWithoutChangingPrivileges(t *testing.T) {
	ctx := context.Background()
	databaseURL := testdb.URL(t)
	pool, err := postgres.Open(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err = pg.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	repo := pg.New(postgres.New(pool))
	svc := &app.Service{
		Tx:                  repo,
		Users:               repo,
		Sessions:            repo,
		Invites:             repo,
		PasswordResets:      repo,
		Principals:          repo,
		Passwords:           crypto.BcryptHasher{},
		Tokens:              crypto.TokenGen{},
		Flags:               flags.New(httpx.Flags{}),
		BootstrapInviteCode: "operator-test-bootstrap",
	}
	admin, adminToken, err := svc.Register(ctx, "admin@example.invalid", "initial-test-password", "Admin", "operator-test-bootstrap")
	if err != nil {
		t.Fatal(err)
	}
	_, code, err := svc.CreateRegistrationInvite(ctx, admin.User.ID)
	if err != nil {
		t.Fatal(err)
	}
	member, memberToken, err := svc.Register(ctx, "member@example.invalid", "initial-test-password", "Member", code)
	if err != nil {
		t.Fatal(err)
	}
	env := map[string]string{"APP_ORIGIN": "https://teamie.example", "DATABASE_URL": databaseURL}
	for _, user := range []domain.Session{member, admin} {
		var out, errOut bytes.Buffer
		if code := Run(ctx, []string{"password-reset-link", "--email", strings.ToUpper(user.User.Email)}, func(key string) string { return env[key] }, &out, &errOut); code != 0 || errOut.Len() != 0 {
			t.Fatal("operator failed to recover account", user.User.PlatformRole, code, errOut.String())
		}
		var output map[string]json.RawMessage
		if err := json.Unmarshal(out.Bytes(), &output); err != nil || len(output) != 2 || output["url"] == nil || output["expiresAt"] == nil {
			t.Fatal("operator output must contain only intentional URL+expiry", err)
		}
		var link string
		var expiry time.Time
		if err := json.Unmarshal(output["url"], &link); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(output["expiresAt"], &expiry); err != nil || expiry.Before(time.Now()) {
			t.Fatal("invalid expiry", err)
		}
		token := strings.TrimPrefix(link, "https://teamie.example/reset-password#token=")
		if len(token) != 64 {
			t.Fatal("operator URL is not a fragment capability")
		}
		info, err := svc.InspectPasswordReset(ctx, token)
		if err != nil || info.Account.ID != user.User.ID {
			t.Fatal("operator recovered wrong account", err)
		}
		var humanSessions int
		var creator *string
		if err = pool.QueryRow(ctx, "SELECT (SELECT count(*) FROM identity_sessions WHERE user_id=$1),created_by FROM identity_password_resets WHERE token_hash=$2", user.User.ID, httpx.Hash(token)).Scan(&humanSessions, &creator); err != nil || humanSessions != 1 || creator != nil {
			t.Fatal("operator issuance revoked session or impersonated administrator", err)
		}
		if err = svc.ResetPassword(ctx, token, "operator-recovered-password"); err != nil {
			t.Fatal(err)
		}
		current, _, err := svc.Login(ctx, user.User.Email, "operator-recovered-password")
		if err != nil || current.User.ID != user.User.ID || current.User.PlatformRole != user.User.PlatformRole {
			t.Fatal("operator reset changed identity or privilege", err)
		}
	}
	for _, token := range []string{adminToken, memberToken} {
		if _, err := svc.Validate(ctx, token); err == nil {
			t.Fatal("reset retained old session")
		}
	}
	var out, errOut bytes.Buffer
	if code := Run(ctx, []string{"password-reset-link", "--email", "missing@example.invalid"}, func(key string) string { return env[key] }, &out, &errOut); code != 1 || out.Len() != 0 || !strings.Contains(errOut.String(), "account is unavailable") {
		t.Fatal("operator fabricated missing account", code, errOut.String())
	}
	path := filepath.Join(t.TempDir(), "flags.json")
	if err := os.WriteFile(path, []byte(`{"revision":"operator-reset-off","flags":{"auth.password_reset":false}}`), 0600); err != nil {
		t.Fatal(err)
	}
	env["FEATURE_FLAGS_FILE"] = path
	out.Reset()
	errOut.Reset()
	if code := Run(ctx, []string{"password-reset-link", "--email", admin.User.Email}, func(key string) string { return env[key] }, &out, &errOut); code != 1 || out.Len() != 0 || !strings.Contains(errOut.String(), "generation is disabled") {
		t.Fatal("operator ignored issuance kill switch", code, errOut.String())
	}
}
