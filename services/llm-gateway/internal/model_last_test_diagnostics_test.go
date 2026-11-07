package gateway_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"go.uber.org/zap"
	gatewaygen "teamie/contracts/gen/agent/hertz_gen/agent/gateway"
	"teamie/contracts/gen/agent/kitex_gen/agent"
	"teamie/pkg/observability"
	httpAdapter "teamie/services/llm-gateway/internal/adapter/http"
	rpcAdapter "teamie/services/llm-gateway/internal/adapter/rpc"
	"teamie/services/llm-gateway/internal/app"
	"teamie/services/llm-gateway/internal/domain"
	rpcx "teamie/services/llm-gateway/internal/infra/rpcclient"
)

func newDiagnosticTestStores(t *testing.T) []struct {
	name      string
	store     Store
	testStore ModelTestStore
} {
	t.Helper()
	mem := &memoryStore{}
	cases := []struct {
		name      string
		store     Store
		testStore ModelTestStore
	}{
		{name: "memory", store: mem, testStore: mem},
	}
	if dsn := os.Getenv("DATABASE_URL_TEST"); dsn != "" {
		ctx := context.Background()
		admin, err := pgx.Connect(ctx, dsn)
		if err != nil {
			t.Fatal(err)
		}
		schema := fmt.Sprintf("gw_lasttest_%d", time.Now().UnixNano())
		if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
			admin.Close(ctx)
			t.Fatal(err)
		}
		admin.Close(ctx)
		t.Cleanup(func() {
			cleanCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if c, err := pgx.Connect(cleanCtx, dsn); err == nil {
				_, _ = c.Exec(cleanCtx, "DROP SCHEMA "+schema+" CASCADE")
				_ = c.Close(cleanCtx)
			}
		})
		u, err := url.Parse(dsn)
		if err != nil {
			t.Fatal(err)
		}
		q := u.Query()
		q.Set("search_path", schema)
		u.RawQuery = q.Encode()
		pgStore, err := Open(ctx, u.String())
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { pgStore.DB.Close() })
		cases = append(cases, struct {
			name      string
			store     Store
			testStore ModelTestStore
		}{name: "postgres", store: pgStore, testStore: pgStore})
	}
	return cases
}

func findAdminModelDTO(t *testing.T, resp *gatewaygen.AdminModelConfigResp, id string) *gatewaygen.AdminModelProfileDTO {
	t.Helper()
	for _, m := range resp.Models {
		if m.ID == id {
			return m
		}
	}
	t.Fatalf("model %q not found in AdminModelConfigResp: %+v", id, resp.Models)
	return nil
}

func TestModelLastTestLifecycleFallbackFreshnessCascadeAndCatalogPrivacy(t *testing.T) {
	for _, backend := range newDiagnosticTestStores(t) {
		t.Run(backend.name, func(t *testing.T) {
			key1 := "sk-prov1-secret-canary-1111"
			key1Rotated := "sk-prov1-rotated-secret-2222"
			key2 := "sk-prov2-secret-canary-3333"

			var upstreamCalls atomic.Int32
			fixture := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				upstreamCalls.Add(1)
				var body struct {
					Model string `json:"model"`
				}
				_ = json.NewDecoder(r.Body).Decode(&body)
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"id":"chatcmpl-test","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"OK"},"finish_reason":"stop"}],"usage":{"prompt_tokens":4,"completion_tokens":1,"total_tokens":5}}`))
			}))
			defer fixture.Close()

			crypto := testCipher(t)
			s := New(backend.store, crypto, testToken)
			s.Provider = Provider{HTTP: fixture.Client()}
			auth := rpcx.AdminAuth{Identity: roleAwareSessionValidator{}, Origin: "https://teamie.example"}
			handler := s.Routes(auth)

			adminCookie := strings.Repeat("a", 64)
			userCookie := strings.Repeat("u", 64)

			doReq := func(method, path, body, cookie string, withCSRF bool, origin string) *httptest.ResponseRecorder {
				t.Helper()
				req := httptest.NewRequest(method, "https://teamie.example"+path, strings.NewReader(body))
				if cookie != "" {
					req.AddCookie(&http.Cookie{Name: "teamie_session", Value: cookie})
				}
				if origin != "" {
					req.Header.Set("Origin", origin)
				}
				if body != "" {
					req.Header.Set("Content-Type", "application/json")
				}
				if withCSRF {
					req.Header.Set("X-CSRF-Token", testToken)
				}
				rec := httptest.NewRecorder()
				handler.ServeHTTP(rec, req)
				return rec
			}

			getAdmin := func() (*gatewaygen.AdminModelConfigResp, string) {
				t.Helper()
				rec := doReq(http.MethodGet, "/api/v1/admin/model-gateway", "", adminCookie, true, "")
				if rec.Code != 200 {
					t.Fatalf("GET /admin/model-gateway failed: %d %s", rec.Code, rec.Body.String())
				}
				var out gatewaygen.AdminModelConfigResp
				if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
					t.Fatal(err)
				}
				return &out, rec.Body.String()
			}

			// 1. Seed two providers (prov-1 owning m1, m2; prov-2 owning m3) and point BaseURL to TLS fixture.
			if rec := doReq(http.MethodPost, "/api/v1/admin/model-gateway/providers", `{"id":"prov-1","displayName":"Provider One","baseUrl":"https://1.1.1.1/v1","apiKey":"`+key1+`","enabled":true}`, adminCookie, true, "https://teamie.example"); rec.Code != 201 {
				t.Fatalf("create prov-1 failed: %d %s", rec.Code, rec.Body.String())
			}
			if rec := doReq(http.MethodPost, "/api/v1/admin/model-gateway/providers", `{"id":"prov-2","displayName":"Provider Two","baseUrl":"https://8.8.8.8/v1","apiKey":"`+key2+`","enabled":true}`, adminCookie, true, "https://teamie.example"); rec.Code != 201 {
				t.Fatalf("create prov-2 failed: %d %s", rec.Code, rec.Body.String())
			}
			for _, spec := range []string{
				`{"id":"m1","providerId":"prov-1","displayName":"Model 1","model":"gpt-4o","enabled":true}`,
				`{"id":"m2","providerId":"prov-1","displayName":"Model 2","model":"gpt-4o-mini","enabled":true}`,
				`{"id":"m3","providerId":"prov-2","displayName":"Model 3","model":"claude-sonnet","enabled":true}`,
			} {
				if rec := doReq(http.MethodPost, "/api/v1/admin/model-gateway/models", spec, adminCookie, true, "https://teamie.example"); rec.Code != 201 {
					t.Fatalf("create model failed: %d %s", rec.Code, rec.Body.String())
				}
			}
			if rec := doReq(http.MethodPut, "/api/v1/admin/model-gateway/routing", `{"defaultModelId":"m1"}`, adminCookie, true, "https://teamie.example"); rec.Code != 200 {
				t.Fatalf("set routing failed: %d %s", rec.Code, rec.Body.String())
			}
			_, err := backend.store.UpdateCatalog(context.Background(), func(providers []ProviderProfile, models []ModelProfile) ([]ProviderProfile, []ModelProfile, error) {
				for i := range providers {
					providers[i].BaseURL = fixture.URL + "/v1"
				}
				return providers, models, nil
			})
			if err != nil {
				t.Fatal(err)
			}

			// Initially, all models omit lastTest.
			initialResp, initialRaw := getAdmin()
			if strings.Contains(initialRaw, `"lastTest"`) {
				t.Fatalf("expected lastTest omitted before any test, got %s", initialRaw)
			}
			if len(initialResp.Models) != 3 {
				t.Fatalf("expected 3 models, got %d", len(initialResp.Models))
			}

			// 2. POST /models/m1/test records lastTest ONLY on m1; test response shape remains strictly {ok, model, latencyMs}.
			testM1Rec := doReq(http.MethodPost, "/api/v1/admin/model-gateway/models/m1/test", `{}`, adminCookie, true, "https://teamie.example")
			if testM1Rec.Code != 200 {
				t.Fatalf("POST /models/m1/test failed: %d %s", testM1Rec.Code, testM1Rec.Body.String())
			}
			var rawTestEnvelope map[string]any
			if err := json.Unmarshal(testM1Rec.Body.Bytes(), &rawTestEnvelope); err != nil {
				t.Fatal(err)
			}
			if len(rawTestEnvelope) != 3 || rawTestEnvelope["ok"] != true || rawTestEnvelope["model"] != "gpt-4o" {
				t.Fatalf("unexpected test endpoint response shape: %s", testM1Rec.Body.String())
			}
			if _, hasLatency := rawTestEnvelope["latencyMs"]; !hasLatency {
				t.Fatalf("expected latencyMs in test response: %s", testM1Rec.Body.String())
			}

			afterM1, afterM1Raw := getAdmin()
			m1DTO := findAdminModelDTO(t, afterM1, "m1")
			if m1DTO.LastTest == nil || !m1DTO.LastTest.Ok || m1DTO.LastTest.TestedAt == "" || m1DTO.LastTest.LatencyMs == nil || m1DTO.LastTest.ErrorCode != nil {
				t.Fatalf("expected fresh passing lastTest on m1 without errorCode, got %+v", m1DTO.LastTest)
			}
			if strings.Contains(afterM1Raw, `"errorCode"`) {
				t.Fatalf("passing lastTest must omit errorCode: %s", afterM1Raw)
			}
			if findAdminModelDTO(t, afterM1, "m2").LastTest != nil || findAdminModelDTO(t, afterM1, "m3").LastTest != nil {
				t.Fatalf("untested models received lastTest: m2=%+v m3=%+v", findAdminModelDTO(t, afterM1, "m2").LastTest, findAdminModelDTO(t, afterM1, "m3").LastTest)
			}

			// 3. Disable m1 (leaving defaultModelId="m1"), then run POST /api/v1/admin/model-gateway/test:
			//    - m1's own lastTest is now omitted because m1.updatedAt changed on disable!
			//    - /test exercises effective fallback model m2 and records lastTest on m2!
			if rec := doReq(http.MethodPatch, "/api/v1/admin/model-gateway/models/m1", `{"enabled":false}`, adminCookie, true, "https://teamie.example"); rec.Code != 200 {
				t.Fatalf("disable m1 failed: %d %s", rec.Code, rec.Body.String())
			}
			afterDisableM1, _ := getAdmin()
			if findAdminModelDTO(t, afterDisableM1, "m1").LastTest != nil {
				t.Fatalf("expected stale lastTest on m1 to be omitted after enable toggle, got %+v", findAdminModelDTO(t, afterDisableM1, "m1").LastTest)
			}

			aggTestRec := doReq(http.MethodPost, "/api/v1/admin/model-gateway/test", `{}`, adminCookie, true, "https://teamie.example")
			if aggTestRec.Code != 200 || !strings.Contains(aggTestRec.Body.String(), `"model":"gpt-4o-mini"`) {
				t.Fatalf("aggregate POST /test did not exercise fallback model m2: %d %s", aggTestRec.Code, aggTestRec.Body.String())
			}
			afterAgg, _ := getAdmin()
			if afterAgg.Routing.DefaultModelId != "m1" {
				t.Fatalf("aggregate /test mutated configured defaultModelId: %q", afterAgg.Routing.DefaultModelId)
			}
			if findAdminModelDTO(t, afterAgg, "m2").LastTest == nil || !findAdminModelDTO(t, afterAgg, "m2").LastTest.Ok {
				t.Fatalf("expected POST /test to record lastTest on effective model m2, got %+v", findAdminModelDTO(t, afterAgg, "m2").LastTest)
			}
			if findAdminModelDTO(t, afterAgg, "m1").LastTest != nil || findAdminModelDTO(t, afterAgg, "m3").LastTest != nil {
				t.Fatalf("expected m1 (stale) and m3 (untested) to omit lastTest")
			}

			// Re-enable m1, test m1 and m3 so all three models (m1, m2, m3) have fresh lastTest.
			if rec := doReq(http.MethodPatch, "/api/v1/admin/model-gateway/models/m1", `{"enabled":true}`, adminCookie, true, "https://teamie.example"); rec.Code != 200 {
				t.Fatal(rec.Code)
			}
			if rec := doReq(http.MethodPost, "/api/v1/admin/model-gateway/models/m1/test", `{}`, adminCookie, true, "https://teamie.example"); rec.Code != 200 {
				t.Fatal(rec.Code)
			}
			if rec := doReq(http.MethodPost, "/api/v1/admin/model-gateway/models/m3/test", `{}`, adminCookie, true, "https://teamie.example"); rec.Code != 200 {
				t.Fatal(rec.Code)
			}

			// 4. Routing and WebSearch mutations preserve lastTest on all models (both in mutation response and GET).
			routeRec := doReq(http.MethodPut, "/api/v1/admin/model-gateway/routing", `{"defaultModelId":"m2"}`, adminCookie, true, "https://teamie.example")
			if routeRec.Code != 200 {
				t.Fatal(routeRec.Code)
			}
			var routeResp gatewaygen.AdminModelConfigResp
			_ = json.Unmarshal(routeRespBytes(t, routeRec), &routeResp)
			for _, id := range []string{"m1", "m2", "m3"} {
				if findAdminModelDTO(t, &routeResp, id).LastTest == nil {
					t.Fatalf("routing update dropped fresh lastTest on %s", id)
				}
			}
			wsRec := doReq(http.MethodPatch, "/api/v1/admin/model-gateway/web-search", `{"enabled":true,"apiKey":"tvly-key-123"}`, adminCookie, true, "https://teamie.example")
			if wsRec.Code != 200 {
				t.Fatal(wsRec.Code)
			}
			afterWS, _ := getAdmin()
			for _, id := range []string{"m1", "m2", "m3"} {
				if findAdminModelDTO(t, afterWS, id).LastTest == nil {
					t.Fatalf("web-search update dropped fresh lastTest on %s", id)
				}
			}

			// 5. Editing m1 (displayName/contextWindow) omits lastTest ONLY on m1; sibling m2 and m3 stay fresh.
			patchM1 := doReq(http.MethodPatch, "/api/v1/admin/model-gateway/models/m1", `{"displayName":"Model 1 Edited","contextWindowTokens":65536}`, adminCookie, true, "https://teamie.example")
			if patchM1.Code != 200 {
				t.Fatalf("patch m1 failed: %d %s", patchM1.Code, patchM1.Body.String())
			}
			var patchM1Resp gatewaygen.AdminModelConfigResp
			_ = json.Unmarshal(routeRespBytes(t, patchM1), &patchM1Resp)
			if findAdminModelDTO(t, &patchM1Resp, "m1").LastTest != nil {
				t.Fatal("expected PATCH /models/m1 response to omit stale lastTest on m1")
			}
			if findAdminModelDTO(t, &patchM1Resp, "m2").LastTest == nil || findAdminModelDTO(t, &patchM1Resp, "m3").LastTest == nil {
				t.Fatal("editing m1 dropped fresh lastTest on m2 or m3")
			}

			// Re-test m1, then rotate prov-1 API key -> BOTH m1 and m2 omit lastTest, while m3 (prov-2) keeps lastTest!
			if rec := doReq(http.MethodPost, "/api/v1/admin/model-gateway/models/m1/test", `{}`, adminCookie, true, "https://teamie.example"); rec.Code != 200 {
				t.Fatal(rec.Code)
			}
			rotProv1 := doReq(http.MethodPatch, "/api/v1/admin/model-gateway/providers/prov-1", `{"apiKey":"`+key1Rotated+`"}`, adminCookie, true, "https://teamie.example")
			if rotProv1.Code != 200 {
				t.Fatalf("rotate prov-1 key failed: %d %s", rotProv1.Code, rotProv1.Body.String())
			}
			// Restore fixture URL on prov-1? Note: apiKey-only PATCH did not touch BaseURL so BaseURL is still fixture.URL+"/v1"!
			afterRot, afterRotRaw := getAdmin()
			if findAdminModelDTO(t, afterRot, "m1").LastTest != nil || findAdminModelDTO(t, afterRot, "m2").LastTest != nil {
				t.Fatalf("rotating parent provider key must omit stale lastTest on child models m1 & m2: %s", afterRotRaw)
			}
			if findAdminModelDTO(t, afterRot, "m3").LastTest == nil {
				t.Fatal("rotating prov-1 unexpectedly omitted fresh lastTest on unrelated prov-2 model m3")
			}

			// 6. Verify provider DTOs and public /api/v1/model-gateway/catalog NEVER contain lastTest/testedAt/errorCode.
			var rawAdminMap map[string]any
			_ = json.Unmarshal([]byte(afterRotRaw), &rawAdminMap)
			rawProviders, _ := json.Marshal(rawAdminMap["providers"])
			if strings.Contains(string(rawProviders), "lastTest") || strings.Contains(string(rawProviders), "testedAt") {
				t.Fatalf("provider DTOs leaked lastTest: %s", string(rawProviders))
			}

			for _, cookie := range []string{userCookie, adminCookie} {
				pubRec := doReq(http.MethodGet, "/api/v1/model-gateway/catalog", "", cookie, true, "")
				if pubRec.Code != 200 {
					t.Fatalf("GET /catalog failed: %d %s", pubRec.Code, pubRec.Body.String())
				}
				pubBody := pubRec.Body.String()
				for _, forbidden := range []string{"lastTest", "testedAt", "latencyMs", "errorCode", key1, key1Rotated, key2} {
					if strings.Contains(pubBody, forbidden) {
						t.Fatalf("public catalog leaked %q: %s", forbidden, pubBody)
					}
				}
			}

			// 7. Deleting m3 cascade-deletes its persisted test row.
			if rec := doReq(http.MethodDelete, "/api/v1/admin/model-gateway/models/m3", "", adminCookie, true, "https://teamie.example"); rec.Code != 200 {
				t.Fatalf("delete m3 failed: %d %s", rec.Code, rec.Body.String())
			}
			storedAfterDel, err := backend.testStore.LoadModelTests(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if _, exists := storedAfterDel["m3"]; exists {
				t.Fatalf("deleting m3 left orphaned row in ModelTestStore: %+v", storedAfterDel)
			}
		})
	}
}

func TestModelLastTestPreconditionsLeavePriorDiagnosticsUntouched(t *testing.T) {
	for _, backend := range newDiagnosticTestStores(t) {
		t.Run(backend.name, func(t *testing.T) {
			var upstreamCalls atomic.Int32
			fixture := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				upstreamCalls.Add(1)
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"id":"chatcmpl-pre","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"OK"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":1,"total_tokens":4}}`))
			}))
			defer fixture.Close()

			crypto := testCipher(t)
			s := New(backend.store, crypto, testToken)
			s.Provider = Provider{HTTP: fixture.Client()}
			auth := rpcx.AdminAuth{Identity: roleAwareSessionValidator{}, Origin: "https://teamie.example"}
			handler := s.Routes(auth)
			adminCookie := strings.Repeat("a", 64)
			userCookie := strings.Repeat("u", 64)

			doReq := func(method, path, body, cookie string, withCSRF bool, origin string) *httptest.ResponseRecorder {
				t.Helper()
				req := httptest.NewRequest(method, "https://teamie.example"+path, strings.NewReader(body))
				if cookie != "" {
					req.AddCookie(&http.Cookie{Name: "teamie_session", Value: cookie})
				}
				if origin != "" {
					req.Header.Set("Origin", origin)
				}
				if body != "" {
					req.Header.Set("Content-Type", "application/json")
				}
				if withCSRF {
					req.Header.Set("X-CSRF-Token", testToken)
				}
				rec := httptest.NewRecorder()
				handler.ServeHTTP(rec, req)
				return rec
			}

			// 1. Completely empty gateway: POST /test returns 409 model_unconfigured and writes nothing.
			if rec := doReq(http.MethodPost, "/api/v1/admin/model-gateway/test", `{}`, adminCookie, true, "https://teamie.example"); rec.Code != 409 || !strings.Contains(rec.Body.String(), "model_unconfigured") {
				t.Fatalf("expected 409 model_unconfigured on empty gateway /test, got %d %s", rec.Code, rec.Body.String())
			}
			if rows, err := backend.testStore.LoadModelTests(context.Background()); err != nil || len(rows) != 0 {
				t.Fatalf("unconfigured /test wrote ModelTestStore: %+v (%v)", rows, err)
			}

			// 2. Seed configured provider+model (m-ready) and keyless provider+model (m-nokey).
			encKey, _ := crypto.Encrypt("sk-precondition-key")
			_, err := backend.store.UpdateCatalog(context.Background(), func(_ []ProviderProfile, _ []ModelProfile) ([]ProviderProfile, []ModelProfile, error) {
				return []ProviderProfile{
						{ID: "prov-ok", DisplayName: "OK Prov", BaseURL: fixture.URL + "/v1", CipherKey: encKey, Enabled: true},
						{ID: "prov-nokey", DisplayName: "NoKey Prov", BaseURL: fixture.URL + "/v1", CipherKey: nil, Enabled: true},
					}, []ModelProfile{
						{ID: "m-ready", ProviderID: "prov-ok", DisplayName: "Ready", Model: "gpt-4o", Enabled: true},
						{ID: "m-nokey", ProviderID: "prov-nokey", DisplayName: "NoKey", Model: "gpt-nokey", Enabled: true},
					}, nil
			})
			if err != nil {
				t.Fatal(err)
			}
			_, _ = backend.store.UpdateRouting(context.Background(), ModelRoutingConfig{DefaultModelID: "m-ready"})

			// Record initial passing test on m-ready.
			if rec := doReq(http.MethodPost, "/api/v1/admin/model-gateway/models/m-ready/test", `{}`, adminCookie, true, "https://teamie.example"); rec.Code != 200 {
				t.Fatalf("initial m-ready test failed: %d %s", rec.Code, rec.Body.String())
			}
			baselineMap, err := backend.testStore.LoadModelTests(context.Background())
			if err != nil || len(baselineMap) != 1 || !baselineMap["m-ready"].OK {
				t.Fatalf("expected baseline m-ready record, got %+v (%v)", baselineMap, err)
			}
			baselineRec := baselineMap["m-ready"]
			upstreamCalls.Store(0)

			// 3. Auth, non-admin, CSRF, Origin, 400 invalid ID, 404 missing model, and 409 model_not_configured rejections.
			for _, tc := range []struct {
				name       string
				path       string
				cookie     string
				csrf       bool
				origin     string
				wantStatus int
				wantCode   string
			}{
				{name: "unauth_agg", path: "/api/v1/admin/model-gateway/test", cookie: "", csrf: true, origin: "https://teamie.example", wantStatus: 401},
				{name: "unauth_model", path: "/api/v1/admin/model-gateway/models/m-ready/test", cookie: "", csrf: true, origin: "https://teamie.example", wantStatus: 401},
				{name: "non_admin_agg", path: "/api/v1/admin/model-gateway/test", cookie: userCookie, csrf: true, origin: "https://teamie.example", wantStatus: 403},
				{name: "non_admin_model", path: "/api/v1/admin/model-gateway/models/m-ready/test", cookie: userCookie, csrf: true, origin: "https://teamie.example", wantStatus: 403},
				{name: "missing_csrf_agg", path: "/api/v1/admin/model-gateway/test", cookie: adminCookie, csrf: false, origin: "https://teamie.example", wantStatus: 403},
				{name: "missing_csrf_model", path: "/api/v1/admin/model-gateway/models/m-ready/test", cookie: adminCookie, csrf: false, origin: "https://teamie.example", wantStatus: 403},
				{name: "bad_origin_model", path: "/api/v1/admin/model-gateway/models/m-ready/test", cookie: adminCookie, csrf: true, origin: "https://evil.example", wantStatus: 403},
				{name: "invalid_model_id", path: "/api/v1/admin/model-gateway/models/bad$id/test", cookie: adminCookie, csrf: true, origin: "https://teamie.example", wantStatus: 400, wantCode: "invalid_request"},
				{name: "missing_model_404", path: "/api/v1/admin/model-gateway/models/does-not-exist/test", cookie: adminCookie, csrf: true, origin: "https://teamie.example", wantStatus: 404, wantCode: "not_found"},
				{name: "model_not_configured", path: "/api/v1/admin/model-gateway/models/m-nokey/test", cookie: adminCookie, csrf: true, origin: "https://teamie.example", wantStatus: 409, wantCode: "model_not_configured"},
			} {
				rec := doReq(http.MethodPost, tc.path, `{}`, tc.cookie, tc.csrf, tc.origin)
				if rec.Code != tc.wantStatus || (tc.wantCode != "" && !strings.Contains(rec.Body.String(), tc.wantCode)) {
					t.Fatalf("%s: expected %d (%s), got %d %s", tc.name, tc.wantStatus, tc.wantCode, rec.Code, rec.Body.String())
				}
			}

			// 4. Exhaust global slots before provider call -> 429 gateway_busy must NOT overwrite baselineRec.
			s.App.SetMaxConcurrentCalls(2)
			rel1, ok1 := s.App.AcquireSlot()
			rel2, ok2 := s.App.AcquireSlot()
			if !ok1 || !ok2 {
				t.Fatal("failed to acquire slots for busy test")
			}
			busyRec := doReq(http.MethodPost, "/api/v1/admin/model-gateway/models/m-ready/test", `{}`, adminCookie, true, "https://teamie.example")
			rel1()
			rel2()
			if busyRec.Code != 429 || !strings.Contains(busyRec.Body.String(), "gateway_busy") {
				t.Fatalf("expected 429 gateway_busy, got %d %s", busyRec.Code, busyRec.Body.String())
			}

			if upstreamCalls.Load() != 0 {
				t.Fatalf("precondition rejections unexpectedly called upstream %d times", upstreamCalls.Load())
			}
			afterPreMap, err := backend.testStore.LoadModelTests(context.Background())
			if err != nil || len(afterPreMap) != 1 || !afterPreMap["m-ready"].TestedAt.Equal(baselineRec.TestedAt) || !afterPreMap["m-ready"].OK {
				t.Fatalf("precondition rejections mutated stored test row: got %+v, want %+v", afterPreMap, baselineRec)
			}

			// 5. Disable model -> POST /models/m-ready/test AND POST /test both return 409 model_disabled without overwriting gateway_model_tests!
			if rec := doReq(http.MethodPatch, "/api/v1/admin/model-gateway/models/m-ready", `{"enabled":false}`, adminCookie, true, "https://teamie.example"); rec.Code != 200 {
				t.Fatal(rec.Code)
			}
			if rec := doReq(http.MethodPost, "/api/v1/admin/model-gateway/models/m-ready/test", `{}`, adminCookie, true, "https://teamie.example"); rec.Code != 409 || !strings.Contains(rec.Body.String(), "model_disabled") {
				t.Fatalf("expected 409 model_disabled on disabled model test, got %d %s", rec.Code, rec.Body.String())
			}
			if rec := doReq(http.MethodPost, "/api/v1/admin/model-gateway/test", `{}`, adminCookie, true, "https://teamie.example"); rec.Code != 409 || !strings.Contains(rec.Body.String(), "model_disabled") {
				t.Fatalf("expected 409 model_disabled on aggregate test when only configured model is disabled, got %d %s", rec.Code, rec.Body.String())
			}
			if upstreamCalls.Load() != 0 {
				t.Fatalf("disabled model test unexpectedly called upstream %d times", upstreamCalls.Load())
			}
			afterDisabledMap, err := backend.testStore.LoadModelTests(context.Background())
			if err != nil || len(afterDisabledMap) != 1 || !afterDisabledMap["m-ready"].TestedAt.Equal(baselineRec.TestedAt) || !afterDisabledMap["m-ready"].OK {
				t.Fatalf("model_disabled rejection overwrote stored gateway_model_tests row: got %+v, want %+v", afterDisabledMap, baselineRec)
			}
		})
	}
}

type stubUpstreamModel struct {
	mu   sync.Mutex
	fn   func(ctx context.Context, c ResolvedModel, key, raw string) (string, int64, int64, error)
	real Provider
}

func (s *stubUpstreamModel) Chat(ctx context.Context, c ResolvedModel, key, raw string, limits ...int) (string, int64, int64, error) {
	s.mu.Lock()
	fn := s.fn
	real := s.real
	s.mu.Unlock()
	if fn != nil {
		return fn(ctx, c, key, raw)
	}
	return real.Chat(ctx, c, key, raw, limits...)
}

func (s *stubUpstreamModel) Stream(ctx context.Context, c ResolvedModel, key, raw string, emit func(string) error, limits ...int) (int64, int64, error) {
	return s.real.Stream(ctx, c, key, raw, emit, limits...)
}

func (s *stubUpstreamModel) Search(ctx context.Context, baseURL, apiKey string, payload map[string]any, maxResults int) ([]domain.WebSearchItem, int, error) {
	return s.real.Search(ctx, baseURL, apiKey, payload, maxResults)
}

func (s *stubUpstreamModel) Extract(ctx context.Context, baseURL, apiKey string, payload map[string]any) ([]domain.WebExtractItem, []domain.WebExtractFailure, int, error) {
	return s.real.Extract(ctx, baseURL, apiKey, payload)
}

func TestModelLastTestSafeErrorCodeAllowlistNoLeakageAndInferenceIsolation(t *testing.T) {
	for _, backend := range newDiagnosticTestStores(t) {
		t.Run(backend.name, func(t *testing.T) {
			var logs gatewayLogBuffer
			undo := zap.ReplaceGlobals(observability.New(observability.Config{Service: "teamie.llm-gateway", Output: &logs}))
			t.Cleanup(undo)

			const (
				canaryKey    = "sk-canary-super-secret-987654321"
				canaryHeader = "hdr-canary-secret-token-555"
				canaryBody   = "raw-upstream-body-secret-leak-777"
			)

			var modeMu sync.Mutex
			upstreamMode := "ok"
			serverDone := make(chan struct{})
			fixture := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				modeMu.Lock()
				mode := upstreamMode
				modeMu.Unlock()

				w.Header().Set("X-Upstream-Secret", canaryHeader)
				switch mode {
				case "401":
					w.WriteHeader(http.StatusUnauthorized)
					_, _ = w.Write([]byte(`{"error":{"message":"` + canaryBody + ` ` + canaryKey + ` ` + r.URL.String() + `"}}`))
				case "403":
					w.WriteHeader(http.StatusForbidden)
					_, _ = w.Write([]byte(`{"error":{"message":"` + canaryBody + ` ` + canaryKey + `"}}`))
				case "429":
					w.Header().Set("Retry-After", "12")
					w.WriteHeader(http.StatusTooManyRequests)
					_, _ = w.Write([]byte(`{"error":{"message":"` + canaryBody + `"}}`))
				case "500":
					w.WriteHeader(http.StatusInternalServerError)
					_, _ = w.Write([]byte(`{"error":{"message":"` + canaryBody + ` ` + canaryKey + `"}}`))
				case "invalid_json":
					w.Header().Set("Content-Type", "application/json")
					_, _ = w.Write([]byte(`not-valid-json-` + canaryBody + `-` + canaryKey))
				case "echoed_key":
					w.Header().Set("Content-Type", "application/json")
					_, _ = w.Write([]byte(`{"id":"1","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"leaked ` + canaryKey + `"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
				case "context_overflow":
					w.WriteHeader(http.StatusBadRequest)
					_, _ = w.Write([]byte(`{"error":{"code":"context_length_exceeded","message":"` + canaryBody + `"}}`))
				case "timeout":
					select {
					case <-r.Context().Done():
					case <-serverDone:
					}
				default:
					w.Header().Set("Content-Type", "application/json")
					_, _ = w.Write([]byte(`{"id":"ok","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"OK"},"finish_reason":"stop"}],"usage":{"prompt_tokens":4,"completion_tokens":1,"total_tokens":5}}`))
				}
			}))
			defer fixture.Close()
			defer close(serverDone)

			closedServer := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
			closedURL := closedServer.URL
			closedServer.Close()

			crypto := testCipher(t)
			stub := &stubUpstreamModel{real: Provider{HTTP: fixture.Client()}}
			appSvc := app.New(backend.store, crypto, stub, stub, testToken)
			httpH := httpAdapter.New(appSvc)
			rpcH := rpcAdapter.New(appSvc)
			auth := rpcx.AdminAuth{Identity: roleAwareSessionValidator{}, Origin: "https://teamie.example"}
			handler := observability.HTTP(httpH.Routes(auth))
			adminCookie := strings.Repeat("a", 64)

			encKey, _ := crypto.Encrypt(canaryKey)
			_, err := backend.store.UpdateCatalog(context.Background(), func(_ []ProviderProfile, _ []ModelProfile) ([]ProviderProfile, []ModelProfile, error) {
				return []ProviderProfile{{
						ID:          "prov-main",
						DisplayName: "Main",
						BaseURL:     fixture.URL + "/v1",
						CipherKey:   encKey,
						Enabled:     true,
					}}, []ModelProfile{{
						ID:          "m-main",
						ProviderID:  "prov-main",
						DisplayName: "Main Model",
						Model:       "gpt-4o",
						Enabled:     true,
					}}, nil
			})
			if err != nil {
				t.Fatal(err)
			}
			_, _ = backend.store.UpdateRouting(context.Background(), ModelRoutingConfig{DefaultModelID: "m-main"})

			doAdminReq := func(ctx context.Context, method, path string) *httptest.ResponseRecorder {
				t.Helper()
				req, err := http.NewRequestWithContext(ctx, method, "https://teamie.example"+path, strings.NewReader(`{}`))
				if err != nil {
					t.Fatal(err)
				}
				req.AddCookie(&http.Cookie{Name: "teamie_session", Value: adminCookie})
				req.Header.Set("Origin", "https://teamie.example")
				req.Header.Set("X-CSRF-Token", testToken)
				req.Header.Set("Content-Type", "application/json")
				rec := httptest.NewRecorder()
				handler.ServeHTTP(rec, req)
				return rec
			}

			assertRecordedErrorCode := func(step, wantCode string) {
				t.Helper()
				getRec := doAdminReq(context.Background(), http.MethodGet, "/api/v1/admin/model-gateway")
				if getRec.Code != 200 {
					t.Fatalf("%s: GET /admin/model-gateway failed: %d %s", step, getRec.Code, getRec.Body.String())
				}
				var adminResp gatewaygen.AdminModelConfigResp
				if err := json.Unmarshal(getRec.Body.Bytes(), &adminResp); err != nil {
					t.Fatal(err)
				}
				// Failed test must NOT alter saved configuration status ("ready").
				if adminResp.Status != "ready" {
					t.Fatalf("%s: failed lastTest altered gateway status to %q", step, adminResp.Status)
				}
				lt := findAdminModelDTO(t, &adminResp, "m-main").LastTest
				if lt == nil || lt.Ok || lt.LatencyMs == nil || *lt.LatencyMs < 0 || lt.ErrorCode == nil || *lt.ErrorCode != wantCode {
					t.Fatalf("%s: expected lastTest {ok:false, errorCode:%q, latencyMs>=0}, got %+v", step, wantCode, lt)
				}
				for _, secret := range []string{canaryKey, canaryHeader, canaryBody} {
					if strings.Contains(getRec.Body.String(), secret) {
						t.Fatalf("%s: admin GET leaked %q: %s", step, secret, getRec.Body.String())
					}
				}
			}

			// 1. Real TLS upstream transport error-code mapping cases.
			for _, tc := range []struct {
				mode     string
				wantCode string
			}{
				{mode: "401", wantCode: "provider_auth_failed"},
				{mode: "403", wantCode: "provider_auth_failed"},
				{mode: "invalid_json", wantCode: "provider_invalid_response"},
				{mode: "echoed_key", wantCode: "provider_invalid_response"},
				{mode: "429", wantCode: "upstream_rate_limited"},
				{mode: "500", wantCode: "upstream_error"},
				{mode: "context_overflow", wantCode: "provider_failed"},
			} {
				modeMu.Lock()
				upstreamMode = tc.mode
				modeMu.Unlock()
				rec := doAdminReq(context.Background(), http.MethodPost, "/api/v1/admin/model-gateway/models/m-main/test")
				if rec.Code == 200 {
					t.Fatalf("mode %s unexpectedly succeeded", tc.mode)
				}
				for _, secret := range []string{canaryKey, canaryHeader, canaryBody, fixture.URL} {
					if strings.Contains(rec.Body.String(), secret) {
						t.Fatalf("mode %s test error response leaked %q: %s", tc.mode, secret, rec.Body.String())
					}
				}
				assertRecordedErrorCode("tls_"+tc.mode, tc.wantCode)
			}

			// 2. Real upstream timeout -> upstream_timeout.
			modeMu.Lock()
			upstreamMode = "timeout"
			modeMu.Unlock()
			tCtx, tCancel := context.WithTimeout(context.Background(), 35*time.Millisecond)
			_ = doAdminReq(tCtx, http.MethodPost, "/api/v1/admin/model-gateway/models/m-main/test")
			tCancel()
			assertRecordedErrorCode("tls_timeout", "upstream_timeout")

			// 3. Real dial refusal (closed server) -> upstream_unavailable.
			// Note: updating BaseURL advances prov-main.updatedAt, then testing against closedURL records fresh upstream_unavailable.
			_, _ = backend.store.UpdateCatalog(context.Background(), func(providers []ProviderProfile, models []ModelProfile) ([]ProviderProfile, []ModelProfile, error) {
				providers[0].BaseURL = closedURL + "/v1"
				return providers, models, nil
			})
			_ = doAdminReq(context.Background(), http.MethodPost, "/api/v1/admin/model-gateway/test")
			assertRecordedErrorCode("tls_unavailable", "upstream_unavailable")

			// Restore live fixture URL.
			_, _ = backend.store.UpdateCatalog(context.Background(), func(providers []ProviderProfile, models []ModelProfile) ([]ProviderProfile, []ModelProfile, error) {
				providers[0].BaseURL = fixture.URL + "/v1"
				return providers, models, nil
			})

			// 4. Direct stubbed codes: upstream_http_error, provider_invalid_response, and untrusted/raw error fallback -> provider_failed.
			for _, sc := range []struct {
				name     string
				retErr   error
				wantCode string
			}{
				{name: "upstream_http_error", retErr: domain.Err(502, "upstream_http_error", "Provider rejected the request"), wantCode: "upstream_http_error"},
				{name: "direct_provider_invalid_response", retErr: domain.Err(502, "provider_invalid_response", "Invalid response"), wantCode: "provider_invalid_response"},
				{name: "unmapped_domain_error_code", retErr: domain.Err(502, "custom_unmapped_code", "Provider request failed"), wantCode: "provider_failed"},
				{name: "raw_stdlib_error_with_secret", retErr: errors.New("dial tcp " + fixture.URL + ": " + canaryKey + " " + canaryBody), wantCode: "provider_failed"},
			} {
				stub.mu.Lock()
				stub.fn = func(context.Context, ResolvedModel, string, string) (string, int64, int64, error) {
					return "", 0, 0, sc.retErr
				}
				stub.mu.Unlock()
				_ = doAdminReq(context.Background(), http.MethodPost, "/api/v1/admin/model-gateway/models/m-main/test")
				assertRecordedErrorCode(sc.name, sc.wantCode)
			}

			// 5. While m-main has lastTest.ok == false ("provider_failed"), verify Ping and live inference still treat m-main
			//    as ready, route to m-main without fallback, and NEVER overwrite m-main's lastTest!
			stub.mu.Lock()
			stub.fn = nil
			stub.mu.Unlock()
			modeMu.Lock()
			upstreamMode = "ok"
			modeMu.Unlock()

			beforeInferenceMap, _ := backend.testStore.LoadModelTests(context.Background())
			beforeInferenceRec := beforeInferenceMap["m-main"]

			pingResp, err := rpcH.Ping(context.Background(), &agent.PingRequest{ServiceToken: ptr(testToken)})
			if err != nil || !pingResp.Ready || !pingResp.GetConfigured() || !pingResp.GetEnabled() {
				t.Fatalf("failed lastTest affected Ping readiness: %+v (%v)", pingResp, err)
			}

			liveOutcome := appSvc.CallChat(context.Background(), "owner-1", "agent-1", "sess-1", "run-live", fixtureNativeRequest(`{"messages":[{"role":"user","content":"live call"}]}`, false))
			if !liveOutcome.Result.OK {
				t.Fatalf("live CallChat failed despite healthy upstream: %+v", liveOutcome.Result)
			}
			afterInferenceMap, _ := backend.testStore.LoadModelTests(context.Background())
			if afterInferenceMap["m-main"].OK || !afterInferenceMap["m-main"].TestedAt.Equal(beforeInferenceRec.TestedAt) || afterInferenceMap["m-main"].ErrorCode != "provider_failed" {
				t.Fatalf("live inference mutated gateway_model_tests: got %+v, want %+v", afterInferenceMap["m-main"], beforeInferenceRec)
			}

			// 6. Now run admin POST /test when upstream is healthy -> overwrites with ok=true and clears errorCode!
			okRec := doAdminReq(context.Background(), http.MethodPost, "/api/v1/admin/model-gateway/test")
			if okRec.Code != 200 {
				t.Fatalf("healthy POST /test failed: %d %s", okRec.Code, okRec.Body.String())
			}
			getRec := doAdminReq(context.Background(), http.MethodGet, "/api/v1/admin/model-gateway")
			var finalAdmin gatewaygen.AdminModelConfigResp
			_ = json.Unmarshal(getRec.Body.Bytes(), &finalAdmin)
			finalLT := findAdminModelDTO(t, &finalAdmin, "m-main").LastTest
			if finalLT == nil || !finalLT.Ok || finalLT.ErrorCode != nil || strings.Contains(getRec.Body.String(), `"errorCode"`) {
				t.Fatalf("passing test did not clear errorCode: %+v (%s)", finalLT, getRec.Body.String())
			}

			// 7. Verify structured logs never leaked canary secret, upstream URL, headers, or body text.
			logDump := logs.snapshot()
			for _, forbidden := range []string{canaryKey, canaryHeader, canaryBody, fixture.URL, closedURL} {
				if strings.Contains(logDump, forbidden) {
					t.Fatalf("structured logs leaked %q:\n%s", forbidden, logDump)
				}
			}
		})
	}
}

func routeRespBytes(t *testing.T, rec *httptest.ResponseRecorder) []byte {
	t.Helper()
	if rec.Code != 200 && rec.Code != 201 {
		t.Fatalf("unexpected HTTP status %d: %s", rec.Code, rec.Body.String())
	}
	return rec.Body.Bytes()
}
