package main

import (
	"teamie/backend/pkg/observability"
	"context"
	"github.com/cloudwego/kitex/server"
	"net"
	"teamie/backend/internal/api"
	"teamie/backend/internal/config"
	"teamie/backend/internal/identity"
	"teamie/backend/kitex_gen/im/identityservice"
)

func main() {
	stopLogging := observability.Configure("teamie.identity", api.BuildVersion, api.BuildSHA)
	defer stopLogging()
	flags := config.Flags{Path: api.Env("FEATURE_FLAGS_FILE", "")}
	if e := flags.Validate(); e != nil {
		observability.Fatal("Service initialization failed", e)
	}
	dbURL := api.Env("DATABASE_URL", "")
	if dbURL == "" {
		observability.Fatal("DATABASE_URL is required", nil)
	}
	origin := api.Env("APP_ORIGIN", "")
	if origin == "" {
		observability.Fatal("APP_ORIGIN is required", nil)
	}
	store, e := identity.Open(context.Background(), dbURL, flags, api.Env("BOOTSTRAP_INVITE_CODE", identity.DefaultBootstrapInviteCode))
	if e != nil {
		observability.Fatal("Service initialization failed", e)
	}
	defer store.DB.Close()
	store.ControlToken = api.Env("IM_PRINCIPAL_CONTROL_TOKEN", "")
	store.RuntimeToken = api.Env("IM_PRINCIPAL_RUNTIME_TOKEN", "")
	addr, e := net.ResolveTCPAddr("tcp", api.Env("RPC_ADDR", ":8881"))
	if e != nil {
		observability.Fatal("Service initialization failed", e)
	}
	rpc := identityservice.NewServer(&identity.RPC{Store: store}, server.WithServiceAddr(addr), observability.RPCServer())
	defer rpc.Stop()
	go func() {
		if e := rpc.Run(); e != nil {
			observability.Fatal("Service initialization failed", e)
		}
	}()
	api.Serve(api.Env("HTTP_ADDR", ":8081"), (&identity.Handler{Store: store, Origin: origin, Secure: api.Env("COOKIE_SECURE", "true") == "true"}).Routes())
}
