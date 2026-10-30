// Package domain contains pure Go entities, value objects, business invariants,
// domain errors, and outbound repository/RPC port interfaces for the Marketplace
// bounded context. It has zero dependencies on HTTP, RPC, or SQL frameworks.
package domain

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"
)

// User represents an authenticated Teamie human or Agent principal.
type User struct {
	ID           string    `json:"id"`
	Email        string    `json:"email"`
	DisplayName  string    `json:"displayName"`
	Kind         string    `json:"kind"`
	PlatformRole string    `json:"platformRole"`
	CreatedAt    time.Time `json:"createdAt"`
}

// WorkspaceScope describes a participant's membership and owner metadata for a workspace.
type WorkspaceScope struct {
	Allowed bool
	OwnerID string
	Role    string
}

// ConversationScope describes a participant's access, parent workspace owner, and conversation type.
type ConversationScope struct {
	Allowed          bool
	WorkspaceID      string
	WorkspaceOwnerID string
	ConversationType string
}

// NewID generates a random RFC 4122 v4 UUID string.
func NewID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[:4], b[4:6], b[6:8], b[8:10], b[10:])
}

// RandomHex generates n random bytes encoded as 2n lowercase hex characters.
func RandomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}
