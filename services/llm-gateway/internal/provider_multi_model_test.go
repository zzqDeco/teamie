package gateway_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/cloudwego/kitex/client/callopt"
	gatewaygen "teamie/contracts/gen/agent/hertz_gen/agent/gateway"
	"teamie/contracts/gen/agent/kitex_gen/agent"
	"teamie/contracts/gen/im/kitex_gen/im"
	rpcx "teamie/services/llm-gateway/internal/infra/rpcclient"
)

type roleAwareSessionValidator struct{}

func (roleAwareSessionValidator) ValidateSession(_ context.Context, r *im.SessionRequest, _ ...callopt.Option) (*im.SessionResponse, error) {
	switch r.Token {
	case strings.Repeat("a", 64):
		role := "admin"
		csrf := testToken
		return &im.SessionResponse{
			Valid:     true,
			User:      &im.User{Id: "admin-1", Kind: "human", PlatformRole: role},
			CsrfToken: &csrf,
		}, nil
	case strings.Repeat("u", 64):
		role := "user"
		csrf := testToken
		return &im.SessionResponse{
			Valid:     true,
			User:      &im.User{Id: "user-1", Kind: "human", PlatformRole: role},
			CsrfToken: &csrf,
		}, nil
	default:
		return &im.SessionResponse{Valid: false}, nil
	}
}

func TestSingleProviderMultipleModelsLifecycleRotationFallbackAndCatalogPrivacy(t *testing.T) {
	initialKey := "sk-shared-provider-initial-key"
	rotatedKey := "sk-shared-provider-rotated-key"
	backupKey := "sk-backup-provider-key"

	var mu sync.Mutex
	type observedCall struct {
		auth  string
		model string
	}
	var calls []observedCall

	fixture := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Model string `json:"model"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		calls = append(calls, observedCall{auth: r.Header.Get("Authorization"), model: body.Model})
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl-prov","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"OK"},"finish_reason":"stop"}],"usage":{"prompt_tokens":4,"completion_tokens":1,"total_tokens":5}}`))
	}))
	defer fixture.Close()

	crypto := testCipher(t)
	store := &memoryStore{}
	s := New(store, crypto, testToken)
	s.Provider = Provider{HTTP: fixture.Client()}
	auth := rpcx.AdminAuth{Identity: roleAwareSessionValidator{}, Origin: "https://teamie.example"}
	handler := s.Routes(auth)

	doReq := func(method, path, body, sessionCookie string, withCSRF bool, origin string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, "https://teamie.example"+path, strings.NewReader(body))
		if sessionCookie != "" {
			req.AddCookie(&http.Cookie{Name: "teamie_session", Value: sessionCookie})
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

	adminCookie := strings.Repeat("a", 64)
	userCookie := strings.Repeat("u", 64)

	// 1. Authorization, CSRF, and Origin checks on provider endpoints.
	if rec := doReq(http.MethodPost, "/api/v1/admin/model-gateway/providers", `{"baseUrl":"https://1.1.1.1/v1","enabled":true}`, "", true, "https://teamie.example"); rec.Code != 401 {
		t.Fatalf("expected 401 for unauthenticated provider create, got %d", rec.Code)
	}
	if rec := doReq(http.MethodPost, "/api/v1/admin/model-gateway/providers", `{"baseUrl":"https://1.1.1.1/v1","enabled":true}`, userCookie, true, "https://teamie.example"); rec.Code != 403 {
		t.Fatalf("expected 403 for non-admin provider create, got %d", rec.Code)
	}
	if rec := doReq(http.MethodPost, "/api/v1/admin/model-gateway/providers", `{"baseUrl":"https://1.1.1.1/v1","enabled":true}`, adminCookie, false, "https://teamie.example"); rec.Code != 403 {
		t.Fatalf("expected 403 for missing CSRF on provider create, got %d", rec.Code)
	}
	if rec := doReq(http.MethodPost, "/api/v1/admin/model-gateway/providers", `{"baseUrl":"https://1.1.1.1/v1","enabled":true}`, adminCookie, true, "https://evil.example"); rec.Code != 403 {
		t.Fatalf("expected 403 for untrusted Origin on provider create, got %d", rec.Code)
	}

	// Note: fixture.URL uses 127.0.0.1 which ValidateEndpoint blocks at HTTP admin admission (SSRF protection).
	// Verify SSRF rejection on provider create first, then seed fixture URL in store for real TLS transport checks,
	// while exercising HTTP admin CRUD with public IP 1.1.1.1.
	if rec := doReq(http.MethodPost, "/api/v1/admin/model-gateway/providers", `{"id":"loopback","baseUrl":"`+fixture.URL+`/v1","enabled":true}`, adminCookie, true, "https://teamie.example"); rec.Code != 400 {
		t.Fatalf("expected SSRF loopback rejection on provider create, got %d", rec.Code)
	}

	// 2. Create Provider once via HTTP, then add two models sharing that Provider without repeating baseUrl/apiKey.
	createProvRec := doReq(http.MethodPost, "/api/v1/admin/model-gateway/providers", `{"id":"openai-main","displayName":"OpenAI Main","baseUrl":"https://1.1.1.1/v1","apiKey":"`+initialKey+`","enabled":true}`, adminCookie, true, "https://teamie.example")
	if createProvRec.Code != 201 || strings.Contains(createProvRec.Body.String(), initialKey) {
		t.Fatalf("provider create failed or leaked secret: code=%d body=%s", createProvRec.Code, createProvRec.Body.String())
	}
	if rec := doReq(http.MethodPost, "/api/v1/admin/model-gateway/models", `{"id":"model-a","providerId":"openai-main","displayName":"GPT-4o","model":"gpt-4o","contextWindowTokens":32768,"enabled":true}`, adminCookie, true, "https://teamie.example"); rec.Code != 201 {
		t.Fatalf("model-a create failed: code=%d body=%s", rec.Code, rec.Body.String())
	}
	if rec := doReq(http.MethodPost, "/api/v1/admin/model-gateway/models", `{"id":"model-b","providerId":"openai-main","displayName":"GPT-4o Mini","model":"gpt-4o-mini","contextWindowTokens":131072,"enabled":true}`, adminCookie, true, "https://teamie.example"); rec.Code != 201 {
		t.Fatalf("model-b create failed: code=%d body=%s", rec.Code, rec.Body.String())
	}
	if rec := doReq(http.MethodPut, "/api/v1/admin/model-gateway/routing", `{"defaultModelId":"model-a"}`, adminCookie, true, "https://teamie.example"); rec.Code != 200 {
		t.Fatalf("routing update failed: %d", rec.Code)
	}

	// Verify admin response shape & redaction.
	adminGet := doReq(http.MethodGet, "/api/v1/admin/model-gateway", "", adminCookie, true, "")
	if adminGet.Code != 200 || strings.Contains(adminGet.Body.String(), initialKey) {
		t.Fatalf("admin GET failed or leaked key: %d %s", adminGet.Code, adminGet.Body.String())
	}
	var adminResp gatewaygen.AdminModelConfigResp
	if err := json.Unmarshal(adminGet.Body.Bytes(), &adminResp); err != nil {
		t.Fatal(err)
	}
	if len(adminResp.Providers) != 1 || adminResp.Providers[0].ID != "openai-main" || adminResp.Providers[0].ModelCount != 2 || !adminResp.Providers[0].HasApiKey {
		t.Fatalf("unexpected admin providers DTO: %+v", adminResp.Providers)
	}
	if len(adminResp.Models) != 2 || adminResp.Models[0].ProviderId != "openai-main" || adminResp.Models[1].ProviderId != "openai-main" || !adminResp.Models[0].EffectiveEnabled || !adminResp.Models[1].EffectiveEnabled {
		t.Fatalf("unexpected admin models DTO: %+v", adminResp.Models)
	}

	// Verify public catalog exposes zero provider/baseUrl/apiKey/upstream-model details to ordinary users.
	pubGet := doReq(http.MethodGet, "/api/v1/model-gateway/catalog", "", userCookie, true, "")
	if pubGet.Code != 200 {
		t.Fatalf("public catalog GET failed: %d %s", pubGet.Code, pubGet.Body.String())
	}
	pubRaw := pubGet.Body.String()
	for _, forbidden := range []string{"openai-main", "OpenAI Main", "1.1.1.1", "baseUrl", "providerId", "hasApiKey", initialKey} {
		if strings.Contains(pubRaw, forbidden) {
			t.Fatalf("public catalog leaked %q: %s", forbidden, pubRaw)
		}
	}

	// Point provider BaseURL in store to the local TLS fixture for live protocol calls & independent model tests.
	_, _ = store.UpdateCatalog(context.Background(), func(providers []ProviderProfile, models []ModelProfile) ([]ProviderProfile, []ModelProfile, error) {
		providers[0].BaseURL = fixture.URL + "/v1"
		return providers, models, nil
	})

	// 3. Test Model A and Model B independently via POST /models/:id/test.
	testA := doReq(http.MethodPost, "/api/v1/admin/model-gateway/models/model-a/test", `{}`, adminCookie, true, "https://teamie.example")
	testB := doReq(http.MethodPost, "/api/v1/admin/model-gateway/models/model-b/test", `{}`, adminCookie, true, "https://teamie.example")
	if testA.Code != 200 || !strings.Contains(testA.Body.String(), `"model":"gpt-4o"`) {
		t.Fatalf("model-a test failed: %d %s", testA.Code, testA.Body.String())
	}
	if testB.Code != 200 || !strings.Contains(testB.Body.String(), `"model":"gpt-4o-mini"`) {
		t.Fatalf("model-b test failed: %d %s", testB.Code, testB.Body.String())
	}

	// 4. Rotate Provider API key once -> both Model A and Model B immediately use rotatedKey, preserving CountingConfigID.
	cfgBefore, _ := store.Load(context.Background())
	cfgIDA := cfgBefore.ResolveModel("model-a").CountingConfigID()
	cfgIDB := cfgBefore.ResolveModel("model-b").CountingConfigID()

	rotRec := doReq(http.MethodPatch, "/api/v1/admin/model-gateway/providers/openai-main", `{"apiKey":"`+rotatedKey+`"}`, adminCookie, true, "https://teamie.example")
	if rotRec.Code != 200 || strings.Contains(rotRec.Body.String(), rotatedKey) {
		t.Fatalf("provider key rotation failed or leaked key: %d %s", rotRec.Code, rotRec.Body.String())
	}
	cfgAfter, _ := store.Load(context.Background())
	if cfgAfter.ResolveModel("model-a").CountingConfigID() != cfgIDA || cfgAfter.ResolveModel("model-b").CountingConfigID() != cfgIDB {
		t.Fatal("rotating provider API key unexpectedly changed CountingConfigID")
	}

	bModelID := "model-b"
	chatResp, err := fixtureHTTPChat(t, s, context.Background(), &agent.GatewayChatRequest{
		ServiceToken: testToken,
		OwnerId:      "owner",
		AgentId:      "agent-1",
		SessionId:    "sess-1",
		RunId:        "run-rot",
		ModelId:      &bModelID,
		RequestJson:  fixtureNativeRequest(`{"messages":[{"role":"user","content":"hello"}]}`, false),
	})
	if err != nil || !chatResp.Ok {
		t.Fatalf("call after key rotation failed: %+v %v", chatResp, err)
	}
	mu.Lock()
	if len(calls) != 3 || calls[0].auth != "Bearer "+initialKey || calls[0].model != "gpt-4o" || calls[1].auth != "Bearer "+initialKey || calls[1].model != "gpt-4o-mini" || calls[2].auth != "Bearer "+rotatedKey || calls[2].model != "gpt-4o-mini" {
		t.Fatalf("unexpected upstream calls sequence: %+v", calls)
	}
	mu.Unlock()

	// 5. Add backup Provider + Model C, then disable openai-main -> catalog hides A & B, TestAdminModelProfile rejects A with 409 model_disabled (no fallback!), while Agent calls smoothly fall back to Model C without rewriting DefaultModelID.
	encBackup, _ := crypto.Encrypt(backupKey)
	_, _ = store.UpdateCatalog(context.Background(), func(providers []ProviderProfile, models []ModelProfile) ([]ProviderProfile, []ModelProfile, error) {
		providers = append(providers, ProviderProfile{
			ID:          "backup-prov",
			DisplayName: "Backup Provider",
			BaseURL:     fixture.URL + "/v1",
			CipherKey:   encBackup,
			Enabled:     true,
		})
		models = append(models, ModelProfile{
			ID:                  "model-c",
			ProviderID:          "backup-prov",
			DisplayName:         "Claude Fallback",
			Model:               "claude-fallback",
			ContextWindowTokens: 65536,
			Enabled:             true,
		})
		return providers, models, nil
	})

	if rec := doReq(http.MethodPatch, "/api/v1/admin/model-gateway/providers/openai-main", `{"enabled":false}`, adminCookie, true, "https://teamie.example"); rec.Code != 200 {
		t.Fatalf("disable provider failed: %d", rec.Code)
	}
	if rec := doReq(http.MethodPost, "/api/v1/admin/model-gateway/models/model-a/test", `{}`, adminCookie, true, "https://teamie.example"); rec.Code != 409 || !strings.Contains(rec.Body.String(), "model_disabled") {
		t.Fatalf("expected 409 model_disabled when testing model under disabled provider, got %d %s", rec.Code, rec.Body.String())
	}
	pubDisabled := doReq(http.MethodGet, "/api/v1/model-gateway/catalog", "", userCookie, true, "")
	if strings.Contains(pubDisabled.Body.String(), "model-a") || strings.Contains(pubDisabled.Body.String(), "model-b") || !strings.Contains(pubDisabled.Body.String(), "model-c") {
		t.Fatalf("disabled provider models remained in public catalog: %s", pubDisabled.Body.String())
	}
	aModelID := "model-a"
	fallbackChat, err := fixtureHTTPChat(t, s, context.Background(), &agent.GatewayChatRequest{
		ServiceToken: testToken,
		OwnerId:      "owner",
		AgentId:      "agent-1",
		SessionId:    "sess-1",
		RunId:        "run-fb",
		ModelId:      &aModelID,
		RequestJson:  fixtureNativeRequest(`{"messages":[{"role":"user","content":"fallback"}]}`, false),
	})
	if err != nil || !fallbackChat.Ok {
		t.Fatalf("fallback to backup provider failed: %+v %v", fallbackChat, err)
	}
	cfgDisabled, _ := store.Load(context.Background())
	if cfgDisabled.Routing.DefaultModelID != "model-a" {
		t.Fatalf("fallback mutated DefaultModelID: %q", cfgDisabled.Routing.DefaultModelID)
	}

	// 6. Non-empty provider delete returns 409 provider_in_use; reassigning/deleting models allows clean provider deletion.
	if rec := doReq(http.MethodDelete, "/api/v1/admin/model-gateway/providers/openai-main", "", adminCookie, true, "https://teamie.example"); rec.Code != 409 || !strings.Contains(rec.Body.String(), "provider_in_use") {
		t.Fatalf("expected 409 provider_in_use on non-empty provider delete, got %d %s", rec.Code, rec.Body.String())
	}
	if rec := doReq(http.MethodPatch, "/api/v1/admin/model-gateway/models/model-a", `{"providerId":"backup-prov"}`, adminCookie, true, "https://teamie.example"); rec.Code != 200 {
		t.Fatalf("reassigning model-a providerId failed: %d %s", rec.Code, rec.Body.String())
	}
	if rec := doReq(http.MethodDelete, "/api/v1/admin/model-gateway/models/model-b", "", adminCookie, true, "https://teamie.example"); rec.Code != 200 {
		t.Fatalf("deleting model-b failed: %d %s", rec.Code, rec.Body.String())
	}
	if rec := doReq(http.MethodDelete, "/api/v1/admin/model-gateway/providers/openai-main", "", adminCookie, true, "https://teamie.example"); rec.Code != 200 {
		t.Fatalf("deleting empty provider openai-main failed: %d %s", rec.Code, rec.Body.String())
	}
}
