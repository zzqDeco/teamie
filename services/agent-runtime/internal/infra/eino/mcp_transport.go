package eino

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	"go.uber.org/zap"
	"teamie/pkg/observability"
)

// mcpForceProductionIPPolicy allows tests to verify production SSRF rejection
// of loopback and private network addresses even while running under go test.
var mcpForceProductionIPPolicy atomic.Bool

func validateMCPTargetURL(endpoint string) error {
	trimmed := strings.TrimSpace(endpoint)
	if trimmed == "" || strings.ContainsAny(trimmed, "\r\n\x00") {
		return errors.New("invalid_mcp_url")
	}
	u, err := url.Parse(trimmed)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.Fragment != "" {
		return errors.New("invalid_mcp_url")
	}
	host := u.Hostname()
	if host == "" {
		return errors.New("invalid_mcp_url")
	}
	if ip, err := netip.ParseAddr(host); err == nil {
		if !isAllowedMCPTargetIP(ip.Unmap()) {
			return errors.New("forbidden_mcp_target_ip")
		}
	}
	return nil
}

var mcpCGNATPrefix = netip.MustParsePrefix("100.64.0.0/10")

func isAllowedMCPTargetIP(ip netip.Addr) bool {
	ip = ip.Unmap()
	if !ip.IsValid() || ip.IsUnspecified() || ip.IsMulticast() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
		return false
	}
	forceProd := mcpForceProductionIPPolicy.Load()
	if ip.IsLoopback() {
		if forceProd {
			return os.Getenv("TEAMIE_MCP_ALLOW_LOOPBACK") == "true"
		}
		return testing.Testing() || os.Getenv("TEAMIE_MCP_ALLOW_LOOPBACK") == "true"
	}
	if ip.IsPrivate() || mcpCGNATPrefix.Contains(ip) {
		return !forceProd && os.Getenv("TEAMIE_MCP_ALLOW_PRIVATE") == "true"
	}
	return true
}

func newSafeMCPTransport(invalidate func()) http.RoundTripper {
	dialer := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
	var tr *http.Transport
	if base, ok := http.DefaultTransport.(*http.Transport); ok {
		tr = base.Clone()
	} else {
		tr = &http.Transport{}
	}
	tr.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, err
		}
		addrs, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
		if err != nil || len(addrs) == 0 {
			return nil, errors.New("mcp_dns_lookup_failed")
		}
		for _, ip := range addrs {
			if !isAllowedMCPTargetIP(ip) {
				return nil, errors.New("forbidden_mcp_target_ip")
			}
		}
		return dialer.DialContext(ctx, network, net.JoinHostPort(addrs[0].Unmap().String(), port))
	}
	return &mcpHTTPTransport{base: tr, invalidate: invalidate}
}

func newMCPHTTPClient(ctx context.Context, endpoint string, headers map[string]string, options ...transport.StreamableHTTPCOption) (*client.Client, error) {
	return newMCPHTTPClientWithInvalidate(ctx, endpoint, headers, nil, options...)
}

func newMCPHTTPClientWithInvalidate(ctx context.Context, endpoint string, headers map[string]string, invalidate func(), options ...transport.StreamableHTTPCOption) (*client.Client, error) {
	if err := validateMCPTargetURL(endpoint); err != nil {
		return nil, err
	}
	httpClient := &http.Client{
		Transport:     newSafeMCPTransport(invalidate),
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	options = append([]transport.StreamableHTTPCOption{
		transport.WithHTTPHeaders(headers),
		transport.WithHTTPLogger(mcpTransportLogger{ctx: ctx}),
		transport.WithHTTPBasicClient(httpClient),
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
