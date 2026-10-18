package main

import (
	"teamie/backend/pkg/observability"
	"context"
	"encoding/json"
	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/cloudwego/hertz/pkg/common/adaptor"
	kitexserver "github.com/cloudwego/kitex/server"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"teamie/agent-protocol/kitex_gen/agent/agentruntimeservice"
	"teamie/agent-runtime/internal/engine"
	"time"
)

var BuildVersion = "unreleased"
var BuildSHA = "unknown"

func env(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}
func main() {
	stopLogging := observability.Configure("teamie.agent-runtime", BuildVersion, BuildSHA)
	defer stopLogging()
	for _, k := range []string{"DATABASE_URL", "AGENT_RUNTIME_TOKEN", "IM_PRINCIPAL_RUNTIME_TOKEN", "GATEWAY_RUNTIME_TOKEN", "SANDBOX_RUNTIME_TOKEN"} {
		if os.Getenv(k) == "" {
			observability.Fatal("Required runtime configuration is missing", nil)
		}
	}
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	startup, done := context.WithTimeout(ctx, 30*time.Second)
	store, err := engine.Open(startup, os.Getenv("DATABASE_URL"))
	done()
	if err != nil {
		observability.Fatal("runtime database initialization failed", nil)
	}
	defer store.DB.Close()
	runtime, err := engine.New(store, engine.Config{ControlAddr: env("AGENT_CONTROL_RPC_ADDR", "agent-control:8884"), GatewayAddr: env("GATEWAY_RPC_ADDR", "llm-gateway:8886"), SandboxAddr: env("SANDBOX_RPC_ADDR", "sandbox:8887"), IdentityAddr: env("IDENTITY_RPC_ADDR", "identity:8881"), IMURL: env("IM_API_URL", "http://chat:8082"), IMRealtimeURL: env("IM_REALTIME_URL", "http://realtime:8083"), ControlToken: os.Getenv("AGENT_RUNTIME_TOKEN"), GatewayToken: os.Getenv("GATEWAY_RUNTIME_TOKEN"), SandboxToken: os.Getenv("SANDBOX_RUNTIME_TOKEN"), IMToken: os.Getenv("IM_PRINCIPAL_RUNTIME_TOKEN"), FlagsFile: os.Getenv("FEATURE_FLAGS_FILE"), GatewayStreamURL: env("GATEWAY_STREAM_URL", "http://llm-gateway:8086/internal/v1/chat/stream")})
	if err != nil {
		observability.Fatal("runtime client initialization failed", nil)
	}
	rpcAddr, err := net.ResolveTCPAddr("tcp", env("RPC_ADDR", ":8885"))
	if err != nil {
		observability.Fatal("invalid runtime RPC address", nil)
	}
	rpc := agentruntimeservice.NewServer(&engine.RuntimeRPC{Engine: runtime}, kitexserver.WithServiceAddr(rpcAddr), observability.RPCServer())
	go func() {
		if rpc.Run() != nil {
			cancel()
		}
	}()
	mux := http.NewServeMux()
	write := func(w http.ResponseWriter, status int, v any) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(v)
	}
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { write(w, 200, map[string]string{"status": "ok"}) })
	mux.HandleFunc("GET /version", func(w http.ResponseWriter, r *http.Request) {
		write(w, 200, map[string]string{"version": BuildVersion, "sha": BuildSHA})
	})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		pc, c := context.WithTimeout(r.Context(), 2*time.Second)
		defer c()
		if runtime.Ready(pc) != nil {
			write(w, 503, map[string]string{"status": "unavailable"})
			return
		}
		write(w, 200, map[string]string{"status": "ready"})
	})
	h := server.Default(server.WithHostPorts(env("HTTP_ADDR", ":8085")), server.WithMaxRequestBodySize(16384))
	h.Any("/*path", adaptor.HertzHandler(observability.HTTP(mux)))
	stopped := make(chan struct{})
	go func() { defer close(stopped); runtime.Run(ctx) }()
	go func() {
		if h.Run() != nil {
			cancel()
		}
	}()
	<-ctx.Done()
	shutdown, stop := context.WithTimeout(context.Background(), 25*time.Second)
	defer stop()
	_ = h.Shutdown(shutdown)
	_ = rpc.Stop()
	select {
	case <-stopped:
	case <-shutdown.Done():
		observability.L(ctx).Warn("Runtime shutdown deadline exceeded")
	}
}
