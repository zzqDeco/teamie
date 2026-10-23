package main

import (
	"context"
	"net"
	"net/http"

	goredis "github.com/redis/go-redis/v9"
	"teamie/contracts/gen/im/kitex_gen/im/chatservice"
	"teamie/pkg/config"
	"teamie/pkg/httpx"
	"teamie/pkg/observability"
	"teamie/pkg/postgres"
	"teamie/pkg/rpcx"
	httpadapter "teamie/services/chat/internal/adapter/http"
	rpcadapter "teamie/services/chat/internal/adapter/rpc"
	"teamie/services/chat/internal/app"
	"teamie/services/chat/internal/infra/flags"
	"teamie/services/chat/internal/infra/pg"
	redisinfra "teamie/services/chat/internal/infra/redis"
	"teamie/services/chat/internal/infra/rpcclient"
)

func main() {
	stopLogging := observability.Configure("teamie.chat", httpx.BuildVersion, httpx.BuildSHA)
	defer stopLogging()

	cfgFlags := config.Flags{Path: httpx.Env("FEATURE_FLAGS_FILE", "")}
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

	identityClient, err := rpcx.NewIdentity(httpx.Env("IDENTITY_RPC_ADDR", "identity:8881"))
	if err != nil {
		observability.Fatal("Service initialization failed", err)
	}

	repo := pg.New(postgres.New(pool))
	appSvc := &app.Service{
		Tx:             repo,
		Workspaces:     repo,
		Conversations:  repo,
		Memberships:    repo,
		Messages:       repo,
		Attachments:    repo,
		Outbox:         repo,
		Identity:       rpcclient.NewIdentity(identityClient),
		Flags:          flags.New(cfgFlags),
		SniffMediaType: http.DetectContentType,
		ControlToken:   httpx.Env("IM_PRINCIPAL_CONTROL_TOKEN", ""),
	}

	rdb := goredis.NewClient(&goredis.Options{Addr: httpx.Env("REDIS_ADDR", "redis:6379")})
	defer rdb.Close()
	go redisinfra.RunOutbox(ctx, appSvc, rdb)
	go redisinfra.RunAttachmentCleanup(ctx, appSvc)

	addr, err := net.ResolveTCPAddr("tcp", httpx.Env("RPC_ADDR", ":8882"))
	if err != nil {
		observability.Fatal("Service initialization failed", err)
	}
	rpcSrv := chatservice.NewServer(&rpcadapter.Server{App: appSvc}, rpcx.ServerOptions(addr, 0)...)
	defer rpcSrv.Stop()
	go func() {
		if err := rpcSrv.Run(); err != nil {
			observability.Fatal("Service initialization failed", err)
		}
	}()

	httpHandler := (&httpadapter.Adapter{
		App:    appSvc,
		Auth:   identityClient,
		Origin: origin,
	}).Routes()
	httpx.Serve(httpx.Env("HTTP_ADDR", ":8082"), httpHandler)
}
