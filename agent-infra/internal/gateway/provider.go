package gateway

import (
	"context"
	"net"
	"net/http"
	"net/url"

	"teamie/agent-infra/internal/gateway/domain"
	"teamie/agent-infra/internal/gateway/infra/upstream"
	"teamie/agent-protocol/modeltransport"
)

const MaxRequestBytes = modeltransport.MaxRequestBytes
const MaxResponseBytes = modeltransport.MaxResponseBytes

func PublicIP(ip net.IP) bool {
	return domain.PublicIP(ip)
}

func endpoint(raw string) (*url.URL, error) {
	u, err := domain.ParseEndpoint(raw)
	return u, toAPIError(err)
}

func ValidateEndpoint(ctx context.Context, raw string) error {
	return toAPIError(domain.ValidateEndpoint(ctx, raw))
}

func NewHTTPClient() *http.Client {
	return upstream.NewHTTPClient()
}

func normalizeRequest(raw, model string) ([]byte, error) {
	return normalizeMode(raw, model, false)
}

func normalizeMode(raw, model string, streaming bool) ([]byte, error) {
	return normalizeModeWithOutputLimit(raw, model, streaming, 4096)
}

func inputMessageLimit(limits ...int) int {
	return domain.InputMessageLimit(limits...)
}

func normalizeModeWithOutputLimit(raw, model string, streaming bool, outputLimit int, messageLimits ...int) ([]byte, error) {
	b, err := domain.NormalizeModeWithOutputLimit(raw, model, streaming, outputLimit, messageLimits...)
	return b, toAPIError(err)
}

type Provider struct {
	HTTP *http.Client
}
