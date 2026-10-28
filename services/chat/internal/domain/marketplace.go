package domain

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"net/url"
	"strings"
	"time"
)

const MarketplaceSessionTTL = time.Hour

// MarketplaceApp represents a platform-registered Web + MCP application.
type MarketplaceApp struct {
	ID            string    `json:"id"`
	Slug          string    `json:"slug"`
	Name          string    `json:"name"`
	Description   string    `json:"description"`
	WebURL        string    `json:"webUrl"`
	MCPURL        string    `json:"mcpUrl"`
	SigningSecret string    `json:"-"`
	Enabled       bool      `json:"enabled"`
	CreatedAt     time.Time `json:"createdAt"`
	UpdatedAt     time.Time `json:"updatedAt"`
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
	if len(raw) == 0 || len(raw) > 512 {
		return false
	}
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return false
	}
	if u.Host == "" || u.User != nil {
		return false
	}
	return true
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
