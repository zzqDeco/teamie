package main

import (
	"context"
	"strings"
	"time"

	"teamie/pkg/httpx"
	"teamie/pkg/observability"
	"teamie/pkg/postgres"
	httpadapter "teamie/services/marketplace/internal/adapter/http"
	"teamie/services/marketplace/internal/app"
	"teamie/services/marketplace/internal/infra/flags"
	"teamie/services/marketplace/internal/infra/pg"
	"teamie/services/marketplace/internal/infra/probe"
	"teamie/services/marketplace/internal/infra/rpcclient"
)

func main() {
	stopLogging := observability.Configure("teamie.marketplace", httpx.BuildVersion, httpx.BuildSHA)
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

	identityClient, err := rpcclient.NewIdentityClient(httpx.Env("IDENTITY_RPC_ADDR", "identity:8881"))
	if err != nil {
		observability.Fatal("Service initialization failed", err)
	}
	chatClient, err := rpcclient.NewChatClient(httpx.Env("CHAT_RPC_ADDR", "chat:8882"))
	if err != nil {
		observability.Fatal("Service initialization failed", err)
	}

	requirePublicHTTPS := strings.EqualFold(httpx.Env("COOKIE_SECURE", ""), "true") ||
		strings.HasPrefix(strings.ToLower(strings.TrimSpace(origin)), "https://")

	repo := pg.New(postgres.New(pool))
	appSvc := &app.Service{
		Tx:                 repo,
		Marketplace:        repo,
		Identity:           rpcclient.NewIdentity(identityClient),
		Chat:               chatClient,
		Flags:              flags.New(cfgFlags),
		AppOrigin:          origin,
		RequirePublicHTTPS: &requirePublicHTTPS,
		VerifyCooldown:     2 * time.Second,
		Verifier: &probe.Verifier{
			AppOrigin:          origin,
			RequirePublicHTTPS: requirePublicHTTPS,
			Timeout:            12 * time.Second,
		},
	}

	httpHandler := (&httpadapter.Adapter{
		App:    appSvc,
		Auth:   identityClient,
		Origin: origin,
	}).Routes()
	httpx.Serve(httpx.Env("HTTP_ADDR", ":8088"), httpHandler)
}
