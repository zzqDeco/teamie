package gateway_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"teamie/contracts/gen/agent/kitex_gen/agent"
	"teamie/contracts/gen/agent/kitex_gen/agent/modelgatewayservice"
	"teamie/contracts/gen/im/kitex_gen/im"
	pkgRPC "teamie/pkg/rpcx"
	httpAdapter "teamie/services/llm-gateway/internal/adapter/http"
	rpcAdapter "teamie/services/llm-gateway/internal/adapter/rpc"
	"teamie/services/llm-gateway/internal/app"
	"teamie/services/llm-gateway/internal/domain"
	"teamie/services/llm-gateway/internal/infra/rpcclient"
)

type stubAdminAuthorizer struct{}

func (stubAdminAuthorizer) Require(*http.Request) (*im.User, error) {
	return &im.User{
		Id:           "admin-1",
		Kind:         "human",
		PlatformRole: "admin",
	}, nil
}

type blockingWebProvider struct {
	started chan context.Context
}

func (w *blockingWebProvider) Search(ctx context.Context, _, _ string, _ map[string]any, _ int) ([]domain.WebSearchItem, int, error) {
	w.started <- ctx
	<-ctx.Done()
	return nil, 0, domain.StreamError(ctx, "gateway_unavailable")
}

func (w *blockingWebProvider) Extract(ctx context.Context, _, _ string, _ map[string]any) ([]domain.WebExtractItem, []domain.WebExtractFailure, int, error) {
	w.started <- ctx
	<-ctx.Done()
	return nil, nil, 0, domain.StreamError(ctx, "gateway_unavailable")
}

type cachedPingStore struct {
	*memoryStore
	invalidations atomic.Int64
}

func (c *cachedPingStore) InvalidatePingCache() {
	c.invalidations.Add(1)
}

type gatewayRolloutSlot struct {
	svc     *app.Service
	peers   *rpcclient.PeerBroadcaster
	httpSrv *httptest.Server
	rpcAddr string
	rpcStop func()
}

func newGatewayRolloutCluster(
	t *testing.T,
	store domain.ConfigStore,
	cipher domain.Cipher,
	model domain.UpstreamModel,
	web domain.WebSearchProvider,
	reconcileInterval time.Duration,
) (*gatewayRolloutSlot, *gatewayRolloutSlot) {
	t.Helper()

	startSlot := func() *gatewayRolloutSlot {
		t.Helper()
		peers := rpcclient.NewPeerBroadcaster(nil, nil, testToken, "")
		svc := app.New(
			store,
			cipher,
			model,
			web,
			testToken,
			app.WithPeerBroadcaster(peers),
			app.WithActiveReconcileInterval(reconcileInterval),
		)
		peers.InstanceID = svc.InstanceID()

		h := httpAdapter.New(svc)
		httpSrv := httptest.NewServer(h.Routes(stubAdminAuthorizer{}))

		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			httpSrv.Close()
			t.Fatalf("reserve rpc port: %v", err)
		}
		rpcAddr := ln.Addr().String()
		_ = ln.Close()
		tcpAddr, err := net.ResolveTCPAddr("tcp", rpcAddr)
		if err != nil {
			httpSrv.Close()
			t.Fatalf("resolve rpc addr: %v", err)
		}

		rpcDone := make(chan struct{})
		rpcSrv := modelgatewayservice.NewServer(
			rpcAdapter.New(svc),
			pkgRPC.ServerOptions(tcpAddr, 0)...,
		)
		go func() {
			defer close(rpcDone)
			_ = rpcSrv.Run()
		}()

		// Wait until the RPC server accepts Ping.
		cli, err := modelgatewayservice.NewClient("teamie.model-gateway", pkgRPC.ClientOptions(rpcAddr, time.Second, 300*time.Millisecond)...)
		if err != nil {
			httpSrv.Close()
			_ = rpcSrv.Stop()
			t.Fatalf("new rpc probe client: %v", err)
		}
		tok := testToken
		deadline := time.Now().Add(3 * time.Second)
		ready := false
		for time.Now().Before(deadline) {
			pCtx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
			resp, pErr := cli.Ping(pCtx, &agent.PingRequest{ServiceToken: &tok})
			cancel()
			if pErr == nil && resp != nil && resp.Ready {
				ready = true
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		if closer, ok := cli.(io.Closer); ok {
			_ = closer.Close()
		}
		if !ready {
			httpSrv.Close()
			_ = rpcSrv.Stop()
			t.Fatalf("gateway rpc server at %s did not become ready", rpcAddr)
		}

		var stopOnce sync.Once
		stopRPC := func() {
			stopOnce.Do(func() {
				_ = rpcSrv.Stop()
				<-rpcDone
			})
		}
		return &gatewayRolloutSlot{
			svc:     svc,
			peers:   peers,
			httpSrv: httpSrv,
			rpcAddr: rpcAddr,
			rpcStop: stopRPC,
		}
	}

	slotA := startSlot()
	slotB := startSlot()

	httpTargets := []string{slotA.httpSrv.URL, slotB.httpSrv.URL}
	rpcTargets := []string{slotA.rpcAddr, slotB.rpcAddr}
	slotA.peers.HTTPTargets = append([]string(nil), httpTargets...)
	slotA.peers.RPCTargets = append([]string(nil), rpcTargets...)
	slotB.peers.HTTPTargets = append([]string(nil), httpTargets...)
	slotB.peers.RPCTargets = append([]string(nil), rpcTargets...)

	t.Cleanup(func() {
		slotA.httpSrv.Close()
		slotB.httpSrv.Close()
		slotA.rpcStop()
		slotB.rpcStop()
	})
	return slotA, slotB
}

func TestDualSlotCrossInstanceRunCancellationAndTombstone(t *testing.T) {
	crypto := testCipher(t)
	encKey, err := crypto.Encrypt("fixture-secret-key")
	if err != nil {
		t.Fatal(err)
	}
	store := &cachedPingStore{
		memoryStore: &memoryStore{
			c: Config{
				Providers: []ProviderProfile{
					{ID: "pa", DisplayName: "Provider A", BaseURL: "https://1.1.1.1/v1", CipherKey: encKey, Enabled: true},
				},
				Models: []ModelProfile{
					{ID: "ma", ProviderID: "pa", Model: "gpt-test", Enabled: true},
				},
				Routing:   ModelRoutingConfig{DefaultModelID: "ma"},
				WebSearch: WebSearchConfig{CipherKey: encKey, Enabled: true},
			},
		},
	}
	modelProv := &blockingModels{started: make(chan modelCallProbe, 8)}
	webProv := &blockingWebProvider{started: make(chan context.Context, 8)}
	slotA, slotB := newGatewayRolloutCluster(t, store, crypto, modelProv, webProv, 0)

	// Client pinned to Slot B's RPC server (simulating round-robin picking Slot B while inference runs on Slot A).
	rpcClientB, err := modelgatewayservice.NewClient(
		"teamie.model-gateway",
		pkgRPC.ClientOptions(slotB.rpcAddr, 2*time.Second, 500*time.Millisecond)...,
	)
	if err != nil {
		t.Fatalf("new rpc client B: %v", err)
	}
	t.Cleanup(func() {
		if closer, ok := rpcClientB.(io.Closer); ok {
			_ = closer.Close()
		}
	})

	// 1. Streaming model call on Slot A must be immediately canceled when Gateway.Cancel RPC hits Slot B.
	streamDone := make(chan string, 1)
	go func() {
		body := `{"ownerId":"owner-1","agentId":"agent-1","sessionId":"session-1","runId":"run-stream-1","requestJson":"{\"messages\":[{\"role\":\"user\",\"content\":\"hi\"}]}"}`
		req, _ := http.NewRequest(http.MethodPost, slotA.httpSrv.URL+"/internal/v1/chat/stream", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+testToken)
		req.Header.Set("Content-Type", "application/json")
		resp, doErr := http.DefaultClient.Do(req)
		if doErr != nil {
			streamDone <- "err:" + doErr.Error()
			return
		}
		defer resp.Body.Close()
		raw, _ := io.ReadAll(resp.Body)
		streamDone <- string(raw)
	}()

	var activeProbe modelCallProbe
	select {
	case activeProbe = <-modelProv.started:
	case <-time.After(3 * time.Second):
		t.Fatal("streaming call on Slot A never reached provider")
	}

	ack, err := rpcClientB.Cancel(context.Background(), &agent.CancelRequest{
		ServiceToken: testToken,
		AgentId:      "agent-1",
		RunId:        "run-stream-1",
	})
	if err != nil || ack == nil || !ack.Ok {
		t.Fatalf("Cancel RPC on Slot B failed: ack=%+v err=%v", ack, err)
	}

	select {
	case <-activeProbe.ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("Cancel RPC on Slot B did not cancel active StreamChat on Slot A")
	}
	select {
	case out := <-streamDone:
		if !strings.Contains(out, `"type":"error"`) {
			t.Fatalf("expected canceled stream error event from Slot A, got %s", out)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Slot A HTTP stream did not terminate after cross-slot Cancel")
	}

	// 2. Active WebSearch call on Slot A must also be canceled when Gateway.Cancel RPC hits Slot B.
	webDone := make(chan int, 1)
	go func() {
		body := `{"ownerId":"owner-1","agentId":"agent-1","sessionId":"session-1","runId":"run-web-1","query":"zero downtime"}`
		req, _ := http.NewRequest(http.MethodPost, slotA.httpSrv.URL+"/internal/v1/web/search", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+testToken)
		req.Header.Set("Content-Type", "application/json")
		resp, doErr := http.DefaultClient.Do(req)
		if doErr != nil {
			webDone <- 0
			return
		}
		_ = resp.Body.Close()
		webDone <- resp.StatusCode
	}()

	var activeWebCtx context.Context
	select {
	case activeWebCtx = <-webProv.started:
	case <-time.After(3 * time.Second):
		t.Fatal("web search on Slot A never reached provider")
	}

	ackWeb, err := rpcClientB.Cancel(context.Background(), &agent.CancelRequest{
		ServiceToken: testToken,
		AgentId:      "agent-1",
		RunId:        "run-web-1",
	})
	if err != nil || ackWeb == nil || !ackWeb.Ok {
		t.Fatalf("Cancel RPC for web call on Slot B failed: ack=%+v err=%v", ackWeb, err)
	}
	select {
	case <-activeWebCtx.Done():
	case <-time.After(time.Second):
		t.Fatal("Cancel RPC on Slot B did not cancel active WebSearch on Slot A")
	}
	<-webDone

	// 3. Pre-arrival tombstone: Cancel RPC hits Slot B BEFORE StreamChat arrives on Slot A.
	ackTombstone, err := rpcClientB.Cancel(context.Background(), &agent.CancelRequest{
		ServiceToken: testToken,
		AgentId:      "agent-1",
		RunId:        "run-pre-canceled",
	})
	if err != nil || ackTombstone == nil || !ackTombstone.Ok {
		t.Fatalf("pre-arrival Cancel RPC on Slot B failed: ack=%+v err=%v", ackTombstone, err)
	}

	out := slotA.svc.StreamChat(
		context.Background(),
		"owner-1",
		"agent-1",
		"session-1",
		"run-pre-canceled",
		"",
		"{}",
		nil,
		func(StreamEvent) error { return nil },
	)
	if out.Failure == nil {
		t.Fatal("expected late-arriving StreamChat on Slot A to be aborted immediately by replicated tombstone")
	}
}

func TestDualSlotCrossInstanceAdminCatalogAndSearchRevocation(t *testing.T) {
	crypto := testCipher(t)
	encKey, err := crypto.Encrypt("fixture-secret-key")
	if err != nil {
		t.Fatal(err)
	}
	store := &cachedPingStore{
		memoryStore: &memoryStore{
			c: Config{
				Providers: []ProviderProfile{
					{ID: "pa", DisplayName: "Provider A", BaseURL: "https://1.1.1.1/v1", CipherKey: encKey, Enabled: true},
					{ID: "pb", DisplayName: "Provider B", BaseURL: "https://1.1.1.1/v1", CipherKey: encKey, Enabled: true},
				},
				Models: []ModelProfile{
					{ID: "ma", ProviderID: "pa", Model: "model-a", Enabled: true},
					{ID: "mb", ProviderID: "pb", Model: "model-b", Enabled: true},
				},
				Routing:   ModelRoutingConfig{DefaultModelID: "ma"},
				WebSearch: WebSearchConfig{CipherKey: encKey, Enabled: true},
			},
		},
	}
	modelProv := &blockingModels{started: make(chan modelCallProbe, 8)}
	webProv := &blockingWebProvider{started: make(chan context.Context, 8)}
	slotA, slotB := newGatewayRolloutCluster(t, store, crypto, modelProv, webProv, 0)

	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	defer func() { cancel(); wg.Wait() }()

	startStreamOnA := func(modelID, runID string) modelCallProbe {
		t.Helper()
		wg.Add(1)
		go func() {
			defer wg.Done()
			cCtx := app.WithRequestedModelID(ctx, modelID)
			slotA.svc.StreamChat(cCtx, "owner-1", "agent-1", "session-1", runID, "", "{}", nil, func(StreamEvent) error { return nil })
		}()
		select {
		case p := <-modelProv.started:
			return p
		case <-time.After(3 * time.Second):
			t.Fatalf("model %s stream on Slot A did not start", modelID)
			return modelCallProbe{}
		}
	}

	probeA := startStreamOnA("ma", "run-ma")
	probeB := startStreamOnA("mb", "run-mb")

	wg.Add(1)
	go func() {
		defer wg.Done()
		slotA.svc.ExecuteWebSearch(ctx, domain.WebSearchInput{
			OwnerID:   "owner-1",
			AgentID:   "agent-1",
			SessionID: "session-1",
			RunID:     "run-web",
			Query:     "teamie",
		})
	}()
	var webCtxOnA context.Context
	select {
	case webCtxOnA = <-webProv.started:
	case <-time.After(3 * time.Second):
		t.Fatal("web search on Slot A did not start")
	}

	// Admin disables model mb via HTTP PATCH on Slot B -> must immediately cancel probeB on Slot A without touching probeA or webCtxOnA!
	beforeInvalidations := store.invalidations.Load()
	patchBody := bytes.NewBufferString(`{"enabled":false}`)
	patchReq, _ := http.NewRequest(http.MethodPatch, slotB.httpSrv.URL+"/api/v1/admin/model-gateway/models/mb", patchBody)
	patchReq.Header.Set("Content-Type", "application/json")
	patchResp, err := http.DefaultClient.Do(patchReq)
	if err != nil {
		t.Fatalf("PATCH model mb on Slot B: %v", err)
	}
	_ = patchResp.Body.Close()
	if patchResp.StatusCode != http.StatusOK {
		t.Fatalf("PATCH model mb status = %d, want 200", patchResp.StatusCode)
	}

	if probeB.ctx.Err() == nil {
		t.Fatal("disabling model mb on Slot B failed to cancel active mb call on Slot A")
	}
	if probeA.ctx.Err() != nil {
		t.Fatal("disabling model mb on Slot B unexpectedly canceled healthy model ma on Slot A")
	}
	if webCtxOnA.Err() != nil {
		t.Fatal("disabling model mb on Slot B unexpectedly canceled active web search on Slot A")
	}
	if store.invalidations.Load() <= beforeInvalidations {
		t.Fatal("expected peer reconcile on Slot A to invalidate Ping cache")
	}

	// Admin disables Provider A on Slot B -> must immediately cancel probeA on Slot A!
	no := false
	if _, err := slotB.svc.UpdateModelProvider(ctx, app.UpdateModelProviderInput{ID: "pa", Enabled: &no}); err != nil {
		t.Fatalf("UpdateModelProvider on Slot B: %v", err)
	}
	if probeA.ctx.Err() == nil {
		t.Fatal("disabling provider pa on Slot B failed to cancel active ma call on Slot A")
	}
	if webCtxOnA.Err() != nil {
		t.Fatal("disabling provider pa unexpectedly canceled active web search on Slot A")
	}

	// Admin disables WebSearch on Slot B -> must immediately cancel webCtxOnA on Slot A!
	if _, err := slotB.svc.UpdateWebSearch(ctx, app.UpdateWebSearchInput{Enabled: &no}); err != nil {
		t.Fatalf("UpdateWebSearch on Slot B: %v", err)
	}
	if webCtxOnA.Err() == nil {
		t.Fatal("disabling web search on Slot B failed to cancel active web search on Slot A")
	}
}

func TestDrainingSlotPostGraceRPCFallbackAndStoreWatcher(t *testing.T) {
	crypto := testCipher(t)
	encKey, err := crypto.Encrypt("fixture-secret-key")
	if err != nil {
		t.Fatal(err)
	}
	store := &memoryStore{
		c: Config{
			Providers: []ProviderProfile{
				{ID: "pa", DisplayName: "Provider A", BaseURL: "https://1.1.1.1/v1", CipherKey: encKey, Enabled: true},
			},
			Models: []ModelProfile{
				{ID: "m1", ProviderID: "pa", Model: "model-1", Enabled: true},
				{ID: "m2", ProviderID: "pa", Model: "model-2", Enabled: true},
			},
			Routing: ModelRoutingConfig{DefaultModelID: "m1"},
		},
	}
	modelProv := &blockingModels{started: make(chan modelCallProbe, 4)}
	slotA, slotB := newGatewayRolloutCluster(t, store, crypto, modelProv, nil, 40*time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	defer func() { cancel(); wg.Wait() }()

	startOnA := func(modelID, runID string) modelCallProbe {
		t.Helper()
		wg.Add(1)
		go func() {
			defer wg.Done()
			cCtx := app.WithRequestedModelID(ctx, modelID)
			slotA.svc.StreamChat(cCtx, "owner-1", "agent-1", "session-1", runID, "", "{}", nil, func(StreamEvent) error { return nil })
		}()
		select {
		case p := <-modelProv.started:
			return p
		case <-time.After(3 * time.Second):
			t.Fatalf("stream %s did not start on Slot A", runID)
			return modelCallProbe{}
		}
	}

	probeCancel := startOnA("m1", "run-draining-cancel")
	probeRevoke := startOnA("m2", "run-draining-revoke")

	// Simulate Slot A entering post-grace Hertz Shutdown: HTTP listener on Slot A is closed,
	// while in-flight streams and Slot A's RPC server remain open until streams finish.
	slotA.httpSrv.Close()

	// 1. Cancel on Slot B falls back to Slot A's open RPC port and cancels probeCancel immediately!
	if !slotB.svc.Cancel(testToken, "agent-1", "run-draining-cancel") {
		t.Fatal("Cancel on Slot B returned false")
	}
	select {
	case <-probeCancel.ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("RPC fallback failed to cancel draining stream on Slot A after HTTP listener closed")
	}
	if probeRevoke.ctx.Err() != nil {
		t.Fatal("targeted Cancel unexpectedly canceled sibling run on Slot A")
	}

	// 2. Disabling model m2 on Slot B revokes probeRevoke on Slot A via active-call store watcher even with HTTP closed!
	no := false
	if _, err := slotB.svc.UpdateModelProfile(ctx, app.UpdateModelProfileInput{ID: "m2", Enabled: &no}); err != nil {
		t.Fatalf("UpdateModelProfile m2 on Slot B: %v", err)
	}
	select {
	case <-probeRevoke.ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("active-call store watcher failed to revoke disabled model m2 on draining Slot A")
	}

	// Verify peer auth boundary: unauthenticated peer endpoint calls are rejected with 401.
	badPayload, _ := json.Marshal(map[string]string{"agentId": "agent-1", "runId": "run-x"})
	badResp, err := http.Post(slotB.httpSrv.URL+"/internal/v1/peer/cancel", "application/json", bytes.NewReader(badPayload))
	if err != nil {
		t.Fatalf("unauth POST /internal/v1/peer/cancel: %v", err)
	}
	_ = badResp.Body.Close()
	if badResp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated peer cancel status = %d, want 401", badResp.StatusCode)
	}
}
