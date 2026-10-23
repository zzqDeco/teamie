package main

import (
	"context"
	"net"

	"teamie/contracts/gen/agent/kitex_gen/agent/agentcontrolservice"
	"teamie/pkg/httpx"
	"teamie/pkg/observability"
	"teamie/pkg/postgres"
	"teamie/pkg/rpcx"
	httpadapter "teamie/services/agent-control/internal/adapter/http"
	rpcadapter "teamie/services/agent-control/internal/adapter/rpc"
	"teamie/services/agent-control/internal/app"
	"teamie/services/agent-control/internal/infra/flags"
	"teamie/services/agent-control/internal/infra/pg"
	"teamie/services/agent-control/internal/infra/rpcclient"
)

func main() {
	stopLogging := observability.Configure("teamie.agent-control", httpx.BuildVersion, httpx.BuildSHA)
	defer stopLogging()

	origin := httpx.Env("APP_ORIGIN", "")
	database := httpx.Env("DATABASE_URL", "")
	token := httpx.Env("IM_PRINCIPAL_CONTROL_TOKEN", "")
	runtimeToken := httpx.Env("AGENT_RUNTIME_TOKEN", "")
	if origin == "" || database == "" || token == "" || runtimeToken == "" {
		observability.Fatal("APP_ORIGIN, DATABASE_URL and scoped service credentials are required", nil)
	}

	imClient, err := rpcclient.NewIMClient(
		httpx.Env("IDENTITY_RPC_ADDR", "identity:8881"),
		httpx.Env("CHAT_RPC_ADDR", "chat:8882"),
		token,
	)
	if err != nil {
		observability.Fatal("Service initialization failed", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	pool, err := postgres.Open(ctx, database)
	if err != nil {
		observability.Fatal("Service initialization failed", err)
	}
	defer pool.Close()

	if err = pg.Migrate(ctx, pool); err != nil {
		observability.Fatal("Service initialization failed", err)
	}

	repo := pg.New(postgres.New(pool))
	appSvc := &app.Service{
		Tx:           repo,
		Agents:       repo,
		IM:           imClient,
		Flags:        flags.Flags{Path: httpx.Env("FEATURE_FLAGS_FILE", "")},
		Notifier:     rpcclient.ObservabilityNotifier{},
		RuntimeToken: runtimeToken,
	}

	addr, err := net.ResolveTCPAddr("tcp", httpx.Env("RPC_ADDR", ":8884"))
	if err != nil {
		observability.Fatal("Service initialization failed", err)
	}
	rpcSrv := agentcontrolservice.NewServer(&rpcadapter.Server{App: appSvc}, rpcx.ServerOptions(addr, 0)...)
	defer rpcSrv.Stop()
	go func() {
		if err := rpcSrv.Run(); err != nil {
			observability.Fatal("Service initialization failed", err)
		}
	}()

	go appSvc.Run(ctx)

	httpHandler := (&httpadapter.Adapter{
		App:     appSvc,
		Origin:  origin,
		Version: httpx.BuildVersion,
		SHA:     httpx.BuildSHA,
	}).Routes()
	httpadapter.Serve(httpx.Env("HTTP_ADDR", ":8084"), httpHandler)
}
