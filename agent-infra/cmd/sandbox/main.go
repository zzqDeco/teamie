package main

import (
	"context"
	"github.com/cloudwego/kitex/pkg/remote/codec"
	"github.com/cloudwego/kitex/server"
	"log"
	"net"
	"teamie/agent-infra/internal/api"
	"teamie/agent-infra/internal/sandbox"
	"teamie/agent-protocol/kitex_gen/agent/sandboxservice"
	"teamie/backend/pkg/imlimits"
	"time"
)

func main() {
	token := api.Env("SANDBOX_RUNTIME_TOKEN", "")
	if len(token) < 24 {
		log.Fatal("SANDBOX_RUNTIME_TOKEN must contain at least 24 characters")
	}
	engine := sandbox.NewDocker(api.Env("DOCKER_SOCKET", "/var/run/docker.sock"))
	service, e := sandbox.New(engine, token, api.Env("SANDBOX_RUNNER_IMAGE", ""), api.Env("SANDBOX_WORKSPACE_ROOT", "/srv/teamie/sandbox-workspaces"), api.Env("SANDBOX_STATE_ROOT", "/srv/teamie/sandbox-state"))
	if e != nil {
		log.Fatal(e)
	}
	defer service.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	e = service.Recover(ctx)
	cancel()
	if e != nil {
		log.Fatal("sandbox durable task recovery failed")
	}
	addr, e := net.ResolveTCPAddr("tcp", api.Env("RPC_ADDR", ":8887"))
	if e != nil {
		log.Fatal("invalid RPC_ADDR")
	}
	rpc := sandboxservice.NewServer(service, server.WithServiceAddr(addr), server.WithCodec(codec.NewDefaultCodecWithSizeLimit(imlimits.AttachmentRequestBytes)))
	defer rpc.Stop()
	go func() {
		if rpc.Run() != nil {
			log.Fatal("sandbox RPC stopped")
		}
	}()
	api.Serve(api.Env("HTTP_ADDR", ":8087"), service.Routes())
}
