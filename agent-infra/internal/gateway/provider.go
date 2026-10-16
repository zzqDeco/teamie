package gateway

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"

	"teamie/agent-infra/internal/api"
)

const MaxRequestBytes = 262144
const MaxResponseBytes = 2097152

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
	tr := &http.Transport{Proxy: nil, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12}, MaxIdleConns: 2, MaxIdleConnsPerHost: 1, IdleConnTimeout: 30 * time.Second, TLSHandshakeTimeout: 5 * time.Second, ResponseHeaderTimeout: 45 * time.Second, MaxResponseHeaderBytes: 32768, DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, e := net.SplitHostPort(address)
		if e != nil {
			return nil, errors.New("invalid provider address")
		}
		ips, e := net.DefaultResolver.LookupIPAddr(ctx, host)
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
	return &http.Client{Transport: tr, Timeout: 50 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}
func normalizeRequest(raw, model string) ([]byte, error) {
	if len(raw) > MaxRequestBytes {
		return nil, api.Err(413, "request_too_large", "Model request is too large")
	}
	var m map[string]json.RawMessage
	if json.Unmarshal([]byte(raw), &m) != nil || m == nil {
		return nil, api.Err(400, "invalid_request", "Invalid chat completion JSON")
	}
	allowed := map[string]bool{"messages": true, "model": true, "stream": true, "tools": true, "tool_choice": true, "parallel_tool_calls": true, "temperature": true, "top_p": true, "max_tokens": true, "max_completion_tokens": true, "stop": true, "response_format": true, "seed": true, "frequency_penalty": true, "presence_penalty": true, "n": true}
	for k := range m {
		if !allowed[k] {
			return nil, api.Err(400, "invalid_request", "Unsupported chat completion option")
		}
	}
	var messages []map[string]json.RawMessage
	if json.Unmarshal(m["messages"], &messages) != nil || len(messages) == 0 || len(messages) > 128 {
		return nil, api.Err(400, "invalid_request", "Expected 1 to 128 chat messages")
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
	if b, ok := m["stream"]; ok && (json.Unmarshal(b, &stream) != nil || stream) {
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
			if n > 4096 {
				m[k] = json.RawMessage("4096")
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
	return json.Marshal(m)
}

type Provider struct{ HTTP *http.Client }

func (p Provider) Chat(ctx context.Context, c Config, key, raw string) (string, int64, int64, error) {
	if ctx.Err() != nil {
		return "", 0, 0, api.Err(499, "canceled", "Model request canceled")
	}
	payload, e := normalizeRequest(raw, c.Model)
	if e != nil {
		return "", 0, 0, e
	}
	u, e := endpoint(c.BaseURL)
	if e != nil {
		return "", 0, 0, e
	}
	u.Path = strings.TrimRight(u.Path, "/") + "/chat/completions"
	req, e := http.NewRequestWithContext(ctx, "POST", u.String(), bytes.NewReader(payload))
	if e != nil {
		return "", 0, 0, api.Err(502, "upstream_error", "Provider request could not be created")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+key)
	res, e := p.HTTP.Do(req)
	if e != nil {
		if ctx.Err() != nil {
			return "", 0, 0, api.Err(499, "canceled", "Model request canceled")
		}
		return "", 0, 0, api.Err(502, "upstream_unavailable", "Provider could not be reached")
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		code := "upstream_error"
		status := 502
		if res.StatusCode == 429 {
			code = "upstream_rate_limited"
			status = 429
		}
		return "", 0, 0, api.Err(status, code, "Provider rejected the request")
	}
	body, e := io.ReadAll(io.LimitReader(res.Body, MaxResponseBytes+1))
	if e != nil || len(body) > MaxResponseBytes {
		return "", 0, 0, api.Err(502, "invalid_upstream_response", "Provider response exceeded limits")
	}
	var result struct {
		Object  string `json:"object"`
		Choices []struct {
			Message struct {
				Role string `json:"role"`
			} `json:"message"`
		} `json:"choices"`
		Usage struct {
			PromptTokens     int64 `json:"prompt_tokens"`
			CompletionTokens int64 `json:"completion_tokens"`
		} `json:"usage"`
	}
	if json.Unmarshal(body, &result) != nil || result.Object != "chat.completion" || len(result.Choices) != 1 || result.Choices[0].Message.Role != "assistant" || result.Usage.PromptTokens < 0 || result.Usage.CompletionTokens < 0 {
		return "", 0, 0, api.Err(502, "invalid_upstream_response", "Invalid chat completion response")
	}
	if bytes.Contains(body, []byte(key)) {
		return "", 0, 0, api.Err(502, "invalid_upstream_response", "Provider returned a credential in its response")
	}
	return string(body), result.Usage.PromptTokens, result.Usage.CompletionTokens, nil
}
