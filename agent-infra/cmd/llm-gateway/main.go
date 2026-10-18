package main

import (
	"context"
	hertzserver "github.com/cloudwego/hertz/pkg/app/server"
	"github.com/cloudwego/kitex/server"
	"log"
	"net"
	"teamie/agent-infra/internal/api"
	"teamie/agent-infra/internal/gateway"
	"teamie/agent-protocol/kitex_gen/agent/modelgatewayservice"
	"teamie/agent-protocol/modeltransport"
	"time"
)

func main() {
	token := api.Env("GATEWAY_RUNTIME_TOKEN", "")
	if len(token) < 24 {
		log.Fatal("GATEWAY_RUNTIME_TOKEN must contain at least 24 characters")
	}
	crypto, e := gateway.NewCipher(api.Env("GATEWAY_MASTER_KEY", ""))
	if e != nil {
		log.Fatal(e)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	store, e := gateway.Open(ctx, api.Env("DATABASE_URL", ""))
	cancel()
	if e != nil {
		log.Fatal("gateway database initialization failed")
	}
	defer store.DB.Close()
	origin := api.Env("APP_ORIGIN", "")
	if origin == "" {
		log.Fatal("APP_ORIGIN is required")
	}
	auth, e := api.NewAdminAuth(api.Env("IDENTITY_RPC_ADDR", "identity:8881"), origin)
	if e != nil {
		log.Fatal("identity client initialization failed")
	}
	service := gateway.New(store, crypto, token)
	addr, e := net.ResolveTCPAddr("tcp", api.Env("RPC_ADDR", ":8886"))
	if e != nil {
		log.Fatal("invalid RPC_ADDR")
	}
	rpc := modelgatewayservice.NewServer(service, server.WithServiceAddr(addr))
	defer rpc.Stop()
	go func() {
		if rpc.Run() != nil {
			log.Fatal("gateway RPC stopped")
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
