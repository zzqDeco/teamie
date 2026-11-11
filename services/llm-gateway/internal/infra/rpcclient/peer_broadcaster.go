package rpcclient

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"teamie/contracts/gen/agent/kitex_gen/agent"
	"teamie/contracts/gen/agent/kitex_gen/agent/modelgatewayservice"
	"teamie/pkg/rpcx"
	"teamie/services/llm-gateway/internal/domain"
)

var _ domain.PeerBroadcaster = (*PeerBroadcaster)(nil)

// PeerBroadcaster fans out run cancellations and configuration reconciliations
// across sibling Model Gateway rollout slots on the internal private network.
type PeerBroadcaster struct {
	HTTPTargets []string
	RPCTargets  []string
	Token       string
	InstanceID  string
	HTTPClient  *http.Client
	LookupIP    func(ctx context.Context, host string) ([]net.IPAddr, error)
}

type resolvedPeer struct {
	httpBase string
	rpcAddr  string
}

// NewInternalPeerHTTPClient constructs a bounded internal-network HTTP client
// without environment proxies or automatic redirect following.
func NewInternalPeerHTTPClient() *http.Client {
	dialer := &net.Dialer{Timeout: 500 * time.Millisecond, KeepAlive: 15 * time.Second}
	return &http.Client{
		Timeout: 1200 * time.Millisecond,
		Transport: &http.Transport{
			Proxy:               nil,
			DialContext:         dialer.DialContext,
			MaxIdleConns:        8,
			MaxIdleConnsPerHost: 2,
			IdleConnTimeout:     15 * time.Second,
		},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

func extractListenPort(listenAddr, fallback string) string {
	addr := strings.TrimSpace(listenAddr)
	if addr == "" {
		return fallback
	}
	if _, port, err := net.SplitHostPort(addr); err == nil && port != "" {
		return port
	}
	return fallback
}

func splitCSVTargets(raw string) []string {
	var out []string
	for _, part := range strings.Split(raw, ",") {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

// DefaultPeerTargets builds the canonical + canary peer target lists for HTTP and RPC.
func DefaultPeerTargets(httpListenAddr, rpcListenAddr, rawHTTPPeers, rawRPCPeers string) ([]string, []string) {
	httpPort := extractListenPort(httpListenAddr, "8086")
	rpcPort := extractListenPort(rpcListenAddr, "8886")
	httpTargets := splitCSVTargets(rawHTTPPeers)
	if len(httpTargets) == 0 {
		httpTargets = []string{
			net.JoinHostPort("llm-gateway", httpPort),
			net.JoinHostPort("llm-gateway-canary", httpPort),
		}
	}
	rpcTargets := splitCSVTargets(rawRPCPeers)
	if len(rpcTargets) == 0 && strings.TrimSpace(rawHTTPPeers) == "" {
		rpcTargets = []string{
			net.JoinHostPort("llm-gateway", rpcPort),
			net.JoinHostPort("llm-gateway-canary", rpcPort),
		}
	}
	return httpTargets, rpcTargets
}

// NewPeerBroadcaster constructs a PeerBroadcaster for internal gateway slot synchronization.
func NewPeerBroadcaster(httpTargets, rpcTargets []string, token, instanceID string) *PeerBroadcaster {
	return &PeerBroadcaster{
		HTTPTargets: append([]string(nil), httpTargets...),
		RPCTargets:  append([]string(nil), rpcTargets...),
		Token:       token,
		InstanceID:  instanceID,
		HTTPClient:  NewInternalPeerHTTPClient(),
	}
}

func (b *PeerBroadcaster) httpClient() *http.Client {
	if b.HTTPClient != nil {
		return b.HTTPClient
	}
	return NewInternalPeerHTTPClient()
}

func (b *PeerBroadcaster) lookupIP(ctx context.Context, host string) ([]net.IPAddr, error) {
	if b.LookupIP != nil {
		return b.LookupIP(ctx, host)
	}
	return net.DefaultResolver.LookupIPAddr(ctx, host)
}

func parseHTTPPeerTarget(raw string) (scheme, host, port string, ok bool) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", "", "", false
	}
	if strings.HasPrefix(trimmed, "http://") || strings.HasPrefix(trimmed, "https://") {
		u, err := url.Parse(trimmed)
		if err != nil || u.Host == "" {
			return "", "", "", false
		}
		h, p, err := net.SplitHostPort(u.Host)
		if err != nil || h == "" || p == "" {
			return "", "", "", false
		}
		return u.Scheme, h, p, true
	}
	h, p, err := net.SplitHostPort(trimmed)
	if err != nil || h == "" || p == "" {
		return "", "", "", false
	}
	return "http", h, p, true
}

func (b *PeerBroadcaster) rpcAddrForIndexAndHost(idx int, resolvedHost string) string {
	if idx >= 0 && idx < len(b.RPCTargets) {
		raw := strings.TrimSpace(b.RPCTargets[idx])
		if rpcHost, rpcPort, err := net.SplitHostPort(raw); err == nil && rpcPort != "" {
			if net.ParseIP(rpcHost) != nil || strings.EqualFold(rpcHost, "localhost") {
				return net.JoinHostPort(rpcHost, rpcPort)
			}
			return net.JoinHostPort(resolvedHost, rpcPort)
		}
	}
	if len(b.RPCTargets) > 0 {
		if _, rpcPort, err := net.SplitHostPort(strings.TrimSpace(b.RPCTargets[0])); err == nil && rpcPort != "" {
			return net.JoinHostPort(resolvedHost, rpcPort)
		}
	}
	return ""
}

func (b *PeerBroadcaster) resolvePeers(ctx context.Context) []resolvedPeer {
	if b == nil || len(b.HTTPTargets) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(b.HTTPTargets)*2)
	out := make([]resolvedPeer, 0, len(b.HTTPTargets)*2)
	add := func(p resolvedPeer) {
		key := p.httpBase + "|" + p.rpcAddr
		if _, exists := seen[key]; exists {
			return
		}
		seen[key] = struct{}{}
		out = append(out, p)
	}

	for idx, raw := range b.HTTPTargets {
		scheme, host, port, ok := parseHTTPPeerTarget(raw)
		if !ok {
			continue
		}
		if net.ParseIP(host) != nil || strings.EqualFold(host, "localhost") {
			add(resolvedPeer{
				httpBase: scheme + "://" + net.JoinHostPort(host, port),
				rpcAddr:  b.rpcAddrForIndexAndHost(idx, host),
			})
			continue
		}
		lCtx, cancel := context.WithTimeout(ctx, 400*time.Millisecond)
		ips, err := b.lookupIP(lCtx, host)
		cancel()
		if err != nil || len(ips) == 0 {
			continue
		}
		for _, ip := range ips {
			ipStr := ip.IP.String()
			add(resolvedPeer{
				httpBase: scheme + "://" + net.JoinHostPort(ipStr, port),
				rpcAddr:  b.rpcAddrForIndexAndHost(idx, ipStr),
			})
		}
	}
	return out
}

func (b *PeerBroadcaster) postJSON(ctx context.Context, fullURL string, payload []byte) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, fullURL, bytes.NewReader(payload))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", "Bearer "+b.Token)
	req.Header.Set("Content-Type", "application/json")
	if b.InstanceID != "" {
		req.Header.Set(domain.PeerInstanceHeader, b.InstanceID)
	}
	resp, err := b.httpClient().Do(req)
	if err != nil {
		return 0, err
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	_ = resp.Body.Close()
	return resp.StatusCode, nil
}

func (b *PeerBroadcaster) fallbackCancelRPC(ctx context.Context, rpcAddr, agentID, runID string) {
	if rpcAddr == "" {
		return
	}
	cli, err := modelgatewayservice.NewClient(
		"teamie.model-gateway",
		rpcx.ClientOptions(rpcAddr, 900*time.Millisecond, 350*time.Millisecond)...,
	)
	if err != nil {
		return
	}
	if closer, ok := cli.(io.Closer); ok {
		defer closer.Close()
	}
	cCtx, cancel := context.WithTimeout(ctx, 900*time.Millisecond)
	defer cancel()
	_, _ = cli.Cancel(cCtx, &agent.CancelRequest{
		ServiceToken: b.Token,
		AgentId:      agentID,
		RunId:        runID,
	})
}

// BroadcastCancel propagates a run cancellation to all discovered sibling gateway slots.
// If a draining peer has closed its HTTP listener while finishing in-flight streams,
// BroadcastCancel falls back to that peer's still-open RPC endpoint.
func (b *PeerBroadcaster) BroadcastCancel(ctx context.Context, agentID, runID string) error {
	if b == nil {
		return nil
	}
	peers := b.resolvePeers(ctx)
	if len(peers) == 0 {
		return nil
	}
	payload, err := json.Marshal(struct {
		AgentID string `json:"agentId"`
		RunID   string `json:"runId"`
	}{
		AgentID: agentID,
		RunID:   runID,
	})
	if err != nil {
		return err
	}
	var wg sync.WaitGroup
	for _, peer := range peers {
		wg.Add(1)
		go func(p resolvedPeer) {
			defer wg.Done()
			if _, httpErr := b.postJSON(ctx, p.httpBase+"/internal/v1/peer/cancel", payload); httpErr != nil {
				b.fallbackCancelRPC(ctx, p.rpcAddr, agentID, runID)
			}
		}(peer)
	}
	wg.Wait()
	return nil
}

// BroadcastReconcile notifies all discovered sibling gateway slots to reload
// persisted catalog/web-search configuration and revoke unavailable active calls.
func (b *PeerBroadcaster) BroadcastReconcile(ctx context.Context, resetProviderID string) error {
	if b == nil {
		return nil
	}
	peers := b.resolvePeers(ctx)
	if len(peers) == 0 {
		return nil
	}
	payload, err := json.Marshal(struct {
		ProviderID string `json:"providerId"`
	}{
		ProviderID: resetProviderID,
	})
	if err != nil {
		return err
	}
	var wg sync.WaitGroup
	for _, peer := range peers {
		wg.Add(1)
		go func(p resolvedPeer) {
			defer wg.Done()
			_, _ = b.postJSON(ctx, p.httpBase+"/internal/v1/peer/reconcile", payload)
		}(peer)
	}
	wg.Wait()
	return nil
}
