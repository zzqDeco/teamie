package marketplace

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"teamie/pkg/httpx"
	"teamie/pkg/postgres"
	"teamie/pkg/postgres/testdb"
	httpadapter "teamie/services/marketplace/internal/adapter/http"
	"teamie/services/marketplace/internal/app"
	"teamie/services/marketplace/internal/domain"
	"teamie/services/marketplace/internal/infra/pg"
	"teamie/services/marketplace/internal/infra/probe"
)

func TestMarketplaceDomainURLSecurityAndTokens(t *testing.T) {
	origin := "https://chat.teamie.example"

	// Same-origin Web URL is always rejected.
	if err := domain.ValidateMarketplaceEndpointURL("https://chat.teamie.example/app", false, origin, true); err == nil {
		t.Fatal("expected same-origin webUrl to be rejected")
	}

	// Production HTTPS + SSRF internal hostname blocklist (including "marketplace").
	for _, bad := range []string{
		"http://ext-app.example.com/ui",
		"https://marketplace:8088/healthz",
		"https://chat:8082/api/v1/bootstrap",
		"https://identity:8081/readyz",
		"https://localhost:3000/ui",
		"https://127.0.0.1/ui",
		"https://10.0.0.8/ui",
		"https://100.64.0.1/ui",
		"https://[fe80::1]/ui",
		"https://svc.internal/ui",
	} {
		if err := domain.ValidateMarketplaceEndpointURL(bad, false, origin, true); err == nil {
			t.Fatalf("expected %q to be rejected under requirePublicHTTPS=true", bad)
		}
	}

	// MCP URL with fragment or credentials must be rejected.
	if err := domain.ValidateMarketplaceEndpointURL("https://mcp.ext-app.example/mcp#frag", true, origin, true); err == nil {
		t.Fatal("expected MCP URL with fragment to be rejected")
	}
	if err := domain.ValidateMarketplaceEndpointURL("https://user:pass@mcp.ext-app.example/mcp", true, origin, true); err == nil {
		t.Fatal("expected MCP URL with credentials to be rejected")
	}

	// Valid external HTTPS endpoints pass.
	if err := domain.ValidateMarketplaceEndpointURL("https://ui.ext-app.example/panel", false, origin, true); err != nil {
		t.Fatalf("unexpected error for valid webUrl: %v", err)
	}
	if err := domain.ValidateMarketplaceEndpointURL("https://mcp.ext-app.example/mcp", true, origin, true); err != nil {
		t.Fatalf("unexpected error for valid mcpUrl: %v", err)
	}

	// Symmetric HS256 Context Token round-trip & expiry.
	secret := domain.NewMarketplaceSigningSecret()
	now := time.Now().UTC().Truncate(time.Second)
	claims := domain.MarketplaceContextClaims{
		Issuer:         "teamie",
		Audience:       "app-1",
		Subject:        "user-1",
		AppID:          "app-1",
		Slug:           "werewolf",
		WorkspaceID:    "ws-1",
		ConversationID: "conv-1",
		UserID:         "user-1",
		DisplayName:    "Alice",
		IssuedAt:       now.Unix(),
		ExpiresAt:      now.Add(domain.MarketplaceSessionTTL).Unix(),
	}
	tok, err := domain.SignMarketplaceContextToken(secret, claims)
	if err != nil {
		t.Fatal(err)
	}
	got, err := domain.VerifyMarketplaceContextToken(secret, tok, now.Add(5*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if got.ConversationID != "conv-1" || got.Subject != "user-1" || got.Slug != "werewolf" {
		t.Fatalf("unexpected claims: %+v", got)
	}
	if _, err := domain.VerifyMarketplaceContextToken("wrong_"+secret, tok, now); err == nil {
		t.Fatal("expected wrong secret to fail verification")
	}
	if _, err := domain.VerifyMarketplaceContextToken(secret, tok, now.Add(2*time.Hour)); err == nil {
		t.Fatal("expected expired token to fail verification")
	}
}

func startMockExternalApp(t *testing.T, secretFn func() string, sseMode bool) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var deletedSessions atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/ui", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte("<!doctype html><html><body>Board Game UI</body></html>"))
	})
	mux.HandleFunc("/mcp", func(w http.ResponseWriter, r *http.Request) {
		auth := strings.TrimSpace(r.Header.Get("Authorization"))
		if !strings.HasPrefix(auth, "Bearer ") {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		tok := strings.TrimSpace(strings.TrimPrefix(auth, "Bearer "))
		if _, err := domain.VerifyMarketplaceContextToken(secretFn(), tok, time.Now().UTC()); err != nil {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		if r.Method == http.MethodDelete {
			if r.Header.Get("Mcp-Session-Id") == "sess-probe-1" {
				deletedSessions.Add(1)
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}
		var rpcReq struct {
			ID     any    `json:"id"`
			Method string `json:"method"`
		}
		_ = json.NewDecoder(r.Body).Decode(&rpcReq)
		writeJSONRPC := func(payload string) {
			w.Header().Set("Mcp-Session-Id", "sess-probe-1")
			if sseMode {
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = w.Write([]byte("event: message\ndata: " + payload + "\n\n"))
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(payload))
		}
		switch rpcReq.Method {
		case "initialize":
			writeJSONRPC(`{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2025-03-26","capabilities":{"tools":{}},"serverInfo":{"name":"werewolf-mcp","version":"1.2.0"}}}`)
		case "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
		case "tools/list":
			writeJSONRPC(`{"jsonrpc":"2.0","id":2,"result":{"tools":[{"name":"cast_vote","description":"Vote a player","inputSchema":{"type":"object","properties":{"seat":{"type":"integer"}}}}]}}`)
		default:
			w.WriteHeader(http.StatusBadRequest)
		}
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, &deletedSessions
}

func TestProbeVerifierFourStagesAndDNSRebindingDefense(t *testing.T) {
	ctx := context.Background()
	secret := domain.NewMarketplaceSigningSecret()

	for _, sse := range []bool{false, true} {
		extSrv, deleted := startMockExternalApp(t, func() string { return secret }, sse)
		v := &probe.Verifier{
			AppOrigin:          "http://teamie.local:8080",
			RequirePublicHTTPS: false,
			Timeout:            5 * time.Second,
		}
		rep, err := v.VerifyEndpoints(ctx, domain.VerifyEndpointsInput{
			AppID:         "app-123",
			Slug:          "werewolf",
			WebURL:        extSrv.URL + "/ui",
			MCPURL:        extSrv.URL + "/mcp",
			SigningSecret: secret,
		})
		if err != nil {
			t.Fatal(err)
		}
		if !rep.OK || len(rep.Checks) != 4 || len(rep.Tools) != 1 || rep.Tools[0].ExposedName != "mcp__werewolf__cast_vote" {
			t.Fatalf("expected 4-stage probe pass (sse=%v), got %+v", sse, rep)
		}
		if deleted.Load() != 1 {
			t.Fatalf("expected probe to DELETE Mcp-Session-Id cleanly, got %d", deleted.Load())
		}
	}

	// DNS-rebinding defense when RequirePublicHTTPS=true: hostname passes URL check,
	// but DNS resolves to private 10.1.2.3 -> blocked before socket connect.
	vProd := &probe.Verifier{
		AppOrigin:          "https://teamie.example.com",
		RequirePublicHTTPS: true,
		Timeout:            3 * time.Second,
		Resolver: func(context.Context, string) ([]netip.Addr, error) {
			return []netip.Addr{netip.MustParseAddr("10.1.2.3")}, nil
		},
	}
	repRebind, err := vProd.VerifyEndpoints(ctx, domain.VerifyEndpointsInput{
		AppID:         "app-rebind",
		Slug:          "evil",
		WebURL:        "https://rebind.attacker.example/ui",
		MCPURL:        "https://rebind.attacker.example/mcp",
		SigningSecret: secret,
	})
	if err != nil {
		t.Fatal(err)
	}
	if repRebind.OK || len(repRebind.Checks) < 2 || repRebind.Checks[1].Stage != "web_ui" || repRebind.Checks[1].OK {
		t.Fatalf("expected DNS rebinding to private IP to fail web_ui probe, got %+v", repRebind)
	}
}

type fakeIdentity struct {
	sessions   map[string]httpx.Session
	principals map[string]httpx.Session
	users      map[string]httpx.User
}

func (f *fakeIdentity) Validate(_ context.Context, token string) (httpx.Session, error) {
	if s, ok := f.sessions[token]; ok {
		return s, nil
	}
	return httpx.Session{}, httpx.Err(401, "unauthenticated", "Sign in")
}

func (f *fakeIdentity) ValidatePrincipal(_ context.Context, token, key string) (httpx.Session, error) {
	if key == "svc-key" {
		if s, ok := f.principals[token]; ok {
			return s, nil
		}
	}
	return httpx.Session{}, httpx.Err(401, "unauthenticated", "Invalid principal")
}

func (f *fakeIdentity) Users(_ context.Context, ids []string) ([]httpx.User, error) {
	out := make([]httpx.User, 0, len(ids))
	for _, id := range ids {
		if u, ok := f.users[id]; ok {
			out = append(out, u)
		}
	}
	return out, nil
}

func (f *fakeIdentity) Ping(context.Context) error { return nil }

type fakeChatScope struct {
	workspaces    map[string]map[string]domain.WorkspaceScope
	conversations map[string]map[string]domain.ConversationScope
}

func (f *fakeChatScope) WorkspaceRole(_ context.Context, wsID, userID string) (domain.WorkspaceScope, error) {
	if byUser, ok := f.workspaces[wsID]; ok {
		if sc, ok2 := byUser[userID]; ok2 {
			return sc, nil
		}
	}
	return domain.WorkspaceScope{Allowed: false}, nil
}

func (f *fakeChatScope) ConversationScope(_ context.Context, convID, userID string) (domain.ConversationScope, error) {
	if byUser, ok := f.conversations[convID]; ok {
		if sc, ok2 := byUser[userID]; ok2 {
			return sc, nil
		}
	}
	return domain.ConversationScope{Allowed: false}, nil
}

func (f *fakeChatScope) Ping(context.Context) error { return nil }

type toggleFlags struct {
	enabled atomic.Bool
}

func (f *toggleFlags) Enabled(key string) bool {
	if key == "im.marketplace.enabled" {
		return f.enabled.Load()
	}
	return true
}

func TestMarketplaceEndToEndLifecycleAndIsolation(t *testing.T) {
	ctx := context.Background()
	pool, err := postgres.Open(ctx, testdb.URL(t))
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	if err := pg.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	var migVersion int
	if err := pool.QueryRow(ctx, "SELECT COALESCE(MAX(version), 0) FROM marketplace_schema_migrations").Scan(&migVersion); err != nil || migVersion != 1 {
		t.Fatalf("expected marketplace_schema_migrations version 1, got %d (%v)", migVersion, err)
	}

	adminUser := httpx.User{ID: domain.NewID(), Email: "admin@example.com", DisplayName: "Admin", Kind: "human", PlatformRole: "admin", CreatedAt: time.Now().UTC()}
	devUser := httpx.User{ID: domain.NewID(), Email: "dev@example.com", DisplayName: "Dev", Kind: "human", PlatformRole: "member", CreatedAt: time.Now().UTC()}
	otherOwner := httpx.User{ID: domain.NewID(), Email: "bob@example.com", DisplayName: "Bob", Kind: "human", PlatformRole: "member", CreatedAt: time.Now().UTC()}
	agentPrincipal := httpx.User{ID: domain.NewID(), DisplayName: "WerewolfBot", Kind: "agent", PlatformRole: "member", CreatedAt: time.Now().UTC()}

	adminTok := strings.Repeat("a", 64)
	devTok := strings.Repeat("d", 64)
	bobTok := strings.Repeat("b", 64)
	agentTok := "agent-principal-token"

	idMock := &fakeIdentity{
		sessions: map[string]httpx.Session{
			adminTok: {User: adminUser, CSRFToken: "csrf-admin"},
			devTok:   {User: devUser, CSRFToken: "csrf-dev"},
			bobTok:   {User: otherOwner, CSRFToken: "csrf-bob"},
		},
		principals: map[string]httpx.Session{
			agentTok: {User: agentPrincipal, ServicePrincipal: true},
		},
		users: map[string]httpx.User{
			adminUser.ID:      adminUser,
			devUser.ID:        devUser,
			otherOwner.ID:     otherOwner,
			agentPrincipal.ID: agentPrincipal,
		},
	}

	devWS := domain.NewID()
	bobWS := domain.NewID()
	devGroup := domain.NewID()
	bobGroup := domain.NewID()
	bobChannel := domain.NewID()

	chatMock := &fakeChatScope{
		workspaces: map[string]map[string]domain.WorkspaceScope{
			devWS: {
				devUser.ID:        {Allowed: true, OwnerID: devUser.ID, Role: "owner"},
				agentPrincipal.ID: {Allowed: true, OwnerID: devUser.ID, Role: "member"},
			},
			bobWS: {
				otherOwner.ID:     {Allowed: true, OwnerID: otherOwner.ID, Role: "owner"},
				agentPrincipal.ID: {Allowed: true, OwnerID: otherOwner.ID, Role: "member"},
			},
		},
		conversations: map[string]map[string]domain.ConversationScope{
			devGroup: {
				devUser.ID:        {Allowed: true, WorkspaceID: devWS, WorkspaceOwnerID: devUser.ID, ConversationType: "group"},
				agentPrincipal.ID: {Allowed: true, WorkspaceID: devWS, WorkspaceOwnerID: devUser.ID, ConversationType: "group"},
			},
			bobGroup: {
				otherOwner.ID:     {Allowed: true, WorkspaceID: bobWS, WorkspaceOwnerID: otherOwner.ID, ConversationType: "group"},
				agentPrincipal.ID: {Allowed: true, WorkspaceID: bobWS, WorkspaceOwnerID: otherOwner.ID, ConversationType: "group"},
			},
			bobChannel: {
				otherOwner.ID: {Allowed: true, WorkspaceID: bobWS, WorkspaceOwnerID: otherOwner.ID, ConversationType: "channel"},
			},
		},
	}

	ff := &toggleFlags{}
	ff.enabled.Store(true)
	origin := "http://teamie.local:8080"
	reqHTTPS := false
	repo := pg.New(postgres.New(pool))
	svc := &app.Service{
		Tx:                 repo,
		Marketplace:        repo,
		Identity:           domainIdentityFromFake{idMock},
		Chat:               chatMock,
		Flags:              ff,
		AppOrigin:          origin,
		RequirePublicHTTPS: &reqHTTPS,
		Verifier: &probe.Verifier{
			AppOrigin:          origin,
			RequirePublicHTTPS: false,
			Timeout:            5 * time.Second,
		},
	}
	srv := httptest.NewServer((&httpadapter.Adapter{
		App:    svc,
		Auth:   idMock,
		Origin: origin,
	}).Routes())
	defer srv.Close()

	doHuman := func(tok, csrf, method, path string, body any) (int, map[string]any) {
		t.Helper()
		var rdr *bytes.Reader
		if body != nil {
			b, _ := json.Marshal(body)
			rdr = bytes.NewReader(b)
		} else {
			rdr = bytes.NewReader(nil)
		}
		req, err := http.NewRequest(method, srv.URL+path, rdr)
		if err != nil {
			t.Fatal(err)
		}
		req.AddCookie(&http.Cookie{Name: "teamie_session", Value: tok})
		if method != http.MethodGet && method != http.MethodHead {
			req.Header.Set("Origin", origin)
			req.Header.Set("X-CSRF-Token", csrf)
			req.Header.Set("Content-Type", "application/json")
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		var out map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&out)
		return resp.StatusCode, out
	}

	doAgent := func(method, path string, body any) (int, map[string]any) {
		t.Helper()
		var rdr *bytes.Reader
		if body != nil {
			b, _ := json.Marshal(body)
			rdr = bytes.NewReader(b)
		} else {
			rdr = bytes.NewReader(nil)
		}
		req, err := http.NewRequest(method, srv.URL+path, rdr)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+agentTok)
		req.Header.Set("X-Teamie-Service-Key", "svc-key")
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		var out map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&out)
		return resp.StatusCode, out
	}

	// 1. Operational /readyz check passes.
	readyResp, err := http.Get(srv.URL + "/readyz")
	if err != nil || readyResp.StatusCode != 200 {
		t.Fatalf("expected /readyz 200, got %v %v", readyResp, err)
	}
	_ = readyResp.Body.Close()

	// 2. Developer creates draft app without URLs first, receives signingSecret.
	var activeSecret atomic.Value
	status, created := doHuman(devTok, "csrf-dev", http.MethodPost, "/api/v1/developer/apps", map[string]any{
		"slug":        "Werewolf",
		"name":        "Werewolf Referee",
		"description": "Group deduction game",
	})
	if status != http.StatusCreated {
		t.Fatalf("create draft failed: %d %+v", status, created)
	}
	signingSecret := created["signingSecret"].(string)
	activeSecret.Store("misconfigured-secret")
	appObj := created["app"].(map[string]any)
	if appObj["slug"] != "werewolf" || appObj["hasDraftChanges"] != true {
		t.Fatalf("expected normalized lowercase slug and hasDraftChanges=true on new draft, got %+v", appObj)
	}
	appID := appObj["id"].(string)

	// 3. Deploy external Web + MCP server, update draft URLs.
	extSrv, _ := startMockExternalApp(t, func() string { return activeSecret.Load().(string) }, false)
	status, _ = doHuman(devTok, "csrf-dev", http.MethodPatch, "/api/v1/developer/apps/"+appID, map[string]any{
		"webUrl": extSrv.URL + "/ui",
		"mcpUrl": extSrv.URL + "/mcp",
	})
	if status != http.StatusOK {
		t.Fatalf("expected 200 updating draft URLs, got %d", status)
	}

	// 4. Before publication: developer can install and test draft in own workspace, but Bob cannot!
	status, installOwn := doHuman(devTok, "csrf-dev", http.MethodPost, "/api/v1/workspaces/"+devWS+"/apps", map[string]any{"appId": appID})
	if status != http.StatusCreated || installOwn["app"].(map[string]any)["servingDraft"] != true {
		t.Fatalf("expected developer own-workspace draft install to succeed with servingDraft=true: %d %+v", status, installOwn)
	}
	status, _ = doHuman(bobTok, "csrf-bob", http.MethodPost, "/api/v1/workspaces/"+bobWS+"/apps", map[string]any{"appId": appID})
	if status != http.StatusNotFound {
		t.Fatalf("expected unpublished draft to be unavailable to Bob's workspace (404), got %d", status)
	}

	// 5. Submit when live endpoint fails protocol verification returns 422; after fixing secret, live /verify + immediate /submit succeed.
	status, _ = doHuman(devTok, "csrf-dev", http.MethodPost, "/api/v1/developer/apps/"+appID+"/submit", map[string]any{"note": "Initial release"})
	if status != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422 submitting broken live draft, got %d", status)
	}
	activeSecret.Store(signingSecret)
	status, verOut := doHuman(devTok, "csrf-dev", http.MethodPost, "/api/v1/developer/apps/"+appID+"/verify", map[string]any{})
	if status != http.StatusOK || verOut["verification"].(map[string]any)["ok"] != true {
		t.Fatalf("expected 200 ok verification, got %d %+v", status, verOut)
	}
	status, subOut := doHuman(devTok, "csrf-dev", http.MethodPost, "/api/v1/developer/apps/"+appID+"/submit", map[string]any{"note": "Ready for v1"})
	if status != http.StatusOK || subOut["app"].(map[string]any)["reviewStatus"] != "pending_review" {
		t.Fatalf("expected pending_review after submit, got %d %+v", status, subOut)
	}

	// 6. Admin approves revision 1 -> published v1 visible in catalog & Bob's workspace & group sessions!
	status, _ = doHuman(adminTok, "csrf-admin", http.MethodPost, "/api/v1/admin/marketplace/apps/"+appID+"/approve", map[string]any{
		"expectedRevision": 99,
	})
	if status != http.StatusConflict {
		t.Fatalf("expected 409 on wrong expectedRevision, got %d", status)
	}
	status, appOut := doHuman(adminTok, "csrf-admin", http.MethodPost, "/api/v1/admin/marketplace/apps/"+appID+"/approve", map[string]any{
		"expectedRevision": 1,
		"note":             "LGTM",
	})
	if status != http.StatusOK || appOut["app"].(map[string]any)["isPublished"] != true {
		t.Fatalf("expected published v1 after approve, got %d %+v", status, appOut)
	}

	status, _ = doHuman(bobTok, "csrf-bob", http.MethodPost, "/api/v1/workspaces/"+bobWS+"/apps", map[string]any{"appId": appID})
	if status != http.StatusCreated {
		t.Fatalf("expected Bob to install published v1 app, got %d", status)
	}

	// Channel conversation must return 403 marketplace_group_only; Agent principal cannot browse public catalog.
	status, agentCatOut := doAgent(http.MethodGet, "/api/v1/marketplace/catalog", nil)
	if status != http.StatusForbidden {
		t.Fatalf("expected 403 when Agent principal browses human catalog, got %d %+v", status, agentCatOut)
	}
	status, chanOut := doHuman(bobTok, "csrf-bob", http.MethodGet, "/api/v1/conversations/"+bobChannel+"/apps", nil)
	if status != http.StatusForbidden || chanOut["error"].(map[string]any)["code"] != "marketplace_group_only" {
		t.Fatalf("expected 403 marketplace_group_only on channel, got %d %+v", status, chanOut)
	}

	// Both human and Agent principal in Bob's group receive valid symmetric HS256 Context Tokens!
	status, humanSess := doHuman(bobTok, "csrf-bob", http.MethodPost, "/api/v1/conversations/"+bobGroup+"/apps/"+appID+"/session", map[string]any{})
	if status != http.StatusOK {
		t.Fatalf("expected 200 human group session, got %d %+v", status, humanSess)
	}
	status, agentSess := doAgent(http.MethodPost, "/api/v1/conversations/"+bobGroup+"/apps/"+appID+"/session", map[string]any{})
	if status != http.StatusOK {
		t.Fatalf("expected 200 agent group session, got %d %+v", status, agentSess)
	}
	agentClaims, err := domain.VerifyMarketplaceContextToken(signingSecret, agentSess["token"].(string), time.Now().UTC())
	if err != nil || agentClaims.Subject != agentPrincipal.ID || agentClaims.ConversationID != bobGroup || agentClaims.WorkspaceID != bobWS {
		t.Fatalf("unexpected agent context token claims: %+v (%v)", agentClaims, err)
	}

	// 7. Disabling im.marketplace.enabled blocks writes/sessions with 403 feature_disabled while preserving persisted reads!
	ff.enabled.Store(false)
	status, _ = doAgent(http.MethodPost, "/api/v1/conversations/"+bobGroup+"/apps/"+appID+"/session", map[string]any{})
	if status != http.StatusForbidden {
		t.Fatalf("expected 403 feature_disabled on session mint when flag disabled, got %d", status)
	}
	status, listOut := doAgent(http.MethodGet, "/api/v1/conversations/"+bobGroup+"/apps", nil)
	if status != http.StatusOK || len(listOut["apps"].([]any)) != 1 {
		t.Fatalf("expected persisted apps read to stay available when write flag disabled, got %d %+v", status, listOut)
	}
}

type domainIdentityFromFake struct {
	id *fakeIdentity
}

func (d domainIdentityFromFake) Users(ctx context.Context, ids []string) ([]domain.User, error) {
	us, err := d.id.Users(ctx, ids)
	if err != nil {
		return nil, err
	}
	out := make([]domain.User, 0, len(us))
	for _, u := range us {
		out = append(out, domain.User{
			ID:           u.ID,
			Email:        u.Email,
			DisplayName:  u.DisplayName,
			Kind:         u.Kind,
			PlatformRole: u.PlatformRole,
			CreatedAt:    u.CreatedAt,
		})
	}
	return out, nil
}

func (d domainIdentityFromFake) Ping(ctx context.Context) error { return d.id.Ping(ctx) }

func TestProbeVerifierCSPFrameAncestorsHostSourceMatching(t *testing.T) {
	ctx := context.Background()
	secret := domain.NewMarketplaceSigningSecret()

	var currentCSP atomic.Value
	currentCSP.Store("")

	mux := http.NewServeMux()
	mux.HandleFunc("/ui", func(w http.ResponseWriter, _ *http.Request) {
		if csp, _ := currentCSP.Load().(string); csp != "" {
			w.Header().Set("Content-Security-Policy", csp)
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte("<!doctype html><html><body>CSP Test</body></html>"))
	})
	mux.HandleFunc("/mcp", func(w http.ResponseWriter, r *http.Request) {
		auth := strings.TrimSpace(r.Header.Get("Authorization"))
		if !strings.HasPrefix(auth, "Bearer ") {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		tok := strings.TrimSpace(strings.TrimPrefix(auth, "Bearer "))
		if _, err := domain.VerifyMarketplaceContextToken(secret, tok, time.Now().UTC()); err != nil {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		if r.Method == http.MethodDelete {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		var rpcReq struct {
			Method string `json:"method"`
		}
		_ = json.NewDecoder(r.Body).Decode(&rpcReq)
		w.Header().Set("Content-Type", "application/json")
		switch rpcReq.Method {
		case "initialize":
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2025-03-26","capabilities":{"tools":{}},"serverInfo":{"name":"csp-mcp","version":"1.0.0"}}}`))
		case "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
		case "tools/list":
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":2,"result":{"tools":[{"name":"ping","description":"Ping"}]}}`))
		default:
			w.WriteHeader(http.StatusBadRequest)
		}
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	cases := []struct {
		name      string
		appOrigin string
		csp       string
		wantOK    bool
	}{
		{name: "exact https origin", appOrigin: "https://chat.example.com", csp: "frame-ancestors 'self' https://chat.example.com", wantOK: true},
		{name: "wildcard subdomain with scheme", appOrigin: "https://chat.example.com", csp: "frame-ancestors https://*.example.com", wantOK: true},
		{name: "scheme-less wildcard subdomain", appOrigin: "https://chat.example.com", csp: "frame-ancestors *.example.com", wantOK: true},
		{name: "scheme-less exact host", appOrigin: "https://chat.example.com", csp: "frame-ancestors chat.example.com", wantOK: true},
		{name: "wildcard port on non-default port", appOrigin: "https://chat.example.com:8443", csp: "frame-ancestors https://*.example.com:*", wantOK: true},
		{name: "explicit port match", appOrigin: "https://chat.example.com:8443", csp: "frame-ancestors https://chat.example.com:8443", wantOK: true},
		{name: "http source upgrades to https origin", appOrigin: "https://chat.example.com", csp: "frame-ancestors http://*.example.com", wantOK: true},
		{name: "wildcard subdomain rejects apex domain", appOrigin: "https://example.com", csp: "frame-ancestors https://*.example.com", wantOK: false},
		{name: "default port rejects custom port", appOrigin: "https://chat.example.com:8443", csp: "frame-ancestors https://*.example.com", wantOK: false},
		{name: "https scheme rejects http origin", appOrigin: "http://chat.example.com", csp: "frame-ancestors https://*.example.com", wantOK: false},
		{name: "self only blocks cross-origin", appOrigin: "https://chat.example.com", csp: "frame-ancestors 'self'", wantOK: false},
		{name: "none blocks all", appOrigin: "https://chat.example.com", csp: "frame-ancestors 'none'", wantOK: false},
		{name: "empty frame-ancestors blocks all", appOrigin: "https://chat.example.com", csp: "default-src 'self'; frame-ancestors", wantOK: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			currentCSP.Store(tc.csp)
			v := &probe.Verifier{
				AppOrigin:          tc.appOrigin,
				RequirePublicHTTPS: false,
				Timeout:            5 * time.Second,
			}
			rep, err := v.VerifyEndpoints(ctx, domain.VerifyEndpointsInput{
				AppID:         "app-csp",
				Slug:          "csp-app",
				WebURL:        srv.URL + "/ui",
				MCPURL:        srv.URL + "/mcp",
				SigningSecret: secret,
			})
			if err != nil {
				t.Fatal(err)
			}
			if rep.OK != tc.wantOK {
				t.Fatalf("csp=%q appOrigin=%q: got OK=%v (checks=%+v), want OK=%v", tc.csp, tc.appOrigin, rep.OK, rep.Checks, tc.wantOK)
			}
			if !tc.wantOK && (len(rep.Checks) < 2 || rep.Checks[1].Code != "web_ui_csp_frame_blocked") {
				t.Fatalf("csp=%q appOrigin=%q: expected web_ui_csp_frame_blocked, got checks=%+v", tc.csp, tc.appOrigin, rep.Checks)
			}
		})
	}
}

func TestWebEndpointHTMLValidationRegression(t *testing.T) {
	ctx := context.Background()
	secret := domain.NewMarketplaceSigningSecret()

	type webResp struct {
		status      int
		contentType string
		disposition string
		body        string
	}
	var current atomic.Value

	mux := http.NewServeMux()
	mux.HandleFunc("/ui", func(w http.ResponseWriter, _ *http.Request) {
		cfg := current.Load().(webResp)
		if cfg.contentType != "" {
			w.Header().Set("Content-Type", cfg.contentType)
		} else {
			w.Header()["Content-Type"] = nil
		}
		if cfg.disposition != "" {
			w.Header().Set("Content-Disposition", cfg.disposition)
		}
		w.WriteHeader(cfg.status)
		if cfg.body != "" {
			_, _ = w.Write([]byte(cfg.body))
		}
	})
	mux.HandleFunc("/mcp", func(w http.ResponseWriter, r *http.Request) {
		auth := strings.TrimSpace(r.Header.Get("Authorization"))
		if !strings.HasPrefix(auth, "Bearer ") {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		tok := strings.TrimSpace(strings.TrimPrefix(auth, "Bearer "))
		if _, err := domain.VerifyMarketplaceContextToken(secret, tok, time.Now().UTC()); err != nil {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		if r.Method == http.MethodDelete {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		var rpcReq struct {
			Method string `json:"method"`
		}
		_ = json.NewDecoder(r.Body).Decode(&rpcReq)
		w.Header().Set("Content-Type", "application/json")
		switch rpcReq.Method {
		case "initialize":
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2025-03-26","capabilities":{"tools":{}},"serverInfo":{"name":"html-check","version":"1.0.0"}}}`))
		case "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
		case "tools/list":
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":2,"result":{"tools":[{"name":"ping","description":"Ping"}]}}`))
		default:
			w.WriteHeader(http.StatusBadRequest)
		}
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	v := &probe.Verifier{
		AppOrigin:          "https://chat.example.com",
		RequirePublicHTTPS: false,
		Timeout:            5 * time.Second,
	}

	cases := []struct {
		name     string
		resp     webResp
		wantOK   bool
		wantCode string
	}{
		{
			name:     "json 200 rejected",
			resp:     webResp{status: http.StatusOK, contentType: "application/json", body: `{"status":"ok"}`},
			wantOK:   false,
			wantCode: "web_ui_not_html",
		},
		{
			name:     "plain text 200 rejected",
			resp:     webResp{status: http.StatusOK, contentType: "text/plain; charset=utf-8", body: "hello world"},
			wantOK:   false,
			wantCode: "web_ui_not_html",
		},
		{
			name:     "attachment download rejected",
			resp:     webResp{status: http.StatusOK, contentType: "text/html", disposition: `attachment; filename="ui.html"`, body: "<!doctype html><html><body>hi</body></html>"},
			wantOK:   false,
			wantCode: "web_ui_not_html",
		},
		{
			name:     "204 no content rejected",
			resp:     webResp{status: http.StatusNoContent, contentType: "text/html"},
			wantOK:   false,
			wantCode: "web_ui_not_html",
		},
		{
			name:     "empty html body rejected",
			resp:     webResp{status: http.StatusOK, contentType: "text/html; charset=utf-8", body: "   "},
			wantOK:   false,
			wantCode: "web_ui_not_html",
		},
		{
			name:     "missing content-type non-html body rejected",
			resp:     webResp{status: http.StatusOK, contentType: "", body: `{"not":"html"}`},
			wantOK:   false,
			wantCode: "web_ui_not_html",
		},
		{
			name:     "missing content-type doctype html body accepted",
			resp:     webResp{status: http.StatusOK, contentType: "", body: "<!DOCTYPE html><html><body>Panel</body></html>"},
			wantOK:   true,
			wantCode: "ok",
		},
		{
			name:     "xhtml content-type accepted",
			resp:     webResp{status: http.StatusOK, contentType: "application/xhtml+xml", body: "<html><body>Panel</body></html>"},
			wantOK:   true,
			wantCode: "ok",
		},
		{
			name:     "standard text/html accepted",
			resp:     webResp{status: http.StatusOK, contentType: "text/html; charset=utf-8", body: "<!doctype html><html><body>Panel</body></html>"},
			wantOK:   true,
			wantCode: "ok",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			current.Store(tc.resp)
			rep, err := v.VerifyEndpoints(ctx, domain.VerifyEndpointsInput{
				AppID:         "app-html",
				Slug:          "html-check",
				WebURL:        srv.URL + "/ui",
				MCPURL:        srv.URL + "/mcp",
				SigningSecret: secret,
			})
			if err != nil {
				t.Fatal(err)
			}
			if rep.OK != tc.wantOK {
				t.Fatalf("got OK=%v (checks=%+v), want OK=%v", rep.OK, rep.Checks, tc.wantOK)
			}
			if len(rep.Checks) < 2 || rep.Checks[1].Code != tc.wantCode {
				t.Fatalf("got stage 2 check=%+v, want code=%q", rep.Checks, tc.wantCode)
			}
		})
	}
}

type gatedVerifier struct {
	inner   domain.MarketplaceEndpointVerifier
	calls   atomic.Int32
	gateFn  func(input domain.VerifyEndpointsInput)
}

func (g *gatedVerifier) VerifyEndpoints(ctx context.Context, input domain.VerifyEndpointsInput) (domain.MarketplaceEndpointVerification, error) {
	g.calls.Add(1)
	if g.gateFn != nil {
		g.gateFn(input)
	}
	return g.inner.VerifyEndpoints(ctx, input)
}

func TestSubmitRechecksLiveEndpointAndFencesConcurrentMutations(t *testing.T) {
	ctx := context.Background()
	pool, err := postgres.Open(ctx, testdb.URL(t))
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err := pg.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}

	dev := domain.User{ID: domain.NewID(), Email: "dev@example.com", DisplayName: "Dev", Kind: "human", PlatformRole: "member", CreatedAt: time.Now().UTC()}
	idMock := &fakeIdentity{
		users: map[string]httpx.User{
			dev.ID: {ID: dev.ID, Email: dev.Email, DisplayName: dev.DisplayName, Kind: dev.Kind, PlatformRole: dev.PlatformRole, CreatedAt: dev.CreatedAt},
		},
	}

	var activeSecret atomic.Value
	activeSecret.Store("initial")
	extSrv, _ := startMockExternalApp(t, func() string { return activeSecret.Load().(string) }, false)

	var gateHook atomic.Value
	verifier := &gatedVerifier{
		inner: &probe.Verifier{
			AppOrigin:          "http://teamie.local:8080",
			RequirePublicHTTPS: false,
			Timeout:            5 * time.Second,
		},
		gateFn: func(in domain.VerifyEndpointsInput) {
			if fn, _ := gateHook.Load().(func(domain.VerifyEndpointsInput)); fn != nil {
				fn(in)
			}
		},
	}

	repo := pg.New(postgres.New(pool))
	origin := "http://teamie.local:8080"
	reqHTTPS := false
	newSvc := func() *app.Service {
		return &app.Service{
			Tx:                 repo,
			Marketplace:        repo,
			Identity:           domainIdentityFromFake{id: idMock},
			Chat:               &fakeChatScope{},
			AppOrigin:          origin,
			RequirePublicHTTPS: &reqHTTPS,
			Verifier:           verifier,
		}
	}
	svc := newSvc()

	created, secret1, err := svc.CreateDeveloperMarketplaceApp(ctx, dev, domain.CreateMarketplaceAppInput{
		Slug:   "fenced-app",
		Name:   "Fenced App",
		WebURL: extSrv.URL + "/ui",
		MCPURL: extSrv.URL + "/mcp",
	})
	if err != nil {
		t.Fatal(err)
	}
	activeSecret.Store(secret1)

	// Part A: /verify passes (calls=1), then live server breaks WITHOUT changing draft URLs -> immediate /submit re-verifies (calls=2) and fails with 422!
	verifiedApp, rep1, err := svc.VerifyDeveloperMarketplaceApp(ctx, dev, created.ID)
	if err != nil || !rep1.OK || verifiedApp.VerificationReport == nil || !verifiedApp.VerificationReport.OK {
		t.Fatalf("expected initial verify to pass: app=%+v rep=%+v err=%v", verifiedApp, rep1, err)
	}
	if verifier.calls.Load() != 1 {
		t.Fatalf("expected 1 verifier call, got %d", verifier.calls.Load())
	}

	// Break the live server's auth secret while keeping identical URLs.
	activeSecret.Store("broken-remote-secret")
	_, _, err = svc.SubmitDeveloperMarketplaceApp(ctx, dev, created.ID, "ship broken server")
	var derr *domain.Error
	if !errors.As(err, &derr) || derr.Status != http.StatusUnprocessableEntity || derr.Code != "endpoint_verification_failed" || derr.Error() != "Deployed Web UI and MCP Server endpoints failed platform protocol verification" {
		t.Fatalf("expected 422 endpoint_verification_failed when live endpoint broke after /verify, got %v (%+v)", err, derr)
	}
	if verifier.calls.Load() != 2 {
		t.Fatalf("expected /submit to invoke live verifier (calls=2), got %d", verifier.calls.Load())
	}
	afterBrokenSubmit, _, err := svc.GetDeveloperMarketplaceApp(ctx, dev, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if afterBrokenSubmit.ReviewStatus != domain.ReviewStatusDraft || afterBrokenSubmit.VerificationReport == nil || afterBrokenSubmit.VerificationReport.OK {
		t.Fatalf("expected app to remain in draft with persisted failed report, got %+v", afterBrokenSubmit)
	}

	// Part B: Concurrent secret rotation while /verify is in flight must fence SaveMarketplaceVerificationReport (409) and keep VerificationReport nil!
	activeSecret.Store(secret1)
	// Wait past verifyCooldown or clear LastVerifiedAt via secret rotation first; let's test rotation during an in-flight probe!
	// Clear LastVerifiedAt first by updating description so /verify does not hit 3s cooldown.
	desc := "Cleared for concurrency test"
	if _, err := svc.UpdateDeveloperMarketplaceApp(ctx, dev, created.ID, domain.UpdateMarketplaceAppInput{Description: &desc}); err != nil {
		t.Fatal(err)
	}
	// Reset in-memory cooldown by creating a fresh Service sharing the same PostgreSQL repo.
	svc = newSvc()

	enteredVerify := make(chan struct{})
	releaseVerify := make(chan struct{})
	var onceVerify atomic.Bool
	gateHook.Store(func(domain.VerifyEndpointsInput) {
		if onceVerify.CompareAndSwap(false, true) {
			close(enteredVerify)
			<-releaseVerify
		}
	})

	verifyErrCh := make(chan error, 1)
	go func() {
		_, _, vErr := svc.VerifyDeveloperMarketplaceApp(ctx, dev, created.ID)
		verifyErrCh <- vErr
	}()

	<-enteredVerify
	// Rotate signing secret S1 -> S2 while probe against S1 is still in flight!
	rotatedApp, secret2, err := svc.RotateDeveloperMarketplaceAppSecret(ctx, dev, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if rotatedApp.VerificationReport != nil || rotatedApp.LastVerifiedAt != nil {
		t.Fatalf("expected secret rotation to clear verification state, got %+v", rotatedApp)
	}
	// Keep activeSecret = secret1 just until probe finishes so the stale probe computes OK=true!
	close(releaseVerify)

	vErr := <-verifyErrCh
	if !errors.As(vErr, &derr) || derr.Status != http.StatusConflict || derr.Code != "revision_conflict" {
		t.Fatalf("expected 409 revision_conflict from stale in-flight /verify after secret rotation, got %v", vErr)
	}
	afterStaleVerify, _, err := svc.GetDeveloperMarketplaceApp(ctx, dev, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if afterStaleVerify.VerificationReport != nil || afterStaleVerify.LastVerifiedAt != nil {
		t.Fatalf("stale in-flight /verify must NOT repopulate cleared VerificationReport after secret rotation: %+v", afterStaleVerify)
	}

	// Part C: Concurrent draft URL change while /submit is in flight must fence (409) and NOT enter pending_review!
	activeSecret.Store(secret2)
	enteredSubmit := make(chan struct{})
	releaseSubmit := make(chan struct{})
	var onceSubmit atomic.Bool
	gateHook.Store(func(domain.VerifyEndpointsInput) {
		if onceSubmit.CompareAndSwap(false, true) {
			close(enteredSubmit)
			<-releaseSubmit
		}
	})

	submitErrCh := make(chan error, 1)
	go func() {
		_, _, sErr := svc.SubmitDeveloperMarketplaceApp(ctx, dev, created.ID, "race submit")
		submitErrCh <- sErr
	}()

	<-enteredSubmit
	newWebURL := extSrv.URL + "/ui?mutated=1"
	if _, err := svc.UpdateDeveloperMarketplaceApp(ctx, dev, created.ID, domain.UpdateMarketplaceAppInput{WebURL: &newWebURL}); err != nil {
		t.Fatal(err)
	}
	close(releaseSubmit)

	sErr := <-submitErrCh
	if !errors.As(sErr, &derr) || derr.Status != http.StatusConflict || derr.Code != "revision_conflict" {
		t.Fatalf("expected 409 revision_conflict when draft URL mutated during /submit, got %v", sErr)
	}
	afterStaleSubmit, _, err := svc.GetDeveloperMarketplaceApp(ctx, dev, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if afterStaleSubmit.ReviewStatus != domain.ReviewStatusDraft || afterStaleSubmit.VerificationReport != nil {
		t.Fatalf("expected app to remain unverified draft after fenced concurrent /submit, got %+v", afterStaleSubmit)
	}
}

func TestDisablePendingAppAndAdminSuspensionInvariants(t *testing.T) {
	ctx := context.Background()
	pool, err := postgres.Open(ctx, testdb.URL(t))
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err := pg.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}

	admin := domain.User{ID: domain.NewID(), Email: "admin@example.com", DisplayName: "Admin", Kind: "human", PlatformRole: "admin", CreatedAt: time.Now().UTC()}
	dev := domain.User{ID: domain.NewID(), Email: "dev@example.com", DisplayName: "Dev", Kind: "human", PlatformRole: "member", CreatedAt: time.Now().UTC()}
	bob := domain.User{ID: domain.NewID(), Email: "bob@example.com", DisplayName: "Bob", Kind: "human", PlatformRole: "member", CreatedAt: time.Now().UTC()}
	idMock := &fakeIdentity{
		users: map[string]httpx.User{
			admin.ID: {ID: admin.ID, Email: admin.Email, DisplayName: admin.DisplayName, Kind: admin.Kind, PlatformRole: admin.PlatformRole, CreatedAt: admin.CreatedAt},
			dev.ID:   {ID: dev.ID, Email: dev.Email, DisplayName: dev.DisplayName, Kind: dev.Kind, PlatformRole: dev.PlatformRole, CreatedAt: dev.CreatedAt},
			bob.ID:   {ID: bob.ID, Email: bob.Email, DisplayName: bob.DisplayName, Kind: bob.Kind, PlatformRole: bob.PlatformRole, CreatedAt: bob.CreatedAt},
		},
	}

	bobWS := domain.NewID()
	bobGroup := domain.NewID()
	chatMock := &fakeChatScope{
		workspaces: map[string]map[string]domain.WorkspaceScope{
			bobWS: {bob.ID: {Allowed: true, Role: "owner", OwnerID: bob.ID}},
		},
		conversations: map[string]map[string]domain.ConversationScope{
			bobGroup: {bob.ID: {Allowed: true, WorkspaceID: bobWS, ConversationType: "group", WorkspaceOwnerID: bob.ID}},
		},
	}

	var activeSecret atomic.Value
	activeSecret.Store("init")
	extSrv, _ := startMockExternalApp(t, func() string { return activeSecret.Load().(string) }, false)

	origin := "http://teamie.local:8080"
	reqHTTPS := false
	repo := pg.New(postgres.New(pool))
	svc := &app.Service{
		Tx:                 repo,
		Marketplace:        repo,
		Identity:           domainIdentityFromFake{id: idMock},
		Chat:               chatMock,
		AppOrigin:          origin,
		RequirePublicHTTPS: &reqHTTPS,
		Verifier: &probe.Verifier{
			AppOrigin:          origin,
			RequirePublicHTTPS: false,
			Timeout:            5 * time.Second,
		},
	}

	created, secret, err := svc.CreateDeveloperMarketplaceApp(ctx, dev, domain.CreateMarketplaceAppInput{
		Slug:   "pending-toggle",
		Name:   "Pending Toggle App",
		WebURL: extSrv.URL + "/ui",
		MCPURL: extSrv.URL + "/mcp",
	})
	if err != nil {
		t.Fatal(err)
	}
	activeSecret.Store(secret)

	submitted, _, err := svc.SubmitDeveloperMarketplaceApp(ctx, dev, created.ID, "ready")
	if err != nil || submitted.ReviewStatus != domain.ReviewStatusPending {
		t.Fatalf("expected pending_review, got %+v (%v)", submitted, err)
	}

	// 1. Owner can toggle Enabled=false and Enabled=true while pending_review!
	f := false
	disabledPending, err := svc.UpdateDeveloperMarketplaceApp(ctx, dev, created.ID, domain.UpdateMarketplaceAppInput{Enabled: &f})
	if err != nil || disabledPending.Enabled || disabledPending.ReviewStatus != domain.ReviewStatusPending {
		t.Fatalf("expected owner to disable pending_review app without leaving pending_review: %+v (%v)", disabledPending, err)
	}
	tr := true
	reenabledPending, err := svc.UpdateDeveloperMarketplaceApp(ctx, dev, created.ID, domain.UpdateMarketplaceAppInput{Enabled: &tr})
	if err != nil || !reenabledPending.Enabled || reenabledPending.ReviewStatus != domain.ReviewStatusPending {
		t.Fatalf("expected owner to re-enable pending_review app: %+v (%v)", reenabledPending, err)
	}

	// 2. Draft field edits alongside Enabled toggle are still rejected with 409 during pending_review!
	mutName := "Sneaky Edit"
	_, err = svc.UpdateDeveloperMarketplaceApp(ctx, dev, created.ID, domain.UpdateMarketplaceAppInput{Enabled: &f, Name: &mutName})
	var derr *domain.Error
	if !errors.As(err, &derr) || derr.Status != http.StatusConflict || derr.Code != "marketplace_app_review_pending" {
		t.Fatalf("expected 409 marketplace_app_review_pending when mutating draft fields during pending_review, got %v", err)
	}

	// 3. Approve -> install in Bob's workspace -> Admin suspends -> Owner Enabled toggle MUST NOT clear or bypass AdminSuspended!
	approved, _, err := svc.ApproveAdminMarketplaceApp(ctx, admin, created.ID, submitted.SubmissionRevision, "approved")
	if err != nil || !approved.IsPublished {
		t.Fatalf("expected approved app, got %+v (%v)", approved, err)
	}
	if _, err := svc.InstallWorkspaceApp(ctx, bobWS, created.ID, bob); err != nil {
		t.Fatalf("expected Bob install to succeed before suspension: %v", err)
	}

	suspended, _, err := svc.SuspendAdminMarketplaceApp(ctx, admin, created.ID, "security incident")
	if err != nil || !suspended.AdminSuspended || suspended.ReviewStatus != domain.ReviewStatusApproved {
		t.Fatalf("expected suspended app, got %+v (%v)", suspended, err)
	}

	// Draft edits on a suspended app are rejected with 403 marketplace_app_suspended.
	if _, err := svc.UpdateDeveloperMarketplaceApp(ctx, dev, created.ID, domain.UpdateMarketplaceAppInput{Name: &mutName}); !errors.As(err, &derr) || derr.Status != http.StatusForbidden || derr.Code != "marketplace_app_suspended" {
		t.Fatalf("expected 403 marketplace_app_suspended when owner attempts draft edit on suspended app, got %v", err)
	}
	// Toggling Enabled=false or Enabled=true must NEVER clear or bypass AdminSuspended!
	if toggledFalse, err := svc.UpdateDeveloperMarketplaceApp(ctx, dev, created.ID, domain.UpdateMarketplaceAppInput{Enabled: &f}); err != nil || !toggledFalse.AdminSuspended {
		t.Fatalf("expected AdminSuspended to remain true after Enabled=false, got %+v (%v)", toggledFalse, err)
	}
	if toggledTrue, err := svc.UpdateDeveloperMarketplaceApp(ctx, dev, created.ID, domain.UpdateMarketplaceAppInput{Enabled: &tr}); err != nil || !toggledTrue.AdminSuspended {
		t.Fatalf("expected AdminSuspended to remain true after Enabled=true, got %+v (%v)", toggledTrue, err)
	}
	afterOwnerAttempt, _, err := svc.GetDeveloperMarketplaceApp(ctx, dev, created.ID)
	if err != nil || !afterOwnerAttempt.AdminSuspended {
		t.Fatalf("expected AdminSuspended to remain true in DB, got %+v (%v)", afterOwnerAttempt, err)
	}

	cat, err := svc.ListMarketplaceCatalog(ctx, bob)
	if err != nil || len(cat) != 0 {
		t.Fatalf("expected suspended app hidden from catalog despite Enabled=true, got %+v (%v)", cat, err)
	}
	if _, err := svc.InstallWorkspaceApp(ctx, bobWS, created.ID, bob); !errors.As(err, &derr) || derr.Status != http.StatusForbidden || derr.Code != "marketplace_app_suspended" {
		t.Fatalf("expected 403 marketplace_app_suspended installing suspended app despite Enabled=true, got %v", err)
	}
	wsApps, err := svc.ListWorkspaceApps(ctx, bobWS, bob.ID)
	if err != nil || len(wsApps) != 0 {
		t.Fatalf("expected suspended app hidden from workspace apps, got %+v (%v)", wsApps, err)
	}
	convApps, err := svc.ListConversationApps(ctx, bobGroup, bob.ID)
	if err != nil || len(convApps) != 0 {
		t.Fatalf("expected suspended app hidden from conversation apps, got %+v (%v)", convApps, err)
	}
	if _, err := svc.CreateConversationAppSession(ctx, bobGroup, created.ID, bob); !errors.As(err, &derr) || derr.Status != http.StatusForbidden || derr.Code != "marketplace_app_suspended" {
		t.Fatalf("expected 403 marketplace_app_suspended minting session for suspended app, got %v", err)
	}
}
