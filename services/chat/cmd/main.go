package main

import (
	"context"
	"net"
	"strings"
	"time"

	goredis "github.com/redis/go-redis/v9"
	"teamie/contracts/gen/im/kitex_gen/im/chatservice"
	"teamie/pkg/httpx"
	"teamie/pkg/observability"
	"teamie/pkg/postgres"
	"teamie/pkg/rpcx"
	httpadapter "teamie/services/chat/internal/adapter/http"
	rpcadapter "teamie/services/chat/internal/adapter/rpc"
	"teamie/services/chat/internal/app"
	"teamie/services/chat/internal/infra/flags"
	"teamie/services/chat/internal/infra/fs"
	"teamie/services/chat/internal/infra/pg"
	"teamie/services/chat/internal/infra/probe"
	redisinfra "teamie/services/chat/internal/infra/redis"
	"teamie/services/chat/internal/infra/rpcclient"
)

func main() {
	stopLogging := observability.Configure("teamie.chat", httpx.BuildVersion, httpx.BuildSHA)
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

	identityClient, err := rpcclient.NewClient(httpx.Env("IDENTITY_RPC_ADDR", "identity:8881"))
	if err != nil {
		observability.Fatal("Service initialization failed", err)
	}

	requirePublicHTTPS := strings.EqualFold(httpx.Env("COOKIE_SECURE", "false"), "true")
	repo := pg.New(postgres.New(pool))
	appSvc := &app.Service{
		Tx:                 repo,
		Mutations:          repo,
		Workspaces:         repo,
		Conversations:      repo,
		Memberships:        repo,
		Messages:           repo,
		Attachments:        repo,
		Marketplace:        repo,
		Outbox:             repo,
		Identity:           rpcclient.NewIdentity(identityClient),
		Flags:              flags.New(cfgFlags),
		Verifier:           probe.New(origin, requirePublicHTTPS),
		SniffMediaType:     fs.SniffMediaType,
		ControlToken:       httpx.Env("IM_PRINCIPAL_CONTROL_TOKEN", ""),
		AppOrigin:          origin,
		RequirePublicHTTPS: requirePublicHTTPS,
		VerifyCooldown:     2 * time.Second,
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
