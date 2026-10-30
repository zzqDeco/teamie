package chat

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"teamie/pkg/httpx"
	"teamie/pkg/postgres/testdb"
	httpadapter "teamie/services/chat/internal/adapter/http"
	"teamie/services/chat/internal/app"
	"teamie/services/chat/internal/domain"
	"teamie/services/chat/internal/infra/flags"
	"teamie/services/chat/internal/infra/probe"
	"teamie/services/chat/internal/infra/rpcclient"
)

type fakeLiveMarketplaceAppServer struct {
	mu                sync.Mutex
	secret            string
	appID             string
	xFrameOptions     string
	csp               string
	webStatus         int
	skipAuthCheck     bool
	skipSigVerify     bool
	useSSE            bool
	exposeTools       []map[string]any
	deletedSessionIDs []string
	initCalls         atomic.Int32
}

func newFakeLiveMarketplaceAppServer(t *testing.T, cfg *fakeLiveMarketplaceAppServer) *httptest.Server {
	t.Helper()
	if cfg.webStatus == 0 {
		cfg.webStatus = http.StatusOK
	}
	if cfg.exposeTools == nil {
		cfg.exposeTools = []map[string]any{
			{
				"name":        "join_room",
				"description": "Join the room for the current group conversation",
				"inputSchema": map[string]any{"type": "object", "properties": map[string]any{"seat": map[string]any{"type": "integer"}}},
			},
			{
				"name":        "night_action",
				"description": "Perform secret night action",
				"inputSchema": map[string]any{"type": "object", "properties": map[string]any{"targetUserId": map[string]any{"type": "string"}}},
			},
		}
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cfg.mu.Lock()
		secret := cfg.secret
		appID := cfg.appID
		xfo := cfg.xFrameOptions
		csp := cfg.csp
		webStatus := cfg.webStatus
		skipAuth := cfg.skipAuthCheck
		skipSig := cfg.skipSigVerify
		useSSE := cfg.useSSE
		tools := append([]map[string]any(nil), cfg.exposeTools...)
		cfg.mu.Unlock()

		switch {
		case strings.HasPrefix(r.URL.Path, "/ui"):
			if xfo != "" {
				w.Header().Set("X-Frame-Options", xfo)
			}
			if csp != "" {
				w.Header().Set("Content-Security-Policy", csp)
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.WriteHeader(webStatus)
			_, _ = w.Write([]byte("<!doctype html><html><body><h1>Werewolf Board</h1></body></html>"))
			return

		case strings.HasPrefix(r.URL.Path, "/mcp"):
			auth := strings.TrimSpace(r.Header.Get("Authorization"))
			if !skipAuth {
				if !strings.HasPrefix(auth, "Bearer ") {
					w.WriteHeader(http.StatusUnauthorized)
					return
				}
				rawToken := strings.TrimSpace(strings.TrimPrefix(auth, "Bearer "))
				if !skipSig {
					claims, err := domain.VerifyMarketplaceContextToken(secret, rawToken, time.Now().UTC())
					if err != nil || (appID != "" && claims.AppID != appID) {
						w.WriteHeader(http.StatusUnauthorized)
						return
					}
				} else if len(strings.Split(rawToken, ".")) != 3 {
					w.WriteHeader(http.StatusUnauthorized)
					return
				}
			}

			if r.Method == http.MethodDelete {
				if sid := r.Header.Get("Mcp-Session-Id"); sid != "" {
					cfg.mu.Lock()
					cfg.deletedSessionIDs = append(cfg.deletedSessionIDs, sid)
					cfg.mu.Unlock()
				}
				w.WriteHeader(http.StatusNoContent)
				return
			}
			if r.Method != http.MethodPost {
				w.WriteHeader(http.StatusMethodNotAllowed)
				return
			}

			var rpcReq struct {
				JSONRPC string `json:"jsonrpc"`
				ID      any    `json:"id"`
				Method  string `json:"method"`
			}
			if err := json.NewDecoder(r.Body).Decode(&rpcReq); err != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}

			w.Header().Set("Mcp-Session-Id", "sess-werewolf-1")
			writeReply := func(result any) {
				payload, _ := json.Marshal(map[string]any{
					"jsonrpc": "2.0",
					"id":      rpcReq.ID,
					"result":  result,
				})
				if useSSE {
					w.Header().Set("Content-Type", "text/event-stream")
					w.WriteHeader(http.StatusOK)
					_, _ = fmt.Fprintf(w, "event: message\ndata: %s\n\n", string(payload))
					return
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write(payload)
			}

			switch rpcReq.Method {
			case "initialize":
				cfg.initCalls.Add(1)
				writeReply(map[string]any{
					"protocolVersion": "2025-03-26",
					"serverInfo":      map[string]any{"name": "werewolf-mcp", "version": "1.2.0"},
					"capabilities":    map[string]any{"tools": map[string]any{"listChanged": true}},
				})
			case "notifications/initialized":
				w.WriteHeader(http.StatusAccepted)
			case "tools/list":
				writeReply(map[string]any{"tools": tools})
			default:
				w.WriteHeader(http.StatusNotFound)
			}
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestProtocolVerifierFailureMatrixAndSSESessionCleanup(t *testing.T) {
	ctx := context.Background()
	secret := domain.NewMarketplaceSigningSecret()
	appID := "app-verify-1"

	// 1. Valid SSE MCP Server + Mcp-Session-Id DELETE cleanup
	sseCfg := &fakeLiveMarketplaceAppServer{secret: secret, appID: appID, useSSE: true}
	sseSrv := newFakeLiveMarketplaceAppServer(t, sseCfg)
	verifier := probe.New("https://teamie.example.com", false)
	rep, err := verifier.VerifyEndpoints(ctx, domain.VerifyEndpointsInput{
		AppID:         appID,
		Slug:          "werewolf",
		WebURL:        sseSrv.URL + "/ui",
		MCPURL:        sseSrv.URL + "/mcp",
		SigningSecret: secret,
	})
	if err != nil {
		t.Fatalf("VerifyEndpoints returned unexpected error: %v", err)
	}
	if !rep.OK || len(rep.Tools) != 2 || rep.Tools[0].ExposedName != "mcp__werewolf__join_room" {
		t.Fatalf("expected valid SSE verification with 2 exposed tools, got: %+v", rep)
	}
	sseCfg.mu.Lock()
	deletedCount := len(sseCfg.deletedSessionIDs)
	sseCfg.mu.Unlock()
	if deletedCount != 1 {
		t.Fatalf("expected verifier to clean up Mcp-Session-Id via DELETE once, got %d", deletedCount)
	}

	// 2. Same-origin webUrl must fail url_security
	sameOriginVerifier := probe.New(sseSrv.URL, false)
	sameRep, _ := sameOriginVerifier.VerifyEndpoints(ctx, domain.VerifyEndpointsInput{
		AppID: appID, Slug: "werewolf", WebURL: sseSrv.URL + "/ui", MCPURL: sseSrv.URL + "/mcp", SigningSecret: secret,
	})
	if sameRep.OK || len(sameRep.Checks) == 0 || sameRep.Checks[0].Code != "same_origin_web_url" {
		t.Fatalf("expected same-origin webUrl to be rejected, got: %+v", sameRep)
	}

	// 3. Production RequirePublicHTTPS must reject http:// and loopback
	prodVerifier := probe.New("https://teamie.example.com", true)
	prodRep, _ := prodVerifier.VerifyEndpoints(ctx, domain.VerifyEndpointsInput{
		AppID: appID, Slug: "werewolf", WebURL: sseSrv.URL + "/ui", MCPURL: sseSrv.URL + "/mcp", SigningSecret: secret,
	})
	if prodRep.OK || prodRep.Checks[0].Stage != "url_security" {
		t.Fatalf("expected production HTTPS/loopback check to reject local HTTP server, got: %+v", prodRep)
	}

	// 4. X-Frame-Options: DENY, CSP frame-ancestors 'none' / wrong wildcard, and valid wildcard frame-ancestors
	for _, tc := range []struct {
		name   string
		xfo    string
		csp    string
		wantOK bool
	}{
		{"xfo_deny", "DENY", "", false},
		{"csp_none", "", "default-src 'self'; frame-ancestors 'none'", false},
		{"csp_wrong_wildcard", "", "frame-ancestors https://*.other.example", false},
		{"csp_apex_only_wildcard", "", "frame-ancestors https://*.teamie.example.com", false},
		{"csp_valid_wildcard_subdomain", "", "default-src 'self'; frame-ancestors https://*.example.com", true},
		{"csp_valid_schemeless_wildcard", "", "frame-ancestors *.example.com", true},
		{"csp_valid_wildcard_port", "", "frame-ancestors https://*.example.com:*", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := &fakeLiveMarketplaceAppServer{secret: secret, appID: appID, xFrameOptions: tc.xfo, csp: tc.csp}
			s := newFakeLiveMarketplaceAppServer(t, c)
			r, _ := verifier.VerifyEndpoints(ctx, domain.VerifyEndpointsInput{
				AppID: appID, Slug: "werewolf", WebURL: s.URL + "/ui", MCPURL: s.URL + "/mcp", SigningSecret: secret,
			})
			if r.OK != tc.wantOK {
				t.Fatalf("expected %s OK=%v, got OK=%v (%+v)", tc.name, tc.wantOK, r.OK, r.Checks)
			}
		})
	}

	// 5. Missing MCP auth guard & forged JWT signature acceptance & zero tools
	for _, tc := range []struct {
		name       string
		skipAuth   bool
		skipSig    bool
		emptyTools bool
		wantCode   string
	}{
		{"missing_auth", true, false, false, "mcp_unauthenticated_allowed"},
		{"forged_jwt_accepted", false, true, false, "mcp_forged_token_allowed"},
		{"zero_tools", false, false, true, "mcp_no_tools"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := &fakeLiveMarketplaceAppServer{secret: secret, appID: appID, skipAuthCheck: tc.skipAuth, skipSigVerify: tc.skipSig}
			if tc.emptyTools {
				c.exposeTools = []map[string]any{}
			}
			s := newFakeLiveMarketplaceAppServer(t, c)
			r, _ := verifier.VerifyEndpoints(ctx, domain.VerifyEndpointsInput{
				AppID: appID, Slug: "werewolf", WebURL: s.URL + "/ui", MCPURL: s.URL + "/mcp", SigningSecret: secret,
			})
			if r.OK {
				t.Fatalf("expected %s to fail verification", tc.name)
			}
			foundCode := false
			for _, chk := range r.Checks {
				if chk.Code == tc.wantCode {
					foundCode = true
				}
			}
			if !foundCode {
				t.Fatalf("expected check code %q in %+v", tc.wantCode, r.Checks)
			}
		})
	}
}

func TestDeveloperPlatformFullLifecycleHTTPAndStagedIsolation(t *testing.T) {
	t.Setenv("INTERNAL_SERVICE_KEY", "svc-secret")
	admin := httpx.User{ID: "u-admin", Email: "admin@example.com", DisplayName: "Platform Admin", Kind: "human", PlatformRole: "admin"}
	dev := httpx.User{ID: "u-dev", Email: "dev@example.com", DisplayName: "Alice Dev", Kind: "human", PlatformRole: "user"}
	other := httpx.User{ID: "u-other", Email: "bob@example.com", DisplayName: "Bob User", Kind: "human", PlatformRole: "user"}
	agent := httpx.User{ID: "u-agent", DisplayName: "Werewolf Bot", Kind: "agent", PlatformRole: "user"}

	h := newMemMarketplaceHarness()
	h.wsRoles["ws-dev"] = map[string]string{dev.ID: "owner", agent.ID: "member"}
	h.wsRoles["ws-other"] = map[string]string{other.ID: "owner"}
	h.wsRoles["ws-admin"] = map[string]string{admin.ID: "owner"}
	h.convs["g-dev"] = domain.Conversation{ID: "g-dev", WorkspaceID: "ws-dev", Type: "group", Name: "Dev Test Room"}
	h.convs["g-other"] = domain.Conversation{ID: "g-other", WorkspaceID: "ws-other", Type: "group", Name: "External Room"}
	h.convs["g-admin"] = domain.Conversation{ID: "g-admin", WorkspaceID: "ws-admin", Type: "group", Name: "Admin Review Room"}
	h.convMembers["g-dev"] = map[string]bool{dev.ID: true, agent.ID: true}
	h.convMembers["g-other"] = map[string]bool{other.ID: true}
	h.convMembers["g-admin"] = map[string]bool{admin.ID: true}

	idAuth := &fakeMarketplaceIdentity{
		sessions: map[string]httpx.Session{
			"admin-tok": {User: admin, CSRFToken: "csrf-admin"},
			"dev-tok":   {User: dev, CSRFToken: "csrf-dev"},
			"other-tok": {User: other, CSRFToken: "csrf-other"},
		},
		users: map[string]httpx.User{
			"agent-tok": agent,
			admin.ID:    admin,
			dev.ID:      dev,
			other.ID:    other,
		},
	}

	svc := &app.Service{
		Tx:            h,
		Workspaces:    h,
		Conversations: h,
		Marketplace:   h,
		Identity:      rpcclient.NewIdentity(idAuth),
		Flags:         flags.New(httpx.Flags{}),
		Verifier:      probe.New("https://teamie.example.com", false),
		AppOrigin:     "https://teamie.example.com",
	}
	srv := httptest.NewServer((&httpadapter.Adapter{
		App:    svc,
		Auth:   idAuth,
		Origin: "https://teamie.example.com",
	}).Routes())
	defer srv.Close()

	doHuman := func(tok, csrf, method, path string, body any) (*http.Response, map[string]any) {
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
		req.AddCookie(&http.Cookie{Name: httpx.CookieName, Value: tok})
		req.Header.Set("X-CSRF-Token", csrf)
		req.Header.Set("Origin", "https://teamie.example.com")
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var out map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&out)
		return resp, out
	}

	// 0. Agent principal is barred from Developer Portal endpoints (403 human_developer_required).
	agentDevReq, _ := http.NewRequest("GET", srv.URL+"/api/v1/developer/apps", nil)
	agentDevReq.Header.Set("Authorization", "Bearer agent-tok")
	agentDevReq.Header.Set("X-Teamie-Service-Key", "svc-secret")
	agentDevResp, err := http.DefaultClient.Do(agentDevReq)
	if err != nil {
		t.Fatal(err)
	}
	agentDevResp.Body.Close()
	if agentDevResp.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403 for agent calling developer portal, got %d", agentDevResp.StatusCode)
	}

	// 1. Stage 1: Developer creates draft app shell WITHOUT URLs to obtain App ID + Signing Secret.
	resp, createOut := doHuman("dev-tok", "csrf-dev", "POST", "/api/v1/developer/apps", map[string]any{
		"slug":        "werewolf",
		"name":        "狼人杀助手",
		"description": "多人实时法官 + AI 联机狼人杀面板",
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("expected 201 creating draft shell, got %d: %+v", resp.StatusCode, createOut)
	}
	signingSecret := createOut["signingSecret"].(string)
	devApp := createOut["app"].(map[string]any)
	appID := devApp["id"].(string)
	if !strings.HasPrefix(signingSecret, "tmsec_") || devApp["reviewStatus"] != "draft" || devApp["isPublished"] != false {
		t.Fatalf("unexpected draft shell state: %+v", devApp)
	}

	// Public catalog must NOT list unpublished draft apps.
	_, catOut := doHuman("other-tok", "csrf-other", "GET", "/api/v1/marketplace/catalog", nil)
	if len(catOut["apps"].([]any)) != 0 {
		t.Fatalf("unpublished draft leaked into public catalog: %+v", catOut)
	}
	// Non-owner cannot inspect developer's draft app.
	resp, _ = doHuman("other-tok", "csrf-other", "GET", "/api/v1/developer/apps/"+appID, nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404 for non-owner GET developer app, got %d", resp.StatusCode)
	}

	// 2. Stage 2: Developer deploys external WebUI + MCP Server using AppID + SigningSecret, patches URLs, and runs live verify.
	liveV1Cfg := &fakeLiveMarketplaceAppServer{secret: signingSecret, appID: appID, useSSE: true}
	liveV1 := newFakeLiveMarketplaceAppServer(t, liveV1Cfg)

	resp, patchOut := doHuman("dev-tok", "csrf-dev", "PATCH", "/api/v1/developer/apps/"+appID, map[string]any{
		"webUrl": liveV1.URL + "/ui-v1",
		"mcpUrl": liveV1.URL + "/mcp-v1",
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 patching draft URLs, got %d: %+v", resp.StatusCode, patchOut)
	}

	resp, verifyOut := doHuman("dev-tok", "csrf-dev", "POST", "/api/v1/developer/apps/"+appID+"/verify", map[string]any{})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 verifying endpoints, got %d: %+v", resp.StatusCode, verifyOut)
	}
	verif := verifyOut["verification"].(map[string]any)
	toolsAny, _ := verif["tools"].([]any)
	if verif["ok"] != true || len(toolsAny) != 2 {
		t.Fatalf("expected verification ok with 2 tools, got: %+v", verif)
	}

	// 3. Sandbox Testing: Developer installs own draft app into ws-dev; both human developer & Agent mint draft sessions!
	resp, _ = doHuman("other-tok", "csrf-other", "POST", "/api/v1/workspaces/ws-other/apps", map[string]any{"appId": appID})
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("external workspace must not install unpublished draft app, got %d", resp.StatusCode)
	}
	resp, installDevOut := doHuman("dev-tok", "csrf-dev", "POST", "/api/v1/workspaces/ws-dev/apps", map[string]any{"appId": appID})
	if resp.StatusCode != http.StatusCreated || installDevOut["app"].(map[string]any)["servingDraft"] != true {
		t.Fatalf("developer workspace install should serve draft, got %d: %+v", resp.StatusCode, installDevOut)
	}
	resp, devSessOut := doHuman("dev-tok", "csrf-dev", "POST", "/api/v1/conversations/g-dev/apps/"+appID+"/session", map[string]any{})
	if resp.StatusCode != http.StatusOK || devSessOut["app"].(map[string]any)["webUrl"] != liveV1.URL+"/ui-v1" {
		t.Fatalf("expected draft webUrl in developer sandbox session, got %d: %+v", resp.StatusCode, devSessOut)
	}
	agentSessReq, _ := http.NewRequest("POST", srv.URL+"/api/v1/conversations/g-dev/apps/"+appID+"/session", bytes.NewReader([]byte("{}")))
	agentSessReq.Header.Set("Authorization", "Bearer agent-tok")
	agentSessReq.Header.Set("X-Teamie-Service-Key", "svc-secret")
	agentSessReq.Header.Set("Content-Type", "application/json")
	agentSessResp, err := http.DefaultClient.Do(agentSessReq)
	if err != nil {
		t.Fatal(err)
	}
	var agentSessOut map[string]any
	_ = json.NewDecoder(agentSessResp.Body).Decode(&agentSessOut)
	agentSessResp.Body.Close()
	if agentSessResp.StatusCode != http.StatusOK || agentSessOut["app"].(map[string]any)["mcpUrl"] != liveV1.URL+"/mcp-v1" {
		t.Fatalf("expected agent in developer workspace to receive draft mcpUrl, got %d: %+v", agentSessResp.StatusCode, agentSessOut)
	}

	// 4. Submit -> Pending lock -> Withdraw -> Re-submit -> Admin preview -> Reject -> Re-submit -> Approve!
	resp, submit1 := doHuman("dev-tok", "csrf-dev", "POST", "/api/v1/developer/apps/"+appID+"/submit", map[string]any{
		"note": "首发版本：支持 6-9 人标准局",
	})
	if resp.StatusCode != http.StatusOK || submit1["app"].(map[string]any)["reviewStatus"] != "pending_review" {
		t.Fatalf("submit 1 failed: %d %+v", resp.StatusCode, submit1)
	}
	// Editing or deleting while pending_review is locked (409).
	resp, _ = doHuman("dev-tok", "csrf-dev", "PATCH", "/api/v1/developer/apps/"+appID, map[string]any{"name": "改名"})
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("expected 409 editing pending_review app, got %d", resp.StatusCode)
	}
	resp, _ = doHuman("dev-tok", "csrf-dev", "DELETE", "/api/v1/developer/apps/"+appID, nil)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("expected 409 deleting pending_review app, got %d", resp.StatusCode)
	}
	// Withdraw back to draft.
	resp, withdrawOut := doHuman("dev-tok", "csrf-dev", "POST", "/api/v1/developer/apps/"+appID+"/withdraw", map[string]any{})
	if resp.StatusCode != http.StatusOK || withdrawOut["app"].(map[string]any)["reviewStatus"] != "draft" {
		t.Fatalf("withdraw failed: %d %+v", resp.StatusCode, withdrawOut)
	}
	// Re-submit (revision 2).
	resp, submit2 := doHuman("dev-tok", "csrf-dev", "POST", "/api/v1/developer/apps/"+appID+"/submit", map[string]any{
		"note": "补充夜间视野隔离说明",
	})
	if resp.StatusCode != http.StatusOK || int(submit2["app"].(map[string]any)["submissionRevision"].(float64)) != 2 {
		t.Fatalf("submit 2 expected revision 2, got: %d %+v", resp.StatusCode, submit2)
	}

	// Admin installs pending_review app into admin-owned workspace for functional review preview.
	resp, adminPreview := doHuman("admin-tok", "csrf-admin", "POST", "/api/v1/workspaces/ws-admin/apps", map[string]any{"appId": appID})
	if resp.StatusCode != http.StatusCreated || adminPreview["app"].(map[string]any)["servingDraft"] != true {
		t.Fatalf("admin review workspace install failed: %d %+v", resp.StatusCode, adminPreview)
	}

	// Stale expectedRevision rejected with 409; empty reject note rejected with 400.
	resp, _ = doHuman("admin-tok", "csrf-admin", "POST", "/api/v1/admin/marketplace/apps/"+appID+"/reject", map[string]any{
		"expectedRevision": 1,
		"note":             "过期版本",
	})
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("expected 409 on stale revision reject, got %d", resp.StatusCode)
	}
	resp, _ = doHuman("admin-tok", "csrf-admin", "POST", "/api/v1/admin/marketplace/apps/"+appID+"/reject", map[string]any{
		"expectedRevision": 2,
		"note":             "   ",
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 on empty reject note, got %d", resp.StatusCode)
	}

	// Valid rejection with feedback note.
	resp, rejectOut := doHuman("admin-tok", "csrf-admin", "POST", "/api/v1/admin/marketplace/apps/"+appID+"/reject", map[string]any{
		"expectedRevision": 2,
		"note":             "请补充女巫解药/毒药参数描述",
	})
	if resp.StatusCode != http.StatusOK || rejectOut["app"].(map[string]any)["reviewStatus"] != "rejected" {
		t.Fatalf("admin reject failed: %d %+v", resp.StatusCode, rejectOut)
	}

	// Developer re-submits (revision 3) and Admin approves!
	resp, _ = doHuman("dev-tok", "csrf-dev", "POST", "/api/v1/developer/apps/"+appID+"/submit", map[string]any{"note": "已补充参数描述"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("submit 3 failed: %d", resp.StatusCode)
	}
	resp, approveOut := doHuman("admin-tok", "csrf-admin", "POST", "/api/v1/admin/marketplace/apps/"+appID+"/approve", map[string]any{
		"expectedRevision": 3,
		"note":             "协议与视野隔离验证通过",
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("approve v1 failed: %d %+v", resp.StatusCode, approveOut)
	}
	approvedApp := approveOut["app"].(map[string]any)
	if approvedApp["isPublished"] != true || int(approvedApp["publishedVersion"].(float64)) != 1 || approvedApp["hasDraftChanges"] != false {
		t.Fatalf("unexpected approved v1 state: %+v", approvedApp)
	}

	// Public catalog & external workspace can now install v1!
	_, catOut = doHuman("other-tok", "csrf-other", "GET", "/api/v1/marketplace/catalog", nil)
	catApps := catOut["apps"].([]any)
	if len(catApps) != 1 || catApps[0].(map[string]any)["publisherName"] != "Alice Dev" {
		t.Fatalf("expected published app with publisherName Alice Dev in catalog, got: %+v", catApps)
	}
	resp, _ = doHuman("other-tok", "csrf-other", "POST", "/api/v1/workspaces/ws-other/apps", map[string]any{"appId": appID})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("external workspace install v1 failed: %d", resp.StatusCode)
	}

	// 5. Staged v2 Draft Update Isolation: external workspace stays on v1 while dev edits & tests v2!
	resp, _ = doHuman("dev-tok", "csrf-dev", "PATCH", "/api/v1/developer/apps/"+appID, map[string]any{"slug": "new-slug"})
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("expected 409 when changing slug after publication, got %d", resp.StatusCode)
	}
	liveV2 := newFakeLiveMarketplaceAppServer(t, &fakeLiveMarketplaceAppServer{secret: signingSecret, appID: appID})
	resp, v2Patch := doHuman("dev-tok", "csrf-dev", "PATCH", "/api/v1/developer/apps/"+appID, map[string]any{
		"name":   "狼人杀助手 2.0",
		"webUrl": liveV2.URL + "/ui-v2",
		"mcpUrl": liveV2.URL + "/mcp-v2",
	})
	if resp.StatusCode != http.StatusOK || v2Patch["app"].(map[string]any)["hasDraftChanges"] != true {
		t.Fatalf("v2 draft patch failed: %d %+v", resp.StatusCode, v2Patch)
	}
	// External workspace still gets v1!
	_, extSess := doHuman("other-tok", "csrf-other", "POST", "/api/v1/conversations/g-other/apps/"+appID+"/session", map[string]any{})
	if extSess["app"].(map[string]any)["webUrl"] != liveV1.URL+"/ui-v1" || extSess["app"].(map[string]any)["servingDraft"] != false {
		t.Fatalf("external workspace must stay pinned to approved v1 snapshot, got: %+v", extSess)
	}
	// Developer's own workspace immediately tests v2 draft!
	_, devV2Sess := doHuman("dev-tok", "csrf-dev", "POST", "/api/v1/conversations/g-dev/apps/"+appID+"/session", map[string]any{})
	if devV2Sess["app"].(map[string]any)["webUrl"] != liveV2.URL+"/ui-v2" || devV2Sess["app"].(map[string]any)["servingDraft"] != true {
		t.Fatalf("developer workspace should serve v2 draft immediately, got: %+v", devV2Sess)
	}

	// Submit & approve v2 -> external workspace upgrades atomically to v2!
	resp, _ = doHuman("dev-tok", "csrf-dev", "POST", "/api/v1/developer/apps/"+appID+"/submit", map[string]any{"note": "升级 2.0 丘比特板子"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("submit v2 failed: %d", resp.StatusCode)
	}
	resp, approveV2 := doHuman("admin-tok", "csrf-admin", "POST", "/api/v1/admin/marketplace/apps/"+appID+"/approve", map[string]any{
		"expectedRevision": 4,
	})
	if resp.StatusCode != http.StatusOK || int(approveV2["app"].(map[string]any)["publishedVersion"].(float64)) != 2 {
		t.Fatalf("approve v2 failed: %d %+v", resp.StatusCode, approveV2)
	}
	_, extSessV2 := doHuman("other-tok", "csrf-other", "POST", "/api/v1/conversations/g-other/apps/"+appID+"/session", map[string]any{})
	if extSessV2["app"].(map[string]any)["webUrl"] != liveV2.URL+"/ui-v2" {
		t.Fatalf("external workspace did not upgrade to v2 after approval: %+v", extSessV2)
	}

	// 6. Admin Suspend & Unsuspend governance
	resp, _ = doHuman("admin-tok", "csrf-admin", "POST", "/api/v1/admin/marketplace/apps/"+appID+"/suspend", map[string]any{
		"reason": "远端服务证书异常，临时下架",
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("suspend failed: %d", resp.StatusCode)
	}
	resp, _ = doHuman("other-tok", "csrf-other", "POST", "/api/v1/conversations/g-other/apps/"+appID+"/session", map[string]any{})
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("suspended app must not mint sessions, got %d", resp.StatusCode)
	}
	resp, _ = doHuman("dev-tok", "csrf-dev", "DELETE", "/api/v1/developer/apps/"+appID, nil)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403 deleting suspended app, got %d", resp.StatusCode)
	}
	resp, _ = doHuman("admin-tok", "csrf-admin", "POST", "/api/v1/admin/marketplace/apps/"+appID+"/unsuspend", map[string]any{})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("unsuspend failed: %d", resp.StatusCode)
	}
	resp, _ = doHuman("dev-tok", "csrf-dev", "DELETE", "/api/v1/developer/apps/"+appID, nil)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("expected 409 deleting published app, got %d", resp.StatusCode)
	}
	resp, _ = doHuman("other-tok", "csrf-other", "POST", "/api/v1/conversations/g-other/apps/"+appID+"/session", map[string]any{})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("unsuspended app should resume minting sessions, got %d", resp.StatusCode)
	}

	// 7. Unpublished draft app can be deleted cleanly by its developer owner.
	resp, tempOut := doHuman("dev-tok", "csrf-dev", "POST", "/api/v1/developer/apps", map[string]any{
		"slug": "temp-draft", "name": "Temp Draft",
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create temp draft failed: %d", resp.StatusCode)
	}
	tempID := tempOut["app"].(map[string]any)["id"].(string)
	resp, _ = doHuman("dev-tok", "csrf-dev", "DELETE", "/api/v1/developer/apps/"+tempID, nil)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("expected 204 deleting unpublished draft app, got %d", resp.StatusCode)
	}
}

func TestDeveloperMarketplacePostgresSchemaV9EndToEnd(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, testdb.URL(t), httpx.Flags{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.DB.Close)

	admin := httpx.User{ID: httpx.ID(), Email: "admin@example.com", DisplayName: "Admin", Kind: "human", PlatformRole: "admin"}
	dev := httpx.User{ID: httpx.ID(), Email: "dev@example.com", DisplayName: "Dev", Kind: "human", PlatformRole: "user"}
	p1 := httpx.User{ID: httpx.ID(), Email: "p1@example.com", DisplayName: "Player 1", Kind: "human", PlatformRole: "user"}
	p2 := httpx.User{ID: httpx.ID(), Email: "p2@example.com", DisplayName: "Player 2", Kind: "human", PlatformRole: "user"}
	ws, _, err := s.CreateWorkspace(ctx, dev.ID, "Dev Sandbox")
	if err != nil {
		t.Fatal(err)
	}
	for _, memberID := range []string{p1.ID, p2.ID} {
		invCode, _, invErr := s.CreateInvite(ctx, dev.ID, ws.ID)
		if invErr != nil {
			t.Fatal(invErr)
		}
		if _, _, invErr = s.AcceptInvite(ctx, memberID, invCode); invErr != nil {
			t.Fatal(invErr)
		}
	}
	group, err := s.CreateConversation(ctx, dev.ID, ws.ID, "group", "Game Room", []string{p1.ID, p2.ID})
	if err != nil {
		t.Fatal(err)
	}
	idAuth := &fakeMarketplaceIdentity{
		sessions: map[string]httpx.Session{
			"admin-tok": {User: admin, CSRFToken: "csrf-a"},
			"dev-tok":   {User: dev, CSRFToken: "csrf-d"},
		},
		users: map[string]httpx.User{admin.ID: admin, dev.ID: dev, p1.ID: p1, p2.ID: p2},
	}
	srv := httptest.NewServer((&Handler{Store: s, Identity: idAuth, Origin: "https://teamie.example.com"}).Routes())
	defer srv.Close()

	postJSON := func(tok, csrf, method, path string, body any) (int, map[string]any) {
		b, _ := json.Marshal(body)
		req, _ := http.NewRequest(method, srv.URL+path, bytes.NewReader(b))
		req.AddCookie(&http.Cookie{Name: httpx.CookieName, Value: tok})
		req.Header.Set("X-CSRF-Token", csrf)
		req.Header.Set("Origin", "https://teamie.example.com")
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var out map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&out)
		return resp.StatusCode, out
	}

	code, created := postJSON("dev-tok", "csrf-d", "POST", "/api/v1/developer/apps", map[string]any{
		"slug": "pg-werewolf", "name": "PG Werewolf",
	})
	if code != 201 {
		t.Fatalf("pg create draft shell failed: %d %+v", code, created)
	}
	appID := created["app"].(map[string]any)["id"].(string)
	secret := created["signingSecret"].(string)

	live := newFakeLiveMarketplaceAppServer(t, &fakeLiveMarketplaceAppServer{secret: secret, appID: appID})
	if code, _ = postJSON("dev-tok", "csrf-d", "PATCH", "/api/v1/developer/apps/"+appID, map[string]any{
		"webUrl": live.URL + "/ui", "mcpUrl": live.URL + "/mcp",
	}); code != 200 {
		t.Fatalf("pg patch draft URLs failed: %d", code)
	}
	if code, _ = postJSON("dev-tok", "csrf-d", "POST", "/api/v1/workspaces/"+ws.ID+"/apps", map[string]any{"appId": appID}); code != 201 {
		t.Fatalf("pg install draft into own workspace failed: %d", code)
	}
	if code, _ = postJSON("dev-tok", "csrf-d", "POST", "/api/v1/conversations/"+group.ID+"/apps/"+appID+"/session", map[string]any{}); code != 200 {
		t.Fatalf("pg mint draft session failed: %d", code)
	}
	if code, _ = postJSON("dev-tok", "csrf-d", "POST", "/api/v1/developer/apps/"+appID+"/submit", map[string]any{"note": "ready"}); code != 200 {
		t.Fatalf("pg submit failed: %d", code)
	}
	if code, _ = postJSON("admin-tok", "csrf-a", "POST", "/api/v1/admin/marketplace/apps/"+appID+"/approve", map[string]any{"expectedRevision": 1}); code != 200 {
		t.Fatalf("pg approve failed: %d", code)
	}
}
