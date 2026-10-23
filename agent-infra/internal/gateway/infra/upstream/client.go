// Package upstream implements SSRF-safe outbound HTTP transport for LLM providers and Tavily web search.
package upstream

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"net/http"
	"time"

	"teamie/agent-infra/internal/gateway/domain"
)

// NewHTTPClient creates an SSRF-safe *http.Client that resolves and pins only
// globally routable public IP addresses and disables environment proxies and redirects.
func NewHTTPClient() *http.Client {
	dialer := &net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}
	tr := &http.Transport{
		Proxy:                  nil,
		TLSClientConfig:        &tls.Config{MinVersion: tls.VersionTLS12},
		MaxIdleConns:           2,
		MaxIdleConnsPerHost:    1,
		IdleConnTimeout:        30 * time.Second,
		TLSHandshakeTimeout:    5 * time.Second,
		MaxResponseHeaderBytes: 32768,
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(address)
			if err != nil {
				return nil, errors.New("invalid provider address")
			}
			resolveCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			ips, err := net.DefaultResolver.LookupIPAddr(resolveCtx, host)
			cancel()
			if err != nil || len(ips) == 0 {
				return nil, errors.New("provider resolution failed")
			}
			for _, ip := range ips {
				if !domain.PublicIP(ip.IP) {
					return nil, errors.New("provider address denied")
				}
			}
			for _, ip := range ips {
				c, err := dialer.DialContext(ctx, network, net.JoinHostPort(ip.IP.String(), port))
				if err == nil {
					return c, nil
				}
			}
			return nil, errors.New("provider connection failed")
		},
	}
	return &http.Client{
		Transport:     tr,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}
