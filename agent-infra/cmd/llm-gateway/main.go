package main

import (
	"context"
	hertzserver "github.com/cloudwego/hertz/pkg/app/server"
	"github.com/cloudwego/kitex/server"
	"net"
	"teamie/agent-infra/internal/api"
	"teamie/agent-infra/internal/gateway"
	"teamie/agent-protocol/kitex_gen/agent/modelgatewayservice"
	"teamie/agent-protocol/modeltransport"
	"teamie/backend/pkg/observability"
	"time"
)

func main() {
	stopLogging := observability.Configure("teamie.llm-gateway", api.BuildVersion, api.BuildSHA)
	defer stopLogging()
	token := api.Env("GATEWAY_RUNTIME_TOKEN", "")
	if len(token) < 24 {
		observability.Fatal("GATEWAY_RUNTIME_TOKEN must contain at least 24 characters", nil)
	}
	crypto, e := gateway.NewCipher(api.Env("GATEWAY_MASTER_KEY", ""))
	if e != nil {
		observability.Fatal("Service initialization failed", e)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	store, e := gateway.Open(ctx, api.Env("DATABASE_URL", ""))
	cancel()
	if e != nil {
		observability.Fatal("gateway database initialization failed", e)
	}
	defer store.DB.Close()
	origin := api.Env("APP_ORIGIN", "")
	if origin == "" {
		observability.Fatal("APP_ORIGIN is required", nil)
	}
	auth, e := api.NewAdminAuth(api.Env("IDENTITY_RPC_ADDR", "identity:8881"), origin)
	if e != nil {
		observability.Fatal("identity client initialization failed", e)
	}
	service := gateway.New(store, crypto, token)
	addr, e := net.ResolveTCPAddr("tcp", api.Env("RPC_ADDR", ":8886"))
	if e != nil {
		observability.Fatal("invalid RPC_ADDR", e)
	}
	rpc := modelgatewayservice.NewServer(service, server.WithServiceAddr(addr), observability.RPCServer())
	defer rpc.Stop()
	go func() {
		if e := rpc.Run(); e != nil {
			observability.Fatal("gateway RPC stopped", e)
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
	api.Serve(api.Env("HTTP_ADDR", ":8086"), service.Routes(auth), hertzserver.WithWriteTimeout(0), hertzserver.WithMaxRequestBodySize(2*modeltransport.MaxRequestBytes+4096))
}
