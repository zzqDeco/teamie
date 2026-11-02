package gateway_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"teamie/contracts/gen/agent/kitex_gen/agent"
)

func TestProviderBurstPacingCrossProviderIsolationAnd429Backoff(t *testing.T) {
	var mu sync.Mutex
	var arrivalsA []time.Time
	var arrivalsB []time.Time

	serverA := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		mu.Lock()
		arrivalsA = append(arrivalsA, time.Now())
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl-a","object":"chat.completion","choices":[{"message":{"role":"assistant","content":"ok-a"},"finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":2}}`))
	}))
	defer serverA.Close()

	serverB := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		mu.Lock()
		arrivalsB = append(arrivalsB, time.Now())
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl-b","object":"chat.completion","choices":[{"message":{"role":"assistant","content":"ok-b"},"finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":2}}`))
	}))
	defer serverB.Close()

	pool := serverA.Client().Transport.(*http.Transport).Clone()
	pool.TLSClientConfig = pool.TLSClientConfig.Clone()
	pool.TLSClientConfig.InsecureSkipVerify = true
	sharedClient := &http.Client{Transport: pool}

	crypto := testCipher(t)
	encKey, err := crypto.Encrypt("fixture-pace-key")
	if err != nil {
		t.Fatal(err)
	}
	store := &memoryStore{c: Config{
		Providers: []ProviderProfile{
			{ID: "prov-a", DisplayName: "Provider A", BaseURL: serverA.URL + "/v1", CipherKey: encKey, Enabled: true},
			{ID: "prov-b", DisplayName: "Provider B", BaseURL: serverB.URL + "/v1", CipherKey: encKey, Enabled: true},
		},
		Models: []ModelProfile{
			{ID: "model-a", ProviderID: "prov-a", DisplayName: "Model A", Model: "gpt-a", Enabled: true},
			{ID: "model-b", ProviderID: "prov-b", DisplayName: "Model B", Model: "gpt-b", Enabled: true},
		},
		Routing: ModelRoutingConfig{DefaultModelID: "model-a"},
	}}
	svc := New(store, crypto, testToken)
	svc.Provider = Provider{HTTP: sharedClient}
	svc.App.SetProviderPace(70*time.Millisecond, 0)
	svc.App.SetRateLimitRetry(2, 25*time.Millisecond, 5*time.Millisecond)

	start := time.Now()
	var wg sync.WaitGroup
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			aid := fmt.Sprintf("agent-a-%d", idx)
			res, callErr := fixtureHTTPChat(t, svc, context.Background(), &agent.GatewayChatRequest{
				ServiceToken: testToken,
				OwnerId:      "owner",
				AgentId:      aid,
				SessionId:    "session-" + aid,
				RunId:        "run-" + aid,
				ModelId:      ptr("model-a"),
				RequestJson:  fixtureNativeRequest(`{"messages":[{"role":"user","content":"burst"}]}`, false),
			})
			if callErr != nil || !res.Ok {
				t.Errorf("provider A call %d failed: %+v (%v)", idx, res, callErr)
			}
		}(i)
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		res, callErr := fixtureHTTPChat(t, svc, context.Background(), &agent.GatewayChatRequest{
			ServiceToken: testToken,
			OwnerId:      "owner",
			AgentId:      "agent-b-0",
			SessionId:    "session-b-0",
			RunId:        "run-b-0",
			ModelId:      ptr("model-b"),
			RequestJson:  fixtureNativeRequest(`{"messages":[{"role":"user","content":"independent"}]}`, false),
		})
		if callErr != nil || !res.Ok {
			t.Errorf("provider B call failed: %+v (%v)", res, callErr)
		}
	}()
	wg.Wait()

	mu.Lock()
	gotA := append([]time.Time(nil), arrivalsA...)
	gotB := append([]time.Time(nil), arrivalsB...)
	mu.Unlock()
	if len(gotA) != 3 || len(gotB) != 1 {
		t.Fatalf("expected 3 arrivals on A and 1 on B, got A=%d B=%d", len(gotA), len(gotB))
	}
	if firstDelay := gotA[0].Sub(start); firstDelay > 50*time.Millisecond {
		t.Fatalf("idle first call on Provider A should not be artificially delayed, took %v", firstDelay)
	}
	if bDelay := gotB[0].Sub(start); bDelay > 50*time.Millisecond {
		t.Fatalf("Provider B should dispatch independently without waiting on Provider A burst, took %v", bDelay)
	}
	if span := gotA[2].Sub(gotA[0]); span < 120*time.Millisecond {
		t.Fatalf("expected 3 concurrent calls on Provider A to be burst-paced >=120ms across 2 intervals, got %v", span)
	}

	// Upstream 429 retry recovery for unary and streaming (before first chunk), exhaustion, and mid-backoff cancellation.
	var rateAttempts atomic.Int32
	var failUntilAttempt atomic.Int32
	entered429 := make(chan struct{}, 4)

	rateServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bodyBytes, _ := io.ReadAll(r.Body)
		n := rateAttempts.Add(1)
		if n <= failUntilAttempt.Load() {
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":{"code":"rate_limit_exceeded","message":"too many requests"}}`))
			select {
			case entered429 <- struct{}{}:
			default:
			}
			return
		}
		if bytes.Contains(bodyBytes, []byte(`"stream":true`)) {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = fmt.Fprint(w, "data: {\"id\":\"s1\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"stream-recovered\"},\"finish_reason\":null}]}\n\n")
			_, _ = fmt.Fprint(w, "data: {\"id\":\"s1\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":2}}\n\n")
			_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl-ok","object":"chat.completion","choices":[{"message":{"role":"assistant","content":"recovered"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":4}}`))
	}))
	defer rateServer.Close()

	rateStore := &memoryStore{c: singleModel(
		ProviderProfile{ID: "prov-rate", BaseURL: rateServer.URL + "/v1", CipherKey: encKey, Enabled: true},
		ModelProfile{ID: "model-rate", Model: "gpt-rate", Enabled: true},
	)}
	rateSvc := New(rateStore, crypto, testToken)
	rateSvc.Provider = Provider{HTTP: rateServer.Client()}
	rateSvc.App.SetProviderPace(0, 0)
	rateSvc.App.SetRateLimitRetry(2, 25*time.Millisecond, 5*time.Millisecond)
	rateHTTP := httptest.NewServer(rateSvc.Routes(AdminAuth{}))
	defer rateHTTP.Close()

	// 1. Unary call hits 429 twice and succeeds on attempt 3.
	rateAttempts.Store(0)
	failUntilAttempt.Store(2)
	unaryRes, err := fixtureHTTPChat(t, rateSvc, context.Background(), &agent.GatewayChatRequest{
		ServiceToken: testToken,
		OwnerId:      "owner",
		AgentId:      "agent-rate-u",
		SessionId:    "session-rate-u",
		RunId:        "run-rate-u",
		RequestJson:  fixtureNativeRequest(`{"messages":[{"role":"user","content":"retry me"}]}`, false),
	})
	if err != nil || !unaryRes.Ok || rateAttempts.Load() != 3 {
		t.Fatalf("expected unary 429 recovery on attempt 3, got attempts=%d res=%+v err=%v", rateAttempts.Load(), unaryRes, err)
	}

	// 2. Streaming call hits 429 twice before any chunk and succeeds on attempt 3.
	rateAttempts.Store(0)
	failUntilAttempt.Store(2)
	sReq := streamRequest(t, context.Background(), rateHTTP.URL, "run-rate-s")
	sResp, err := http.DefaultClient.Do(sReq)
	if err != nil {
		t.Fatal(err)
	}
	defer sResp.Body.Close()
	scan := bufio.NewScanner(sResp.Body)
	var sawChunk, sawDone bool
	for scan.Scan() {
		var ev StreamEvent
		if uErr := json.Unmarshal(scan.Bytes(), &ev); uErr != nil {
			t.Fatal(uErr)
		}
		if ev.Type == "chunk" {
			sawChunk = true
		}
		if ev.Type == "done" {
			sawDone = true
		}
	}
	if !sawChunk || !sawDone || rateAttempts.Load() != 3 {
		t.Fatalf("expected stream 429 recovery on attempt 3, sawChunk=%v sawDone=%v attempts=%d", sawChunk, sawDone, rateAttempts.Load())
	}

	// 3. Persistent 429 exhausts maxRetries=2 (3 total attempts) and returns 429 upstream_rate_limited.
	rateAttempts.Store(0)
	failUntilAttempt.Store(99)
	exhausted, err := fixtureHTTPChat(t, rateSvc, context.Background(), &agent.GatewayChatRequest{
		ServiceToken: testToken,
		OwnerId:      "owner",
		AgentId:      "agent-rate-ex",
		SessionId:    "session-rate-ex",
		RunId:        "run-rate-ex",
		RequestJson:  fixtureNativeRequest(`{"messages":[{"role":"user","content":"exhaust"}]}`, false),
	})
	if err != nil || exhausted.Ok || exhausted.GetErrorCode() != "upstream_rate_limited" || exhausted.GetStatusCode() != 429 || rateAttempts.Load() != 3 {
		t.Fatalf("expected exhausted 429 after 3 attempts, got attempts=%d res=%+v err=%v", rateAttempts.Load(), exhausted, err)
	}

	// 4. Canceling an active run during 429 backoff aborts immediately without another upstream attempt.
	rateSvc.App.SetRateLimitRetry(2, 2*time.Second, 0)
	rateAttempts.Store(0)
	failUntilAttempt.Store(99)
	for len(entered429) > 0 {
		<-entered429
	}
	cancelDone := make(chan *agent.GatewayChatResponse, 1)
	go func() {
		res, _ := fixtureHTTPChat(t, rateSvc, context.Background(), &agent.GatewayChatRequest{
			ServiceToken: testToken,
			OwnerId:      "owner",
			AgentId:      "agent",
			SessionId:    "session",
			RunId:        "run-cancel-backoff",
			RequestJson:  fixtureNativeRequest(`{"messages":[{"role":"user","content":"cancel"}]}`, false),
		})
		cancelDone <- res
	}()
	select {
	case <-entered429:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for first 429")
	}
	rateSvc.CancelRun("agent", "run-cancel-backoff")
	select {
	case canceledRes := <-cancelDone:
		if canceledRes == nil || canceledRes.Ok || canceledRes.GetErrorCode() != "canceled" || rateAttempts.Load() != 1 {
			t.Fatalf("expected immediate cancellation during backoff after 1 attempt, got attempts=%d res=%+v", rateAttempts.Load(), canceledRes)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("CancelRun did not abort 429 backoff sleep promptly")
	}
}
