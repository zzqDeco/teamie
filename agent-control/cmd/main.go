package main

import (
	"context"
	"github.com/cloudwego/kitex/server"
	"net"
	"os"
	"teamie/agent-control/internal/control"
	"teamie/agent-protocol/kitex_gen/agent/agentcontrolservice"
	"teamie/backend/pkg/observability"
)

func env(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}
func main() {
	stopLogging := observability.Configure("teamie.agent-control", control.Version, control.SHA)
	defer stopLogging()
	origin := env("APP_ORIGIN", "")
	database := env("DATABASE_URL", "")
	token := env("IM_PRINCIPAL_CONTROL_TOKEN", "")
	runtimeToken := env("AGENT_RUNTIME_TOKEN", "")
	if origin == "" || database == "" || token == "" || runtimeToken == "" {
		observability.Fatal("APP_ORIGIN, DATABASE_URL and scoped service credentials are required", nil)
	}
	im, e := control.NewIM(env("IDENTITY_RPC_ADDR", "identity:8881"), env("CHAT_RPC_ADDR", "chat:8882"), token)
	if e != nil {
		observability.Fatal("Service initialization failed", e)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	store, e := control.Open(ctx, database, im, control.Flags{Path: env("FEATURE_FLAGS_FILE", "")})
	if e != nil {
		observability.Fatal("Service initialization failed", e)
	}
	defer store.DB.Close()
	store.RuntimeToken = runtimeToken
	addr, e := net.ResolveTCPAddr("tcp", env("RPC_ADDR", ":8884"))
	if e != nil {
		observability.Fatal("Service initialization failed", e)
	}
	rpc := agentcontrolservice.NewServer(&control.RPC{Store: store}, server.WithServiceAddr(addr), observability.RPCServer())
	defer rpc.Stop()
	go func() {
		if e := rpc.Run(); e != nil {
			observability.Fatal("Service initialization failed", e)
		}
	}()
	go store.Run(ctx)
	control.Serve(env("HTTP_ADDR", ":8084"), (&control.Handler{Store: store, Origin: origin}).Routes())
}
