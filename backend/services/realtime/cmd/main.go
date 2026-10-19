package main

import (
	"teamie/backend/pkg/observability"
	"context"
	"github.com/redis/go-redis/v9"
	"teamie/backend/internal/api"
	"teamie/backend/internal/config"
	"teamie/backend/internal/realtime"
	"teamie/backend/internal/rpcutil"
)

func main() {
	stopLogging := observability.Configure("teamie.realtime", api.BuildVersion, api.BuildSHA)
	defer stopLogging()
	flags := config.Flags{Path: api.Env("FEATURE_FLAGS_FILE", "")}
	if e := flags.Validate(); e != nil {
		observability.Fatal("Service initialization failed", e)
	}
	origin := api.Env("APP_ORIGIN", "")
	if origin == "" {
		observability.Fatal("APP_ORIGIN is required", nil)
	}
	identity, e := rpcutil.NewIdentity(api.Env("IDENTITY_RPC_ADDR", "identity:8881"))
	if e != nil {
		observability.Fatal("Service initialization failed", e)
	}
	chat, e := rpcutil.NewChat(api.Env("CHAT_RPC_ADDR", "chat:8882"))
	if e != nil {
		observability.Fatal("Service initialization failed", e)
	}
	rdb := redis.NewClient(&redis.Options{Addr: api.Env("REDIS_ADDR", "redis:6379")})
	defer rdb.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	hub := &realtime.Hub{Identity: identity, Chat: chat, Redis: rdb, Flags: flags, Origin: origin}
	go hub.Run(ctx)
	api.Serve(api.Env("HTTP_ADDR", ":8083"), hub.Routes())
}
