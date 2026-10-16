package main

import (
	"context"
	"github.com/cloudwego/kitex/server"
	"log"
	"net"
	"os"
	"teamie/agent-control/internal/control"
	"teamie/agent-protocol/kitex_gen/agent/agentcontrolservice"
)

func env(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}
func main() {
	origin := env("APP_ORIGIN", "")
	database := env("DATABASE_URL", "")
	token := env("IM_PRINCIPAL_CONTROL_TOKEN", "")
	runtimeToken := env("AGENT_RUNTIME_TOKEN", "")
	if origin == "" || database == "" || token == "" || runtimeToken == "" {
		log.Fatal("APP_ORIGIN, DATABASE_URL and scoped service credentials are required")
	}
	im, e := control.NewIM(env("IDENTITY_RPC_ADDR", "identity:8881"), env("CHAT_RPC_ADDR", "chat:8882"), token)
	if e != nil {
		log.Fatal(e)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	store, e := control.Open(ctx, database, im, control.Flags{Path: env("FEATURE_FLAGS_FILE", "")})
	if e != nil {
		log.Fatal(e)
	}
	defer store.DB.Close()
	store.RuntimeToken = runtimeToken
	addr, e := net.ResolveTCPAddr("tcp", env("RPC_ADDR", ":8884"))
	if e != nil {
		log.Fatal(e)
	}
	rpc := agentcontrolservice.NewServer(&control.RPC{Store: store}, server.WithServiceAddr(addr))
	defer rpc.Stop()
	go func() {
		if e := rpc.Run(); e != nil {
			log.Fatal(e)
		}
	}()
	go store.Run(ctx)
	runtime, e := control.NewRuntime(env("AGENT_RUNTIME_RPC_ADDR", "agent-runtime:8885"))
	if e != nil {
		log.Fatal(e)
	}
	control.Serve(env("HTTP_ADDR", ":8084"), (&control.Handler{Store: store, Origin: origin, Runtime: runtime}).Routes())
}
