package gateway_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/cloudwego/kitex/client/callopt"
	"teamie/pkg/rpcx"
	"teamie/contracts/gen/agent/kitex_gen/agent"
	"teamie/contracts/gen/agent/modeltransport"
	"teamie/contracts/gen/im/kitex_gen/im"
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
	store := &memoryStore{}
	s := New(store, crypto, testToken)
	srv := httptest.NewServer(s.Routes(testAdminAuth()))
	defer srv.Close()

	doAdmin := func(method, body string) (int, map[string]any, string) {
		var reader io.Reader
		if body != "" {
			reader = strings.NewReader(body)
		}
		req, err := http.NewRequest(method, srv.URL+"/api/v1/admin/model-gateway", reader)
		if err != nil {
			t.Fatal(err)
		}
		req.AddCookie(&http.Cookie{Name: "teamie_session", Value: strings.Repeat("a", 64)})
		req.Header.Set("Origin", "https://teamie.example")
		req.Header.Set("X-CSRF-Token", testToken)
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		raw, _ := io.ReadAll(resp.Body)
		var out map[string]any
		_ = json.Unmarshal(raw, &out)
		return resp.StatusCode, out, string(raw)
	}

	status, initial, _ := doAdmin(http.MethodGet, "")
	if status != 200 {
		t.Fatalf("unexpected status: %d", status)
	}
	cfg := initial["config"].(map[string]any)
	ws := cfg["webSearch"].(map[string]any)
	if ws["provider"] != "tavily" || ws["enabled"] != false || ws["hasApiKey"] != false || ws["configured"] != false {
		t.Fatalf("unexpected initial webSearch view: %+v", ws)
	}

	secretKey := "tvly-secret-fixture-key-999"
	status, saved, rawSaved := doAdmin(http.MethodPut, `{"baseUrl":"https://1.1.1.1/v1","model":"gpt-test","enabled":false,"webSearchEnabled":true,"webSearchApiKey":"`+secretKey+`"}`)
	if status != 200 || strings.Contains(rawSaved, secretKey) {
		t.Fatalf("save failed or leaked key: status=%d body=%s", status, rawSaved)
	}
	ws = saved["config"].(map[string]any)["webSearch"].(map[string]any)
	if ws["enabled"] != true || ws["hasApiKey"] != true || ws["configured"] != true {
		t.Fatalf("unexpected saved webSearch view: %+v", ws)
	}

	// Omitting webSearch fields when updating model settings must preserve them.
	status, preserved, rawPreserved := doAdmin(http.MethodPut, `{"baseUrl":"https://1.1.1.1/v1","model":"gpt-next","enabled":false}`)
	if status != 200 || strings.Contains(rawPreserved, secretKey) {
		t.Fatalf("preserve failed: status=%d body=%s", status, rawPreserved)
	}
	ws = preserved["config"].(map[string]any)["webSearch"].(map[string]any)
	if ws["enabled"] != true || ws["hasApiKey"] != true || ws["configured"] != true {
		t.Fatalf("omitted webSearch config was not preserved: %+v", ws)
	}

	// Simultaneous key and clear must fail with 400.
	status, _, _ = doAdmin(http.MethodPut, `{"baseUrl":"https://1.1.1.1/v1","model":"gpt-next","enabled":false,"webSearchApiKey":"tvly-new","clearWebSearchApiKey":true}`)
	if status != 400 {
		t.Fatalf("expected 400 when setting and clearing key simultaneously, got %d", status)
	}

	// Explicit clear removes the key and disables configured state.
	status, cleared, _ := doAdmin(http.MethodPut, `{"baseUrl":"https://1.1.1.1/v1","model":"gpt-next","enabled":false,"webSearchEnabled":false,"clearWebSearchApiKey":true}`)
	if status != 200 {
		t.Fatalf("clear failed: %d", status)
	}
	ws = cleared["config"].(map[string]any)["webSearch"].(map[string]any)
	if ws["enabled"] != false || ws["hasApiKey"] != false || ws["configured"] != false {
		t.Fatalf("expected cleared webSearch state, got %+v", ws)
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
	store := &memoryStore{c: Config{WebSearchCipherKey: encryptedWebKey, WebSearchEnabled: true}}
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
	store := &memoryStore{c: Config{WebSearchCipherKey: encryptedWebKey, WebSearchEnabled: true}}
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
