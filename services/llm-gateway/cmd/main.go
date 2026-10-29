package main

import (
	"context"
	"net"
	"time"

	hertzserver "github.com/cloudwego/hertz/pkg/app/server"
	"go.uber.org/zap"
	"teamie/contracts/gen/agent/kitex_gen/agent/modelgatewayservice"
	"teamie/contracts/gen/agent/hertz_gen/agent/modeltransport"
	"teamie/pkg/httpx"
	"teamie/pkg/observability"
	"teamie/pkg/rpcx"
	httpAdapter "teamie/services/llm-gateway/internal/adapter/http"
	rpcAdapter "teamie/services/llm-gateway/internal/adapter/rpc"
	"teamie/services/llm-gateway/internal/app"
	"teamie/services/llm-gateway/internal/infra/kms"
	"teamie/services/llm-gateway/internal/infra/pg"
	"teamie/services/llm-gateway/internal/infra/rpcclient"
	"teamie/services/llm-gateway/internal/infra/upstream"
)

func main() {
	stopLogging := observability.Configure("teamie.llm-gateway", httpx.BuildVersion, httpx.BuildSHA)
	defer stopLogging()
	token := httpx.Env("GATEWAY_RUNTIME_TOKEN", "")
	if len(token) < 24 {
		observability.Fatal("GATEWAY_RUNTIME_TOKEN must contain at least 24 characters", nil)
	}
	cipher, err := kms.NewCipher(httpx.Env("GATEWAY_MASTER_KEY", ""))
	if err != nil {
		observability.Fatal("Service initialization failed", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	store, err := pg.Open(ctx, httpx.Env("DATABASE_URL", ""))
	cancel()
	if err != nil {
		observability.Fatal("gateway database initialization failed", err)
	}
	defer store.DB.Close()
	origin := httpx.Env("APP_ORIGIN", "")
	if origin == "" {
		observability.Fatal("APP_ORIGIN is required", nil)
	}
	auth, err := rpcclient.NewAdminAuth(httpx.Env("IDENTITY_RPC_ADDR", "identity:8881"), origin)
	if err != nil {
		observability.Fatal("identity client initialization failed", err)
	}
	maxConcurrentCalls, err := app.ParseMaxConcurrentCalls(httpx.Env("GATEWAY_MAX_CONCURRENT_CALLS", ""))
	if err != nil {
		observability.Fatal("invalid GATEWAY_MAX_CONCURRENT_CALLS", err)
	}
	provider := upstream.Provider{HTTP: upstream.NewHTTPClient()}
	svc := app.New(store, cipher, provider, provider, token, app.WithMaxConcurrentCalls(maxConcurrentCalls))
	observability.L(nil).Info("Gateway concurrency configured",
		zap.String("event.action", "gateway-concurrency-configured"),
		zap.String("event.outcome", "success"),
		zap.Int("custom.gateway.max_concurrent_calls", svc.MaxConcurrentCalls()),
	)
	rpcSrv := rpcAdapter.New(svc)
	httpHandler := httpAdapter.New(svc)

	addr, err := net.ResolveTCPAddr("tcp", httpx.Env("RPC_ADDR", ":8886"))
	if err != nil {
		observability.Fatal("invalid RPC_ADDR", err)
	}
	rpc := modelgatewayservice.NewServer(rpcSrv, rpcx.ServerOptions(addr, 0)...)
	defer rpc.Stop()
	go func() {
		if err := rpc.Run(); err != nil {
			observability.Fatal("gateway RPC stopped", err)
		}
	}()
	go func() {
		ticker := time.NewTicker(time.Hour)
		defer ticker.Stop()
		for range ticker.C {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			_ = store.Prune(ctx)
			cancel()
		}
	}()
	httpx.ServeWithOptions(
		httpx.Env("HTTP_ADDR", ":8086"),
		httpHandler.Routes(auth),
		hertzserver.WithWriteTimeout(0),
		hertzserver.WithMaxRequestBodySize(2*modeltransport.MaxRequestBytes+4096),
	)
}
