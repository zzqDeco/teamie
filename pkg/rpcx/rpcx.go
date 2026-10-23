// Package rpcx provides shared CloudWeGo Kitex client and server option builders
// with ECSZap observability and bounded timeouts.
package rpcx

import (
	"net"
	"time"

	"github.com/cloudwego/kitex/client"
	"github.com/cloudwego/kitex/pkg/remote/codec"
	"github.com/cloudwego/kitex/server"
	"teamie/pkg/observability"
)

// ClientOptions constructs the standard Kitex client options with ECS observability
// and bounded RPC/connection timeouts.
func ClientOptions(addr string, rpcTimeout, connectTimeout time.Duration) []client.Option {
	if rpcTimeout <= 0 {
		rpcTimeout = 3 * time.Second
	}
	if connectTimeout <= 0 {
		connectTimeout = time.Second
	}
	return []client.Option{
		observability.RPCClient(),
		client.WithHostPorts(addr),
		client.WithRPCTimeout(rpcTimeout),
		client.WithConnectTimeout(connectTimeout),
	}
}

// ServerOptions constructs the standard Kitex server options for the given TCP address
// with ECS observability and optional payload size limit.
func ServerOptions(addr net.Addr, maxPayloadBytes int, extra ...server.Option) []server.Option {
	opts := []server.Option{
		server.WithServiceAddr(addr),
		observability.RPCServer(),
	}
	if maxPayloadBytes > 0 {
		opts = append(opts, server.WithCodec(codec.NewDefaultCodecWithSizeLimit(maxPayloadBytes)))
	}
	return append(opts, extra...)
}
