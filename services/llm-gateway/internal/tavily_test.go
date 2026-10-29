package gateway_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"teamie/services/llm-gateway/internal/app"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/cloudwego/kitex/client/callopt"
	"teamie/contracts/gen/agent/hertz_gen/agent/modeltransport"
	"teamie/contracts/gen/agent/kitex_gen/agent"
	"teamie/contracts/gen/im/kitex_gen/im"
	rpcx "teamie/services/llm-gateway/internal/infra/rpcclient"
)

type stubSessionValidator struct{}

func (stubSessionValidator) ValidateSession(_ context.Context, r *im.SessionRequest, _ ...callopt.Option) (*im.SessionResponse, error) {
	if r == nil || r.Token != strings.Repeat("a", 64) {
		return &im.SessionResponse{Valid: false}, nil
	}
	csrf := testToken
	return &im.SessionResponse{
		Valid:     true,
		CsrfToken: &csrf,
		User:      &im.User{Id: "admin-1", PlatformRole: "admin", Kind: "human"},
	}, nil
}

func testAdminAuth() rpcx.AdminAuth {
	return rpcx.AdminAuth{Identity: stubSessionValidator{}, Origin: "https://teamie.example"}
}

func TestWebSearchAdminConfigRedactionAndPreservation(t *testing.T) {
	crypto := testCipher(t)
	aKey, _ := crypto.Encrypt("fixture-a")
	bKey, _ := crypto.Encrypt("fixture-b")
	store := &memoryStore{c: Config{
		Providers: []ProviderProfile{
			{ID: "pa", DisplayName: "Provider A", BaseURL: "https://1.1.1.1/v1", CipherKey: aKey, Enabled: true},
			{ID: "pb", DisplayName: "Provider B", BaseURL: "https://1.1.1.1/v1", CipherKey: bKey, Enabled: true},
		},
		Models: []ModelProfile{
			{ID: "a", ProviderID: "pa", Model: "a", Enabled: false},
			{ID: "b", ProviderID: "pb", Model: "b", Enabled: true},
		},
		Routing: ModelRoutingConfig{DefaultModelID: "a"},
	}}
	service := New(store, crypto, testToken)
	handler := service.Routes(testAdminAuth())
	do := func(method, path, body string, authenticated, csrf bool) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, "https://teamie.example/api/v1/admin/model-gateway"+path, strings.NewReader(body))
		if authenticated {
			req.AddCookie(&http.Cookie{Name: "teamie_session", Value: strings.Repeat("a", 64)})
		}
		req.Header.Set("Origin", "https://teamie.example")
		req.Header.Set("Content-Type", "application/json")
		if csrf {
			req.Header.Set("X-CSRF-Token", testToken)
		}
		out := httptest.NewRecorder()
		handler.ServeHTTP(out, req)
		return out
	}
	for _, path := range []string{"/web-search", "/routing"} {
		method, body := http.MethodPatch, `{"enabled":false}`
		if path == "/routing" {
			method, body = http.MethodPut, `{"defaultModelId":"b"}`
		}
		if got := do(method, path, body, false, true); got.Code != 401 {
			t.Fatal("unauthenticated write accepted", got.Code)
		}
		if got := do(method, path, body, true, false); got.Code != 403 {
			t.Fatal("write without CSRF accepted", got.Code)
		}
	}
	before, _ := store.Load(context.Background())
	key := "fixture-search-secret"
	saved := do(http.MethodPatch, "/web-search", `{"enabled":true,"apiKey":"`+key+`"}`, true, true)
	if saved.Code != 200 || strings.Contains(saved.Body.String(), key) {
		t.Fatal("search save failed or leaked secret", saved.Code)
	}
	after, _ := store.Load(context.Background())
	if !reflect.DeepEqual(before.Providers, after.Providers) || !reflect.DeepEqual(before.Models, after.Models) || before.Routing != after.Routing {
		t.Fatal("search changed providers, models or routing")
	}
	if got := do(http.MethodPatch, "/web-search", `{"enabled":false}`, true, true); got.Code != 200 {
		t.Fatal(got.Code)
	}
	after, _ = store.Load(context.Background())
	plain, _ := crypto.Decrypt(after.WebSearch.CipherKey)
	if plain != key || after.WebSearch.Enabled {
		t.Fatal("omitted search key not preserved")
	}
	if got := do(http.MethodPatch, "/web-search", `{"apiKey":"fixture-new","clearApiKey":true}`, true, true); got.Code != 400 {
		t.Fatal("conflicting credential intent accepted")
	}
	// A stale whole-config form is rejected before any writes.
	before, _ = store.Load(context.Background())
	if got := do(http.MethodPut, "", `{"baseUrl":"https://1.1.1.1/v1","model":"b","enabled":true,"webSearchEnabled":true}`, true, true); got.Code != 409 {
		t.Fatal("legacy write should require refresh", got.Code)
	}
	after, _ = store.Load(context.Background())
	if !reflect.DeepEqual(before, after) {
		t.Fatal("retired config endpoint changed settings")
	}
	if got := do(http.MethodPatch, "/web-search", `{"enabled":false,"clearApiKey":true}`, true, true); got.Code != 200 {
		t.Fatal(got.Code)
	}
	after, _ = store.Load(context.Background())
	if after.WebSearch.Configured() {
		t.Fatal("explicit search key clear failed")
	}
	// Search cancellation is independent even when there are no usable models.
	modelCtx, done := service.App.RegisterRunCancel(context.Background(), "a", "run", "default")
	defer done()
	webCtx, webDone := service.App.RegisterWebCancel(context.Background(), "a", "run")
	defer webDone()
	no := false
	if _, err := service.App.UpdateWebSearch(context.Background(), app.UpdateWebSearchInput{Enabled: &no}); err != nil {
		t.Fatal(err)
	}
	if modelCtx.Err() != nil || webCtx.Err() == nil {
		t.Fatal("search update crossed cancellation boundary")
	}
}

func TestTavilySearchAndExtractProtocolAndValidation(t *testing.T) {
	tavilyKey := "tvly-protocol-fixture-key"
	var lastPath string
	var lastAuth string
	var lastPayload map[string]any

	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		lastPath = r.URL.Path
		lastAuth = r.Header.Get("Authorization")
		_ = json.NewDecoder(r.Body).Decode(&lastPayload)
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/search":
			longSnippet := strings.Repeat("中文摘要内容", 200)
			_, _ = w.Write([]byte(`{"results":[{"title":"Go 1.24 Release Notes","url":"https://go.dev/doc/go1.24","content":"` + longSnippet + `","published_date":"2026-02-01","score":0.95}]}`))
		case "/extract":
			urls, _ := lastPayload["urls"].([]any)
			if len(urls) == 1 && urls[0] == "https://blocked.example.com/paywall" {
				_, _ = w.Write([]byte(`{"results":[],"failed_results":[{"url":"https://blocked.example.com/paywall","error":"Paywall or anti-bot blocked"}]}`))
				return
			}
			_, _ = w.Write([]byte(`{"results":[{"url":"https://go.dev/doc/go1.24","raw_content":"# Go 1.24\nClean markdown body."}],"failed_results":[{"url":"https://go.dev/missing","error":"404 Not Found"}]}`))
		default:
			w.WriteHeader(404)
		}
	}))
	defer upstream.Close()

	crypto := testCipher(t)
	encryptedWebKey, _ := crypto.Encrypt(tavilyKey)
	store := &memoryStore{c: Config{WebSearch: WebSearchConfig{CipherKey: encryptedWebKey, Enabled: true}}}
	s := New(store, crypto, testToken)
	s.WebSearchURL = upstream.URL
	s.Provider = Provider{HTTP: upstream.Client()}
	srv := httptest.NewServer(s.Routes(testAdminAuth()))
	defer srv.Close()

	postJSON := func(path, token string, body any, out any) int {
		raw, _ := json.Marshal(body)
		req, err := http.NewRequest(http.MethodPost, srv.URL+path, bytes.NewReader(raw))
		if err != nil {
			t.Fatal(err)
		}
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		_ = json.NewDecoder(resp.Body).Decode(out)
		return resp.StatusCode
	}

	// 1. Unauthorized check
	var searchRes modeltransport.WebSearchResponse
	if code := postJSON(modeltransport.WebSearchPath, "wrong", modeltransport.WebSearchRequest{}, &searchRes); code != 401 {
		t.Fatalf("expected 401, got %d", code)
	}

	// 2. Valid search call enforces basic depth and truncates snippets safely on UTF-8 boundary
	searchReq := modeltransport.WebSearchRequest{
		OwnerID:        "owner-1",
		AgentID:        "agent-1",
		SessionID:      "agent:agent-1",
		RunID:          "run-1:attempt-1",
		Query:          "Go 1.24 release notes",
		MaxResults:     3,
		TimeRange:      "month",
		IncludeDomains: []string{"Go.Dev"},
	}
	if code := postJSON(modeltransport.WebSearchPath, testToken, searchReq, &searchRes); code != 200 || !searchRes.OK {
		t.Fatalf("search failed: code=%d res=%+v", code, searchRes)
	}
	if lastPath != "/search" || lastAuth != "Bearer "+tavilyKey {
		t.Fatalf("unexpected upstream request: path=%s auth=%s", lastPath, lastAuth)
	}
	if lastPayload["search_depth"] != "basic" || lastPayload["include_answer"] != false || lastPayload["include_raw_content"] != false || lastPayload["include_images"] != false || lastPayload["time_range"] != "month" {
		t.Fatalf("unexpected search payload policy: %+v", lastPayload)
	}
	if len(searchRes.Results) != 1 || searchRes.Results[0].URL != "https://go.dev/doc/go1.24" || len(searchRes.Results[0].Snippet) > maxSearchSnippetBytes || !utf8.ValidString(searchRes.Results[0].Snippet) {
		t.Fatalf("unexpected search result: %+v", searchRes.Results)
	}

	// 3. Extract URL validation rejects internal/private/invalid URLs before calling Tavily
	lastPath = ""
	for _, badURLs := range [][]string{
		{},
		{"ftp://example.com/file"},
		{"http://localhost:8080/internal"},
		{"http://chat:8082/api/v1/bootstrap"},
		{"http://127.0.0.1/admin"},
		{"https://169.254.169.254/latest/meta-data"},
		{"https://10.0.0.8/secret"},
		{"https://user:pass@example.com/page"},
		{"https://example.com/page#fragment"},
		{"https://a.com/1", "https://a.com/2", "https://a.com/3", "https://a.com/4", "https://a.com/5", "https://a.com/6"},
	} {
		var extractRes modeltransport.WebExtractResponse
		req := modeltransport.WebExtractRequest{OwnerID: "o", AgentID: "a", SessionID: "s", RunID: "r", URLs: badURLs}
		if code := postJSON(modeltransport.WebExtractPath, testToken, req, &extractRes); code != 200 || extractRes.OK || extractRes.ErrorCode != "invalid_request" {
			t.Fatalf("accepted unsafe or invalid URLs %v: code=%d res=%+v", badURLs, code, extractRes)
		}
	}
	if lastPath != "" {
		t.Fatalf("unsafe URL reached upstream Tavily server: %s", lastPath)
	}

	// 4. Valid extract call with partial failure returns both results and failedResults
	var extractRes modeltransport.WebExtractResponse
	extractReq := modeltransport.WebExtractRequest{
		OwnerID:   "owner-1",
		AgentID:   "agent-1",
		SessionID: "agent:agent-1",
		RunID:     "run-1:attempt-1",
		URLs:      []string{"https://go.dev/doc/go1.24", "https://go.dev/missing"},
	}
	if code := postJSON(modeltransport.WebExtractPath, testToken, extractReq, &extractRes); code != 200 || !extractRes.OK {
		t.Fatalf("extract failed: code=%d res=%+v", code, extractRes)
	}
	if lastPath != "/extract" || lastPayload["extract_depth"] != "basic" || lastPayload["format"] != "markdown" || lastPayload["include_images"] != false {
		t.Fatalf("unexpected extract payload policy: %+v", lastPayload)
	}
	if len(extractRes.Results) != 1 || extractRes.Results[0].Content != "# Go 1.24\nClean markdown body." || len(extractRes.FailedResults) != 1 || extractRes.FailedResults[0].URL != "https://go.dev/missing" {
		t.Fatalf("unexpected extract output: %+v", extractRes)
	}

	// 5. All URLs failing returns web_extract_failed with no local fallback
	extractReq.URLs = []string{"https://blocked.example.com/paywall"}
	if code := postJSON(modeltransport.WebExtractPath, testToken, extractReq, &extractRes); code != 200 || extractRes.OK || extractRes.ErrorCode != "web_extract_failed" || len(extractRes.FailedResults) != 1 {
		t.Fatalf("expected web_extract_failed when all URLs fail, got: code=%d res=%+v", code, extractRes)
	}
}

func TestWebSearchCancellationStopsInFlightTavilyRequest(t *testing.T) {
	started := make(chan struct{})
	canceled := make(chan struct{})
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		close(started)
		<-r.Context().Done()
		close(canceled)
	}))
	defer upstream.Close()

	crypto := testCipher(t)
	encryptedWebKey, _ := crypto.Encrypt("tvly-cancel-key")
	store := &memoryStore{c: Config{WebSearch: WebSearchConfig{CipherKey: encryptedWebKey, Enabled: true}}}
	s := New(store, crypto, testToken)
	s.WebSearchURL = upstream.URL
	s.Provider = Provider{HTTP: upstream.Client()}
	srv := httptest.NewServer(s.Routes(testAdminAuth()))
	defer srv.Close()

	done := make(chan modeltransport.WebSearchResponse, 1)
	go func() {
		raw, _ := json.Marshal(modeltransport.WebSearchRequest{
			OwnerID:   "owner",
			AgentID:   "agent-cancel",
			SessionID: "agent:agent-cancel",
			RunID:     "run-cancel:attempt-1",
			Query:     "long running query",
		})
		req, _ := http.NewRequest(http.MethodPost, srv.URL+modeltransport.WebSearchPath, bytes.NewReader(raw))
		req.Header.Set("Authorization", "Bearer "+testToken)
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return
		}
		defer resp.Body.Close()
		var out modeltransport.WebSearchResponse
		_ = json.NewDecoder(resp.Body).Decode(&out)
		done <- out
	}()

	<-started
	ack, err := s.Cancel(context.Background(), &agent.CancelRequest{ServiceToken: testToken, AgentId: "agent-cancel", RunId: "run-cancel:attempt-1"})
	if err != nil || !ack.Ok {
		t.Fatalf("cancel failed: %+v %v", ack, err)
	}
	select {
	case res := <-done:
		if res.OK || res.ErrorCode != "canceled" {
			t.Fatalf("expected canceled error code, got %+v", res)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("web search request did not return after CancelRun")
	}
	select {
	case <-canceled:
	case <-time.After(3 * time.Second):
		t.Fatal("upstream Tavily handler did not observe context cancellation")
	}
}
