package domain

import (
	"crypto/ecdh"
	"encoding/base64"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	// MaxSubscriptionsPerUser caps persisted push registrations per user.
	MaxSubscriptionsPerUser = 10
	// MaxEndpointLength bounds stored Web Push endpoint URLs.
	MaxEndpointLength = 2048
	// MaxUserAgentLength bounds stored device/browser labels.
	MaxUserAgentLength = 256
	// HintChannel is the shared disposable Redis pub/sub channel used by Chat outbox hints.
	HintChannel = "teamie:im:hints:v1"
)

// VAPIDConfig holds validated application-server VAPID credentials (RFC 8292).
type VAPIDConfig struct {
	PublicKey  string // Raw URL-safe base64 (unpadded), 65-byte uncompressed P-256 point
	PrivateKey string // Raw URL-safe base64 (unpadded), 32-byte P-256 scalar
	Subject    string // mailto:... or https://...
}

// Configured reports whether all three VAPID fields are populated.
func (c VAPIDConfig) Configured() bool {
	return strings.TrimSpace(c.PublicKey) != "" &&
		strings.TrimSpace(c.PrivateKey) != "" &&
		strings.TrimSpace(c.Subject) != ""
}

// PushConfig is the public client configuration returned by GET /api/v1/push/config.
type PushConfig struct {
	Enabled          bool
	VAPIDPublicKey   string
	MaxSubscriptions int
}

// PushSubscription represents a stored browser Web Push endpoint belonging to one human user.
type PushSubscription struct {
	ID            string
	UserID        string
	EndpointHash  string
	Endpoint      string
	EndpointHost  string
	P256DH        string
	Auth          string
	UserAgent     string
	CreatedAt     time.Time
	UpdatedAt     time.Time
	LastSuccessAt *time.Time
	LastErrorCode string
}

// UpsertSubscriptionInput carries validated user-supplied subscription fields.
type UpsertSubscriptionInput struct {
	Endpoint  string
	P256DH    string
	Auth      string
	UserAgent string
}

// PushMessageHint is the opaque, content-free Redis hint emitted by Chat on message creation.
type PushMessageHint struct {
	Type           string   `json:"type"`
	WorkspaceID    string   `json:"workspaceId"`
	ConversationID string   `json:"conversationId"`
	MessageID      string   `json:"messageId"`
	SenderID       string   `json:"senderId"`
	UserIDs        []string `json:"userIds"`
}

// PushEvaluationResult holds the opaque wake target returned by ChatService.EvaluatePushRecipients.
type PushEvaluationResult struct {
	WorkspaceID      string
	ConversationID   string
	MessageID        string
	RecipientUserIDs []string
}

// PushWakePayload is the encrypted JSON payload delivered to the browser Service Worker.
// Privacy invariant: NEVER contains message bodies, attachments, sender names, or conversation titles.
type PushWakePayload struct {
	Type           string `json:"type"`
	WorkspaceID    string `json:"workspaceId"`
	ConversationID string `json:"conversationId"`
	MessageID      string `json:"messageId"`
	URL            string `json:"url"`
}

// DeliveryResult describes the outcome of delivering one Web Push HTTP request to a push relay.
type DeliveryResult struct {
	StatusCode int
	Gone       bool // True when relay returned 404 or 410 (subscription permanently invalid)
	Reason     string
}

var (
	cgnatPrefix       = netip.MustParsePrefix("100.64.0.0/10")
	internalHostNames = map[string]struct{}{
		"localhost": {}, "postgres": {}, "redis": {}, "chat": {}, "identity": {},
		"realtime": {}, "marketplace": {}, "push": {}, "agent-control": {},
		"agent-runtime": {}, "llm-gateway": {}, "sandbox": {}, "caddy": {}, "web": {},
	}
)

// IsPublicTargetIP reports whether ip is a routable global unicast address safe for outbound push delivery.
func IsPublicTargetIP(ip netip.Addr) bool {
	ip = ip.Unmap()
	if !ip.IsValid() || ip.IsUnspecified() || ip.IsLoopback() || ip.IsPrivate() ||
		ip.IsMulticast() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || cgnatPrefix.Contains(ip) {
		return false
	}
	return true
}

// ValidateEndpointURL validates and normalizes a Web Push endpoint URL against SSRF and scheme rules.
func ValidateEndpointURL(raw string, requirePublicHTTPS bool) (normalized string, host string, err error) {
	trimmed := strings.TrimSpace(raw)
	if len(trimmed) == 0 || len(trimmed) > MaxEndpointLength || strings.ContainsAny(trimmed, " \t\r\n\x00") {
		return "", "", ErrInvalidEndpoint()
	}
	u, parseErr := url.Parse(trimmed)
	if parseErr != nil || u.Host == "" || u.Hostname() == "" || u.User != nil || u.Fragment != "" {
		return "", "", ErrInvalidEndpoint()
	}
	scheme := strings.ToLower(u.Scheme)
	if requirePublicHTTPS {
		if scheme != "https" {
			return "", "", ErrInvalidEndpoint()
		}
	} else if scheme != "https" && scheme != "http" {
		return "", "", ErrInvalidEndpoint()
	}
	lowerHost := strings.ToLower(u.Hostname())
	if requirePublicHTTPS {
		if _, internal := internalHostNames[lowerHost]; internal ||
			strings.HasSuffix(lowerHost, ".localhost") ||
			strings.HasSuffix(lowerHost, ".local") ||
			strings.HasSuffix(lowerHost, ".internal") ||
			strings.HasSuffix(lowerHost, ".home.arpa") {
			return "", "", ErrForbiddenEndpoint()
		}
		if ip, ipErr := netip.ParseAddr(lowerHost); ipErr == nil {
			if !IsPublicTargetIP(ip) {
				return "", "", ErrForbiddenEndpoint()
			}
		} else if parsedIP := net.ParseIP(lowerHost); parsedIP != nil {
			return "", "", ErrForbiddenEndpoint()
		}
	}
	return trimmed, lowerHost, nil
}

// DecodeBase64URL decodes standard unpadded or padded base64url strings cleanly.
func DecodeBase64URL(raw string) ([]byte, error) {
	trimmed := strings.TrimRight(strings.TrimSpace(raw), "=")
	if trimmed == "" {
		return nil, fmt.Errorf("empty base64url value")
	}
	return base64.RawURLEncoding.DecodeString(trimmed)
}

// ValidateP256DH validates an RFC 8291 client public key (65-byte uncompressed P-256 point).
func ValidateP256DH(raw string) (string, error) {
	decoded, err := DecodeBase64URL(raw)
	if err != nil || len(decoded) != 65 || decoded[0] != 0x04 {
		return "", ErrInvalidSubscriptionKeys()
	}
	if _, err := ecdh.P256().NewPublicKey(decoded); err != nil {
		return "", ErrInvalidSubscriptionKeys()
	}
	return base64.RawURLEncoding.EncodeToString(decoded), nil
}

// ValidateAuthSecret validates an RFC 8291 16-byte client authentication secret.
func ValidateAuthSecret(raw string) (string, error) {
	decoded, err := DecodeBase64URL(raw)
	if err != nil || len(decoded) != 16 {
		return "", ErrInvalidSubscriptionKeys()
	}
	return base64.RawURLEncoding.EncodeToString(decoded), nil
}

// ValidateVAPIDConfig validates an optional or configured VAPID keypair and subject.
// If all fields are empty, returns an empty VAPIDConfig and nil error (push stays inactive).
// If partially set or malformed, returns an error so misconfiguration fails closed.
func ValidateVAPIDConfig(publicKey, privateKey, subject string) (VAPIDConfig, error) {
	pub := strings.TrimSpace(publicKey)
	priv := strings.TrimSpace(privateKey)
	sub := strings.TrimSpace(subject)
	if pub == "" && priv == "" && sub == "" {
		return VAPIDConfig{}, nil
	}
	if pub == "" || priv == "" || sub == "" {
		return VAPIDConfig{}, fmt.Errorf("PUSH_VAPID_PUBLIC_KEY, PUSH_VAPID_PRIVATE_KEY, and PUSH_VAPID_SUBJECT must all be set together")
	}
	pubBytes, err := DecodeBase64URL(pub)
	if err != nil || len(pubBytes) != 65 || pubBytes[0] != 0x04 {
		return VAPIDConfig{}, fmt.Errorf("PUSH_VAPID_PUBLIC_KEY must be a base64url-encoded 65-byte uncompressed P-256 public key")
	}
	privBytes, err := DecodeBase64URL(priv)
	if err != nil || len(privBytes) != 32 {
		return VAPIDConfig{}, fmt.Errorf("PUSH_VAPID_PRIVATE_KEY must be a base64url-encoded 32-byte P-256 private key")
	}
	ecdhPriv, err := ecdh.P256().NewPrivateKey(privBytes)
	if err != nil {
		return VAPIDConfig{}, fmt.Errorf("PUSH_VAPID_PRIVATE_KEY is not a valid P-256 scalar")
	}
	derivedPub := ecdhPriv.PublicKey().Bytes()
	if base64.RawURLEncoding.EncodeToString(derivedPub) != base64.RawURLEncoding.EncodeToString(pubBytes) {
		return VAPIDConfig{}, fmt.Errorf("PUSH_VAPID_PUBLIC_KEY does not match PUSH_VAPID_PRIVATE_KEY")
	}
	lowerSub := strings.ToLower(sub)
	if !strings.HasPrefix(lowerSub, "mailto:") && !strings.HasPrefix(lowerSub, "https://") {
		return VAPIDConfig{}, fmt.Errorf("PUSH_VAPID_SUBJECT must start with mailto: or https://")
	}
	if strings.ContainsAny(sub, " \t\r\n\x00") || len(sub) > 256 {
		return VAPIDConfig{}, fmt.Errorf("PUSH_VAPID_SUBJECT is invalid")
	}
	return VAPIDConfig{
		PublicKey:  base64.RawURLEncoding.EncodeToString(pubBytes),
		PrivateKey: base64.RawURLEncoding.EncodeToString(privBytes),
		Subject:    sub,
	}, nil
}

// SanitizeUserAgent trims control characters and bounds user-agent labels.
func SanitizeUserAgent(raw string) string {
	cleaned := strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, strings.TrimSpace(raw))
	if utf8.RuneCountInString(cleaned) <= MaxUserAgentLength {
		return cleaned
	}
	runes := []rune(cleaned)
	return string(runes[:MaxUserAgentLength])
}

// EndpointFingerprint returns a safe non-reversible short fingerprint for observability logs.
func EndpointFingerprint(endpointHash string) string {
	if len(endpointHash) <= 16 {
		return endpointHash
	}
	return endpointHash[:16]
}

// CanonicalAudience returns the RFC 8292 VAPID JWT audience ("scheme://host[:nonDefaultPort]").
func CanonicalAudience(endpoint string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(endpoint))
	if err != nil || u.Scheme == "" || u.Hostname() == "" {
		return "", ErrInvalidEndpoint()
	}
	scheme := strings.ToLower(u.Scheme)
	host := strings.ToLower(u.Hostname())
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	port := u.Port()
	if (scheme == "http" && port == "80") || (scheme == "https" && port == "443") {
		port = ""
	}
	if port != "" {
		return scheme + "://" + host + ":" + port, nil
	}
	return scheme + "://" + host, nil
}
