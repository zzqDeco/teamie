package main

import (
	"context"
	"github.com/redis/go-redis/v9"
	"log"
	"teamie/backend/internal/api"
	"teamie/backend/internal/config"
	"teamie/backend/internal/realtime"
	"teamie/backend/internal/rpcutil"
)

func main() {
	flags := config.Flags{Path: api.Env("FEATURE_FLAGS_FILE", "")}
	if e := flags.Validate(); e != nil {
		log.Fatal(e)
	}
	origin := api.Env("APP_ORIGIN", "")
	if origin == "" {
		log.Fatal("APP_ORIGIN is required")
	}
	identity, e := rpcutil.NewIdentity(api.Env("IDENTITY_RPC_ADDR", "identity:8881"))
	if e != nil {
		log.Fatal(e)
	}
	chat, e := rpcutil.NewChat(api.Env("CHAT_RPC_ADDR", "chat:8882"))
	if e != nil {
		log.Fatal(e)
	}
	rdb := redis.NewClient(&redis.Options{Addr: api.Env("REDIS_ADDR", "redis:6379")})
	defer rdb.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	hub := &realtime.Hub{Identity: identity, Chat: chat, Redis: rdb, Flags: flags, Origin: origin}
	go hub.Run(ctx)
	api.Serve(api.Env("HTTP_ADDR", ":8083"), hub.Routes())
}
