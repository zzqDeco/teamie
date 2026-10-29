package eino

import (
	"context"
	"net/http"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	"go.uber.org/zap"
	"teamie/pkg/observability"
)

func newMCPHTTPClient(ctx context.Context, endpoint string, headers map[string]string, options ...transport.StreamableHTTPCOption) (*client.Client, error) {
	options = append([]transport.StreamableHTTPCOption{
		transport.WithHTTPHeaders(headers),
		transport.WithHTTPLogger(mcpTransportLogger{ctx: ctx}),
		transport.WithHTTPBasicClient(&http.Client{Transport: &mcpHTTPTransport{base: http.DefaultTransport}}),
	}, options...)
	return client.NewStreamableHttpClient(endpoint, options...)
}

// StreamableHTTP in the pinned SDK has no connection-lost callback. Inspect
// session/auth invalidation at the HTTP boundary instead. Ordinary disconnects
// remain the SDK's reconnect responsibility; only a dead session needs Initialize.
type mcpHTTPTransport struct {
	base       http.RoundTripper
	invalidate func()
}

func (t *mcpHTTPTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Method == http.MethodPost && req.Header.Get("Idempotency-Key") != "" {
		// net/http treats this header as permission to replay a POST after a
		// reused connection drops. MCP apps may ignore the key; retain the
		// header for cooperative servers without allowing implicit resends.
		req = req.Clone(req.Context())
		req.GetBody = nil
	}
	response, err := t.base.RoundTrip(req)
	if err == nil && t.invalidate != nil && req.Method != http.MethodDelete && req.Header.Get("Mcp-Session-Id") != "" {
		switch response.StatusCode {
		case http.StatusNotFound, http.StatusUnauthorized, http.StatusForbidden:
			t.invalidate()
		}
	}
	return response, err
}

// SDK diagnostics can contain endpoint URLs and unrestricted response bodies.
// Keep transport boundary events in the shared logger without those arguments.
type mcpTransportLogger struct{ ctx context.Context }

func (l mcpTransportLogger) Infof(string, ...any) {
	observability.L(l.ctx).Debug("MCP transport listening", zap.String("event.action", "mcp-transport-listening"))
}

func (l mcpTransportLogger) Errorf(string, ...any) {
	observability.L(l.ctx).Warn("MCP transport request failed", zap.String("event.action", "mcp-transport-request-failed"))
}
