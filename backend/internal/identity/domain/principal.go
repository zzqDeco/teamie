package domain

import (
	"crypto/subtle"
	"net/mail"
	"strings"
	"time"
)

// DefaultBootstrapInviteCode is the documented default bootstrap invitation code.
const DefaultBootstrapInviteCode = "teamie-bootstrap"

// User represents an authenticated human or machine principal in Teamie.
type User struct {
	ID           string    `json:"id"`
	Email        string    `json:"email"`
	DisplayName  string    `json:"displayName"`
	Kind         string    `json:"kind"`
	PlatformRole string    `json:"platformRole"`
	CreatedAt    time.Time `json:"createdAt"`
}

// Session represents an authenticated browser or service principal session.
type Session struct {
	User             User   `json:"user"`
	CSRFToken        string `json:"csrfToken"`
	ServicePrincipal bool   `json:"-"`
}

// BoundedString reports whether strings.TrimSpace(v) has between min and max runes.
func BoundedString(v string, min, max int) bool {
	n := len([]rune(strings.TrimSpace(v)))
	return n >= min && n <= max
}

// NormalizeEmail normalizes and validates an email address according to identity policy.
func NormalizeEmail(v string) (string, error) {
	v = strings.ToLower(strings.TrimSpace(v))
	a, err := mail.ParseAddress(v)
	if err != nil || a.Address != v || len(v) > 254 {
		return "", ErrInvalidEmail()
	}
	return v, nil
}

// ValidateDisplayName trims and validates a user display name (1 to 80 runes).
func ValidateDisplayName(v string) (string, error) {
	v = strings.TrimSpace(v)
	if !BoundedString(v, 1, 80) {
		return "", ErrInvalidName()
	}
	return v, nil
}

// ValidatePrincipalLifecycle checks invariants on machine principal provisioning requests.
func ValidatePrincipalLifecycle(id, name, kind string, enabled, deleted bool, revision int64) error {
	if !BoundedString(id, 1, 128) || !BoundedString(name, 1, 80) || kind != "agent" || revision < 1 || (enabled && deleted) {
		return ErrInvalidPrincipalLifecycle()
	}
	return nil
}

// ValidServiceKey performs a constant-time comparison of non-empty service credentials.
func ValidServiceKey(expected, provided string) bool {
	return expected != "" && provided != "" && subtle.ConstantTimeCompare([]byte(expected), []byte(provided)) == 1
}
