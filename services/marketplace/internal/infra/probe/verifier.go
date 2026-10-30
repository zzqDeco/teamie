// Package probe implements the outbound 4-stage live protocol verifier for
// developer-deployed Web UI and MCP Streamable HTTP (2025-03-26) endpoints.
package probe

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"go.uber.org/zap"
	"teamie/pkg/observability"
	"teamie/services/marketplace/internal/domain"
)

const (
	probeProtocolVersion = "2025-03-26"
	probeUserID          = "00000000-0000-4000-8000-000000000001"
	probeWorkspaceID     = "00000000-0000-4000-8000-000000000002"
	probeConversationID  = "00000000-0000-4000-8000-000000000003"
	maxWebHTMLReadBytes  = 8 * 1024
	maxWebBodyBytes      = 256 * 1024
	maxMCPBodyBytes      = 512 * 1024
	maxSchemaBytes       = 4 * 1024
	maxRecordedTools     = 64
)

// Verifier implements domain.MarketplaceEndpointVerifier with DNS-pinned SSRF protection.
type Verifier struct {
	AppOrigin          string
	RequirePublicHTTPS bool
	Timeout            time.Duration
	Resolver           func(ctx context.Context, host string) ([]netip.Addr, error)
}

func (v *Verifier) timeout() time.Duration {
	if v != nil && v.Timeout > 0 {
		return v.Timeout
	}
	return 12 * time.Second
}

func (v *Verifier) resolveHost(ctx context.Context, host string) ([]netip.Addr, error) {
	if v != nil && v.Resolver != nil {
		return v.Resolver(ctx, host)
	}
	if ip, err := netip.ParseAddr(host); err == nil {
		return []netip.Addr{ip.Unmap()}, nil
	}
	addrs, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	if err != nil {
		return nil, err
	}
	out := make([]netip.Addr, 0, len(addrs))
	for _, a := range addrs {
		out = append(out, a.Unmap())
	}
	return out, nil
}

func (v *Verifier) buildTransport() *http.Transport {
	dialer := &net.Dialer{
		Timeout:   5 * time.Second,
		KeepAlive: -1,
	}
	return &http.Transport{
		DisableKeepAlives:     true,
		ForceAttemptHTTP2:     true,
		TLSHandshakeTimeout:   5 * time.Second,
		ResponseHeaderTimeout: 8 * time.Second,
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			if !v.RequirePublicHTTPS {
				return dialer.DialContext(ctx, network, addr)
			}
			host, port, err := net.SplitHostPort(addr)
			if err != nil {
				return nil, err
			}
			ips, err := v.resolveHost(ctx, host)
			if err != nil || len(ips) == 0 {
				return nil, fmt.Errorf("failed to resolve endpoint host")
			}
			for _, ip := range ips {
				if !domain.IsPublicTargetIP(ip) {
					return nil, fmt.Errorf("endpoint resolved to non-public IP address")
				}
			}
			return dialer.DialContext(ctx, network, net.JoinHostPort(ips[0].String(), port))
		},
	}
}

// VerifyEndpoints runs the 4-stage live protocol inspection (url_security, web_ui, mcp_auth, mcp_handshake).
func (v *Verifier) VerifyEndpoints(ctx context.Context, input domain.VerifyEndpointsInput) (domain.MarketplaceEndpointVerification, error) {
	started := time.Now().UTC()
	probeCtx, cancel := context.WithTimeout(ctx, v.timeout())
	defer cancel()

	report := domain.MarketplaceEndpointVerification{
		OK:         false,
		VerifiedAt: started,
		WebURL:     strings.TrimSpace(input.WebURL),
		MCPURL:     strings.TrimSpace(input.MCPURL),
		Checks:     []domain.EndpointProbeCheck{},
		Tools:      []domain.DiscoveredMCPTool{},
	}

	defer func() {
		outcome := "failure"
		if report.OK {
			outcome = "success"
		}
		observability.L(ctx).Info(
			"Marketplace endpoint verification completed",
			zap.String("event.action", "marketplace-endpoint-verify"),
			zap.String("event.outcome", outcome),
			zap.Int64("event.duration", time.Since(started).Nanoseconds()),
			zap.String("custom.marketplace.app_id", input.AppID),
			zap.Bool("custom.marketplace.verified", report.OK),
			zap.Int("custom.marketplace.tool_count", len(report.Tools)),
		)
	}()

	// Stage 1: url_security
	if err := domain.ValidateMarketplaceEndpointURL(report.WebURL, false, v.AppOrigin, v.RequirePublicHTTPS); err != nil {
		code := "invalid_web_url"
		var derr *domain.Error
		if errors.As(err, &derr) {
			code = derr.Code
		}
		report.Checks = append(report.Checks, domain.EndpointProbeCheck{
			Stage:   "url_security",
			OK:      false,
			Code:    code,
			Summary: err.Error(),
		})
		return report, nil
	}
	if err := domain.ValidateMarketplaceEndpointURL(report.MCPURL, true, v.AppOrigin, v.RequirePublicHTTPS); err != nil {
		code := "invalid_mcp_url"
		var derr *domain.Error
		if errors.As(err, &derr) {
			code = derr.Code
		}
		report.Checks = append(report.Checks, domain.EndpointProbeCheck{
			Stage:   "url_security",
			OK:      false,
			Code:    code,
			Summary: err.Error(),
		})
		return report, nil
	}
	report.Checks = append(report.Checks, domain.EndpointProbeCheck{
		Stage:   "url_security",
		OK:      true,
		Code:    "ok",
		Summary: "Web UI and MCP URLs satisfy cross-origin and network target rules",
	})

	tr := v.buildTransport()
	defer tr.CloseIdleConnections()

	// Stage 2: web_ui
	webCheck := v.probeWebUI(probeCtx, tr, report.WebURL)
	report.Checks = append(report.Checks, webCheck)
	if !webCheck.OK {
		return report, nil
	}

	// Stage 3: mcp_auth (must reject unauthenticated AND forged-signature tokens with 401 or 403)
	mcpClient := &http.Client{
		Transport: tr,
		Timeout:   8 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	authCheck := v.probeMCPAuth(probeCtx, mcpClient, input, started)
	report.Checks = append(report.Checks, authCheck)
	if !authCheck.OK {
		return report, nil
	}

	// Stage 4: mcp_handshake (valid HS256 token -> initialize -> notifications/initialized -> tools/list -> DELETE)
	handshakeCheck, tools, srvName, srvVer, protoVer := v.probeMCPHandshake(probeCtx, mcpClient, input, started)
	report.Checks = append(report.Checks, handshakeCheck)
	report.MCPServerName = srvName
	report.MCPServerVersion = srvVer
	report.MCPProtocolVersion = protoVer
	if tools != nil {
		report.Tools = tools
	}
	report.OK = handshakeCheck.OK
	return report, nil
}

func (v *Verifier) probeWebUI(ctx context.Context, tr *http.Transport, webURL string) domain.EndpointProbeCheck {
	webClient := &http.Client{
		Transport: tr,
		Timeout:   8 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 3 {
				return errors.New("too many redirects")
			}
			return domain.ValidateMarketplaceEndpointURL(req.URL.String(), false, v.AppOrigin, v.RequirePublicHTTPS)
		},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, webURL, nil)
	if err != nil {
		return domain.EndpointProbeCheck{Stage: "web_ui", OK: false, Code: "web_ui_invalid_request", Summary: "Failed to construct Web UI GET request"}
	}
	req.Header.Set("Accept", "text/html,application/xhtml+xml")
	req.Header.Set("User-Agent", "Teamie-Marketplace-Verifier/1.0")

	resp, err := webClient.Do(req)
	if err != nil {
		return domain.EndpointProbeCheck{Stage: "web_ui", OK: false, Code: "web_ui_unreachable", Summary: "Web UI endpoint could not be reached over HTTP(S)"}
	}
	defer func() { _ = resp.Body.Close() }()
	snippet, _ := io.ReadAll(io.LimitReader(resp.Body, maxWebHTMLReadBytes))
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxWebBodyBytes))

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return domain.EndpointProbeCheck{
			Stage:   "web_ui",
			OK:      false,
			Code:    "web_ui_bad_status",
			Summary: fmt.Sprintf("Web UI returned HTTP %d (expected 200 OK HTML page)", resp.StatusCode),
		}
	}

	xfo := strings.ToUpper(strings.TrimSpace(resp.Header.Get("X-Frame-Options")))
	if xfo == "DENY" || xfo == "SAMEORIGIN" {
		return domain.EndpointProbeCheck{
			Stage:   "web_ui",
			OK:      false,
			Code:    "web_ui_frame_blocked",
			Summary: fmt.Sprintf("Web UI blocks cross-origin iframe embedding via X-Frame-Options: %s", xfo),
		}
	}

	pageURL := webURL
	if resp.Request != nil && resp.Request.URL != nil {
		pageURL = resp.Request.URL.String()
	}
	if blocked, reason := blocksFrameAncestors(resp.Header.Values("Content-Security-Policy"), v.AppOrigin, pageURL); blocked {
		return domain.EndpointProbeCheck{
			Stage:   "web_ui",
			OK:      false,
			Code:    "web_ui_csp_frame_blocked",
			Summary: reason,
		}
	}

	if resp.StatusCode == http.StatusNoContent || resp.StatusCode == http.StatusResetContent {
		return domain.EndpointProbeCheck{
			Stage:   "web_ui",
			OK:      false,
			Code:    "web_ui_not_html",
			Summary: fmt.Sprintf("Web UI returned HTTP %d without an HTML document (expected Content-Type: text/html)", resp.StatusCode),
		}
	}

	cd := strings.ToLower(strings.TrimSpace(resp.Header.Get("Content-Disposition")))
	if strings.HasPrefix(cd, "attachment") {
		return domain.EndpointProbeCheck{
			Stage:   "web_ui",
			OK:      false,
			Code:    "web_ui_not_html",
			Summary: "Web UI returned Content-Disposition: attachment download instead of an embeddable HTML page",
		}
	}

	ct := strings.ToLower(strings.TrimSpace(resp.Header.Get("Content-Type")))
	trimmedBody := strings.ToLower(strings.TrimSpace(string(snippet)))
	isHTMLContentType := strings.Contains(ct, "text/html") || strings.Contains(ct, "application/xhtml+xml")
	hasHTMLPrefix := strings.HasPrefix(trimmedBody, "<!doctype html") || strings.HasPrefix(trimmedBody, "<html")
	if len(trimmedBody) == 0 || (!isHTMLContentType && !(ct == "" && hasHTMLPrefix)) {
		return domain.EndpointProbeCheck{
			Stage:   "web_ui",
			OK:      false,
			Code:    "web_ui_not_html",
			Summary: "Web UI endpoint did not return an HTML page (expected Content-Type: text/html)",
		}
	}

	return domain.EndpointProbeCheck{
		Stage:   "web_ui",
		OK:      true,
		Code:    "ok",
		Summary: fmt.Sprintf("Web UI reachable (HTTP %d) and permits cross-origin iframe embedding", resp.StatusCode),
	}
}

func blocksFrameAncestors(policies []string, appOrigin string, pageURL string) (bool, string) {
	appScheme, appHost, appPort, appValid := parseCSPTargetOrigin(appOrigin)
	pageScheme := ""
	if pu, err := url.Parse(strings.TrimSpace(pageURL)); err == nil {
		pageScheme = strings.ToLower(pu.Scheme)
	}
	for _, policy := range policies {
		for _, directive := range strings.Split(policy, ";") {
			fields := strings.Fields(strings.TrimSpace(directive))
			if len(fields) == 0 || !strings.EqualFold(fields[0], "frame-ancestors") {
				continue
			}
			sources := fields[1:]
			if len(sources) == 0 {
				return true, "Content-Security-Policy frame-ancestors is empty and blocks embedding"
			}
			allowed := false
			for _, src := range sources {
				if cspFrameSourceAllows(src, appScheme, appHost, appPort, appValid, pageScheme) {
					allowed = true
					break
				}
			}
			if !allowed {
				for _, src := range sources {
					s := strings.ToLower(strings.TrimSpace(src))
					if s == "'none'" || s == "'self'" {
						return true, fmt.Sprintf("Content-Security-Policy frame-ancestors (%s) blocks Teamie iframe embedding", src)
					}
				}
				return true, "Content-Security-Policy frame-ancestors does not include Teamie APP_ORIGIN"
			}
		}
	}
	return false, ""
}

func defaultSchemePort(scheme string) string {
	switch scheme {
	case "https":
		return "443"
	case "http":
		return "80"
	default:
		return ""
	}
}

func parseCSPTargetOrigin(raw string) (scheme, host, port string, ok bool) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "", "", "", false
	}
	scheme = strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return "", "", "", false
	}
	host = strings.ToLower(u.Hostname())
	if host == "" {
		return "", "", "", false
	}
	port = u.Port()
	if port == "" {
		port = defaultSchemePort(scheme)
	}
	return scheme, host, port, true
}

func cspSchemeMatches(srcScheme, appScheme, pageScheme string) bool {
	if srcScheme == "" {
		if pageScheme == "https" {
			return appScheme == "https"
		}
		return appScheme == "http" || appScheme == "https"
	}
	if srcScheme == appScheme {
		return true
	}
	return srcScheme == "http" && appScheme == "https"
}

func cspFrameSourceAllows(rawSrc, appScheme, appHost, appPort string, appValid bool, pageScheme string) bool {
	s := strings.ToLower(strings.TrimSpace(rawSrc))
	if s == "" || strings.HasPrefix(s, "'") {
		return false
	}
	if s == "*" {
		return true
	}
	if !appValid {
		return false
	}
	if strings.HasSuffix(s, ":") && !strings.Contains(s, "/") {
		return cspSchemeMatches(strings.TrimSuffix(s, ":"), appScheme, pageScheme)
	}
	srcScheme := ""
	rest := s
	if schemePart, after, hasScheme := strings.Cut(s, "://"); hasScheme {
		if schemePart != "http" && schemePart != "https" {
			return false
		}
		srcScheme = schemePart
		rest = after
	}
	if !cspSchemeMatches(srcScheme, appScheme, pageScheme) {
		return false
	}
	hostPort := rest
	if slash := strings.IndexByte(rest, '/'); slash >= 0 {
		if rest[slash:] != "/" {
			return false
		}
		hostPort = rest[:slash]
	}
	if hostPort == "" {
		return false
	}
	srcHost, srcPort := splitCSPHostPort(hostPort)
	if srcHost == "" {
		return false
	}
	if srcHost != "*" {
		if strings.HasPrefix(srcHost, "*.") {
			base := strings.TrimPrefix(srcHost, "*.")
			if base == "" || !strings.HasSuffix(appHost, "."+base) || len(appHost) <= len(base)+1 {
				return false
			}
		} else if srcHost != appHost {
			return false
		}
	}
	if srcPort == "*" {
		return true
	}
	if srcPort == "" {
		return appPort == defaultSchemePort(appScheme)
	}
	return appPort == srcPort
}

func splitCSPHostPort(hostPort string) (string, string) {
	if strings.HasPrefix(hostPort, "[") {
		if strings.HasSuffix(hostPort, "]") {
			return strings.Trim(hostPort, "[]"), ""
		}
		h, p, err := net.SplitHostPort(hostPort)
		if err != nil {
			return "", ""
		}
		return h, p
	}
	if strings.Count(hostPort, ":") == 1 {
		h, p, ok := strings.Cut(hostPort, ":")
		if !ok || p == "" {
			return "", ""
		}
		return h, p
	}
	if strings.Contains(hostPort, ":") {
		return "", ""
	}
	return hostPort, ""
}

func initializePayload() []byte {
	return []byte(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"` + probeProtocolVersion + `","capabilities":{},"clientInfo":{"name":"teamie-marketplace-verifier","version":"1.0.0"}}}`)
}

func (v *Verifier) probeMCPAuth(ctx context.Context, client *http.Client, input domain.VerifyEndpointsInput, now time.Time) domain.EndpointProbeCheck {
	initBody := initializePayload()

	// 3a: Unauthenticated request MUST return 401 or 403.
	reqNoAuth, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimSpace(input.MCPURL), bytes.NewReader(initBody))
	if err != nil {
		return domain.EndpointProbeCheck{Stage: "mcp_auth", OK: false, Code: "mcp_request_invalid", Summary: "Failed to build MCP auth probe request"}
	}
	reqNoAuth.Header.Set("Content-Type", "application/json")
	reqNoAuth.Header.Set("Accept", "application/json, text/event-stream")
	reqNoAuth.Header.Set("User-Agent", "Teamie-Marketplace-Verifier/1.0")
	respNoAuth, err := client.Do(reqNoAuth)
	if err != nil {
		return domain.EndpointProbeCheck{Stage: "mcp_auth", OK: false, Code: "mcp_unreachable", Summary: "MCP Server endpoint could not be reached"}
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(respNoAuth.Body, 16*1024))
	_ = respNoAuth.Body.Close()
	if respNoAuth.StatusCode != http.StatusUnauthorized && respNoAuth.StatusCode != http.StatusForbidden {
		return domain.EndpointProbeCheck{
			Stage:   "mcp_auth",
			OK:      false,
			Code:    "mcp_unauthenticated_allowed",
			Summary: fmt.Sprintf("MCP Server returned HTTP %d without Authorization header (must return HTTP 401 or 403)", respNoAuth.StatusCode),
		}
	}

	// 3b: Forged HS256 signature request MUST return 401 or 403.
	forgedToken, err := domain.SignMarketplaceContextToken("forged_invalid_secret_"+input.SigningSecret, domain.MarketplaceContextClaims{
		Issuer:         "teamie",
		Audience:       input.AppID,
		Subject:        probeUserID,
		AppID:          input.AppID,
		Slug:           input.Slug,
		WorkspaceID:    probeWorkspaceID,
		ConversationID: probeConversationID,
		UserID:         probeUserID,
		DisplayName:    "Forged Probe",
		IssuedAt:       now.Unix(),
		ExpiresAt:      now.Add(5 * time.Minute).Unix(),
	})
	if err != nil {
		return domain.EndpointProbeCheck{Stage: "mcp_auth", OK: false, Code: "probe_token_error", Summary: "Failed to construct probe token"}
	}
	reqForged, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimSpace(input.MCPURL), bytes.NewReader(initBody))
	if err != nil {
		return domain.EndpointProbeCheck{Stage: "mcp_auth", OK: false, Code: "mcp_request_invalid", Summary: "Failed to build MCP forged-token probe"}
	}
	reqForged.Header.Set("Content-Type", "application/json")
	reqForged.Header.Set("Accept", "application/json, text/event-stream")
	reqForged.Header.Set("User-Agent", "Teamie-Marketplace-Verifier/1.0")
	reqForged.Header.Set("Authorization", "Bearer "+forgedToken)
	respForged, err := client.Do(reqForged)
	if err != nil {
		return domain.EndpointProbeCheck{Stage: "mcp_auth", OK: false, Code: "mcp_unreachable", Summary: "MCP Server unreachable during forged-token check"}
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(respForged.Body, 16*1024))
	_ = respForged.Body.Close()
	if respForged.StatusCode != http.StatusUnauthorized && respForged.StatusCode != http.StatusForbidden {
		return domain.EndpointProbeCheck{
			Stage:   "mcp_auth",
			OK:      false,
			Code:    "mcp_forged_token_allowed",
			Summary: fmt.Sprintf("MCP Server returned HTTP %d for a forged HS256 token (must verify Signing Secret and return HTTP 401 or 403)", respForged.StatusCode),
		}
	}

	return domain.EndpointProbeCheck{
		Stage:   "mcp_auth",
		OK:      true,
		Code:    "ok",
		Summary: "MCP Server properly rejects unauthenticated and forged-signature requests with HTTP 401/403",
	}
}

type jsonRPCEnvelope struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

func decodeMCPResponse(resp *http.Response) (jsonRPCEnvelope, error) {
	var env jsonRPCEnvelope
	ct := strings.ToLower(resp.Header.Get("Content-Type"))
	limited := io.LimitReader(resp.Body, maxMCPBodyBytes)
	if strings.Contains(ct, "text/event-stream") {
		scanner := bufio.NewScanner(limited)
		scanner.Buffer(make([]byte, 0, 64*1024), maxMCPBodyBytes)
		var dataLines []string
		tryFlush := func() (jsonRPCEnvelope, bool) {
			if len(dataLines) == 0 {
				return jsonRPCEnvelope{}, false
			}
			payload := strings.TrimSpace(strings.Join(dataLines, "\n"))
			dataLines = nil
			var cand jsonRPCEnvelope
			if json.Unmarshal([]byte(payload), &cand) == nil && (len(cand.Result) > 0 || cand.Error != nil) {
				return cand, true
			}
			return jsonRPCEnvelope{}, false
		}
		for scanner.Scan() {
			line := scanner.Text()
			if strings.TrimSpace(line) == "" {
				if got, ok := tryFlush(); ok {
					return got, nil
				}
				continue
			}
			if after, ok := strings.CutPrefix(line, "data:"); ok {
				dataLines = append(dataLines, strings.TrimSpace(after))
			}
		}
		if err := scanner.Err(); err != nil {
			return env, err
		}
		if got, ok := tryFlush(); ok {
			return got, nil
		}
		return env, fmt.Errorf("no JSON-RPC response event found in SSE stream")
	}

	raw, err := io.ReadAll(limited)
	if err != nil {
		return env, err
	}
	if err := json.Unmarshal(bytes.TrimSpace(raw), &env); err != nil {
		return env, fmt.Errorf("invalid JSON-RPC response body")
	}
	return env, nil
}

func (v *Verifier) probeMCPHandshake(
	ctx context.Context,
	client *http.Client,
	input domain.VerifyEndpointsInput,
	now time.Time,
) (domain.EndpointProbeCheck, []domain.DiscoveredMCPTool, string, string, string) {
	validToken, err := domain.SignMarketplaceContextToken(input.SigningSecret, domain.MarketplaceContextClaims{
		Issuer:         "teamie",
		Audience:       input.AppID,
		Subject:        probeUserID,
		AppID:          input.AppID,
		Slug:           input.Slug,
		WorkspaceID:    probeWorkspaceID,
		ConversationID: probeConversationID,
		UserID:         probeUserID,
		DisplayName:    "Teamie Protocol Verifier",
		IssuedAt:       now.Unix(),
		ExpiresAt:      now.Add(5 * time.Minute).Unix(),
	})
	if err != nil {
		return domain.EndpointProbeCheck{Stage: "mcp_handshake", OK: false, Code: "token_sign_error", Summary: "Failed to sign valid probe token"}, nil, "", "", ""
	}

	mcpURL := strings.TrimSpace(input.MCPURL)
	doRPC := func(method string, body []byte, sessionID string) (*http.Response, error) {
		req, reqErr := http.NewRequestWithContext(ctx, method, mcpURL, bytes.NewReader(body))
		if reqErr != nil {
			return nil, reqErr
		}
		if len(body) > 0 {
			req.Header.Set("Content-Type", "application/json")
		}
		req.Header.Set("Accept", "application/json, text/event-stream")
		req.Header.Set("User-Agent", "Teamie-Marketplace-Verifier/1.0")
		req.Header.Set("Authorization", "Bearer "+validToken)
		if sessionID != "" {
			req.Header.Set("Mcp-Session-Id", sessionID)
		}
		return client.Do(req)
	}

	// 1. initialize
	initResp, err := doRPC(http.MethodPost, initializePayload(), "")
	if err != nil {
		return domain.EndpointProbeCheck{Stage: "mcp_handshake", OK: false, Code: "mcp_init_failed", Summary: "MCP initialize request failed"}, nil, "", "", ""
	}
	sessionID := strings.TrimSpace(initResp.Header.Get("Mcp-Session-Id"))
	if sessionID != "" {
		defer func() {
			cleanCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			if delReq, reqErr := http.NewRequestWithContext(cleanCtx, http.MethodDelete, mcpURL, nil); reqErr == nil {
				delReq.Header.Set("Authorization", "Bearer "+validToken)
				delReq.Header.Set("Mcp-Session-Id", sessionID)
				if delResp, doErr := client.Do(delReq); doErr == nil {
					_ = delResp.Body.Close()
				}
			}
		}()
	}
	if initResp.StatusCode < 200 || initResp.StatusCode >= 300 {
		_ = initResp.Body.Close()
		return domain.EndpointProbeCheck{
			Stage:   "mcp_handshake",
			OK:      false,
			Code:    "mcp_init_rejected",
			Summary: fmt.Sprintf("MCP initialize with valid Signing Secret returned HTTP %d", initResp.StatusCode),
		}, nil, "", "", ""
	}
	initEnv, err := decodeMCPResponse(initResp)
	_ = initResp.Body.Close()
	if err != nil || initEnv.Error != nil {
		return domain.EndpointProbeCheck{
			Stage:   "mcp_handshake",
			OK:      false,
			Code:    "mcp_init_invalid_jsonrpc",
			Summary: "MCP initialize did not return a valid JSON-RPC 2.0 result",
		}, nil, "", "", ""
	}

	var initResult struct {
		ProtocolVersion string `json:"protocolVersion"`
		ServerInfo      struct {
			Name    string `json:"name"`
			Version string `json:"version"`
		} `json:"serverInfo"`
	}
	_ = json.Unmarshal(initEnv.Result, &initResult)
	srvName := truncateRunes(initResult.ServerInfo.Name, 80)
	srvVer := truncateRunes(initResult.ServerInfo.Version, 40)
	protoVer := truncateRunes(initResult.ProtocolVersion, 40)

	// 2. notifications/initialized
	notifResp, err := doRPC(http.MethodPost, []byte(`{"jsonrpc":"2.0","method":"notifications/initialized"}`), sessionID)
	if err == nil {
		_, _ = io.Copy(io.Discard, io.LimitReader(notifResp.Body, 8*1024))
		_ = notifResp.Body.Close()
	}

	// 3. tools/list
	toolsResp, err := doRPC(http.MethodPost, []byte(`{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}`), sessionID)
	if err != nil {
		return domain.EndpointProbeCheck{Stage: "mcp_handshake", OK: false, Code: "mcp_tools_list_failed", Summary: "MCP tools/list request failed"}, nil, srvName, srvVer, protoVer
	}
	if toolsResp.StatusCode < 200 || toolsResp.StatusCode >= 300 {
		_ = toolsResp.Body.Close()
		return domain.EndpointProbeCheck{
			Stage:   "mcp_handshake",
			OK:      false,
			Code:    "mcp_tools_list_failed",
			Summary: fmt.Sprintf("MCP tools/list returned HTTP %d", toolsResp.StatusCode),
		}, nil, srvName, srvVer, protoVer
	}
	toolsEnv, err := decodeMCPResponse(toolsResp)
	_ = toolsResp.Body.Close()
	if err != nil || toolsEnv.Error != nil {
		return domain.EndpointProbeCheck{
			Stage:   "mcp_handshake",
			OK:      false,
			Code:    "mcp_tools_invalid_jsonrpc",
			Summary: "MCP tools/list did not return a valid JSON-RPC 2.0 result",
		}, nil, srvName, srvVer, protoVer
	}

	var toolsResult struct {
		Tools []struct {
			Name        string          `json:"name"`
			Description string          `json:"description"`
			InputSchema json.RawMessage `json:"inputSchema"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(toolsEnv.Result, &toolsResult); err != nil {
		return domain.EndpointProbeCheck{
			Stage:   "mcp_handshake",
			OK:      false,
			Code:    "mcp_tools_invalid_jsonrpc",
			Summary: "MCP tools/list result could not be decoded",
		}, nil, srvName, srvVer, protoVer
	}

	seenExposed := make(map[string]string, len(toolsResult.Tools))
	discovered := make([]domain.DiscoveredMCPTool, 0, min(len(toolsResult.Tools), maxRecordedTools))
	for _, t := range toolsResult.Tools {
		rawName := strings.TrimSpace(t.Name)
		if rawName == "" {
			continue
		}
		exposed := domain.ExposedMCPToolName(input.Slug, rawName)
		if len(exposed) > 64 {
			return domain.EndpointProbeCheck{
				Stage:   "mcp_handshake",
				OK:      false,
				Code:    "mcp_tool_name_too_long",
				Summary: fmt.Sprintf("Exposed tool name %q exceeds the 64-character limit (%d chars)", exposed, len(exposed)),
			}, nil, srvName, srvVer, protoVer
		}
		if prev, dup := seenExposed[exposed]; dup {
			return domain.EndpointProbeCheck{
				Stage:   "mcp_handshake",
				OK:      false,
				Code:    "mcp_duplicate_tool_name",
				Summary: fmt.Sprintf("Tools %q and %q collide on normalized name %q", prev, rawName, exposed),
			}, nil, srvName, srvVer, protoVer
		}
		seenExposed[exposed] = rawName
		if len(discovered) >= maxRecordedTools {
			continue
		}

		schemaStr := ""
		if len(t.InputSchema) > 0 && len(t.InputSchema) <= maxSchemaBytes && json.Valid(t.InputSchema) && string(t.InputSchema) != "null" {
			var compact bytes.Buffer
			if json.Compact(&compact, t.InputSchema) == nil {
				schemaStr = compact.String()
			}
		}
		discovered = append(discovered, domain.DiscoveredMCPTool{
			Name:            truncateRunes(rawName, 80),
			ExposedName:     exposed,
			Description:     truncateRunes(strings.TrimSpace(t.Description), 500),
			InputSchemaJSON: schemaStr,
		})
	}
	if len(discovered) == 0 {
		return domain.EndpointProbeCheck{
			Stage:   "mcp_handshake",
			OK:      false,
			Code:    "mcp_no_tools",
			Summary: "MCP Server returned 0 tools; expose at least 1 tool for Agent participants",
		}, nil, srvName, srvVer, protoVer
	}

	return domain.EndpointProbeCheck{
		Stage:   "mcp_handshake",
		OK:      true,
		Code:    "ok",
		Summary: fmt.Sprintf("MCP handshake succeeded and discovered %d valid tool(s)", len(discovered)),
	}, discovered, srvName, srvVer, protoVer
}

func truncateRunes(s string, max int) string {
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	r := []rune(s)
	return string(r[:max])
}
