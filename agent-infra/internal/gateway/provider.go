package gateway

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"

	"teamie/agent-infra/internal/api"
	"teamie/agent-protocol/modeltransport"
)

const MaxRequestBytes = modeltransport.MaxRequestBytes
const MaxResponseBytes = modeltransport.MaxResponseBytes

var deniedPrefixes = []string{"0.0.0.0/8", "10.0.0.0/8", "100.64.0.0/10", "127.0.0.0/8", "169.254.0.0/16", "172.16.0.0/12", "192.0.0.0/24", "192.0.2.0/24", "192.168.0.0/16", "198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "224.0.0.0/4", "240.0.0.0/4", "::/96", "::ffff:0:0/96", "64:ff9b::/96", "64:ff9b:1::/48", "100::/64", "2001::/23", "2001:db8::/32", "2002::/16", "fc00::/7", "fe80::/10", "ff00::/8"}

func PublicIP(ip net.IP) bool {
	a, ok := netip.AddrFromSlice(ip)
	if !ok {
		return false
	}
	a = a.Unmap()
	if !a.IsGlobalUnicast() {
		return false
	}
	for _, p := range deniedPrefixes {
		if netip.MustParsePrefix(p).Contains(a) {
			return false
		}
	}
	return true
}
func endpoint(raw string) (*url.URL, error) {
	u, e := url.Parse(raw)
	if e != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || len(raw) > 2048 {
		return nil, api.Err(400, "invalid_endpoint", "Use a public HTTPS API base URL without credentials, query or fragment")
	}
	if strings.ContainsAny(raw, "\r\n\t\\") || strings.Contains(u.EscapedPath(), "%") || strings.Contains(u.Path, "..") || strings.HasSuffix(u.Path, "/chat/completions") {
		return nil, api.Err(400, "invalid_endpoint", "Use the API base URL, for example https://provider.example/v1")
	}
	if p := u.Port(); p != "" {
		n, e := strconv.Atoi(p)
		if e != nil || n < 1 || n > 65535 {
			return nil, api.Err(400, "invalid_endpoint", "Invalid provider port")
		}
	}
	return u, nil
}
func ValidateEndpoint(ctx context.Context, raw string) error {
	if raw == "" {
		return nil
	}
	u, e := endpoint(raw)
	if e != nil {
		return e
	}
	ips, e := net.DefaultResolver.LookupIPAddr(ctx, u.Hostname())
	if e != nil || len(ips) == 0 {
		return api.Err(400, "invalid_endpoint", "Provider host could not be resolved")
	}
	for _, ip := range ips {
		if !PublicIP(ip.IP) {
			return api.Err(400, "invalid_endpoint", "Provider host must resolve only to public addresses")
		}
	}
	return nil
}

// Every connection resolves and pins validated public addresses. No environment proxy,
// redirect, or second unchecked DNS lookup may carry the provider credential.
func NewHTTPClient() *http.Client {
	dialer := &net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}
	tr := &http.Transport{Proxy: nil, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12}, MaxIdleConns: 2, MaxIdleConnsPerHost: 1, IdleConnTimeout: 30 * time.Second, TLSHandshakeTimeout: 5 * time.Second, MaxResponseHeaderBytes: 32768, DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, e := net.SplitHostPort(address)
		if e != nil {
			return nil, errors.New("invalid provider address")
		}
		resolveCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		ips, e := net.DefaultResolver.LookupIPAddr(resolveCtx, host)
		cancel()
		if e != nil || len(ips) == 0 {
			return nil, errors.New("provider resolution failed")
		}
		for _, ip := range ips {
			if !PublicIP(ip.IP) {
				return nil, errors.New("provider address denied")
			}
		}
		for _, ip := range ips {
			c, e := dialer.DialContext(ctx, network, net.JoinHostPort(ip.IP.String(), port))
			if e == nil {
				return c, nil
			}
		}
		return nil, errors.New("provider connection failed")
	}}
	// Header arrival and body reads include provider inference. Only the caller
	// context bounds that work; DNS, connection and TLS setup remain bounded.
	return &http.Client{Transport: tr, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}
func normalizeRequest(raw, model string) ([]byte, error) { return normalizeMode(raw, model, false) }
func normalizeMode(raw, model string, streaming bool) ([]byte, error) {
	return normalizeModeWithOutputLimit(raw, model, streaming, 4096)
}

func normalizeModeWithOutputLimit(raw, model string, streaming bool, outputLimit int) ([]byte, error) {
	if len(raw) > MaxRequestBytes {
		return nil, api.Err(413, "request_too_large", "Model request is too large")
	}
	var m map[string]json.RawMessage
	if json.Unmarshal([]byte(raw), &m) != nil || m == nil {
		return nil, api.Err(400, "invalid_request", "Invalid chat completion JSON")
	}
	allowed := map[string]bool{"messages": true, "model": true, "stream": true, "stream_options": true, "tools": true, "tool_choice": true, "parallel_tool_calls": true, "temperature": true, "top_p": true, "max_tokens": true, "max_completion_tokens": true, "stop": true, "response_format": true, "seed": true, "frequency_penalty": true, "presence_penalty": true, "n": true, "enable_thinking": true}
	for k := range m {
		if !allowed[k] {
			return nil, api.Err(400, "invalid_request", "Unsupported chat completion option")
		}
	}
	if raw, ok := m["enable_thinking"]; ok {
		if value := strings.TrimSpace(string(raw)); value != "true" && value != "false" {
			return nil, api.Err(400, "invalid_request", "Invalid thinking option")
		}
	}
	var messages []map[string]json.RawMessage
	if json.Unmarshal(m["messages"], &messages) != nil || len(messages) == 0 || len(messages) > modeltransport.MaxInputMessages {
		return nil, api.Err(400, "invalid_request", "Invalid chat message count")
	}
	for _, msg := range messages {
		var role string
		_ = json.Unmarshal(msg["role"], &role)
		switch role {
		case "system", "developer", "user", "assistant", "tool":
		default:
			return nil, api.Err(400, "invalid_request", "Unsupported message role")
		}
	}
	var stream bool
	if b, ok := m["stream"]; ok && (json.Unmarshal(b, &stream) != nil || stream && !streaming) {
		return nil, api.Err(400, "stream_unsupported", "Use non-streaming chat completions")
	}
	var tools []json.RawMessage
	if b, ok := m["tools"]; ok && (json.Unmarshal(b, &tools) != nil || len(tools) > 32) {
		return nil, api.Err(400, "invalid_request", "At most 32 tools are supported")
	}
	for _, k := range []string{"max_tokens", "max_completion_tokens"} {
		if b, ok := m[k]; ok {
			var n int
			if json.Unmarshal(b, &n) != nil || n < 1 {
				return nil, api.Err(400, "invalid_request", "Invalid completion token limit")
			}
			if n > outputLimit {
				m[k] = json.RawMessage(strconv.Itoa(outputLimit))
			}
		}
	}
	if _, ok := m["max_tokens"]; !ok {
		if _, ok = m["max_completion_tokens"]; !ok {
			m["max_completion_tokens"] = json.RawMessage("2048")
		}
	}
	m["model"], _ = json.Marshal(model)
	m["n"] = json.RawMessage("1")
	m["stream"] = json.RawMessage("false")
	delete(m, "stream_options")
	if streaming {
		m["stream"] = json.RawMessage("true")
		m["stream_options"] = json.RawMessage(`{"include_usage":true}`)
	}
	return json.Marshal(m)
}

type Provider struct{ HTTP *http.Client }
