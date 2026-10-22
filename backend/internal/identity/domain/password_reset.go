package domain

import (
	"encoding/base64"
	"encoding/hex"
	"strings"
	"time"
	"unicode/utf8"
)

// PasswordReset represents an issued password recovery capability.
type PasswordReset struct {
	ID        string    `json:"id"`
	CreatedAt time.Time `json:"createdAt"`
	ExpiresAt time.Time `json:"expiresAt"`
}

// PasswordResetAccount represents the masked target account metadata for a reset link.
type PasswordResetAccount struct {
	ID          string `json:"id"`
	DisplayName string `json:"displayName"`
	EmailMasked string `json:"emailMasked"`
}

// PasswordResetInspection represents the result of inspecting an active reset token.
type PasswordResetInspection struct {
	Account   PasswordResetAccount `json:"account"`
	ExpiresAt time.Time            `json:"expiresAt"`
}

// AdminUserPage represents a paginated directory of human accounts for administrators.
type AdminUserPage struct {
	Users  []User `json:"users"`
	Cursor string `json:"cursor,omitempty"`
}

// ValidateResetTokenFormat checks that token is a 64-character hex string.
func ValidateResetTokenFormat(token string) error {
	if len(token) != 64 {
		return ErrInvalidResetLink()
	}
	if _, err := hex.DecodeString(token); err != nil {
		return ErrInvalidResetLink()
	}
	return nil
}

// MaskEmail masks the local part of an email address while preserving the first rune and domain.
func MaskEmail(email string) string {
	local, domainPart, ok := strings.Cut(email, "@")
	if !ok || local == "" {
		return "***"
	}
	first, _ := utf8.DecodeRuneInString(local)
	return string(first) + "***@" + domainPart
}

// ValidateAdminUsersQuery validates and normalizes pagination and search parameters
// for the administrator user directory.
func ValidateAdminUsersQuery(cursor string, limit int, query string) (afterID string, normalizedLimit int, normalizedQuery string, likePattern string, err error) {
	if limit == 0 {
		limit = 50
	}
	if limit < 1 || limit > 100 || len(query) > 800 || !utf8.ValidString(query) || strings.ContainsRune(query, '\x00') || utf8.RuneCountInString(query) > 200 {
		return "", 0, "", "", ErrInvalidAdminUsersRequest()
	}
	if cursor != "" {
		if len(cursor) > 172 {
			return "", 0, "", "", ErrInvalidUsersCursor()
		}
		decoded, decErr := base64.RawURLEncoding.DecodeString(cursor)
		if decErr != nil || len(decoded) == 0 || len(decoded) > 128 || !utf8.Valid(decoded) || strings.ContainsRune(string(decoded), '\x00') {
			return "", 0, "", "", ErrInvalidUsersCursor()
		}
		afterID = string(decoded)
	}
	normalizedQuery = strings.TrimSpace(query)
	likePattern = "%" + strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(normalizedQuery) + "%"
	return afterID, limit, normalizedQuery, likePattern, nil
}

// EncodeUsersCursor encodes the last user ID on a full page as a URL-safe base64 cursor.
func EncodeUsersCursor(userID string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(userID))
}
