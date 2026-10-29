package gateway_test

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"teamie/contracts/gen/agent/kitex_gen/agent"
	"teamie/services/llm-gateway/internal/app"
	rpcx "teamie/services/llm-gateway/internal/infra/rpcclient"
	"testing"
	"time"
)

const testToken = "0123456789abcdef0123456789abcdef"

type memoryStore struct {
	mu    sync.Mutex
	c     Config
	usage []Usage
}

func (m *memoryStore) Ready(context.Context) error { return nil }
func (m *memoryStore) Load(context.Context) (Config, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.c, nil
}
func (m *memoryStore) UpdateCatalog(_ context.Context, change func([]ProviderProfile, []ModelProfile) ([]ProviderProfile, []ModelProfile, error)) (Config, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	providers, models, err := change(append([]ProviderProfile(nil), m.c.Providers...), append([]ModelProfile(nil), m.c.Models...))
	if err != nil {
		return Config{}, err
	}
	m.c.Providers = providers
	m.c.Models = models
	return m.c, nil
}
func (m *memoryStore) UpdateRouting(_ context.Context, routing ModelRoutingConfig) (Config, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.c.Routing = routing
	return m.c, nil
}
func (m *memoryStore) UpdateWebSearch(_ context.Context, change func(WebSearchConfig) (WebSearchConfig, error)) (Config, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	c, err := change(m.c.WebSearch)
	if err != nil {
		return Config{}, err
	}
	m.c.WebSearch = c
	return m.c, nil
}
func (m *memoryStore) Record(_ context.Context, u Usage) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.usage = append(m.usage, u)
	return nil
}
func testCipher(t *testing.T) Cipher {
	t.Helper()
	c, e := NewCipher(strings.Repeat("ab", 32))
	if e != nil {
		t.Fatal(e)
	}
	return c
}
func TestEncryptionAndWriteOnlyConfig(t *testing.T) {
	crypto := testCipher(t)
	key := "secret-provider-key"
	a, e := crypto.Encrypt(key)
	if e != nil {
		t.Fatal(e)
	}
	b, _ := crypto.Encrypt(key)
	if string(a) == string(b) || strings.Contains(string(a), key) {
		t.Fatal("ciphertext must use fresh nonce")
	}
	plain, e := crypto.Decrypt(a)
	if e != nil || plain != key {
		t.Fatal(e)
	}
	a[len(a)-1] ^= 1
	if _, e = crypto.Decrypt(a); e == nil {
		t.Fatal("modified ciphertext accepted")
	}
	store := &memoryStore{}
	service := New(store, crypto, testToken)
	if _, err := service.App.CreateModelProvider(context.Background(), app.CreateModelProviderInput{ID: "default", DisplayName: "Default Provider", BaseURL: "https://1.1.1.1/v1", Enabled: true, APIKey: &key}); err != nil {
		t.Fatal(err)
	}
	c, err := service.App.CreateModelProfile(context.Background(), app.CreateModelProfileInput{ID: "default", ProviderID: "default", Model: "model", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	dto, err := service.HTTP.GetAdminModelConfig(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	view, _ := json.Marshal(dto)
	if strings.Contains(string(view), key) || !strings.Contains(string(view), `"hasApiKey":true`) {
		t.Fatal(string(view))
	}
	name := "next"
	c, err = service.App.UpdateModelProfile(context.Background(), app.UpdateModelProfileInput{ID: "default", Model: &name})
	if err != nil || !c.ResolveModel("").Configured() {
		t.Fatal("omitted provider key was removed during model update", err)
	}
	providerLabel := "Updated Provider"
	c, err = service.App.UpdateModelProvider(context.Background(), app.UpdateModelProviderInput{ID: "default", DisplayName: &providerLabel})
	if err != nil || !c.ResolveModel("").Configured() {
		t.Fatal("omitted provider key was removed during provider update", err)
	}
	c, err = service.App.UpdateModelProvider(context.Background(), app.UpdateModelProviderInput{ID: "default", ClearAPIKey: true})
	if err != nil || c.ResolveModel("").Configured() {
		t.Fatal("explicit provider key clear failed", err)
	}

}
func TestEndpointPolicy(t *testing.T) {
	for _, raw := range []string{"http://example.com/v1", "https://user:key@example.com/v1", "https://127.0.0.1/v1", "https://169.254.169.254/latest", "https://[::1]/v1", "https://example.com/v1?token=secret", "https://example.com/v1#key", "https://example.com/v1/chat/completions"} {
		if e := ValidateEndpoint(context.Background(), raw); e == nil {
			t.Fatalf("accepted unsafe endpoint %s", raw)
		}
	}
	for _, ip := range []string{"127.0.0.1", "10.2.3.4", "100.100.100.200", "192.168.1.2", "169.254.169.254", "198.18.0.1", "::1", "fd00::1", "::ffff:127.0.0.1", "64:ff9b::a00:1"} {
		if PublicIP(net.ParseIP(ip)) {
			t.Fatal(ip)
		}
	}
	if !PublicIP(net.ParseIP("8.8.8.8")) {
		t.Fatal("public address rejected")
	}
}
func TestProtocolFixtureModelLimitsToolsAndUsage(t *testing.T) {
	key := "fixture-key-no-real-provider"
	var observed map[string]json.RawMessage
	fixture := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer "+key {
			t.Errorf("incorrect provider request")
		}
		_ = json.NewDecoder(r.Body).Decode(&observed)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl-fixture","object":"chat.completion","created":1,"model":"configured","choices":[{"index":0,"message":{"role":"assistant","content":null,"tool_calls":[{"id":"call-1","type":"function","function":{"name":"exec","arguments":"{\"command\":\"pwd\"}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":12,"completion_tokens":8,"total_tokens":20}}`))
	}))
	defer fixture.Close()
	crypto := testCipher(t)
	encrypted, _ := crypto.Encrypt(key)
	store := &memoryStore{c: singleModel(
		ProviderProfile{BaseURL: fixture.URL + "/v1", CipherKey: encrypted, Enabled: true},
		ModelProfile{Model: "configured", Enabled: true},
	)}
	s := New(store, crypto, testToken)
	s.Provider = Provider{HTTP: fixture.Client()}
	r, e := fixtureHTTPChat(t, s, context.Background(), &agent.GatewayChatRequest{ServiceToken: testToken, OwnerId: "owner", AgentId: "agent", SessionId: "session", RunId: "run", RequestJson: fixtureNativeRequest(`{"model":"bypass","messages":[{"role":"user","content":"hello"}],"max_tokens":262144,"tools":[{"type":"function","function":{"name":"exec","parameters":{"type":"object"}}}]}`, false)})
	if e != nil || !r.Ok {
		t.Fatalf("%+v %v", r, e)
	}
	if string(observed["model"]) != `"configured"` || string(observed["max_completion_tokens"]) != "131072" || !strings.Contains(r.GetResponseJson(), "function_tool_call") {
		t.Fatal(observed)
	}
	if len(store.usage) != 1 || store.usage[0].OwnerID != "owner" || store.usage[0].PromptTokens != 12 || store.usage[0].CompletionTokens != 8 {
		t.Fatal(store.usage)
	}
}
func TestUnconfiguredAndUnauthorizedNeverCallProvider(t *testing.T) {
	s := New(&memoryStore{}, testCipher(t), testToken)
	bad, _ := fixtureHTTPChat(t, s, context.Background(), &agent.GatewayChatRequest{ServiceToken: "wrong"})
	if bad.GetErrorCode() != "unauthorized" {
		t.Fatal(bad)
	}
	r, _ := fixtureHTTPChat(t, s, context.Background(), &agent.GatewayChatRequest{ServiceToken: testToken, OwnerId: "o", AgentId: "a", SessionId: "s", RunId: "r", RequestJson: `{}`})
	if r.GetErrorCode() != "model_unconfigured" {
		t.Fatal(r)
	}
	ping, _ := s.Ping(context.Background(), &agent.PingRequest{ServiceToken: &s.Token})
	if !ping.Ready || ping.GetConfigured() {
		t.Fatal(ping)
	}
}
func TestCancellationAndRedactedUpstreamErrors(t *testing.T) {
	key := "fixture-key-for-cancellation"
	started := make(chan struct{})
	canceled := make(chan struct{})
	fixture := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		close(started)
		<-r.Context().Done()
		close(canceled)
	}))
	defer fixture.Close()
	crypto := testCipher(t)
	encrypted, _ := crypto.Encrypt(key)
	s := New(&memoryStore{c: singleModel(
		ProviderProfile{BaseURL: fixture.URL, CipherKey: encrypted, Enabled: true},
		ModelProfile{Model: "fixture", Enabled: true},
	)}, crypto, testToken)
	s.Provider = Provider{HTTP: fixture.Client()}
	done := make(chan *agent.GatewayChatResponse, 1)
	go func() {
		r, _ := fixtureHTTPChat(t, s, context.Background(), &agent.GatewayChatRequest{ServiceToken: testToken, OwnerId: "o", AgentId: "a", SessionId: "s", RunId: "r", RequestJson: fixtureNativeRequest(`{"messages":[{"role":"user","content":"hello"}]}`, false)})
		done <- r
	}()
	<-started
	ack, _ := s.Cancel(context.Background(), &agent.CancelRequest{ServiceToken: testToken, AgentId: "a", RunId: "r"})
	if !ack.Ok {
		t.Fatal(ack)
	}
	select {
	case r := <-done:
		if r.GetErrorCode() != "canceled" {
			t.Fatal(r)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("cancellation did not stop provider request")
	}
	select {
	case <-canceled:
	case <-time.After(3 * time.Second):
		t.Fatal("upstream connection did not observe cancellation")
	}
}
func TestRequestLimitsAndNoStream(t *testing.T) {
	for _, raw := range []string{`{"messages":[],"stream":false}`, `{"messages":[{"role":"user","content":"hi"}],"stream":true}`, `{"messages":[{"role":"user","content":"hi"}],"base_url":"http://private"}`, `{"messages":[{"role":"forged"}]}`} {
		if _, e := normalizeRequest(raw, "model"); e == nil {
			t.Fatal(raw)
		}
	}
}

func TestCanceledAttemptDoesNotCancelNewAttempt(t *testing.T) {
	key := "fixture-attempt-key"
	calls := 0
	fixture := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		_, _ = w.Write([]byte(`{"id":"fixture","object":"chat.completion","choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`))
	}))
	defer fixture.Close()
	crypto := testCipher(t)
	encrypted, _ := crypto.Encrypt(key)
	s := New(&memoryStore{c: singleModel(
		ProviderProfile{BaseURL: fixture.URL, CipherKey: encrypted, Enabled: true},
		ModelProfile{Model: "fixture", Enabled: true},
	)}, crypto, testToken)
	s.Provider = Provider{HTTP: fixture.Client()}
	_, _ = s.Cancel(context.Background(), &agent.CancelRequest{ServiceToken: testToken, AgentId: "a", RunId: "durable-run:attempt-1"})
	req := &agent.GatewayChatRequest{ServiceToken: testToken, OwnerId: "owner", AgentId: "a", SessionId: "session", RunId: "durable-run:attempt-1", RequestJson: fixtureNativeRequest(`{"messages":[{"role":"user","content":"hi"}]}`, false)}
	old, _ := fixtureHTTPChat(t, s, context.Background(), req)
	if old.GetErrorCode() != "canceled" || calls != 0 {
		t.Fatal(old, calls)
	}
	req.RunId = "durable-run:attempt-2"
	next, _ := fixtureHTTPChat(t, s, context.Background(), req)
	if !next.Ok || calls != 1 {
		t.Fatal(next, calls)
	}
}

func TestMultiModelRoutingSmoothFallbackAndConcurrentForegroundCalls(t *testing.T) {
	defaultKey := "fixture-default-key"
	r1Key := "fixture-r1-key"
	var mu sync.Mutex
	var observedModels []string
	inFlight := make(chan struct{}, 2)
	releaseBoth := make(chan struct{})
	fixture := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Model string `json:"model"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		observedModels = append(observedModels, body.Model)
		mu.Unlock()
		if body.Model == "concurrent-model" {
			inFlight <- struct{}{}
			<-releaseBoth
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl-multi","object":"chat.completion","choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":3}}`))
	}))
	defer fixture.Close()

	crypto := testCipher(t)
	encDefault, _ := crypto.Encrypt(defaultKey)
	encR1, _ := crypto.Encrypt(r1Key)
	cfg := Config{
		Routing: ModelRoutingConfig{DefaultModelID: "default"},
		Providers: []ProviderProfile{
			{
				ID:          "openai-shared",
				DisplayName: "OpenAI Shared",
				BaseURL:     fixture.URL + "/v1",
				CipherKey:   encDefault,
				Enabled:     true,
			},
			{
				ID:          "deepseek-shared",
				DisplayName: "DeepSeek Shared",
				BaseURL:     fixture.URL + "/v1",
				CipherKey:   encR1,
				Enabled:     true,
			},
		},
		Models: []ModelProfile{
			{
				ID:                  "default",
				ProviderID:          "openai-shared",
				DisplayName:         "Default GPT-4o",
				Model:               "gpt-4o-default",
				ContextWindowTokens: 16384,
				Enabled:             true,
			},
			{
				ID:                  "deepseek-r1",
				ProviderID:          "deepseek-shared",
				DisplayName:         "DeepSeek R1",
				Model:               "deepseek-reasoner",
				ContextWindowTokens: 65536,
				Enabled:             true,
			},
			{
				ID:                  "disabled-model",
				ProviderID:          "deepseek-shared",
				DisplayName:         "Disabled Model",
				Model:               "should-not-be-used",
				ContextWindowTokens: 16384,
				Enabled:             false,
			},
			{
				ID:                  "concurrent",
				ProviderID:          "openai-shared",
				DisplayName:         "Concurrent Model",
				Model:               "concurrent-model",
				ContextWindowTokens: 16384,
				Enabled:             true,
			},
		},
	}
	store := &memoryStore{c: cfg}
	s := New(store, crypto, testToken)
	s.Provider = Provider{HTTP: fixture.Client()}

	// 1. Explicit enabled modelId routes to deepseek-reasoner.
	r1ModelID := "deepseek-r1"
	resp1, err := fixtureHTTPChat(t, s, context.Background(), &agent.GatewayChatRequest{
		ServiceToken: testToken,
		OwnerId:      "owner",
		AgentId:      "agent-1",
		SessionId:    "s-1",
		RunId:        "r-1",
		ModelId:      &r1ModelID,
		RequestJson:  fixtureNativeRequest(`{"messages":[{"role":"user","content":"hi"}]}`, false),
	})
	if err != nil || !resp1.Ok {
		t.Fatalf("expected deepseek-r1 call to succeed: %+v %v", resp1, err)
	}

	// 2. Disabled or deleted modelId smoothly falls back to the platform default model (gpt-4o-default).
	for _, fallbackID := range []string{"disabled-model", "deleted-model", ""} {
		idCopy := fallbackID
		respFallback, err := fixtureHTTPChat(t, s, context.Background(), &agent.GatewayChatRequest{
			ServiceToken: testToken,
			OwnerId:      "owner",
			AgentId:      "agent-1",
			SessionId:    "s-1",
			RunId:        "r-fallback-" + fallbackID,
			ModelId:      &idCopy,
			RequestJson:  fixtureNativeRequest(`{"messages":[{"role":"user","content":"hi"}]}`, false),
		})
		if err != nil || !respFallback.Ok {
			t.Fatalf("expected fallback for %q to succeed: %+v %v", fallbackID, respFallback, err)
		}
	}
	mu.Lock()
	if len(observedModels) != 4 || observedModels[0] != "deepseek-reasoner" || observedModels[1] != "gpt-4o-default" || observedModels[2] != "gpt-4o-default" || observedModels[3] != "gpt-4o-default" {
		t.Fatalf("unexpected routed models: %v", observedModels)
	}
	mu.Unlock()

	// 3. Two concurrent foreground model calls execute in parallel without 429 gateway_busy blocking.
	concModelID := "concurrent"
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for _, agentID := range []string{"agent-a", "agent-b"} {
		wg.Add(1)
		go func(aid string) {
			defer wg.Done()
			res, e := fixtureHTTPChat(t, s, context.Background(), &agent.GatewayChatRequest{
				ServiceToken: testToken,
				OwnerId:      "owner",
				AgentId:      aid,
				SessionId:    "session-" + aid,
				RunId:        "run-" + aid,
				ModelId:      &concModelID,
				RequestJson:  fixtureNativeRequest(`{"messages":[{"role":"user","content":"parallel"}]}`, false),
			})
			if e != nil || !res.Ok {
				errs <- io.EOF
			}
		}(agentID)
	}
	for i := 0; i < 2; i++ {
		select {
		case <-inFlight:
		case <-time.After(3 * time.Second):
			t.Fatal("expected both concurrent foreground model calls to reach upstream simultaneously")
		}
	}
	close(releaseBoth)
	wg.Wait()
	close(errs)
	for e := range errs {
		t.Fatalf("concurrent foreground model call failed: %v", e)
	}
}

func TestGatewayForegroundConcurrencyCapUnaryAndStreaming(t *testing.T) {
	// 1. Verify default cap (> 1) and GATEWAY_MAX_CONCURRENT_CALLS parsing.
	defaultSvc := New(&memoryStore{}, testCipher(t), testToken)
	if got := defaultSvc.App.MaxConcurrentCalls(); got != app.DefaultMaxConcurrentCalls || got <= 1 {
		t.Fatalf("expected default max concurrent calls %d (> 1), got %d", app.DefaultMaxConcurrentCalls, got)
	}
	if got, err := app.ParseMaxConcurrentCalls(""); err != nil || got != app.DefaultMaxConcurrentCalls {
		t.Fatalf("expected empty env to return %d, got %d (%v)", app.DefaultMaxConcurrentCalls, got, err)
	}
	if got, err := app.ParseMaxConcurrentCalls("24"); err != nil || got != 24 {
		t.Fatalf("expected 24, got %d (%v)", got, err)
	}
	for _, invalid := range []string{"1", "0", "-4", "unlimited"} {
		if _, err := app.ParseMaxConcurrentCalls(invalid); err == nil {
			t.Fatalf("expected ParseMaxConcurrentCalls(%q) to fail", invalid)
		}
	}

	// 2. Unary concurrency cap: with cap=3 via GATEWAY_MAX_CONCURRENT_CALLS, 3 concurrent calls across
	// different Agents run simultaneously while call 4 receives 429 gateway_busy without hitting upstream.
	const unaryCap = 3
	t.Setenv("GATEWAY_MAX_CONCURRENT_CALLS", fmt.Sprint(unaryCap))
	var activeUnary, maxActiveUnary, totalUnaryCalls atomic.Int32
	unaryEntered := make(chan struct{}, unaryCap+2)
	releaseUnary := make(chan struct{})
	unaryFixture := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		totalUnaryCalls.Add(1)
		cur := activeUnary.Add(1)
		for {
			prev := maxActiveUnary.Load()
			if cur <= prev || maxActiveUnary.CompareAndSwap(prev, cur) {
				break
			}
		}
		defer activeUnary.Add(-1)
		unaryEntered <- struct{}{}
		<-releaseUnary
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl-cap","object":"chat.completion","choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":2}}`))
	}))
	defer unaryFixture.Close()

	crypto := testCipher(t)
	encKey, _ := crypto.Encrypt("fixture-cap-key")
	unaryStore := &memoryStore{c: singleModel(
		ProviderProfile{BaseURL: unaryFixture.URL + "/v1", CipherKey: encKey, Enabled: true},
		ModelProfile{Model: "cap-model", Enabled: true},
	)}
	unarySvc := New(unaryStore, crypto, testToken)
	unarySvc.Provider = Provider{HTTP: unaryFixture.Client()}
	if got := unarySvc.App.MaxConcurrentCalls(); got != unaryCap {
		t.Fatalf("expected GATEWAY_MAX_CONCURRENT_CALLS=%d to configure service, got %d", unaryCap, got)
	}

	var unaryWG sync.WaitGroup
	unaryResults := make(chan *agent.GatewayChatResponse, unaryCap)
	for i := 0; i < unaryCap; i++ {
		unaryWG.Add(1)
		go func(idx int) {
			defer unaryWG.Done()
			aid := fmt.Sprintf("agent-u-%d", idx)
			res, err := fixtureHTTPChat(t, unarySvc, context.Background(), &agent.GatewayChatRequest{
				ServiceToken: testToken,
				OwnerId:      "owner",
				AgentId:      aid,
				SessionId:    "session-" + aid,
				RunId:        "run-" + aid,
				RequestJson:  fixtureNativeRequest(`{"messages":[{"role":"user","content":"unary cap"}]}`, false),
			})
			if err != nil {
				t.Errorf("unexpected unary transport error: %v", err)
			}
			unaryResults <- res
		}(i)
	}
	for i := 0; i < unaryCap; i++ {
		select {
		case <-unaryEntered:
		case <-time.After(3 * time.Second):
			t.Fatalf("timed out waiting for unary call %d/%d to enter provider", i+1, unaryCap)
		}
	}
	if got := unarySvc.App.InFlightSlots(); got != unaryCap {
		t.Fatalf("expected %d in-flight slots, got %d", unaryCap, got)
	}

	// Excess concurrent unary call (cap + 1) must be rejected with 429 gateway_busy before provider.
	excessUnary, err := fixtureHTTPChat(t, unarySvc, context.Background(), &agent.GatewayChatRequest{
		ServiceToken: testToken,
		OwnerId:      "owner",
		AgentId:      "agent-u-excess",
		SessionId:    "session-u-excess",
		RunId:        "run-u-excess",
		RequestJson:  fixtureNativeRequest(`{"messages":[{"role":"user","content":"overflow"}]}`, false),
	})
	if err != nil || excessUnary.Ok || excessUnary.GetErrorCode() != "gateway_busy" || excessUnary.GetStatusCode() != 429 {
		t.Fatalf("expected excess unary call to fail with 429 gateway_busy, got %+v (%v)", excessUnary, err)
	}
	if totalUnaryCalls.Load() != unaryCap || maxActiveUnary.Load() > unaryCap {
		t.Fatalf("excess unary call reached provider: total=%d maxActive=%d cap=%d", totalUnaryCalls.Load(), maxActiveUnary.Load(), unaryCap)
	}

	// Release active unary calls and verify slot recovery for a subsequent call.
	close(releaseUnary)
	unaryWG.Wait()
	close(unaryResults)
	for res := range unaryResults {
		if res == nil || !res.Ok {
			t.Fatalf("expected admitted unary call to succeed, got %+v", res)
		}
	}
	if got := unarySvc.App.InFlightSlots(); got != 0 {
		t.Fatalf("expected 0 in-flight slots after unary completion, got %d", got)
	}
	afterUnary, err := fixtureHTTPChat(t, unarySvc, context.Background(), &agent.GatewayChatRequest{
		ServiceToken: testToken,
		OwnerId:      "owner",
		AgentId:      "agent-u-after",
		SessionId:    "session-u-after",
		RunId:        "run-u-after",
		RequestJson:  fixtureNativeRequest(`{"messages":[{"role":"user","content":"after"}]}`, false),
	})
	if err != nil || !afterUnary.Ok || totalUnaryCalls.Load() != unaryCap+1 {
		t.Fatalf("expected unary call after slot release to succeed, got %+v (%v)", afterUnary, err)
	}

	// 3. Streaming concurrency cap: with cap=2, 2 concurrent streams run simultaneously while stream 3
	// receives 429 gateway_busy without hitting upstream, and slots are released when streams finish.
	const streamCap = 2
	var activeStreams, maxActiveStreams, totalStreamCalls atomic.Int32
	releaseStreams := make(chan struct{})
	streamSvc, streamSrv := streamFixtureService(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		totalStreamCalls.Add(1)
		cur := activeStreams.Add(1)
		for {
			prev := maxActiveStreams.Load()
			if cur <= prev || maxActiveStreams.CompareAndSwap(prev, cur) {
				break
			}
		}
		defer activeStreams.Add(-1)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "data: %s\n\n", delta)
		w.(http.Flusher).Flush()
		<-releaseStreams
		fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", finalDelta)
	})
	defer streamSrv.Close()
	streamSvc.App.SetMaxConcurrentCalls(streamCap)
	if got := streamSvc.App.MaxConcurrentCalls(); got != streamCap {
		t.Fatalf("expected stream cap %d, got %d", streamCap, got)
	}

	firstChunkReady := make(chan struct{}, streamCap)
	var streamWG sync.WaitGroup
	streamErrs := make(chan error, streamCap)
	for i := 0; i < streamCap; i++ {
		streamWG.Add(1)
		go func(idx int) {
			defer streamWG.Done()
			req := streamRequest(t, context.Background(), streamSrv.URL, fmt.Sprintf("stream-run-%d", idx))
			resp, e := http.DefaultClient.Do(req)
			if e != nil {
				streamErrs <- e
				return
			}
			defer resp.Body.Close()
			scan := bufio.NewScanner(resp.Body)
			if !scan.Scan() {
				streamErrs <- fmt.Errorf("stream %d missing first chunk: %v", idx, scan.Err())
				return
			}
			var first StreamEvent
			if err := json.Unmarshal(scan.Bytes(), &first); err != nil || first.Type != "chunk" {
				streamErrs <- fmt.Errorf("stream %d unexpected first event: %+v (%v)", idx, first, err)
				return
			}
			firstChunkReady <- struct{}{}
			var sawDone bool
			for scan.Scan() {
				var ev StreamEvent
				if err := json.Unmarshal(scan.Bytes(), &ev); err != nil {
					streamErrs <- err
					return
				}
				if ev.Type == "done" {
					sawDone = true
				}
			}
			if err := scan.Err(); err != nil || !sawDone {
				streamErrs <- fmt.Errorf("stream %d did not complete cleanly: sawDone=%v err=%v", idx, sawDone, err)
			}
		}(i)
	}
	for i := 0; i < streamCap; i++ {
		select {
		case <-firstChunkReady:
		case <-time.After(3 * time.Second):
			t.Fatalf("timed out waiting for stream %d/%d first chunk", i+1, streamCap)
		}
	}
	if got := streamSvc.App.InFlightSlots(); got != streamCap {
		t.Fatalf("expected %d in-flight stream slots, got %d", streamCap, got)
	}

	// Excess concurrent streaming call (cap + 1) must receive 429 gateway_busy error event.
	excessResp, err := http.DefaultClient.Do(streamRequest(t, context.Background(), streamSrv.URL, "stream-run-excess"))
	if err != nil {
		t.Fatal(err)
	}
	excessBody, _ := io.ReadAll(excessResp.Body)
	_ = excessResp.Body.Close()
	var excessEvent StreamEvent
	if err := json.Unmarshal(excessBody, &excessEvent); err != nil || excessEvent.Type != "error" || excessEvent.ErrorCode != "gateway_busy" || excessEvent.StatusCode != 429 {
		t.Fatalf("expected excess stream call to receive 429 gateway_busy, got body=%s event=%+v err=%v", string(excessBody), excessEvent, err)
	}
	if totalStreamCalls.Load() != streamCap || maxActiveStreams.Load() > streamCap {
		t.Fatalf("excess stream call reached provider: total=%d maxActive=%d cap=%d", totalStreamCalls.Load(), maxActiveStreams.Load(), streamCap)
	}

	// Release active streams and verify slots return to 0 and a subsequent stream succeeds.
	close(releaseStreams)
	streamWG.Wait()
	close(streamErrs)
	for e := range streamErrs {
		t.Fatalf("admitted concurrent stream failed: %v", e)
	}
	if got := streamSvc.App.InFlightSlots(); got != 0 {
		t.Fatalf("expected 0 in-flight slots after streams finished, got %d", got)
	}
	afterStreamResp, err := http.DefaultClient.Do(streamRequest(t, context.Background(), streamSrv.URL, "stream-run-after"))
	if err != nil {
		t.Fatal(err)
	}
	afterStreamBody, _ := io.ReadAll(afterStreamResp.Body)
	_ = afterStreamResp.Body.Close()
	if !strings.Contains(string(afterStreamBody), `"type":"done"`) || totalStreamCalls.Load() != streamCap+1 {
		t.Fatalf("expected stream after slot release to succeed, got %s", string(afterStreamBody))
	}
	_ = rpcx.AdminAuth{}
}
