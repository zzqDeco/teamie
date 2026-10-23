package gateway_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	hertzserver "github.com/cloudwego/hertz/pkg/app/server"
	"github.com/cloudwego/hertz/pkg/common/adaptor"
	"github.com/cloudwego/kitex/client"
	kitexserver "github.com/cloudwego/kitex/server"
	rpcx "teamie/services/llm-gateway/internal/infra/rpcclient"
	"teamie/contracts/gen/agent/kitex_gen/agent"
	"teamie/contracts/gen/agent/kitex_gen/agent/modelgatewayservice"
	"teamie/contracts/gen/agent/hertz_gen/agent/modeltransport"
)

func fixtureHTTPChat(t *testing.T, s *Service, ctx context.Context, in *agent.GatewayChatRequest) (*agent.GatewayChatResponse, error) {
	t.Helper()
	raw, _ := json.Marshal(modeltransport.CallRequest{OwnerID: in.OwnerId, AgentID: in.AgentId, SessionID: in.SessionId, RunID: in.RunId, RequestJSON: in.RequestJson})
	r, _ := http.NewRequestWithContext(ctx, http.MethodPost, modeltransport.GeneratePath, bytes.NewReader(raw))
	r.Header.Set("Authorization", "Bearer "+in.ServiceToken)
	w := httptest.NewRecorder()
	s.Routes(rpcx.AdminAuth{}).ServeHTTP(w, r)
	if w.Code != 200 {
		var failure struct {
			Error struct{ Code, Message string }
		}
		if err := json.Unmarshal(w.Body.Bytes(), &failure); err != nil {
			return nil, err
		}
		return &agent.GatewayChatResponse{Ok: false, ErrorCode: &failure.Error.Code, ErrorMessage: &failure.Error.Message, StatusCode: ptr(int32(w.Code))}, nil
	}
	var out agent.GatewayChatResponse
	err := json.Unmarshal(w.Body.Bytes(), &out)
	if hdr := strings.TrimSpace(w.Header().Get(modeltransport.ModelConfigIDHeader)); hdr != "" {
		out.ModelConfigId = &hdr
	}
	return &out, err
}

func TestRetiredKitexChatFailsClosedBeforeProvider(t *testing.T) {
	var calls atomic.Int32
	s, unused := streamFixtureService(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		t.Error("retired RPC reached the provider")
	})
	unused.Close()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := modelgatewayservice.NewServer(s, kitexserver.WithListener(listener), kitexserver.WithExitWaitTime(100*time.Millisecond))
	done := make(chan error, 1)
	go func() { done <- server.Run() }()
	defer func() { _ = server.Stop(); _ = listener.Close(); <-done }()
	caller, err := modelgatewayservice.NewClient("teamie.model-gateway", client.WithHostPorts(listener.Addr().String()), client.WithConnectTimeout(time.Second), client.WithRPCTimeout(0))
	if err != nil {
		t.Fatal(err)
	}
	for _, token := range []string{"wrong", testToken} {
		ctx, cancel := context.WithCancel(context.Background())
		response, err := caller.Chat(ctx, &agent.GatewayChatRequest{ServiceToken: token, OwnerId: "o", AgentId: "a", SessionId: "s", RunId: "kitex-cancel", RequestJson: fixtureNativeRequest(`{"messages":[{"role":"user","content":"silent model"}]}`, false)})
		cancel()
		want := "unauthorized"
		if token == testToken {
			want = "protocol_unsupported"
		}
		if err != nil || response.Ok || response.GetErrorCode() != want {
			t.Fatalf("retired RPC did not fail closed: %+v %v", response, err)
		}
	}
	if calls.Load() != 0 || s.App.InFlightSlots() != 0 {
		t.Fatal("retired RPC occupied provider capacity")
	}
}

func serveUnaryHertz(t *testing.T, handler http.Handler) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	h := hertzserver.New(hertzserver.WithListener(listener), hertzserver.WithReadTimeout(15*time.Second), hertzserver.WithWriteTimeout(0), hertzserver.WithSenseClientDisconnection(true), hertzserver.WithMaxRequestBodySize(2*modeltransport.MaxRequestBytes+4096))
	h.Any("/*path", adaptor.HertzHandler(handler))
	stopped := make(chan error, 1)
	go func() { stopped <- h.Run() }()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = h.Shutdown(ctx)
		_ = listener.Close()
		select {
		case <-stopped:
		case <-ctx.Done():
		}
	})
	return "http://" + listener.Addr().String()
}

func unaryRequest(t *testing.T, ctx context.Context, base, run string) *http.Request {
	t.Helper()
	raw, _ := json.Marshal(modeltransport.CallRequest{OwnerID: "o", AgentID: "a", SessionID: "s", RunID: run, RequestJSON: fixtureNativeRequest(`{"messages":[{"role":"user","content":"silent model"}]}`, false)})
	r, err := http.NewRequestWithContext(ctx, http.MethodPost, base+modeltransport.GeneratePath, bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	r.Header.Set("Authorization", "Bearer "+testToken)
	return r
}

func TestHertzUnaryDisconnectCancelsProviderAndReleasesSlotWithoutCancelRPC(t *testing.T) {
	for _, ending := range []string{"waiting_for_provider_headers", "reading_provider_body", "caller_connection_lost"} {
		t.Run(ending, func(t *testing.T) {
			caller := &http.Client{Transport: &http.Transport{Proxy: nil}}
			defer caller.CloseIdleConnections()
			entered, canceled := make(chan struct{}), make(chan struct{})
			var calls atomic.Int32
			s, unused := streamFixtureService(t, func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				if calls.Add(1) != 1 {
					w.Header().Set("Content-Type", "application/json")
					fmt.Fprint(w, lifetimeCompletion)
					return
				}
				if ending == "reading_provider_body" {
					w.Header().Set("Content-Type", "application/json")
					fmt.Fprint(w, `{"choices":[`)
					w.(http.Flusher).Flush()
				}
				close(entered)
				<-r.Context().Done()
				close(canceled)
			})
			unused.Close()
			// Cleanup is the only explicit cancellation call in this fixture.
			// The assertions below must succeed before it can run.
			t.Cleanup(func() { s.CancelRun("a", "disconnect") })
			base := serveUnaryHertz(t, s.Routes(rpcx.AdminAuth{}))
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var stopCaller func()
			finished := make(chan error, 1)
			if ending == "caller_connection_lost" {
				conn, err := net.Dial("tcp", strings.TrimPrefix(base, "http://"))
				if err != nil {
					t.Fatal(err)
				}
				defer conn.Close()
				if err = unaryRequest(t, context.Background(), base, "disconnect").Write(conn); err != nil {
					t.Fatal(err)
				}
				stopCaller = func() { _ = conn.Close() }
			} else {
				go func() {
					res, err := caller.Do(unaryRequest(t, ctx, base, "disconnect"))
					if res != nil {
						res.Body.Close()
					}
					finished <- err
				}()
				stopCaller = cancel
			}
			select {
			case <-entered:
			case <-time.After(2 * time.Second):
				t.Fatal("Hertz unary call did not reach the provider")
			}
			stopCaller()
			select {
			case <-canceled:
			case <-time.After(time.Second):
				t.Fatal("Hertz caller disconnect left the provider active")
			}
			if ending != "caller_connection_lost" {
				if err := <-finished; err == nil {
					t.Fatal("canceled caller reported success")
				}
			}
			deadline := time.Now().Add(time.Second)
			for s.App.InFlightSlots() > 0 && time.Now().Before(deadline) {
				time.Sleep(time.Millisecond)
			}
			if s.App.InFlightSlots() != 0 {
				t.Fatal("canceled provider retained the sole model slot")
			}
			response, err := caller.Do(unaryRequest(t, context.Background(), base, "next-run"))
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			var out agent.GatewayChatResponse
			if json.NewDecoder(response.Body).Decode(&out) != nil || !out.Ok || calls.Load() != 2 {
				t.Fatalf("next generation did not reuse recovered capacity: %+v", out)
			}
			usage := s.Store.(*memoryStore)
			usage.mu.Lock()
			defer usage.mu.Unlock()
			if len(usage.usage) != 2 || usage.usage[0].Status != "canceled" || usage.usage[1].Status != "success" {
				t.Fatalf("disconnect was not audited before the next complete call: %+v", usage.usage)
			}
		})
	}
}

func TestPrivateGenerationRequiresServiceBearerRatherThanBrowserCredentials(t *testing.T) {
	var calls atomic.Int32
	s, unused := streamFixtureService(t, func(w http.ResponseWriter, r *http.Request) { calls.Add(1) })
	unused.Close()
	for _, path := range []string{modeltransport.GeneratePath, modeltransport.StreamPath} {
		for _, bearer := range []string{"", "Bearer wrong"} {
			request := unaryRequest(t, context.Background(), "http://gateway.example", "browser")
			request.URL.Path = path
			request.Header.Set("Authorization", bearer)
			request.AddCookie(&http.Cookie{Name: "teamie_session", Value: strings.Repeat("b", 64)})
			request.Header.Set("Origin", "https://teamie.example")
			request.Header.Set("X-CSRF-Token", strings.Repeat("c", 64))
			response := httptest.NewRecorder()
			s.Routes(rpcx.AdminAuth{}).ServeHTTP(response, request)
			if response.Code != 401 || !strings.Contains(response.Body.String(), `"unauthorized"`) {
				t.Fatalf("browser credentials admitted private generation at %s: %s", path, response.Body.String())
			}
		}
	}
	if calls.Load() != 0 || s.App.InFlightSlots() != 0 {
		t.Fatal("unauthorized browser call reached model capacity")
	}
}

func TestGatewayExposesAndRotatesModelConfigIDAcrossUnaryStreamAndPing(t *testing.T) {
	var providerCalls atomic.Int32
	s, provider := streamFixtureService(t, func(w http.ResponseWriter, r *http.Request) {
		providerCalls.Add(1)
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, lifetimeCompletion)
	})
	defer provider.Close()

	pingA, err := s.Ping(context.Background(), &agent.PingRequest{ServiceToken: &s.Token})
	if err != nil || !pingA.Ready || pingA.GetModelConfigId() == "" {
		t.Fatalf("gateway Ping did not expose opaque ModelConfigId: %+v (%v)", pingA, err)
	}
	configA := pingA.GetModelConfigId()
	if strings.Contains(configA, provider.URL) || strings.Contains(configA, "fixture") {
		t.Fatalf("ModelConfigId leaked raw URL or model name: %q", configA)
	}

	// Verify unary POST response carries X-Teamie-Model-Config-Id header while keeping the JSON body compatible with older DisallowUnknownFields decoders.
	rawCall, _ := json.Marshal(modeltransport.CallRequest{
		OwnerID:     "o",
		AgentID:     "a",
		SessionID:   "s",
		RunID:       "r-1",
		RequestJSON: fixtureNativeRequest(`{"messages":[{"role":"user","content":"hello"}]}`, false),
	})
	postReq, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, modeltransport.GeneratePath, bytes.NewReader(rawCall))
	postReq.Header.Set("Authorization", "Bearer "+testToken)
	postReq.Header.Set(modeltransport.ModelConfigIDHeader, configA)
	postRec := httptest.NewRecorder()
	s.Routes(rpcx.AdminAuth{}).ServeHTTP(postRec, postReq)
	var legacyOut struct {
		Ok           bool    `json:"ok"`
		ResponseJson *string `json:"responseJson,omitempty"`
		ErrorCode    *string `json:"errorCode,omitempty"`
		ErrorMessage *string `json:"errorMessage,omitempty"`
		StatusCode   *int32  `json:"statusCode,omitempty"`
	}
	dec := json.NewDecoder(bytes.NewReader(postRec.Body.Bytes()))
	dec.DisallowUnknownFields()
	if postRec.Code != 200 || dec.Decode(&legacyOut) != nil || !legacyOut.Ok || postRec.Header().Get(modeltransport.ModelConfigIDHeader) != configA || strings.Contains(postRec.Body.String(), "modelConfigId") {
		t.Fatalf("unary response incompatible or missing header: header=%q body=%s", postRec.Header().Get(modeltransport.ModelConfigIDHeader), postRec.Body.String())
	}

	// Rotate model in store while keeping ContextWindowTokens unchanged -> ModelConfigId must change.
	cfg, err := s.Store.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	cfg.Model = "rotated-model-b"
	if _, err := s.Store.Save(context.Background(), cfg, nil, false, s.Cipher); err != nil {
		t.Fatal(err)
	}
	pingB, err := s.Ping(context.Background(), &agent.PingRequest{ServiceToken: &s.Token})
	if err != nil || !pingB.Ready || pingB.GetModelConfigId() == "" || pingB.GetModelConfigId() == configA {
		t.Fatalf("rotating model under same window did not rotate ModelConfigId: before=%q after=%+v", configA, pingB)
	}
	configB := pingB.GetModelConfigId()

	// Requests bound to stale configA must be rejected before reaching the provider.
	callsBefore := providerCalls.Load()
	staleUnaryReq, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, modeltransport.GeneratePath, bytes.NewReader(rawCall))
	staleUnaryReq.Header.Set("Authorization", "Bearer "+testToken)
	staleUnaryReq.Header.Set(modeltransport.ModelConfigIDHeader, configA)
	staleUnaryRec := httptest.NewRecorder()
	s.Routes(rpcx.AdminAuth{}).ServeHTTP(staleUnaryRec, staleUnaryReq)
	var staleOut agent.GatewayChatResponse
	if staleUnaryRec.Code != 200 || json.Unmarshal(staleUnaryRec.Body.Bytes(), &staleOut) != nil || staleOut.Ok || staleOut.GetErrorCode() != "gateway_unavailable" || staleUnaryRec.Header().Get(modeltransport.ModelConfigIDHeader) != configB {
		t.Fatalf("stale unary request was not rejected with active config header: header=%q out=%+v", staleUnaryRec.Header().Get(modeltransport.ModelConfigIDHeader), staleOut)
	}

	staleStreamReq, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, modeltransport.StreamPath, bytes.NewReader(rawCall))
	staleStreamReq.Header.Set("Authorization", "Bearer "+testToken)
	staleStreamReq.Header.Set(modeltransport.ModelConfigIDHeader, configA)
	staleStreamRec := httptest.NewRecorder()
	s.Routes(rpcx.AdminAuth{}).ServeHTTP(staleStreamRec, staleStreamReq)
	var staleStreamEvent StreamEvent
	if staleStreamRec.Code != 200 || json.Unmarshal(staleStreamRec.Body.Bytes(), &staleStreamEvent) != nil || staleStreamEvent.Type != "error" || staleStreamEvent.ErrorCode != "gateway_unavailable" || staleStreamRec.Header().Get(modeltransport.ModelConfigIDHeader) != configB {
		t.Fatalf("stale stream request was not rejected before provider: header=%q event=%+v", staleStreamRec.Header().Get(modeltransport.ModelConfigIDHeader), staleStreamEvent)
	}
	if providerCalls.Load() != callsBefore {
		t.Fatalf("stale config requests reached upstream provider: before=%d after=%d", callsBefore, providerCalls.Load())
	}
}
