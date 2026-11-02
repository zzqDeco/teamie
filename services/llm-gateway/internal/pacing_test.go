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

func TestProviderVirtualWorkersCrossProviderIsolationAnd429Backoff(t *testing.T) {
	var (
		mu         sync.Mutex
		arrivalsA  []time.Time
		arrivalsB  []time.Time
		activeA    atomic.Int32
		maxActiveA atomic.Int32
		holdGateMu sync.Mutex
		holdNextA  chan struct{}
		enteredHold chan struct{}
	)

	serverA := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		cur := activeA.Add(1)
		for {
			prev := maxActiveA.Load()
			if cur <= prev || maxActiveA.CompareAndSwap(prev, cur) {
				break
			}
		}
		defer activeA.Add(-1)

		mu.Lock()
		arrivalsA = append(arrivalsA, time.Now())
		mu.Unlock()

		holdGateMu.Lock()
		hold := holdNextA
		entered := enteredHold
		holdGateMu.Unlock()
		if entered != nil {
			select {
			case entered <- struct{}{}:
			default:
			}
		}
		if hold != nil {
			<-hold
		} else {
			time.Sleep(35 * time.Millisecond)
		}

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
	svc.App.SetMaxConcurrentCalls(2)
	svc.App.SetProviderWorkers(1, 8)
	svc.App.SetRateLimitRetry(2, 25*time.Millisecond, 5*time.Millisecond)

	// 1. Burst of 4 calls on Provider A queues cleanly behind Provider A's 1 virtual worker
	//    without holding the 2nd global gateway slot, so Provider B executes with 0ms wait.
	start := time.Now()
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
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
		time.Sleep(10 * time.Millisecond)
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
	if len(gotA) != 4 || len(gotB) != 1 {
		t.Fatalf("expected 4 arrivals on A (even under 2-slot cap) and 1 on B, got A=%d B=%d", len(gotA), len(gotB))
	}
	if maxActiveA.Load() != 1 {
		t.Fatalf("expected Provider A virtual worker pool (maxWorkers=1) to cap simultaneous upstream calls at 1, got %d", maxActiveA.Load())
	}
	if firstDelay := gotA[0].Sub(start); firstDelay > 50*time.Millisecond {
		t.Fatalf("idle first call on Provider A should dispatch immediately, took %v", firstDelay)
	}
	if bDelay := gotB[0].Sub(start); bDelay > 60*time.Millisecond {
		t.Fatalf("Provider B should dispatch without waiting on Provider A queue or starving on 2-slot pool, took %v", bDelay)
	}

	// 2. Bounded queue overflow returns 429 gateway_busy, and canceling a queued waiter frees its queue slot cleanly.
	svc.App.SetProviderWorkers(1, 1)
	holdCh := make(chan struct{})
	holdEntered := make(chan struct{}, 2)
	holdGateMu.Lock()
	holdNextA = holdCh
	enteredHold = holdEntered
	holdGateMu.Unlock()

	holderDone := make(chan *agent.GatewayChatResponse, 1)
	go func() {
		res, _ := fixtureHTTPChat(t, svc, context.Background(), &agent.GatewayChatRequest{
			ServiceToken: testToken,
			OwnerId:      "owner",
			AgentId:      "agent-holder",
			SessionId:    "session-holder",
			RunId:        "run-holder",
			ModelId:      ptr("model-a"),
			RequestJson:  fixtureNativeRequest(`{"messages":[{"role":"user","content":"hold"}]}`, false),
		})
		holderDone <- res
	}()
	select {
	case <-holdEntered:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for holder to enter Provider A")
	}

	queued1Done := make(chan *agent.GatewayChatResponse, 1)
	go func() {
		res, _ := fixtureHTTPChat(t, svc, context.Background(), &agent.GatewayChatRequest{
			ServiceToken: testToken,
			OwnerId:      "owner",
			AgentId:      "agent-q1",
			SessionId:    "session-q1",
			RunId:        "run-q1",
			ModelId:      ptr("model-a"),
			RequestJson:  fixtureNativeRequest(`{"messages":[{"role":"user","content":"queued-1"}]}`, false),
		})
		queued1Done <- res
	}()
	// Give agent-q1 a moment to park in the 1-slot wait queue.
	time.Sleep(20 * time.Millisecond)

	overflowRes, err := fixtureHTTPChat(t, svc, context.Background(), &agent.GatewayChatRequest{
		ServiceToken: testToken,
		OwnerId:      "owner",
		AgentId:      "agent-overflow",
		SessionId:    "session-overflow",
		RunId:        "run-overflow",
		ModelId:      ptr("model-a"),
		RequestJson:  fixtureNativeRequest(`{"messages":[{"role":"user","content":"overflow"}]}`, false),
	})
	if err != nil || overflowRes.Ok || overflowRes.GetErrorCode() != "gateway_busy" || overflowRes.GetStatusCode() != 429 {
		t.Fatalf("expected provider queue overflow to fail fast with 429 gateway_busy, got %+v (%v)", overflowRes, err)
	}

	// Cancel queued waiter agent-q1; verify it exits promptly and frees the queue slot for agent-q2.
	svc.CancelRun("agent-q1", "run-q1")
	select {
	case q1Res := <-queued1Done:
		if q1Res == nil || q1Res.Ok || q1Res.GetErrorCode() != "canceled" {
			t.Fatalf("expected canceled queued waiter to return canceled, got %+v", q1Res)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("CancelRun did not abort queued provider waiter promptly")
	}

	queued2Done := make(chan *agent.GatewayChatResponse, 1)
	go func() {
		res, _ := fixtureHTTPChat(t, svc, context.Background(), &agent.GatewayChatRequest{
			ServiceToken: testToken,
			OwnerId:      "owner",
			AgentId:      "agent-q2",
			SessionId:    "session-q2",
			RunId:        "run-q2",
			ModelId:      ptr("model-a"),
			RequestJson:  fixtureNativeRequest(`{"messages":[{"role":"user","content":"queued-2"}]}`, false),
		})
		queued2Done <- res
	}()
	time.Sleep(20 * time.Millisecond)

	holdGateMu.Lock()
	holdNextA = nil
	enteredHold = nil
	holdGateMu.Unlock()
	close(holdCh)

	for name, ch := range map[string]chan *agent.GatewayChatResponse{"holder": holderDone, "queued-2": queued2Done} {
		select {
		case res := <-ch:
			if res == nil || !res.Ok {
				t.Fatalf("expected %s to complete after release, got %+v", name, res)
			}
		case <-time.After(time.Second):
			t.Fatalf("timed out waiting for %s to complete", name)
		}
	}

	// 3. Upstream 429 retry recovery for unary and streaming (before first chunk), exhaustion, and mid-backoff cancellation.
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

	rateStore := &memoryStore{c: Config{
		Providers: []ProviderProfile{
			{ID: "prov-rate", DisplayName: "Rate Provider", BaseURL: rateServer.URL + "/v1", CipherKey: encKey, Enabled: true},
			{ID: "prov-healthy", DisplayName: "Healthy Provider", BaseURL: serverB.URL + "/v1", CipherKey: encKey, Enabled: true},
		},
		Models: []ModelProfile{
			{ID: "model-rate", ProviderID: "prov-rate", DisplayName: "Rate Model", Model: "gpt-rate", Enabled: true},
			{ID: "model-healthy", ProviderID: "prov-healthy", DisplayName: "Healthy Model", Model: "gpt-healthy", Enabled: true},
		},
		Routing: ModelRoutingConfig{DefaultModelID: "model-rate"},
	}}
	rateSvc := New(rateStore, crypto, testToken)
	rateSvc.Provider = Provider{HTTP: sharedClient}
	rateSvc.App.SetMaxConcurrentCalls(2)
	rateSvc.App.SetProviderWorkers(2, 16)
	rateSvc.App.SetRateLimitRetry(2, 25*time.Millisecond, 5*time.Millisecond)
	rateHTTP := httptest.NewServer(rateSvc.Routes(AdminAuth{}))
	defer rateHTTP.Close()

	// 3a. Unary call hits 429 twice and succeeds on attempt 3.
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

	// 3b. Streaming call hits 429 twice before any chunk and succeeds on attempt 3.
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

	// 3c. Persistent 429 exhausts maxRetries=2 (3 total attempts) and returns 429 upstream_rate_limited.
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

	// 3d. Slots and virtual workers are released during 429 backoff so 2 sleeping calls on prov-rate
	//     do not starve prov-healthy on a 2-slot gateway, and CancelRun aborts backoff immediately.
	rateSvc.App.SetRateLimitRetry(2, 2*time.Second, 0)
	rateAttempts.Store(0)
	failUntilAttempt.Store(99)
	for len(entered429) > 0 {
		<-entered429
	}
	cancelDone1 := make(chan *agent.GatewayChatResponse, 1)
	cancelDone2 := make(chan *agent.GatewayChatResponse, 1)
	go func() {
		res, _ := fixtureHTTPChat(t, rateSvc, context.Background(), &agent.GatewayChatRequest{
			ServiceToken: testToken,
			OwnerId:      "owner",
			AgentId:      "agent-1",
			SessionId:    "session-1",
			RunId:        "run-cancel-backoff-1",
			RequestJson:  fixtureNativeRequest(`{"messages":[{"role":"user","content":"cancel-1"}]}`, false),
		})
		cancelDone1 <- res
	}()
	go func() {
		res, _ := fixtureHTTPChat(t, rateSvc, context.Background(), &agent.GatewayChatRequest{
			ServiceToken: testToken,
			OwnerId:      "owner",
			AgentId:      "agent-2",
			SessionId:    "session-2",
			RunId:        "run-cancel-backoff-2",
			RequestJson:  fixtureNativeRequest(`{"messages":[{"role":"user","content":"cancel-2"}]}`, false),
		})
		cancelDone2 <- res
	}()
	for range 2 {
		select {
		case <-entered429:
		case <-time.After(time.Second):
			t.Fatal("timed out waiting for 429 entry before backoff")
		}
	}
	healthyRes, healthyErr := fixtureHTTPChat(t, rateSvc, context.Background(), &agent.GatewayChatRequest{
		ServiceToken: testToken,
		OwnerId:      "owner",
		AgentId:      "agent-healthy",
		SessionId:    "session-healthy",
		RunId:        "run-healthy",
		ModelId:      ptr("model-healthy"),
		RequestJson:  fixtureNativeRequest(`{"messages":[{"role":"user","content":"healthy-while-backoff"}]}`, false),
	})
	if healthyErr != nil || !healthyRes.Ok {
		t.Fatalf("expected healthy provider call to succeed while 2 calls sleep in 429 backoff on a 2-slot gateway, got res=%+v err=%v", healthyRes, healthyErr)
	}
	rateSvc.CancelRun("agent-1", "run-cancel-backoff-1")
	rateSvc.CancelRun("agent-2", "run-cancel-backoff-2")
	for _, ch := range []chan *agent.GatewayChatResponse{cancelDone1, cancelDone2} {
		select {
		case canceledRes := <-ch:
			if canceledRes == nil || canceledRes.Ok || canceledRes.GetErrorCode() != "canceled" {
				t.Fatalf("expected immediate cancellation during backoff, got res=%+v", canceledRes)
			}
		case <-time.After(500 * time.Millisecond):
			t.Fatal("CancelRun did not abort 429 backoff sleep promptly")
		}
	}
	if rateAttempts.Load() != 2 {
		t.Fatalf("expected exactly 2 upstream attempts (1 per canceled call), got %d", rateAttempts.Load())
	}
}
