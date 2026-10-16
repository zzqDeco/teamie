package main

import (
	"context"
	"github.com/cloudwego/kitex/server"
	"github.com/redis/go-redis/v9"
	"log"
	"net"
	"teamie/backend/internal/api"
	"teamie/backend/internal/chat"
	"teamie/backend/internal/config"
	"teamie/backend/internal/rpcutil"
	"teamie/backend/kitex_gen/im/chatservice"
)

func main() {
	flags := config.Flags{Path: api.Env("FEATURE_FLAGS_FILE", "")}
	if e := flags.Validate(); e != nil {
		log.Fatal(e)
	}
	dbURL := api.Env("DATABASE_URL", "")
	if dbURL == "" {
		log.Fatal("DATABASE_URL is required")
	}
	origin := api.Env("APP_ORIGIN", "")
	if origin == "" {
		log.Fatal("APP_ORIGIN is required")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	store, e := chat.Open(ctx, dbURL, flags)
	if e != nil {
		log.Fatal(e)
	}
	defer store.DB.Close()
	identity, e := rpcutil.NewIdentity(api.Env("IDENTITY_RPC_ADDR", "identity:8881"))
	if e != nil {
		log.Fatal(e)
	}
	rdb := redis.NewClient(&redis.Options{Addr: api.Env("REDIS_ADDR", "redis:6379")})
	defer rdb.Close()
	go store.RunOutbox(ctx, rdb)
	go store.RunAttachmentCleanup(ctx)
	addr, e := net.ResolveTCPAddr("tcp", api.Env("RPC_ADDR", ":8882"))
	if e != nil {
		log.Fatal(e)
	}
	rpc := chatservice.NewServer(&chat.RPC{Store: store, Identity: identity, ControlToken: api.Env("IM_PRINCIPAL_CONTROL_TOKEN", "")}, server.WithServiceAddr(addr))
	defer rpc.Stop()
	go func() {
		if e := rpc.Run(); e != nil {
			log.Fatal(e)
		}
	}()
	api.Serve(api.Env("HTTP_ADDR", ":8082"), (&chat.Handler{Store: store, Identity: identity, Origin: origin}).Routes())
}
