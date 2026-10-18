package main

import (
	"teamie/backend/pkg/observability"
	"context"
	"github.com/cloudwego/kitex/server"
	"github.com/redis/go-redis/v9"
	"net"
	"teamie/backend/internal/api"
	"teamie/backend/internal/chat"
	"teamie/backend/internal/config"
	"teamie/backend/internal/rpcutil"
	"teamie/backend/kitex_gen/im/chatservice"
)

func main() {
	stopLogging := observability.Configure("teamie.chat", api.BuildVersion, api.BuildSHA)
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
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	store, e := chat.Open(ctx, dbURL, flags)
	if e != nil {
		observability.Fatal("Service initialization failed", e)
	}
	defer store.DB.Close()
	identity, e := rpcutil.NewIdentity(api.Env("IDENTITY_RPC_ADDR", "identity:8881"))
	if e != nil {
		observability.Fatal("Service initialization failed", e)
	}
	rdb := redis.NewClient(&redis.Options{Addr: api.Env("REDIS_ADDR", "redis:6379")})
	defer rdb.Close()
	go store.RunOutbox(ctx, rdb)
	go store.RunAttachmentCleanup(ctx)
	addr, e := net.ResolveTCPAddr("tcp", api.Env("RPC_ADDR", ":8882"))
	if e != nil {
		observability.Fatal("Service initialization failed", e)
	}
	rpc := chatservice.NewServer(&chat.RPC{Store: store, Identity: identity, ControlToken: api.Env("IM_PRINCIPAL_CONTROL_TOKEN", "")}, server.WithServiceAddr(addr), observability.RPCServer())
	defer rpc.Stop()
	go func() {
		if e := rpc.Run(); e != nil {
			observability.Fatal("Service initialization failed", e)
		}
	}()
	api.Serve(api.Env("HTTP_ADDR", ":8082"), (&chat.Handler{Store: store, Identity: identity, Origin: origin}).Routes())
}
