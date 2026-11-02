package gateway_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"teamie/contracts/gen/agent/kitex_gen/agent"
	"teamie/services/llm-gateway/internal/infra/upstream"
)

func TestParseRetryAfterHeaderUnit(t *testing.T) {
	now := time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC)

	cases := []struct {
		name      string
		retry     string
		retryMs   string
		wantExact time.Duration
	}{
		{name: "empty", wantExact: 0},
		{name: "integer seconds", retry: "3", wantExact: 3 * time.Second},
		{name: "decimal seconds", retry: "1.25", wantExact: 1250 * time.Millisecond},
		{name: "zero seconds", retry: "0", wantExact: 0},
		{name: "negative seconds", retry: "-5", wantExact: 0},
		{name: "malformed text", retry: "invalid-secret-header", wantExact: 0},
		{name: "http date future", retry: now.Add(4 * time.Second).Format(http.TimeFormat), wantExact: 4 * time.Second},
		{name: "http date past", retry: now.Add(-2 * time.Second).Format(http.TimeFormat), wantExact: 0},
		{name: "retry-after-ms", retryMs: "350", wantExact: 350 * time.Millisecond},
		{name: "max of retry-after and ms", retry: "1", retryMs: "1500", wantExact: 1500 * time.Millisecond},
		{name: "clamped huge seconds", retry: "9999999", wantExact: upstream.MaxRetryAfterCooldown},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := make(http.Header)
			if tc.retry != "" {
				h.Set("Retry-After", tc.retry)
			}
			if tc.retryMs != "" {
				h.Set("Retry-After-Ms", tc.retryMs)
			}
			got := upstream.ParseRetryAfterHeader(h, now)
			if got != tc.wantExact {
				t.Fatalf("ParseRetryAfterHeader(%q, %q) = %v, want %v", tc.retry, tc.retryMs, got, tc.wantExact)
			}
		})
	}
}

func TestRetryAfterHonoredAndCooldownPersistsAcrossExhaustedCallsAndModels(t *testing.T) {
	var (
		mu             sync.Mutex
		sharedArrivals []time.Time
		otherArrivals  []time.Time
		mode           atomic.Int32
		sharedCalls    atomic.Int32
	)

	// Modes:
	// 1: Return 429 with Retry-After-Ms: 220 on call 1, then 200 OK on call 2.
	// 2: Return 429 with HTTP-date Retry-After (~1s ahead) on call 1, then 200 OK on call 2.
	// 3: Always return 429 (no Retry-After) until mode changes.
	// 4: Return 429 on first call of this phase, then 200 OK.
	sharedServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		now := time.Now()
		mu.Lock()
		sharedArrivals = append(sharedArrivals, now)
		mu.Unlock()
		n := sharedCalls.Add(1)

		switch mode.Load() {
		case 1:
			if n == 1 {
				w.Header().Set("Retry-After-Ms", "220")
				w.WriteHeader(http.StatusTooManyRequests)
				_, _ = w.Write([]byte(`{"error":{"message":"sensitive-upstream-detail"}}`))
				return
			}
		case 2:
			if n == 1 {
				target := time.Now().UTC().Truncate(time.Second).Add(2 * time.Second)
				w.Header().Set("Retry-After", target.Format(http.TimeFormat))
				w.WriteHeader(http.StatusTooManyRequests)
				_, _ = w.Write([]byte(`{"error":{"message":"rate_limited"}}`))
				return
			}
		case 3:
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":{"message":"quota"}}`))
			return
		case 4:
			if n == 1 {
				w.WriteHeader(http.StatusTooManyRequests)
				_, _ = w.Write([]byte(`{"error":{"message":"still_cooling"}}`))
				return
			}
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl-s","object":"chat.completion","choices":[{"message":{"role":"assistant","content":"ok-shared"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":2}}`))
	}))
	defer sharedServer.Close()

	otherServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		mu.Lock()
		otherArrivals = append(otherArrivals, time.Now())
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl-o","object":"chat.completion","choices":[{"message":{"role":"assistant","content":"ok-other"},"finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":2}}`))
	}))
	defer otherServer.Close()

	pool := sharedServer.Client().Transport.(*http.Transport).Clone()
	pool.TLSClientConfig = pool.TLSClientConfig.Clone()
	pool.TLSClientConfig.InsecureSkipVerify = true
	client := &http.Client{Transport: pool}

	crypto := testCipher(t)
	encKey, err := crypto.Encrypt("fixture-retry-key")
	if err != nil {
		t.Fatal(err)
	}
	store := &memoryStore{c: Config{
		Providers: []ProviderProfile{
			{ID: "prov-shared", DisplayName: "Shared Provider", BaseURL: sharedServer.URL + "/v1", CipherKey: encKey, Enabled: true},
			{ID: "prov-other", DisplayName: "Other Provider", BaseURL: otherServer.URL + "/v1", CipherKey: encKey, Enabled: true},
		},
		Models: []ModelProfile{
			{ID: "model-s1", ProviderID: "prov-shared", DisplayName: "Shared 1", Model: "gpt-s1", Enabled: true},
			{ID: "model-s2", ProviderID: "prov-shared", DisplayName: "Shared 2", Model: "gpt-s2", Enabled: true},
			{ID: "model-other", ProviderID: "prov-other", DisplayName: "Other", Model: "gpt-other", Enabled: true},
		},
		Routing: ModelRoutingConfig{DefaultModelID: "model-s1"},
	}}

	svc := New(store, crypto, testToken)
	svc.Provider = Provider{HTTP: client}
	svc.App.SetMaxConcurrentCalls(4)
	svc.App.SetProviderWorkers(4, 16)

	// Phase A: Retry-After header (220ms) overrides tiny baseBackoff (10ms).
	svc.App.SetRateLimitRetry(2, 10*time.Millisecond, 0)
	mode.Store(1)
	sharedCalls.Store(0)
	mu.Lock()
	sharedArrivals = nil
	mu.Unlock()

	res1, err := fixtureHTTPChat(t, svc, context.Background(), &agent.GatewayChatRequest{
		ServiceToken: testToken,
		OwnerId:      "owner",
		AgentId:      "agent-1",
		SessionId:    "sess-1",
		RunId:        "run-ra-ms",
		ModelId:      ptr("model-s1"),
		RequestJson:  fixtureNativeRequest(`{"messages":[{"role":"user","content":"hi"}]}`, false),
	})
	if err != nil || !res1.Ok {
		t.Fatalf("expected Retry-After-Ms call to recover, got res=%+v err=%v", res1, err)
	}
	mu.Lock()
	if len(sharedArrivals) != 2 || sharedArrivals[1].Sub(sharedArrivals[0]) < 190*time.Millisecond {
		gap := time.Duration(0)
		if len(sharedArrivals) >= 2 {
			gap = sharedArrivals[1].Sub(sharedArrivals[0])
		}
		mu.Unlock()
		t.Fatalf("expected >=190ms Retry-After delay before attempt 2, got len=%d gap=%v", len(sharedArrivals), gap)
	}
	mu.Unlock()

	// Phase B: HTTP-date Retry-After honored.
	svc.App.SetRateLimitRetry(2, 10*time.Millisecond, 0)
	mode.Store(2)
	sharedCalls.Store(0)
	mu.Lock()
	sharedArrivals = nil
	mu.Unlock()

	res2, err := fixtureHTTPChat(t, svc, context.Background(), &agent.GatewayChatRequest{
		ServiceToken: testToken,
		OwnerId:      "owner",
		AgentId:      "agent-1",
		SessionId:    "sess-1",
		RunId:        "run-ra-date",
		ModelId:      ptr("model-s1"),
		RequestJson:  fixtureNativeRequest(`{"messages":[{"role":"user","content":"date"}]}`, false),
	})
	if err != nil || !res2.Ok {
		t.Fatalf("expected HTTP-date Retry-After call to recover, got res=%+v err=%v", res2, err)
	}
	mu.Lock()
	if len(sharedArrivals) != 2 || sharedArrivals[1].Sub(sharedArrivals[0]) < 800*time.Millisecond {
		gap := time.Duration(0)
		if len(sharedArrivals) >= 2 {
			gap = sharedArrivals[1].Sub(sharedArrivals[0])
		}
		mu.Unlock()
		t.Fatalf("expected HTTP-date Retry-After delay (~1-2s), got len=%d gap=%v", len(sharedArrivals), gap)
	}
	mu.Unlock()

	// Phase C: Exhausted retries MUST retain provider cooldown across subsequent calls,
	// across sibling models on the same ProviderProfile, and escalate consecutive429
	// across calls while keeping other ProviderProfiles completely unblocked.
	svc.App.SetRateLimitRetry(1, 60*time.Millisecond, 0)
	mode.Store(3)
	sharedCalls.Store(0)
	mu.Lock()
	sharedArrivals = nil
	otherArrivals = nil
	mu.Unlock()

	// Call #1 (model-s1): attempt 0 -> 429 (60ms), attempt 1 -> 429 (exhausted, sets 120ms cooldown!).
	exhausted, err := fixtureHTTPChat(t, svc, context.Background(), &agent.GatewayChatRequest{
		ServiceToken: testToken,
		OwnerId:      "owner",
		AgentId:      "agent-a",
		SessionId:    "sess-a",
		RunId:        "run-exhaust",
		ModelId:      ptr("model-s1"),
		RequestJson:  fixtureNativeRequest(`{"messages":[{"role":"user","content":"exhaust"}]}`, false),
	})
	exhaustReturnedAt := time.Now()
	if err != nil || exhausted.Ok || exhausted.GetStatusCode() != 429 || exhausted.GetErrorCode() != "upstream_rate_limited" {
		t.Fatalf("expected exhausted 429, got res=%+v err=%v", exhausted, err)
	}

	// Switch upstream to Mode 4 (fail 1st arrival of Call #2 with 429, succeed on 2nd arrival).
	mode.Store(4)
	sharedCalls.Store(0)

	// While prov-shared is in its post-exhaustion 120ms cooldown, prov-other must succeed immediately.
	otherStart := time.Now()
	otherRes, otherErr := fixtureHTTPChat(t, svc, context.Background(), &agent.GatewayChatRequest{
		ServiceToken: testToken,
		OwnerId:      "owner",
		AgentId:      "agent-other",
		SessionId:    "sess-other",
		RunId:        "run-other",
		ModelId:      ptr("model-other"),
		RequestJson:  fixtureNativeRequest(`{"messages":[{"role":"user","content":"fast"}]}`, false),
	})
	if otherErr != nil || !otherRes.Ok || time.Since(otherStart) > 80*time.Millisecond {
		t.Fatalf("expected prov-other to be unaffected by prov-shared cooldown, res=%+v err=%v elapsed=%v", otherRes, otherErr, time.Since(otherStart))
	}

	// Immediate Call #2 on sibling model-s2 (same prov-shared, different Agent):
	// 1) Must wait out the ~120ms cooldown left by exhausted Call #1;
	// 2) When its first attempt hits 429, consecutive429 escalates to 3 -> 240ms backoff (not reset to 60ms!);
	// 3) Succeeds on retry and resets consecutive429 back to 0.
	resCross, err := fixtureHTTPChat(t, svc, context.Background(), &agent.GatewayChatRequest{
		ServiceToken: testToken,
		OwnerId:      "owner",
		AgentId:      "agent-b",
		SessionId:    "sess-b",
		RunId:        "run-cross-model",
		ModelId:      ptr("model-s2"),
		RequestJson:  fixtureNativeRequest(`{"messages":[{"role":"user","content":"cross"}]}`, false),
	})
	if err != nil || !resCross.Ok {
		t.Fatalf("expected cross-call recovery on model-s2, got res=%+v err=%v", resCross, err)
	}

	mu.Lock()
	if len(sharedArrivals) != 4 {
		mu.Unlock()
		t.Fatalf("expected 4 total upstream arrivals (2 in Call #1 + 2 in Call #2), got %d", len(sharedArrivals))
	}
	postExhaustGap := sharedArrivals[2].Sub(sharedArrivals[1])
	escalatedGap := sharedArrivals[3].Sub(sharedArrivals[2])
	_ = exhaustReturnedAt
	mu.Unlock()

	if postExhaustGap < 100*time.Millisecond {
		t.Fatalf("post-exhaustion cooldown was dropped: Call #2 arrived %v after Call #1's final 429 (want >=100ms)", postExhaustGap)
	}
	if escalatedGap < 210*time.Millisecond {
		t.Fatalf("cross-call consecutive429 did not escalate to ~240ms: got %v", escalatedGap)
	}

	// Phase D: Verify 200 OK in Call #2 reset consecutive429 back to base 60ms!
	mode.Store(4)
	sharedCalls.Store(0)
	mu.Lock()
	sharedArrivals = nil
	mu.Unlock()

	resReset, err := fixtureHTTPChat(t, svc, context.Background(), &agent.GatewayChatRequest{
		ServiceToken: testToken,
		OwnerId:      "owner",
		AgentId:      "agent-b",
		SessionId:    "sess-b",
		RunId:        "run-after-reset",
		ModelId:      ptr("model-s1"),
		RequestJson:  fixtureNativeRequest(`{"messages":[{"role":"user","content":"after-reset"}]}`, false),
	})
	if err != nil || !resReset.Ok {
		t.Fatalf("expected recovery after reset, got res=%+v err=%v", resReset, err)
	}
	mu.Lock()
	resetGap := sharedArrivals[1].Sub(sharedArrivals[0])
	mu.Unlock()
	if resetGap < 50*time.Millisecond || resetGap > 150*time.Millisecond {
		t.Fatalf("expected consecutive429 reset to base ~60ms after 200 OK, got %v", resetGap)
	}

	// Phase E: Caller cancellation while waiting on post-exhaustion cooldown exits promptly without leaking workers.
	svc.App.SetRateLimitRetry(0, 2*time.Second, 0)
	mode.Store(3)
	_, _ = fixtureHTTPChat(t, svc, context.Background(), &agent.GatewayChatRequest{
		ServiceToken: testToken,
		OwnerId:      "owner",
		AgentId:      "agent-seed-cd",
		SessionId:    "sess-seed-cd",
		RunId:        "run-seed-cd",
		ModelId:      ptr("model-s1"),
		RequestJson:  fixtureNativeRequest(`{"messages":[{"role":"user","content":"seed-cd"}]}`, false),
	})

	cancelCtx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
	defer cancel()
	cStart := time.Now()
	_, _ = fixtureHTTPChat(t, svc, cancelCtx, &agent.GatewayChatRequest{
		ServiceToken: testToken,
		OwnerId:      "owner",
		AgentId:      "agent-cancel-cd",
		SessionId:    "sess-cancel-cd",
		RunId:        "run-cancel-cd",
		ModelId:      ptr("model-s2"),
		RequestJson:  fixtureNativeRequest(`{"messages":[{"role":"user","content":"cancel-cd"}]}`, false),
	})
	if elapsed := time.Since(cStart); elapsed > 400*time.Millisecond {
		t.Fatalf("caller cancellation during provider cooldown took too long: %v", elapsed)
	}
}
