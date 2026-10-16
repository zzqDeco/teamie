package identity

import (
	"context"
	"net/http"
	"net/http/httptest"
	"teamie/backend/internal/api"
	"teamie/backend/internal/config"
	"teamie/backend/internal/database"
	"teamie/backend/internal/testdb"
	"teamie/backend/kitex_gen/im"
	"testing"
	"time"
)

func TestPrincipalCredentialScopeRotationAndRevocation(t *testing.T) {
	ctx := context.Background()
	s, e := Open(ctx, testdb.URL(t), config.Flags{}, "bootstrap")
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(s.DB.Close)
	s.ControlToken, s.RuntimeToken = "control-secret", "runtime-secret"
	rpc := &RPC{Store: s}
	id := api.ID()
	req := &im.PrincipalControlRequest{ServiceToken: s.RuntimeToken, PrincipalId: id, DisplayName: "Assistant", Kind: "agent", Enabled: true, Revision: 1}
	if r, e := rpc.ProvisionPrincipal(ctx, req); e != nil || r.Ok {
		t.Fatal("runtime credential managed identity", e)
	}
	req.ServiceToken = s.ControlToken
	if r, e := rpc.ProvisionPrincipal(ctx, req); e != nil || !r.Ok || r.User.Kind != "agent" || r.User.PlatformRole != "user" {
		t.Fatal("provision", r, e)
	}
	if r, e := rpc.IssuePrincipalToken(ctx, &im.PrincipalTokenRequest{ServiceToken: s.ControlToken, PrincipalId: id}); e != nil || r.Ok {
		t.Fatal("control credential minted runtime token", e)
	}
	r, e := rpc.IssuePrincipalToken(ctx, &im.PrincipalTokenRequest{ServiceToken: s.RuntimeToken, PrincipalId: id})
	if e != nil || !r.Ok || len(r.Token) != 64 {
		t.Fatal("issue", r, e)
	}
	expiry, e := time.Parse(time.RFC3339Nano, r.ExpiresAt)
	if e != nil || time.Until(expiry) < 14*time.Minute || time.Until(expiry) > 16*time.Minute {
		t.Fatal("token TTL", expiry, e)
	}
	if _, e = s.Validate(ctx, r.Token); e == nil {
		t.Fatal("machine token accepted as browser session")
	}
	for _, key := range []string{"", s.ControlToken, "wrong"} {
		if _, e = s.ValidatePrincipal(ctx, r.Token, key); e == nil {
			t.Fatal("missing or wrong runtime scope accepted")
		}
	}
	ss, e := s.ValidatePrincipal(ctx, r.Token, s.RuntimeToken)
	if e != nil || !ss.ServicePrincipal || ss.User.ID != id {
		t.Fatal("runtime auth", e)
	}
	h := api.Auth(s, "https://teamie.example", func(w http.ResponseWriter, r *http.Request, s api.Session) error { w.WriteHeader(204); return nil })
	call := func(key, origin string) int {
		req := httptest.NewRequest("POST", "/", nil)
		req.Header.Set("Authorization", "Bearer "+r.Token)
		req.Header.Set("X-Teamie-Service-Key", key)
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		w := httptest.NewRecorder()
		h(w, req)
		return w.Code
	}
	if call("", "") != 401 || call(s.RuntimeToken, "https://teamie.example") != 403 || call(s.RuntimeToken, "") != 204 {
		t.Fatal("machine/browser or dual credential boundary failed")
	}
	second, _, e := s.IssuePrincipalToken(ctx, id)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.ValidatePrincipal(ctx, r.Token, s.RuntimeToken); e == nil {
		t.Fatal("rotation preserved old token")
	}
	if _, e = s.DB.Exec(ctx, "UPDATE principal_tokens SET expires_at=now()-interval '1 second' WHERE principal_id=$1", id); e != nil {
		t.Fatal(e)
	}
	if _, e = s.ValidatePrincipal(ctx, second, s.RuntimeToken); e == nil {
		t.Fatal("expired token accepted")
	}
	third, _, e := s.IssuePrincipalToken(ctx, id)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.ProvisionPrincipal(ctx, id, "Assistant", "agent", false, false, 2); e != nil {
		t.Fatal(e)
	}
	if _, e = s.ValidatePrincipal(ctx, third, s.RuntimeToken); e == nil {
		t.Fatal("disable did not revoke token")
	}
	if _, _, e = s.IssuePrincipalToken(ctx, id); e == nil {
		t.Fatal("disabled principal minted token")
	}
	if _, e = s.ProvisionPrincipal(ctx, id, "Stale", "agent", true, false, 1); e != nil {
		t.Fatal(e)
	}
	if _, _, e = s.IssuePrincipalToken(ctx, id); e == nil {
		t.Fatal("stale revision reenabled principal")
	}
	if _, e = s.ProvisionPrincipal(ctx, id, "Conflict", "agent", true, false, 2); e == nil {
		t.Fatal("same revision conflict accepted")
	}
	if _, e = s.ProvisionPrincipal(ctx, id, "Assistant", "agent", false, true, 3); e != nil {
		t.Fatal(e)
	}
	if _, e = s.ProvisionPrincipal(ctx, id, "Assistant", "agent", true, false, 4); e == nil {
		t.Fatal("deleted principal resurrected")
	}
}

func TestIdentityV1UpgradePreservesHumanSession(t *testing.T) {
	ctx := context.Background()
	url := testdb.URL(t)
	db, e := database.Open(ctx, url)
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	if _, e = db.Exec(ctx, schema); e != nil {
		t.Fatal(e)
	}
	if _, e = db.Exec(ctx, "INSERT INTO identity_users(id,email,display_name,password_hash,platform_role) VALUES('human','human@example.invalid','Existing human','preserved-hash','admin'); INSERT INTO identity_sessions(token_hash,user_id,csrf_token,expires_at) VALUES('hash','human','csrf',now()+interval '1 day')"); e != nil {
		t.Fatal(e)
	}
	s, e := Open(ctx, url, config.Flags{}, "bootstrap")
	if e != nil {
		t.Fatal(e)
	}
	defer s.DB.Close()
	if e = s.Migrate(ctx); e != nil {
		t.Fatal("migration not idempotent", e)
	}
	var count, version int
	if e = s.DB.QueryRow(ctx, "SELECT count(*) FROM identity_sessions WHERE token_hash='hash' AND user_id='human'").Scan(&count); e != nil || count != 1 {
		t.Fatal("existing session lost", e)
	}
	if e = s.DB.QueryRow(ctx, "SELECT max(version) FROM identity_schema_migrations").Scan(&version); e != nil || version != 2 {
		t.Fatal("ledger", version, e)
	}
	if _, e = s.ProvisionPrincipal(ctx, "human", "Hijack", "agent", true, false, 1); e == nil {
		t.Fatal("existing human became machine")
	}
	var name, role, hash string
	if e = s.DB.QueryRow(ctx, "SELECT display_name,platform_role,password_hash FROM identity_users WHERE id='human'").Scan(&name, &role, &hash); e != nil || name != "Existing human" || role != "admin" || hash != "preserved-hash" {
		t.Fatal("existing user changed", e)
	}
}
