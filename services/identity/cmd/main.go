package main

import (
	"context"
	"net"

	"teamie/contracts/gen/im/kitex_gen/im/identityservice"
	"teamie/pkg/httpx"
	"teamie/pkg/observability"
	"teamie/pkg/postgres"
	"teamie/pkg/rpcx"
	httpadapter "teamie/services/identity/internal/adapter/http"
	rpcadapter "teamie/services/identity/internal/adapter/rpc"
	"teamie/services/identity/internal/app"
	"teamie/services/identity/internal/domain"
	"teamie/services/identity/internal/infra/crypto"
	"teamie/services/identity/internal/infra/flags"
	"teamie/services/identity/internal/infra/pg"
)

func main() {
	stopLogging := observability.Configure("teamie.identity", httpx.BuildVersion, httpx.BuildSHA)
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

	ctx := context.Background()
	pool, err := postgres.Open(ctx, dbURL)
	if err != nil {
		observability.Fatal("Service initialization failed", err)
	}
	defer pool.Close()

	if err = pg.Migrate(ctx, pool); err != nil {
		observability.Fatal("Service initialization failed", err)
	}

	repo := pg.New(postgres.New(pool))
	appSvc := &app.Service{
		Tx:                  repo,
		Users:               repo,
		Sessions:            repo,
		Invites:             repo,
		PasswordResets:      repo,
		Principals:          repo,
		Passwords:           crypto.BcryptHasher{},
		Tokens:              crypto.TokenGen{},
		Flags:               flags.New(cfgFlags),
		BootstrapInviteCode: httpx.Env("BOOTSTRAP_INVITE_CODE", domain.DefaultBootstrapInviteCode),
		ControlToken:        httpx.Env("IM_PRINCIPAL_CONTROL_TOKEN", ""),
		RuntimeToken:        httpx.Env("IM_PRINCIPAL_RUNTIME_TOKEN", ""),
	}

	addr, err := net.ResolveTCPAddr("tcp", httpx.Env("RPC_ADDR", ":8881"))
	if err != nil {
		observability.Fatal("Service initialization failed", err)
	}
	rpcSrv := identityservice.NewServer(&rpcadapter.Server{App: appSvc}, rpcx.ServerOptions(addr, 0)...)
	defer rpcSrv.Stop()
	go func() {
		if err := rpcSrv.Run(); err != nil {
			observability.Fatal("Service initialization failed", err)
		}
	}()

	httpHandler := (&httpadapter.Adapter{
		App:    appSvc,
		Origin: origin,
		Secure: httpx.Env("COOKIE_SECURE", "true") == "true",
	}).Routes()
	httpx.Serve(httpx.Env("HTTP_ADDR", ":8081"), httpHandler)
}
