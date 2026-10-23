package identity_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/cloudwego/kitex/client"
	"github.com/cloudwego/kitex/server"
	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"teamie/contracts/gen/im/kitex_gen/im"
	"teamie/contracts/gen/im/kitex_gen/im/identityservice"
	"teamie/pkg/observability"
)

type rpcLogBuffer struct {
	mu   sync.Mutex
	data bytes.Buffer
}

func (b *rpcLogBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.data.Write(p)
}

func (b *rpcLogBuffer) Bytes() []byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]byte{}, b.data.Bytes()...)
}

type pingHandler struct {
	im.IdentityService
	observed chan trace.SpanContext
}

func (p *pingHandler) Ping(ctx context.Context, _ *im.PingRequest) (*im.PingResponse, error) {
	p.observed <- trace.SpanContextFromContext(ctx)
	observability.L(ctx).Info("RPC handled")
	return &im.PingResponse{Ready: true}, nil
}

func TestRealKitexTTHeaderPropagatesCurrentOperation(t *testing.T) {
	var out rpcLogBuffer
	logger := observability.New(observability.Config{
		Service: "logging-test",
		Version: "test",
		SHA:     "checked-sha",
		Node:    "fixture-node",
		Output:  &out,
		Level:   zapcore.DebugLevel,
	})
	undo := zap.ReplaceGlobals(logger)
	previous := otel.GetTracerProvider()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSampler(sdktrace.NeverSample()))
	otel.SetTracerProvider(provider)
	t.Cleanup(func() {
		undo()
		otel.SetTracerProvider(previous)
		_ = provider.Shutdown(context.Background())
	})

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	handler := &pingHandler{observed: make(chan trace.SpanContext, 1)}
	srv := identityservice.NewServer(handler, server.WithListener(listener), observability.RPCServer())
	ended := make(chan error, 1)
	go func() { ended <- srv.Run() }()
	t.Cleanup(func() { _ = srv.Stop(); <-ended })

	c, err := identityservice.NewClient("logging-fixture", client.WithHostPorts(listener.Addr().String()), observability.RPCClient(), client.WithRPCTimeout(2*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	parent, outer := observability.Begin(context.Background(), "rpc-fixture")
	before := trace.SpanContextFromContext(parent)
	res, err := c.Ping(parent, &im.PingRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Ready {
		t.Fatal("fixture not ready")
	}
	actual := <-handler.observed
	if !actual.IsValid() || actual.TraceID() != before.TraceID() || actual.SpanID() == before.SpanID() {
		t.Fatal("TTHeader correlation failed", actual, before)
	}
	outer.End(nil)

	var clientSpan, serverSpan, serverParent string
	for _, line := range bytes.Split(bytes.TrimSpace(out.Bytes()), []byte("\n")) {
		if len(line) == 0 {
			continue
		}
		var e map[string]any
		if err := json.Unmarshal(line, &e); err != nil {
			t.Fatal(err)
		}
		if e["event.action"] == "rpc-client" {
			clientSpan, _ = e["span.id"].(string)
		}
		if e["event.action"] == "rpc-server" {
			serverSpan, _ = e["span.id"].(string)
			serverParent, _ = e["custom.trace.parent_span_id"].(string)
		}
	}
	if clientSpan == "" || serverSpan == "" || clientSpan == serverSpan || serverParent != clientSpan {
		t.Fatal("RPC operation spans absent or reused")
	}
}
