package main

import (
	"context"

	goredis "github.com/redis/go-redis/v9"
	"teamie/pkg/config"
	"teamie/pkg/httpx"
	"teamie/pkg/observability"
	"teamie/pkg/rpcx"
	wsadapter "teamie/services/realtime/internal/adapter/ws"
	"teamie/services/realtime/internal/app"
	"teamie/services/realtime/internal/domain"
	"teamie/services/realtime/internal/infra"
)

func main() {
	stopLogging := observability.Configure("teamie.realtime", httpx.BuildVersion, httpx.BuildSHA)
	defer stopLogging()

	cfgFlags := config.Flags{Path: httpx.Env("FEATURE_FLAGS_FILE", "")}
	if err := cfgFlags.Validate(); err != nil {
		observability.Fatal("Service initialization failed", err)
	}
	origin := httpx.Env("APP_ORIGIN", "")
	if origin == "" {
		observability.Fatal("APP_ORIGIN is required", nil)
	}

	identityClient, err := rpcx.NewIdentity(httpx.Env("IDENTITY_RPC_ADDR", "identity:8881"))
	if err != nil {
		observability.Fatal("Service initialization failed", err)
	}
	chatClient, err := rpcx.NewChat(httpx.Env("CHAT_RPC_ADDR", "chat:8882"))
	if err != nil {
		observability.Fatal("Service initialization failed", err)
	}
	rdb := goredis.NewClient(&goredis.Options{Addr: httpx.Env("REDIS_ADDR", "redis:6379")})
	defer rdb.Close()

	var policy domain.TypingPolicy
	hubSvc := &app.HubService{
		Identity: infra.IdentityValidator{Identity: identityClient},
		Chat:     infra.ChatAuthority{Chat: chatClient},
		Flags:    infra.FlagsProvider{Flags: &cfgFlags},
		Bus:      infra.RedisBus{Client: rdb},
		Policy:   &policy,
	}

	adapter := &wsadapter.Adapter{
		App:    hubSvc,
		Auth:   identityClient,
		Origin: origin,
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go infra.RunSubscriber(ctx, rdb, hubSvc, adapter.ActiveClients, adapter.CloseAll)

	httpx.Serve(httpx.Env("HTTP_ADDR", ":8083"), adapter.Routes())
}
