package domain

import (
	"context"
	"net"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"unicode/utf8"
)

const (
	DefaultTavilyBaseURL   = "https://api.tavily.com"
	MaxExtractContentBytes = 64 << 10
	MaxSearchSnippetBytes  = 1500
	MaxSearchTitleBytes    = 512
	MaxWebResponseBytes    = 1 << 20
)

var deniedPrefixes = []string{
	"0.0.0.0/8", "10.0.0.0/8", "100.64.0.0/10", "127.0.0.0/8",
	"169.254.0.0/16", "172.16.0.0/12", "192.0.0.0/24", "192.0.2.0/24",
	"192.168.0.0/16", "198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24",
	"224.0.0.0/4", "240.0.0.0/4", "::/96", "::ffff:0:0/96",
	"64:ff9b::/96", "64:ff9b:1::/48", "100::/64", "2001::/23",
	"2001:db8::/32", "2002::/16", "fc00::/7", "fe80::/10", "ff00::/8",
}

// WebSearchPolicy groups SSRF, endpoint, domain-filter, and URL-extraction invariants.
type WebSearchPolicy struct{}

// PublicIP reports whether ip is a globally routable public unicast address.
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

// ParseEndpoint validates that raw is a canonical HTTPS provider base URL.
func ParseEndpoint(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || len(raw) > 2048 {
		return nil, Err(400, "invalid_endpoint", "Use a public HTTPS API base URL without credentials, query or fragment")
	}
	if strings.ContainsAny(raw, "\r\n\t\\") || strings.Contains(u.EscapedPath(), "%") || strings.Contains(u.Path, "..") || strings.HasSuffix(u.Path, "/chat/completions") {
		return nil, Err(400, "invalid_endpoint", "Use the API base URL, for example https://provider.example/v1")
	}
	if p := u.Port(); p != "" {
		n, err := strconv.Atoi(p)
		if err != nil || n < 1 || n > 65535 {
			return nil, Err(400, "invalid_endpoint", "Invalid provider port")
		}
	}
	return u, nil
}

// ValidateEndpoint resolves and checks that a provider base URL points only to public IPs.
func ValidateEndpoint(ctx context.Context, raw string) error {
	if raw == "" {
		return nil
	}
	u, err := ParseEndpoint(raw)
	if err != nil {
		return err
	}
	ips, err := net.DefaultResolver.LookupIPAddr(ctx, u.Hostname())
	if err != nil || len(ips) == 0 {
		return Err(400, "invalid_endpoint", "Provider host could not be resolved")
	}
	for _, ip := range ips {
		if !PublicIP(ip.IP) {
			return Err(400, "invalid_endpoint", "Provider host must resolve only to public addresses")
		}
	}
	return nil
}

// UTF8Prefix truncates text to at most limit bytes on a valid UTF-8 rune boundary.
func UTF8Prefix(text string, limit int) string {
	if len(text) <= limit {
		return text
	}
	for limit > 0 && !utf8.RuneStart(text[limit]) {
		limit--
	}
	return text[:limit]
}

// ValidDomainName checks and normalizes a domain filter entry.
func ValidDomainName(raw string) (string, bool) {
	d := strings.ToLower(strings.TrimSpace(raw))
	if d == "" || len(d) > 253 || strings.HasPrefix(d, ".") || strings.HasSuffix(d, ".") || !strings.Contains(d, ".") {
		return "", false
	}
	if d == "localhost" || strings.HasSuffix(d, ".localhost") || strings.HasSuffix(d, ".local") || strings.HasSuffix(d, ".internal") {
		return "", false
	}
	if net.ParseIP(d) != nil {
		return "", false
	}
	labels := strings.Split(d, ".")
	for _, label := range labels {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return "", false
		}
		for i := 0; i < len(label); i++ {
			ch := label[i]
			if !((ch >= 'a' && ch <= 'z') || (ch >= '0' && ch <= '9') || ch == '-') {
				return "", false
			}
		}
	}
	return d, true
}

// NormalizeDomains validates and deduplicates up to 10 domain filter entries.
func NormalizeDomains(items []string) ([]string, error) {
	if len(items) > 10 {
		return nil, Err(400, "invalid_request", "At most 10 domain filters are allowed")
	}
	if len(items) == 0 {
		return nil, nil
	}
	out := make([]string, 0, len(items))
	seen := make(map[string]bool, len(items))
	for _, raw := range items {
		d, ok := ValidDomainName(raw)
		if !ok {
			return nil, Err(400, "invalid_request", "Invalid domain filter")
		}
		if !seen[d] {
			seen[d] = true
			out = append(out, d)
		}
	}
	return out, nil
}

// ValidateExtractURLs validates and deduplicates between 1 and 5 public HTTP/HTTPS target URLs.
func ValidateExtractURLs(rawURLs []string) ([]string, error) {
	if len(rawURLs) == 0 || len(rawURLs) > 5 {
		return nil, Err(400, "invalid_request", "Between 1 and 5 URLs are required")
	}
	out := make([]string, 0, len(rawURLs))
	seen := make(map[string]bool, len(rawURLs))
	for _, raw := range rawURLs {
		trimmed := strings.TrimSpace(raw)
		if trimmed == "" || len(trimmed) > 2048 || strings.ContainsAny(trimmed, "\r\n\t\x00") {
			return nil, Err(400, "invalid_request", "Invalid target URL")
		}
		u, err := url.Parse(trimmed)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || u.Fragment != "" {
			return nil, Err(400, "invalid_request", "Use public HTTP or HTTPS URLs without credentials or fragments")
		}
		if p := u.Port(); p != "" {
			port, err := strconv.Atoi(p)
			if err != nil || port < 1 || port > 65535 {
				return nil, Err(400, "invalid_request", "Invalid URL port")
			}
		}
		host := strings.ToLower(strings.TrimSuffix(u.Hostname(), "."))
		if host == "localhost" || strings.HasSuffix(host, ".localhost") || strings.HasSuffix(host, ".local") || strings.HasSuffix(host, ".internal") || !strings.Contains(host, ".") {
			return nil, Err(400, "invalid_request", "Private or internal hostnames are not permitted")
		}
		if ip := net.ParseIP(host); ip != nil && !PublicIP(ip) {
			return nil, Err(400, "invalid_request", "Private or reserved IP addresses are not permitted")
		}
		normalized := u.String()
		if !seen[normalized] {
			seen[normalized] = true
			out = append(out, normalized)
		}
	}
	return out, nil
}

// WebSearchInput holds the validated web search request fields.
type WebSearchInput struct {
	OwnerID        string
	AgentID        string
	SessionID      string
	RunID          string
	Query          string
	MaxResults     int
	TimeRange      string
	IncludeDomains []string
	ExcludeDomains []string
}

// WebExtractInput holds the validated web extract request fields.
type WebExtractInput struct {
	OwnerID   string
	AgentID   string
	SessionID string
	RunID     string
	URLs      []string
}

// WebSearchItem is a normalized web search result item.
type WebSearchItem struct {
	Title         string
	URL           string
	Snippet       string
	PublishedDate string
	Score         float64
}

// WebExtractItem is a normalized extracted page result.
type WebExtractItem struct {
	URL     string
	Content string
}

// WebExtractFailure records a failed URL extraction.
type WebExtractFailure struct {
	URL   string
	Error string
}
