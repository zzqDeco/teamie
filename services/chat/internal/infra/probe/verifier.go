// Package probe verifies developer-deployed Web UI and MCP Streamable HTTP
// endpoints against the Teamie Marketplace Protocol using only the Go standard library.
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
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"go.uber.org/zap"
	"teamie/pkg/observability"
	"teamie/services/chat/internal/domain"
)

const (
	defaultProbeTimeout = 10 * time.Second
	maxWebHTMLReadBytes = 8 * 1024
	maxMCPReadBytes     = 256 * 1024
	maxSchemaBytes      = 4 * 1024
	maxRecordedTools    = 64
	probeActorUUID      = "00000000-0000-4000-8000-000000000001"
	mcpProtocolVersion  = "2025-03-26"
)

// Verifier implements domain.MarketplaceEndpointVerifier.
type Verifier struct {
	AppOrigin          string
	RequirePublicHTTPS bool
	Timeout            time.Duration
}

// New creates a protocol endpoint verifier bound to the deployment's APP_ORIGIN and security mode.
func New(appOrigin string, requirePublicHTTPS bool) *Verifier {
	return &Verifier{
		AppOrigin:          strings.TrimSpace(appOrigin),
		RequirePublicHTTPS: requirePublicHTTPS,
		Timeout:            defaultProbeTimeout,
	}
}

func (v *Verifier) VerifyEndpoints(ctx context.Context, in domain.VerifyEndpointsInput) (domain.MarketplaceEndpointVerification, error) {
	started := time.Now().UTC()
	timeout := v.Timeout
	if timeout <= 0 {
		timeout = defaultProbeTimeout
	}
	probeCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	webURL := strings.TrimSpace(in.WebURL)
	mcpURL := strings.TrimSpace(in.MCPURL)
	out := domain.MarketplaceEndpointVerification{
		VerifiedAt: started,
		WebURL:     webURL,
		MCPURL:     mcpURL,
		Checks:     []domain.EndpointProbeCheck{},
		Tools:      []domain.DiscoveredMCPTool{},
	}

	defer func() {
		observability.L(ctx).Info(
			"Marketplace endpoint verification completed",
			zap.String("app_id", in.AppID),
			zap.Bool("ok", out.OK),
			zap.Int("tool_count", len(out.Tools)),
			zap.Int64("duration_ns", time.Since(started).Nanoseconds()),
		)
	}()

	// Stage 1: URL syntax, cross-origin isolation & SSRF policy.
	if err := domain.ValidateMarketplaceEndpointURL(webURL, false, v.AppOrigin, v.RequirePublicHTTPS); err != nil {
		out.Checks = append(out.Checks, failedCheck("url_security", err))
		return out, nil
	}
	if err := domain.ValidateMarketplaceEndpointURL(mcpURL, true, v.AppOrigin, v.RequirePublicHTTPS); err != nil {
		out.Checks = append(out.Checks, failedCheck("url_security", err))
		return out, nil
	}
	out.Checks = append(out.Checks, domain.EndpointProbeCheck{
		Stage:   "url_security",
		OK:      true,
		Code:    "ok",
		Summary: "Web UI 与 MCP 端点协议格式及跨源隔离策略校验通过",
	})

	client := v.newSafeHTTPClient()

	// Stage 2: Web UI reachability & iframe embeddability.
	webCheck := v.probeWebUI(probeCtx, client, webURL)
	out.Checks = append(out.Checks, webCheck)
	if !webCheck.OK {
		return out, nil
	}

	// Stage 3: MCP Bearer JWT authentication gate (must reject unauthenticated & forged tokens).
	authCheck := v.probeMCPAuthGate(probeCtx, client, in, mcpURL, started)
	out.Checks = append(out.Checks, authCheck)
	if !authCheck.OK {
		return out, nil
	}

	// Stage 4: MCP Streamable HTTP initialize + tools/list discovery.
	handshakeCheck, serverName, serverVer, protoVer, tools := v.probeMCPHandshake(probeCtx, client, in, mcpURL, started)
	out.Checks = append(out.Checks, handshakeCheck)
	out.MCPServerName = serverName
	out.MCPServerVersion = serverVer
	out.MCPProtocolVersion = protoVer
	out.Tools = tools
	out.OK = handshakeCheck.OK
	return out, nil
}

func (v *Verifier) newSafeHTTPClient() *http.Client {
	dialer := &net.Dialer{Timeout: 5 * time.Second, KeepAlive: 15 * time.Second}
	tr := &http.Transport{
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          8,
		IdleConnTimeout:       15 * time.Second,
		TLSHandshakeTimeout:   5 * time.Second,
		ResponseHeaderTimeout: 6 * time.Second,
	}
	tr.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, err
		}
		addrs, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
		if err != nil || len(addrs) == 0 {
			return nil, errors.New("dns_lookup_failed")
		}
		if v.RequirePublicHTTPS {
			for _, ip := range addrs {
				if !domain.IsPublicTargetIP(ip) {
					return nil, errors.New("forbidden_private_target_ip")
				}
			}
		}
		return dialer.DialContext(ctx, network, net.JoinHostPort(addrs[0].Unmap().String(), port))
	}
	return &http.Client{
		Transport: tr,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 2 {
				return http.ErrUseLastResponse
			}
			return domain.ValidateMarketplaceEndpointURL(req.URL.String(), false, v.AppOrigin, v.RequirePublicHTTPS)
		},
	}
}

func (v *Verifier) probeWebUI(ctx context.Context, client *http.Client, webURL string) domain.EndpointProbeCheck {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, webURL, nil)
	if err != nil {
		return domain.EndpointProbeCheck{Stage: "web_ui", Code: "web_ui_invalid_request", Summary: "无法构造 Web UI 探测请求"}
	}
	req.Header.Set("Accept", "text/html,application/xhtml+xml")
	req.Header.Set("User-Agent", "Teamie-Marketplace-Verifier/1.0")
	resp, err := client.Do(req)
	if err != nil {
		return domain.EndpointProbeCheck{Stage: "web_ui", Code: "web_ui_unreachable", Summary: "无法连接到已部署的 Web UI 地址，请确认服务在线且公网可达"}
	}
	defer resp.Body.Close()
	snippet, _ := io.ReadAll(io.LimitReader(resp.Body, maxWebHTMLReadBytes))

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return domain.EndpointProbeCheck{
			Stage:   "web_ui",
			Code:    "web_ui_bad_status",
			Summary: fmt.Sprintf("Web UI 返回了非成功状态码 HTTP %d（期望 200 OK）", resp.StatusCode),
		}
	}
	xfo := strings.ToUpper(strings.TrimSpace(resp.Header.Get("X-Frame-Options")))
	if xfo == "DENY" || xfo == "SAMEORIGIN" {
		return domain.EndpointProbeCheck{
			Stage:   "web_ui",
			Code:    "web_ui_frame_blocked",
			Summary: fmt.Sprintf("Web UI 响应头包含 X-Frame-Options: %s，会阻止嵌入 Teamie 群聊侧边栏 iframe", xfo),
		}
	}
	if cspBlocksFrame(resp.Header.Values("Content-Security-Policy"), v.AppOrigin, webURL) {
		return domain.EndpointProbeCheck{
			Stage:   "web_ui",
			Code:    "web_ui_csp_frame_blocked",
			Summary: "Web UI 的 Content-Security-Policy frame-ancestors 阻止了 Teamie 嵌入，请允许主站域名或移除限制",
		}
	}
	ct := strings.ToLower(resp.Header.Get("Content-Type"))
	trimmedBody := strings.ToLower(strings.TrimSpace(string(snippet)))
	if !strings.Contains(ct, "text/html") && !strings.Contains(ct, "application/xhtml+xml") &&
		!strings.HasPrefix(trimmedBody, "<!doctype html") && !strings.HasPrefix(trimmedBody, "<html") {
		return domain.EndpointProbeCheck{
			Stage:   "web_ui",
			Code:    "web_ui_not_html",
			Summary: "Web UI 端点未返回 HTML 页面（期望 Content-Type: text/html）",
		}
	}
	return domain.EndpointProbeCheck{
		Stage:   "web_ui",
		OK:      true,
		Code:    "ok",
		Summary: fmt.Sprintf("Web UI 在线可达（HTTP %d）且允许跨源 iframe 嵌入", resp.StatusCode),
	}
}

func cspBlocksFrame(policies []string, appOrigin string, pageURL string) bool {
	appScheme, appHost, appPort, appValid := parseCSPTargetOrigin(appOrigin)
	pageScheme := ""
	if pu, err := url.Parse(strings.TrimSpace(pageURL)); err == nil {
		pageScheme = strings.ToLower(pu.Scheme)
	}
	for _, policy := range policies {
		for _, directive := range strings.Split(policy, ";") {
			fields := strings.Fields(strings.TrimSpace(directive))
			if len(fields) < 2 || !strings.EqualFold(fields[0], "frame-ancestors") {
				continue
			}
			allowed := false
			for _, src := range fields[1:] {
				if cspFrameSourceAllows(src, appScheme, appHost, appPort, appValid, pageScheme) {
					allowed = true
					break
				}
			}
			if !allowed {
				return true
			}
		}
	}
	return false
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

func (v *Verifier) probeMCPAuthGate(
	ctx context.Context,
	client *http.Client,
	in domain.VerifyEndpointsInput,
	mcpURL string,
	now time.Time,
) domain.EndpointProbeCheck {
	initPayload := map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "initialize",
		"params": map[string]any{
			"protocolVersion": mcpProtocolVersion,
			"capabilities":    map[string]any{},
			"clientInfo":      map[string]any{"name": "teamie-auth-check", "version": "1.0.0"},
		},
	}

	statusNoAuth, _, _, err := doMCPPost(ctx, client, mcpURL, "", "", initPayload, false)
	if err != nil {
		return domain.EndpointProbeCheck{Stage: "mcp_auth", Code: "mcp_unreachable", Summary: "无法连接到已部署的 MCP Server 端点"}
	}
	if statusNoAuth != http.StatusUnauthorized && statusNoAuth != http.StatusForbidden {
		return domain.EndpointProbeCheck{
			Stage:   "mcp_auth",
			Code:    "mcp_unauthenticated_allowed",
			Summary: fmt.Sprintf("MCP Server 未拒绝无鉴权请求（返回了 HTTP %d，期望 401 或 403），请使用 Signing Secret 强制校验 Bearer Token", statusNoAuth),
		}
	}

	forgedToken, _ := domain.SignMarketplaceContextToken("tmsec_forged_invalid_secret_00000000000000000000000000000000", probeClaims(in, now))
	statusForged, _, _, err := doMCPPost(ctx, client, mcpURL, forgedToken, "", initPayload, false)
	if err != nil {
		return domain.EndpointProbeCheck{Stage: "mcp_auth", Code: "mcp_unreachable", Summary: "校验伪造签名时无法连接 MCP Server"}
	}
	if statusForged != http.StatusUnauthorized && statusForged != http.StatusForbidden {
		return domain.EndpointProbeCheck{
			Stage:   "mcp_auth",
			Code:    "mcp_forged_token_allowed",
			Summary: fmt.Sprintf("MCP Server 接受了错误签名的伪造 JWT（返回了 HTTP %d，期望 401 或 403），请务必校验 HMAC-SHA256 签名而非仅解码 Payload", statusForged),
		}
	}

	return domain.EndpointProbeCheck{
		Stage:   "mcp_auth",
		OK:      true,
		Code:    "ok",
		Summary: "MCP Server 已正确拦截无 Token 与伪造签名请求（401/403 门禁生效）",
	}
}

func (v *Verifier) probeMCPHandshake(
	ctx context.Context,
	client *http.Client,
	in domain.VerifyEndpointsInput,
	mcpURL string,
	now time.Time,
) (domain.EndpointProbeCheck, string, string, string, []domain.DiscoveredMCPTool) {
	token, err := domain.SignMarketplaceContextToken(in.SigningSecret, probeClaims(in, now))
	if err != nil {
		return domain.EndpointProbeCheck{Stage: "mcp_handshake", Code: "token_sign_error", Summary: "生成探针 Context Token 失败"}, "", "", "", nil
	}

	initReq := map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "initialize",
		"params": map[string]any{
			"protocolVersion": mcpProtocolVersion,
			"capabilities":    map[string]any{},
			"clientInfo":      map[string]any{"name": "teamie-marketplace-verifier", "version": "1.0.0"},
		},
	}
	status, sessionID, initBody, err := doMCPPost(ctx, client, mcpURL, token, "", initReq, true)
	if err != nil {
		return domain.EndpointProbeCheck{Stage: "mcp_handshake", Code: "mcp_init_failed", Summary: "MCP initialize 握手请求失败或响应格式非法"}, "", "", "", nil
	}
	defer func() {
		if sessionID != "" {
			closeMCPSession(ctx, client, mcpURL, token, sessionID)
		}
	}()
	if status < 200 || status >= 300 {
		return domain.EndpointProbeCheck{
			Stage:   "mcp_handshake",
			Code:    "mcp_init_rejected",
			Summary: fmt.Sprintf("MCP initialize 拒绝了平台签发的合法 Context Token（HTTP %d），请检查 Signing Secret 与 aud=%s 配置", status, in.AppID),
		}, "", "", "", nil
	}

	var initResp struct {
		Error  *struct{ Message string } `json:"error"`
		Result struct {
			ProtocolVersion string `json:"protocolVersion"`
			ServerInfo      struct {
				Name    string `json:"name"`
				Version string `json:"version"`
			} `json:"serverInfo"`
		} `json:"result"`
	}
	if err := json.Unmarshal(initBody, &initResp); err != nil || initResp.Error != nil {
		return domain.EndpointProbeCheck{Stage: "mcp_handshake", Code: "mcp_init_invalid_jsonrpc", Summary: "MCP initialize 未返回合法的 JSON-RPC result"}, "", "", "", nil
	}

	// Send standard MCP notifications/initialized before tools/list for stateful SDK compatibility.
	_, _, _, _ = doMCPPost(ctx, client, mcpURL, token, sessionID, map[string]any{
		"jsonrpc": "2.0",
		"method":  "notifications/initialized",
	}, false)

	listReq := map[string]any{
		"jsonrpc": "2.0",
		"id":      2,
		"method":  "tools/list",
		"params":  map[string]any{},
	}
	listStatus, _, listBody, err := doMCPPost(ctx, client, mcpURL, token, sessionID, listReq, true)
	if err != nil || listStatus < 200 || listStatus >= 300 {
		return domain.EndpointProbeCheck{
			Stage:   "mcp_handshake",
			Code:    "mcp_tools_list_failed",
			Summary: fmt.Sprintf("MCP tools/list 工具发现失败（HTTP %d）", listStatus),
		}, initResp.Result.ServerInfo.Name, initResp.Result.ServerInfo.Version, initResp.Result.ProtocolVersion, nil
	}

	var listResp struct {
		Error  *struct{ Message string } `json:"error"`
		Result struct {
			Tools []struct {
				Name        string          `json:"name"`
				Description string          `json:"description"`
				InputSchema json.RawMessage `json:"inputSchema"`
			} `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal(listBody, &listResp); err != nil || listResp.Error != nil {
		return domain.EndpointProbeCheck{
			Stage:   "mcp_handshake",
			Code:    "mcp_tools_invalid_jsonrpc",
			Summary: "MCP tools/list 未返回合法的 JSON-RPC tools 列表",
		}, initResp.Result.ServerInfo.Name, initResp.Result.ServerInfo.Version, initResp.Result.ProtocolVersion, nil
	}

	discovered := make([]domain.DiscoveredMCPTool, 0, len(listResp.Result.Tools))
	seenExposed := map[string]struct{}{}
	for _, rawTool := range listResp.Result.Tools {
		name := strings.TrimSpace(rawTool.Name)
		if name == "" {
			continue
		}
		exposed := domain.ExposedMCPToolName(in.Slug, name)
		if len(exposed) > 64 {
			return domain.EndpointProbeCheck{
				Stage:   "mcp_handshake",
				Code:    "mcp_tool_name_too_long",
				Summary: fmt.Sprintf("MCP 工具名称规范化后超过 64 字符上限（%d 字符），请缩短工具名或应用 Slug", len(exposed)),
			}, initResp.Result.ServerInfo.Name, initResp.Result.ServerInfo.Version, initResp.Result.ProtocolVersion, nil
		}
		if _, dup := seenExposed[exposed]; dup {
			return domain.EndpointProbeCheck{
				Stage:   "mcp_handshake",
				Code:    "mcp_duplicate_tool_name",
				Summary: fmt.Sprintf("MCP 工具名称规范化后冲突（%s），请保持工具名唯一", exposed),
			}, initResp.Result.ServerInfo.Name, initResp.Result.ServerInfo.Version, initResp.Result.ProtocolVersion, nil
		}
		seenExposed[exposed] = struct{}{}
		if len(discovered) >= maxRecordedTools {
			break
		}
		schemaStr := ""
		if len(rawTool.InputSchema) > 0 && len(rawTool.InputSchema) <= maxSchemaBytes && json.Valid(rawTool.InputSchema) {
			var compact bytes.Buffer
			if json.Compact(&compact, rawTool.InputSchema) == nil {
				schemaStr = compact.String()
			}
		}
		discovered = append(discovered, domain.DiscoveredMCPTool{
			Name:            truncateRunes(name, 80),
			ExposedName:     exposed,
			Description:     truncateRunes(strings.TrimSpace(rawTool.Description), 500),
			InputSchemaJSON: schemaStr,
		})
	}
	if len(discovered) == 0 {
		return domain.EndpointProbeCheck{
			Stage:   "mcp_handshake",
			Code:    "mcp_no_tools",
			Summary: "MCP Server 握手成功但未暴露任何工具（tools/list 为空），请至少注册 1 个供 Agent 调用的工具",
		}, initResp.Result.ServerInfo.Name, initResp.Result.ServerInfo.Version, initResp.Result.ProtocolVersion, nil
	}

	return domain.EndpointProbeCheck{
		Stage:   "mcp_handshake",
		OK:      true,
		Code:    "ok",
		Summary: fmt.Sprintf("MCP Streamable HTTP 握手成功，发现 %d 个可用工具", len(discovered)),
	}, truncateRunes(initResp.Result.ServerInfo.Name, 80), truncateRunes(initResp.Result.ServerInfo.Version, 40), truncateRunes(initResp.Result.ProtocolVersion, 40), discovered
}

func probeClaims(in domain.VerifyEndpointsInput, now time.Time) domain.MarketplaceContextClaims {
	return domain.MarketplaceContextClaims{
		Issuer:         "teamie",
		Audience:       in.AppID,
		Subject:        probeActorUUID,
		AppID:          in.AppID,
		Slug:           in.Slug,
		WorkspaceID:    probeActorUUID,
		ConversationID: probeActorUUID,
		UserID:         probeActorUUID,
		DisplayName:    "Teamie Protocol Verifier",
		IssuedAt:       now.Unix(),
		ExpiresAt:      now.Add(time.Minute).Unix(),
	}
}

func doMCPPost(
	ctx context.Context,
	client *http.Client,
	mcpURL, bearerToken, sessionID string,
	payload any,
	parseRPCMessage bool,
) (int, string, []byte, error) {
	rawReq, err := json.Marshal(payload)
	if err != nil {
		return 0, "", nil, err
	}
	noRedirectClient := *client
	noRedirectClient.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, mcpURL, bytes.NewReader(rawReq))
	if err != nil {
		return 0, "", nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("User-Agent", "Teamie-Marketplace-Verifier/1.0")
	if bearerToken != "" {
		req.Header.Set("Authorization", "Bearer "+bearerToken)
	}
	if sessionID != "" {
		req.Header.Set("Mcp-Session-Id", sessionID)
	}
	resp, err := noRedirectClient.Do(req)
	if err != nil {
		return 0, "", nil, err
	}
	defer resp.Body.Close()

	nextSessionID := strings.TrimSpace(resp.Header.Get("Mcp-Session-Id"))
	if nextSessionID == "" {
		nextSessionID = sessionID
	}
	if !parseRPCMessage || resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return resp.StatusCode, nextSessionID, nil, nil
	}
	ct := strings.ToLower(resp.Header.Get("Content-Type"))
	if strings.Contains(ct, "text/event-stream") {
		msg, err := readFirstJSONRPCResponseFromSSEStream(io.LimitReader(resp.Body, maxMCPReadBytes))
		if err != nil {
			return resp.StatusCode, nextSessionID, nil, err
		}
		return resp.StatusCode, nextSessionID, msg, nil
	}
	bodyBytes, err := io.ReadAll(io.LimitReader(resp.Body, maxMCPReadBytes))
	if err != nil {
		return resp.StatusCode, nextSessionID, nil, err
	}
	return resp.StatusCode, nextSessionID, bodyBytes, nil
}

func readFirstJSONRPCResponseFromSSEStream(r io.Reader) ([]byte, error) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 16*1024), maxMCPReadBytes)
	var dataBuf bytes.Buffer
	flushEvent := func() ([]byte, bool) {
		trimmed := bytes.TrimSpace(dataBuf.Bytes())
		dataBuf.Reset()
		if len(trimmed) == 0 {
			return nil, false
		}
		var probe map[string]any
		if json.Unmarshal(trimmed, &probe) == nil {
			if _, hasResult := probe["result"]; hasResult {
				return append([]byte(nil), trimmed...), true
			}
			if _, hasErr := probe["error"]; hasErr {
				return append([]byte(nil), trimmed...), true
			}
		}
		return nil, false
	}
	for scanner.Scan() {
		line := scanner.Text()
		if strings.TrimSpace(line) == "" {
			if msg, ok := flushEvent(); ok {
				return msg, nil
			}
			continue
		}
		if after, ok := strings.CutPrefix(line, "data:"); ok {
			if dataBuf.Len() > 0 {
				dataBuf.WriteByte('\n')
			}
			dataBuf.WriteString(strings.TrimSpace(after))
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if msg, ok := flushEvent(); ok {
		return msg, nil
	}
	return nil, errors.New("empty_sse_jsonrpc")
}

func closeMCPSession(ctx context.Context, client *http.Client, mcpURL, bearerToken, sessionID string) {
	cleanCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(cleanCtx, http.MethodDelete, mcpURL, nil)
	if err != nil {
		return
	}
	req.Header.Set("Authorization", "Bearer "+bearerToken)
	req.Header.Set("Mcp-Session-Id", sessionID)
	resp, err := client.Do(req)
	if err == nil && resp != nil {
		_ = resp.Body.Close()
	}
}

func failedCheck(stage string, err error) domain.EndpointProbeCheck {
	var domErr *domain.Error
	if errors.As(err, &domErr) {
		return domain.EndpointProbeCheck{
			Stage:   stage,
			OK:      false,
			Code:    domErr.Code,
			Summary: domErr.Message,
		}
	}
	return domain.EndpointProbeCheck{
		Stage:   stage,
		OK:      false,
		Code:    "validation_failed",
		Summary: err.Error(),
	}
}

func truncateRunes(s string, max int) string {
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	r := []rune(s)
	return string(r[:max])
}
