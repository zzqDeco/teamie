package main

import (
	"context"

	goredis "github.com/redis/go-redis/v9"
	"teamie/pkg/httpx"
	"teamie/pkg/observability"
	wsadapter "teamie/services/realtime/internal/adapter/ws"
	"teamie/services/realtime/internal/app"
	"teamie/services/realtime/internal/domain"
	redisinfra "teamie/services/realtime/internal/infra/redis"
	"teamie/services/realtime/internal/infra/rpcclient"
)

func main() {
	stopLogging := observability.Configure("teamie.realtime", httpx.BuildVersion, httpx.BuildSHA)
	defer stopLogging()

	cfgFlags := httpx.Flags{Path: httpx.Env("FEATURE_FLAGS_FILE", "")}
	if err := cfgFlags.Validate(); err != nil {
		observability.Fatal("Service initialization failed", err)
	}
	origin := httpx.Env("APP_ORIGIN", "")
	if origin == "" {
		observability.Fatal("APP_ORIGIN is required", nil)
	}

	identityClient, err := rpcclient.NewIdentity(httpx.Env("IDENTITY_RPC_ADDR", "identity:8881"))
	if err != nil {
		observability.Fatal("Service initialization failed", err)
	}
	chatClient, err := rpcclient.NewChat(httpx.Env("CHAT_RPC_ADDR", "chat:8882"))
	if err != nil {
		observability.Fatal("Service initialization failed", err)
	}
	rdb := goredis.NewClient(&goredis.Options{Addr: httpx.Env("REDIS_ADDR", "redis:6379")})
	defer rdb.Close()

	var policy domain.TypingTracker
	streamApp := &app.StreamApp{
		Identity: rpcclient.IdentityValidator{Identity: identityClient},
		Chat:     rpcclient.ChatAuthority{Chat: chatClient},
		Flags:    redisinfra.FlagsProvider{Flags: &cfgFlags},
		Bus:      redisinfra.RedisBus{Client: rdb},
		Policy:   &policy,
	}

	adapter := &wsadapter.Adapter{
		App:    streamApp,
		Auth:   identityClient,
		Origin: origin,
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go redisinfra.RunSubscriber(ctx, rdb, streamApp, adapter.ActiveClients, adapter.CloseAll)

	httpx.Serve(httpx.Env("HTTP_ADDR", ":8083"), adapter.Routes())
}
