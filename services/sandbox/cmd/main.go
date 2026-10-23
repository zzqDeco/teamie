package main

import (
	"context"
	"net"
	"time"

	"teamie/contracts/gen/agent/kitex_gen/agent/sandboxservice"
	"teamie/pkg/httpx"
	"teamie/pkg/imlimits"
	"teamie/pkg/observability"
	"teamie/pkg/rpcx"
	httpAdapter "teamie/services/sandbox/internal/adapter/http"
	rpcAdapter "teamie/services/sandbox/internal/adapter/rpc"
	"teamie/services/sandbox/internal/app"
	"teamie/services/sandbox/internal/infra/docker"
)

func main() {
	stopLogging := observability.Configure("teamie.sandbox", httpx.BuildVersion, httpx.BuildSHA)
	defer stopLogging()

	token := httpx.Env("SANDBOX_RUNTIME_TOKEN", "")
	if len(token) < 24 {
		observability.Fatal("SANDBOX_RUNTIME_TOKEN must contain at least 24 characters", nil)
	}

	engine := docker.NewDocker(httpx.Env("DOCKER_SOCKET", "/var/run/docker.sock"))
	appSvc, err := app.New(
		engine,
		token,
		httpx.Env("SANDBOX_RUNNER_IMAGE", ""),
		httpx.Env("SANDBOX_WORKSPACE_ROOT", "/srv/teamie/sandbox-workspaces"),
		httpx.Env("SANDBOX_STATE_ROOT", "/srv/teamie/sandbox-state"),
	)
	if err != nil {
		observability.Fatal("Service initialization failed", err)
	}
	defer appSvc.Close()

	rpcServer := rpcAdapter.New(appSvc)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	err = appSvc.Recover(ctx)
	cancel()
	if err != nil {
		observability.Fatal("sandbox durable task recovery failed", err)
	}

	addr, err := net.ResolveTCPAddr("tcp", httpx.Env("RPC_ADDR", ":8887"))
	if err != nil {
		observability.Fatal("invalid RPC_ADDR", err)
	}
	rpc := sandboxservice.NewServer(rpcServer, rpcx.ServerOptions(addr, imlimits.AttachmentRequestBytes)...)
	defer rpc.Stop()
	go func() {
		if err := rpc.Run(); err != nil {
			observability.Fatal("sandbox RPC stopped", err)
		}
	}()

	httpHandler := httpAdapter.New(appSvc)
	httpx.ServeWithOptions(httpx.Env("HTTP_ADDR", ":8087"), httpHandler.Routes())
}
