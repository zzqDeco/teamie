// Package domain contains pure Push domain entities, RFC 8291/8292 value validation,
// SSRF endpoint policy, coded errors, and outbound repository/transport ports.
package domain

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"
)

// User represents an authenticated principal on the Push service boundary.
type User struct {
	ID           string
	Email        string
	DisplayName  string
	Kind         string
	PlatformRole string
	CreatedAt    time.Time
}

// IsHuman reports whether the principal is an interactive human browser account.
func (u User) IsHuman() bool {
	return u.Kind == "human"
}

// NewID generates a random UUIDv4 string.
func NewID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[:4], b[4:6], b[6:8], b[8:10], b[10:])
}

// HashString returns the lowercase SHA-256 hex digest of v.
func HashString(v string) string {
	sum := sha256.Sum256([]byte(v))
	return hex.EncodeToString(sum[:])
}
