package main

import (
	"context"
	"strings"
	"sync"
	"time"

	goredis "github.com/redis/go-redis/v9"
	"teamie/pkg/httpx"
	"teamie/pkg/observability"
	"teamie/pkg/postgres"
	httpadapter "teamie/services/push/internal/adapter/http"
	"teamie/services/push/internal/app"
	"teamie/services/push/internal/domain"
	"teamie/services/push/internal/infra/flags"
	"teamie/services/push/internal/infra/pg"
	redisinfra "teamie/services/push/internal/infra/redis"
	"teamie/services/push/internal/infra/rpcclient"
	"teamie/services/push/internal/infra/webpush"
)

type hintHandlerAdapter struct {
	svc *app.Service
}

func (a hintHandlerAdapter) HandleMessageHint(ctx context.Context, hint domain.PushMessageHint) error {
	_, err := a.svc.HandleMessageHint(ctx, hint)
	return err
}

func main() {
	stopLogging := observability.Configure("teamie.push", httpx.BuildVersion, httpx.BuildSHA)
	defer stopLogging()

	cfgFlags := httpx.Flags{Path: httpx.Env("FEATURE_FLAGS_FILE", "")}
	if err := cfgFlags.Validate(); err != nil {
		observability.Fatal("Service initialization failed", err)
	}
	dbURL := httpx.Env("DATABASE_URL", "")
	if dbURL == "" {
		observability.Fatal("DATABASE_URL is required", nil)
	}
	origin := httpx.Env("APP_ORIGIN", "")
	if origin == "" {
		observability.Fatal("APP_ORIGIN is required", nil)
	}

	vapidCfg, err := domain.ValidateVAPIDConfig(
		httpx.Env("PUSH_VAPID_PUBLIC_KEY", ""),
		httpx.Env("PUSH_VAPID_PRIVATE_KEY", ""),
		httpx.Env("PUSH_VAPID_SUBJECT", ""),
	)
	if err != nil {
		observability.Fatal("Invalid VAPID configuration", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	pool, err := postgres.Open(ctx, dbURL)
	if err != nil {
		observability.Fatal("Service initialization failed", err)
	}
	defer pool.Close()

	if err = pg.Migrate(ctx, pool); err != nil {
		observability.Fatal("Service initialization failed", err)
	}

	identityClient, err := rpcclient.NewIdentityClient(httpx.Env("IDENTITY_RPC_ADDR", "identity:8881"))
	if err != nil {
		observability.Fatal("Service initialization failed", err)
	}
	chatClient, err := rpcclient.NewChatClient(
		httpx.Env("CHAT_RPC_ADDR", "chat:8882"),
		httpx.Env("PUSH_SERVICE_TOKEN", ""),
	)
	if err != nil {
		observability.Fatal("Service initialization failed", err)
	}

	requirePublicHTTPS := strings.EqualFold(httpx.Env("COOKIE_SECURE", ""), "true") ||
		strings.HasPrefix(strings.ToLower(strings.TrimSpace(origin)), "https://")

	repo := pg.New(postgres.New(pool))
	appSvc := &app.Service{
		Tx:                 repo,
		Subscriptions:      repo,
		Chat:               chatClient,
		Identity:           identityClient,
		Flags:              flags.New(cfgFlags),
		VAPID:              vapidCfg,
		RequirePublicHTTPS: &requirePublicHTTPS,
		Sender: &webpush.Sender{
			RequirePublicHTTPS: requirePublicHTTPS,
			Timeout:            8 * time.Second,
		},
	}

	rdb := goredis.NewClient(&goredis.Options{Addr: httpx.Env("REDIS_ADDR", "redis:6379")})
	defer rdb.Close()

	var bgWG sync.WaitGroup
	defer func() {
		cancel()
		bgWG.Wait()
	}()
	bgWG.Add(1)
	go func() {
		defer bgWG.Done()
		redisinfra.RunSubscriber(ctx, rdb, hintHandlerAdapter{svc: appSvc})
	}()

	httpHandler := (&httpadapter.Adapter{
		App:    appSvc,
		Auth:   identityClient,
		Origin: origin,
	}).Routes()
	httpx.ServeLifecycle(
		httpx.Env("HTTP_ADDR", ":8089"),
		httpHandler,
		httpx.OnDrainStart(cancel),
		httpx.OnShutdown(func(context.Context) {
			cancel()
		}),
	)
}
