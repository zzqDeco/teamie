package domain

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"net"
	"net/netip"
	"net/url"
	"strings"
	"time"
)

const (
	MarketplaceSessionTTL  = time.Hour
	DeveloperAppQuota      = 20
	OfficialPublisherLabel = "Teamie 官方"

	ReviewStatusDraft    = "draft"
	ReviewStatusPending  = "pending_review"
	ReviewStatusApproved = "approved"
	ReviewStatusRejected = "rejected"

	ReviewActionSubmit    = "submit"
	ReviewActionWithdraw  = "withdraw"
	ReviewActionApprove   = "approve"
	ReviewActionReject    = "reject"
	ReviewActionSuspend   = "suspend"
	ReviewActionUnsuspend = "unsuspend"
)

// DiscoveredMCPTool describes a tool discovered from a developer's deployed MCP Server.
type DiscoveredMCPTool struct {
	Name            string `json:"name"`
	ExposedName     string `json:"exposedName"`
	Description     string `json:"description"`
	InputSchemaJSON string `json:"inputSchemaJson,omitempty"`
}

// EndpointProbeCheck records one stage of the deployed WebUI + MCP protocol probe.
type EndpointProbeCheck struct {
	Stage   string `json:"stage"`
	OK      bool   `json:"ok"`
	Code    string `json:"code"`
	Summary string `json:"summary"`
}

// MarketplaceEndpointVerification captures a full live protocol inspection of a deployed service.
type MarketplaceEndpointVerification struct {
	OK                 bool                 `json:"ok"`
	VerifiedAt         time.Time            `json:"verifiedAt"`
	WebURL             string               `json:"webUrl"`
	MCPURL             string               `json:"mcpUrl"`
	MCPServerName      string               `json:"mcpServerName,omitempty"`
	MCPServerVersion   string               `json:"mcpServerVersion,omitempty"`
	MCPProtocolVersion string               `json:"mcpProtocolVersion,omitempty"`
	Checks             []EndpointProbeCheck `json:"checks"`
	Tools              []DiscoveredMCPTool  `json:"tools"`
}

// FirstFailureSummary returns the human-readable summary of the first failing probe check.
func (v MarketplaceEndpointVerification) FirstFailureSummary() string {
	for _, check := range v.Checks {
		if !check.OK && strings.TrimSpace(check.Summary) != "" {
			return check.Summary
		}
	}
	return ""
}

// MarketplaceAppReview represents an immutable submission/review/governance audit record.
type MarketplaceAppReview struct {
	ID        string    `json:"id"`
	AppID     string    `json:"appId"`
	Revision  int       `json:"revision"`
	Action    string    `json:"action"`
	ActorID   string    `json:"actorId"`
	ActorName string    `json:"actorName,omitempty"`
	Note      string    `json:"note"`
	WebURL    string    `json:"webUrl"`
	MCPURL    string    `json:"mcpUrl"`
	CreatedAt time.Time `json:"createdAt"`
}

// MarketplaceApp represents a platform-registered or developer-submitted Web + MCP application.
type MarketplaceApp struct {
	ID                 string                           `json:"id"`
	Slug               string                           `json:"slug"`
	Name               string                           `json:"name"`
	Description        string                           `json:"description"`
	WebURL             string                           `json:"webUrl"`
	MCPURL             string                           `json:"mcpUrl"`
	SigningSecret      string                           `json:"-"`
	Enabled            bool                             `json:"enabled"`
	CreatedAt          time.Time                        `json:"createdAt"`
	UpdatedAt          time.Time                        `json:"updatedAt"`
	OwnerID            *string                          `json:"ownerId,omitempty"`
	OwnerDisplayName   string                           `json:"ownerDisplayName,omitempty"`
	PublisherName      string                           `json:"publisherName,omitempty"`
	ServingDraft       bool                             `json:"servingDraft,omitempty"`
	IsPublished        bool                             `json:"isPublished"`
	PublishedVersion   int                              `json:"publishedVersion"`
	ReviewStatus       string                           `json:"reviewStatus"`
	DraftName          string                           `json:"draftName"`
	DraftDescription   string                           `json:"draftDescription"`
	DraftWebURL        string                           `json:"draftWebUrl"`
	DraftMCPURL        string                           `json:"draftMcpUrl"`
	HasDraftChanges    bool                             `json:"hasDraftChanges"`
	SubmissionRevision int                              `json:"submissionRevision"`
	SubmissionNote     string                           `json:"submissionNote"`
	SubmittedAt        *time.Time                       `json:"submittedAt,omitempty"`
	ReviewedBy         *string                          `json:"reviewedBy,omitempty"`
	ReviewedAt         *time.Time                       `json:"reviewedAt,omitempty"`
	ReviewNote         string                           `json:"reviewNote"`
	AdminSuspended     bool                             `json:"adminSuspended"`
	SuspendedReason    string                           `json:"suspendedReason"`
	SuspendedAt        *time.Time                       `json:"suspendedAt,omitempty"`
	LastVerifiedAt     *time.Time                       `json:"lastVerifiedAt,omitempty"`
	VerificationReport *MarketplaceEndpointVerification `json:"verificationReport,omitempty"`
}

// IsOfficial returns true for apps created directly by a platform administrator without a developer owner.
func (a MarketplaceApp) IsOfficial() bool {
	return a.OwnerID == nil || strings.TrimSpace(*a.OwnerID) == ""
}

// ResolveForWorkspace resolves which snapshot (published vs draft) is visible and runnable in a workspace.
// Returns false if the app is disabled, suspended, or not authorized to run in that workspace.
func (a MarketplaceApp) ResolveForWorkspace(workspaceOwnerID string, reviewPreview bool) (MarketplaceApp, bool) {
	if !a.Enabled || a.AdminSuspended {
		return MarketplaceApp{}, false
	}
	isDeveloperOwnWorkspace := a.OwnerID != nil && *a.OwnerID != "" && *a.OwnerID == workspaceOwnerID
	isAdminReviewWorkspace := reviewPreview && a.ReviewStatus == ReviewStatusPending
	if isDeveloperOwnWorkspace || isAdminReviewWorkspace {
		if strings.TrimSpace(a.DraftWebURL) == "" || strings.TrimSpace(a.DraftMCPURL) == "" {
			return MarketplaceApp{}, false
		}
		resolved := a
		resolved.Name = a.DraftName
		resolved.Description = a.DraftDescription
		resolved.WebURL = a.DraftWebURL
		resolved.MCPURL = a.DraftMCPURL
		resolved.ServingDraft = !a.IsPublished || a.HasDraftChanges || isAdminReviewWorkspace
		return resolved, true
	}
	if !a.IsPublished || strings.TrimSpace(a.WebURL) == "" || strings.TrimSpace(a.MCPURL) == "" {
		return MarketplaceApp{}, false
	}
	resolved := a
	resolved.ServingDraft = false
	return resolved, true
}

// CreateMarketplaceAppInput holds fields for registering a new MarketplaceApp.
type CreateMarketplaceAppInput struct {
	Slug        string
	Name        string
	Description string
	WebURL      string
	MCPURL      string
	Enabled     *bool
}

// UpdateMarketplaceAppInput holds optional fields for updating a MarketplaceApp.
type UpdateMarketplaceAppInput struct {
	Slug        *string
	Name        *string
	Description *string
	WebURL      *string
	MCPURL      *string
	Enabled     *bool
}

// ConversationAppSession holds an issued group-conversation app context token.
type ConversationAppSession struct {
	App       MarketplaceApp `json:"app"`
	Token     string         `json:"token"`
	ExpiresAt time.Time      `json:"expiresAt"`
}

// MarketplaceContextClaims defines the symmetric HMAC-SHA256 JWT payload shared
// identically by human and Agent group participants when calling an MCP app.
type MarketplaceContextClaims struct {
	Issuer         string `json:"iss"`
	Audience       string `json:"aud"`
	Subject        string `json:"sub"`
	AppID          string `json:"appId"`
	Slug           string `json:"slug"`
	WorkspaceID    string `json:"workspaceId"`
	ConversationID string `json:"conversationId"`
	UserID         string `json:"userId"`
	DisplayName    string `json:"displayName"`
	IssuedAt       int64  `json:"iat"`
	ExpiresAt      int64  `json:"exp"`
}

// NewMarketplaceSigningSecret generates a high-entropy signing secret for an app.
func NewMarketplaceSigningSecret() string {
	return "tmsec_" + RandomHex(32)
}

// ValidMarketplaceSlug checks whether slug matches ^[a-z0-9][a-z0-9_-]{0,63}$.
func ValidMarketplaceSlug(slug string) bool {
	if len(slug) < 1 || len(slug) > 64 {
		return false
	}
	for i := 0; i < len(slug); i++ {
		c := slug[i]
		isLowerDigit := (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9')
		if i == 0 {
			if !isLowerDigit {
				return false
			}
			continue
		}
		if !isLowerDigit && c != '-' && c != '_' {
			return false
		}
	}
	return true
}

// ValidMarketplaceHTTPURL checks that raw is a valid http:// or https:// URL without embedded credentials.
func ValidMarketplaceHTTPURL(raw string) bool {
	raw = strings.TrimSpace(raw)
	if len(raw) == 0 || len(raw) > 512 || strings.ContainsAny(raw, "\r\n\x00") {
		return false
	}
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return false
	}
	if u.Host == "" || u.Hostname() == "" || u.User != nil {
		return false
	}
	return true
}

// CanonicalOrigin normalizes an http(s) URL to RFC 6454 scheme://host[:nonDefaultPort].
func CanonicalOrigin(raw string) (string, bool) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
		return "", false
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
		return scheme + "://" + host + ":" + port, true
	}
	return scheme + "://" + host, true
}

var (
	cgnatPrefix       = netip.MustParsePrefix("100.64.0.0/10")
	internalHostNames = map[string]struct{}{
		"localhost": {}, "postgres": {}, "redis": {}, "chat": {}, "identity": {},
		"realtime": {}, "agent-control": {}, "agent-runtime": {}, "llm-gateway": {},
		"sandbox": {}, "caddy": {}, "web": {},
	}
)

// IsPublicTargetIP checks whether ip is a routable global unicast address.
func IsPublicTargetIP(ip netip.Addr) bool {
	ip = ip.Unmap()
	if !ip.IsValid() || ip.IsUnspecified() || ip.IsLoopback() || ip.IsPrivate() ||
		ip.IsMulticast() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || cgnatPrefix.Contains(ip) {
		return false
	}
	return true
}

// ValidateMarketplaceEndpointURL validates a single webUrl or mcpUrl against origin isolation and network policy.
func ValidateMarketplaceEndpointURL(raw string, isMCP bool, appOrigin string, requirePublicHTTPS bool) error {
	raw = strings.TrimSpace(raw)
	if !ValidMarketplaceHTTPURL(raw) {
		if isMCP {
			return ErrInvalidMarketplaceMCPURL()
		}
		return ErrInvalidMarketplaceURL()
	}
	u, _ := url.Parse(raw)
	if isMCP && u.Fragment != "" {
		return ErrInvalidMarketplaceMCPURL()
	}
	if !isMCP && strings.TrimSpace(appOrigin) != "" {
		webOrig, ok1 := CanonicalOrigin(raw)
		hostOrig, ok2 := CanonicalOrigin(appOrigin)
		if ok1 && ok2 && webOrig == hostOrig {
			return ErrSameOriginMarketplaceURL()
		}
	}
	if requirePublicHTTPS {
		if !strings.EqualFold(u.Scheme, "https") {
			return ErrMarketplaceHTTPSRequired()
		}
		host := strings.ToLower(u.Hostname())
		if _, internal := internalHostNames[host]; internal ||
			strings.HasSuffix(host, ".localhost") ||
			strings.HasSuffix(host, ".local") ||
			strings.HasSuffix(host, ".internal") {
			return ErrForbiddenMarketplaceTarget()
		}
		if ip, err := netip.ParseAddr(host); err == nil {
			if !IsPublicTargetIP(ip) {
				return ErrForbiddenMarketplaceTarget()
			}
		} else if parsedIP := net.ParseIP(host); parsedIP != nil {
			return ErrForbiddenMarketplaceTarget()
		}
	}
	return nil
}

// SanitizeMCPToolSegment matches agent-runtime's tool segment normalization.
func SanitizeMCPToolSegment(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_' || c == '-' {
			b.WriteByte(c)
		} else {
			b.WriteByte('_')
		}
	}
	out := strings.Trim(b.String(), "_")
	if out == "" {
		return "tool"
	}
	return out
}

// ExposedMCPToolName formats mcp__<slug>__<tool>.
func ExposedMCPToolName(slug, remoteName string) string {
	return "mcp__" + SanitizeMCPToolSegment(slug) + "__" + SanitizeMCPToolSegment(remoteName)
}

// SignMarketplaceContextToken signs a standard HS256 JWT for the given claims.
func SignMarketplaceContextToken(secret string, claims MarketplaceContextClaims) (string, error) {
	headerJSON := `{"alg":"HS256","typ":"JWT"}`
	payloadBytes, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	unsigned := base64.RawURLEncoding.EncodeToString([]byte(headerJSON)) + "." +
		base64.RawURLEncoding.EncodeToString(payloadBytes)
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(unsigned))
	sig := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return unsigned + "." + sig, nil
}

// VerifyMarketplaceContextToken verifies an HS256 JWT and returns its claims.
func VerifyMarketplaceContextToken(secret, token string, now time.Time) (MarketplaceContextClaims, error) {
	var claims MarketplaceContextClaims
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return claims, Err(401, "invalid_token", "Malformed context token")
	}
	unsigned := parts[0] + "." + parts[1]
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(unsigned))
	expectedSig := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	if subtle.ConstantTimeCompare([]byte(expectedSig), []byte(parts[2])) != 1 {
		return claims, Err(401, "invalid_token", "Invalid context token signature")
	}
	payloadBytes, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return claims, Err(401, "invalid_token", "Invalid context token payload")
	}
	if err := json.Unmarshal(payloadBytes, &claims); err != nil {
		return claims, Err(401, "invalid_token", "Invalid context token claims")
	}
	if !now.IsZero() && claims.ExpiresAt <= now.Unix() {
		return claims, Err(401, "token_expired", "Context token has expired")
	}
	return claims, nil
}
