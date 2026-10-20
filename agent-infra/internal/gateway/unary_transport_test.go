package gateway

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
	"teamie/agent-infra/internal/api"
	"teamie/agent-protocol/kitex_gen/agent"
	"teamie/agent-protocol/kitex_gen/agent/modelgatewayservice"
	"teamie/agent-protocol/modeltransport"
)

func fixtureHTTPChat(t *testing.T, s *Service, ctx context.Context, in *agent.GatewayChatRequest) (*agent.GatewayChatResponse, error) {
	t.Helper()
	raw, _ := json.Marshal(modeltransport.CallRequest{OwnerID: in.OwnerId, AgentID: in.AgentId, SessionID: in.SessionId, RunID: in.RunId, RequestJSON: in.RequestJson})
	r, _ := http.NewRequestWithContext(ctx, http.MethodPost, modeltransport.GeneratePath, bytes.NewReader(raw))
	r.Header.Set("Authorization", "Bearer "+in.ServiceToken)
	w := httptest.NewRecorder()
	s.Routes(api.AdminAuth{}).ServeHTTP(w, r)
	if w.Code != 200 {
		var failure struct {
			Error struct{ Code, Message string }
		}
		if err := json.Unmarshal(w.Body.Bytes(), &failure); err != nil {
			return nil, err
		}
		return failed(api.Err(w.Code, failure.Error.Code, failure.Error.Message)), nil
	}
	var out agent.GatewayChatResponse
	err := json.Unmarshal(w.Body.Bytes(), &out)
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
	if calls.Load() != 0 || len(s.slots) != 0 {
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
			base := serveUnaryHertz(t, s.Routes(api.AdminAuth{}))
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
			for len(s.slots) > 0 && time.Now().Before(deadline) {
				time.Sleep(time.Millisecond)
			}
			if len(s.slots) != 0 {
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
			s.Routes(api.AdminAuth{}).ServeHTTP(response, request)
			if response.Code != 401 || !strings.Contains(response.Body.String(), `"unauthorized"`) {
				t.Fatalf("browser credentials admitted private generation at %s: %s", path, response.Body.String())
			}
		}
	}
	if calls.Load() != 0 || len(s.slots) != 0 {
		t.Fatal("unauthorized browser call reached model capacity")
	}
}

func TestGatewayExposesAndRotatesModelConfigIDAcrossUnaryStreamAndPing(t *testing.T) {
	s, provider := streamFixtureService(t, func(w http.ResponseWriter, r *http.Request) {
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

	// Verify unary POST response carries both ModelConfigId field and X-Teamie-Model-Config-Id header.
	rawCall, _ := json.Marshal(modeltransport.CallRequest{
		OwnerID:     "o",
		AgentID:     "a",
		SessionID:   "s",
		RunID:       "r-1",
		RequestJSON: fixtureNativeRequest(`{"messages":[{"role":"user","content":"hello"}]}`, false),
	})
	postReq, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, modeltransport.GeneratePath, bytes.NewReader(rawCall))
	postReq.Header.Set("Authorization", "Bearer "+testToken)
	postRec := httptest.NewRecorder()
	s.Routes(api.AdminAuth{}).ServeHTTP(postRec, postReq)
	var chatOut agent.GatewayChatResponse
	if postRec.Code != 200 || json.Unmarshal(postRec.Body.Bytes(), &chatOut) != nil || !chatOut.Ok || chatOut.GetModelConfigId() != configA || postRec.Header().Get(modeltransport.ModelConfigIDHeader) != configA {
		t.Fatalf("unary response did not carry ModelConfigId: header=%q out=%+v", postRec.Header().Get(modeltransport.ModelConfigIDHeader), chatOut)
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
}
