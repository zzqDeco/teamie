package main

import (
	"context"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/cloudwego/hertz/pkg/app/server"
	"go.uber.org/zap"
	"teamie/contracts/gen/agent/kitex_gen/agent/agentruntimeservice"
	"teamie/pkg/httpx"
	"teamie/pkg/observability"
	"teamie/pkg/rpcx"
	httpadapter "teamie/services/agent-runtime/internal/adapter/http"
	rpcadapter "teamie/services/agent-runtime/internal/adapter/rpc"
	"teamie/services/agent-runtime/internal/adapter/worker"
	"teamie/services/agent-runtime/internal/app"
	"teamie/services/agent-runtime/internal/infra/eino"
	"teamie/services/agent-runtime/internal/infra/rpcclient"
)

var BuildVersion = "unreleased"
var BuildSHA = "unknown"

func main() {
	stopLogging := observability.Configure("teamie.agent-runtime", BuildVersion, BuildSHA)
	defer stopLogging()
	for _, k := range []string{"DATABASE_URL", "AGENT_RUNTIME_TOKEN", "IM_PRINCIPAL_RUNTIME_TOKEN", "GATEWAY_RUNTIME_TOKEN", "SANDBOX_RUNTIME_TOKEN"} {
		if os.Getenv(k) == "" {
			observability.Fatal(k+" is required", nil)
		}
	}
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	startup, done := context.WithTimeout(ctx, 30*time.Second)
	store, err := eino.Open(startup, os.Getenv("DATABASE_URL"))
	done()
	if err != nil {
		observability.Fatal("runtime database initialization failed", err)
	}
	defer store.DB.Close()

	cfg := eino.Config{
		ControlAddr:      httpx.Env("AGENT_CONTROL_RPC_ADDR", "agent-control:8884"),
		GatewayAddr:      httpx.Env("GATEWAY_RPC_ADDR", "llm-gateway:8886"),
		SandboxAddr:      httpx.Env("SANDBOX_RPC_ADDR", "sandbox:8887"),
		IdentityAddr:     httpx.Env("IDENTITY_RPC_ADDR", "identity:8881"),
		IMURL:            httpx.Env("IM_API_URL", "http://chat:8082"),
		IMRealtimeURL:    httpx.Env("IM_REALTIME_URL", "http://realtime:8083"),
		ControlToken:     os.Getenv("AGENT_RUNTIME_TOKEN"),
		GatewayToken:     os.Getenv("GATEWAY_RUNTIME_TOKEN"),
		SandboxToken:     os.Getenv("SANDBOX_RUNTIME_TOKEN"),
		IMToken:          os.Getenv("IM_PRINCIPAL_RUNTIME_TOKEN"),
		FlagsFile:        os.Getenv("FEATURE_FLAGS_FILE"),
		GatewayStreamURL: httpx.Env("GATEWAY_STREAM_URL", "http://llm-gateway:8086/internal/v1/chat/stream"),
	}
	runtimeEngine, err := eino.New(store, cfg)
	if err != nil {
		observability.Fatal("runtime client initialization failed", err)
	}

	repo := store.Repo()
	appSvc := &app.Service{
		Tx:           repo,
		Sessions:     repo,
		Schedules:    repo,
		Archive:      repo,
		ToolReceipts: repo,
		Inspect:      repo,
		Control:      &rpcclient.ControlAdapter{Client: runtimeEngine.Control, Token: cfg.ControlToken},
		Model:        &rpcclient.ModelGatewayAdapter{Client: runtimeEngine.Gateway, Token: cfg.GatewayToken},
		Sandbox:      &rpcclient.SandboxAdapter{Client: runtimeEngine.Sandbox, Token: cfg.SandboxToken},
		Engine:       runtimeEngine,
		ControlToken: cfg.ControlToken,
	}

	rpcAddr, err := net.ResolveTCPAddr("tcp", httpx.Env("RPC_ADDR", ":8885"))
	if err != nil {
		observability.Fatal("invalid runtime RPC address", err)
	}
	rpc := agentruntimeservice.NewServer(&rpcadapter.Server{App: appSvc}, rpcx.ServerOptions(rpcAddr, 0)...)
	go func() {
		if err := rpc.Run(); err != nil {
			observability.L(ctx).Error("Runtime RPC stopped", zap.Error(err))
			cancel()
		}
	}()

	h := server.Default(server.WithHostPorts(httpx.Env("HTTP_ADDR", ":8085")), server.WithMaxRequestBodySize(16384))
	(&httpadapter.Handler{
		App:     appSvc,
		Version: BuildVersion,
		SHA:     BuildSHA,
	}).RegisterRoutes(h)

	stopped := make(chan struct{})
	runtimeWorker := worker.New(appSvc)
	go func() { defer close(stopped); runtimeWorker.Run(ctx) }()
	go func() {
		if err := h.Run(); err != nil {
			observability.L(ctx).Error("Runtime HTTP stopped", zap.Error(err))
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
