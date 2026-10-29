package domain

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Agent is the authoritative domain entity for an owned Agent definition and
// its internal reconciliation/runtime state.
type Agent struct {
	CreationHash              string     `json:"-"`
	ID                        string     `json:"id"`
	PrincipalID               string     `json:"principalId"`
	OwnerID                   string     `json:"ownerId"`
	OwnerName                 string     `json:"ownerName"`
	Name                      string     `json:"name"`
	Description               string     `json:"description"`
	ModelID                   string     `json:"modelId,omitempty"`
	WorkspaceIDs              []string   `json:"workspaceIds"`
	Enabled                   bool       `json:"enabled"`
	Revision                  int64      `json:"revision"`
	CreatedAt                 time.Time  `json:"createdAt"`
	UpdatedAt                 time.Time  `json:"updatedAt"`
	DeletedAt                 *time.Time `json:"deletedAt,omitempty"`
	RuntimeState              string     `json:"runtimeState"`
	SessionID                 string     `json:"sessionId"`
	LastActiveAt              *time.Time `json:"lastActiveAt,omitempty"`
	LastError                 string     `json:"lastError,omitempty"`
	MembershipRevision        int64      `json:"-"`
	AppliedRevision           int64      `json:"-"`
	AppliedMembershipRevision int64      `json:"-"`
	PolicyEnabled             bool       `json:"-"`
}

// EffectiveEnabled reports whether the Agent is active in runtime definitions
// after owner, policy, feature-flag, deletion, and revision checks.
func (a Agent) EffectiveEnabled(globalEnabled bool) bool {
	return a.Enabled && a.PolicyEnabled && globalEnabled && a.DeletedAt == nil && a.AppliedRevision == a.Revision
}

// CreationHash binds an idempotency key to the original normalized public intent.
// It deliberately excludes mutable reconciliation state and profile revisions.
func CreationHash(a Agent) string {
	raw, _ := json.Marshal(struct {
		Name         string   `json:"name"`
		Description  string   `json:"description"`
		ModelID      string   `json:"modelId"`
		WorkspaceIDs []string `json:"workspaceIds"`
		Enabled      bool     `json:"enabled"`
	}{a.Name, a.Description, a.ModelID, Normalized(a.WorkspaceIDs), a.Enabled})
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// Changes represents a partial or complete owner mutation to an Agent profile.
type Changes struct {
	Name           *string   `json:"name"`
	Description    *string   `json:"description"`
	ModelID        *string   `json:"modelId"`
	WorkspaceIDs   *[]string `json:"workspaceIds"`
	Enabled        *bool     `json:"enabled"`
	IdempotencyKey string    `json:"-"`
}

// PublicAgent is the browser contract. Internal runtime state, session
// IDs, revisions, and unrequested workspace memberships never enter PublicAgent.
type PublicAgent struct {
	ID           string    `json:"id"`
	PrincipalID  string    `json:"principalId"`
	OwnerID      string    `json:"ownerId"`
	OwnerName    string    `json:"ownerName"`
	Name         string    `json:"name"`
	Description  string    `json:"description"`
	ModelID      string    `json:"modelId,omitempty"`
	WorkspaceIDs []string  `json:"workspaceIds"`
	Enabled      bool      `json:"enabled"`
	CreatedAt    time.Time `json:"createdAt"`
	UpdatedAt    time.Time `json:"updatedAt"`
}

// PublicProfile projects an Agent into its safe public representation.
func PublicProfile(a Agent) PublicAgent {
	return PublicAgent{
		ID:           a.ID,
		PrincipalID:  a.PrincipalID,
		OwnerID:      a.OwnerID,
		OwnerName:    a.OwnerName,
		Name:         a.Name,
		Description:  a.Description,
		ModelID:      a.ModelID,
		WorkspaceIDs: append([]string{}, a.WorkspaceIDs...),
		Enabled:      a.Enabled,
		CreatedAt:    a.CreatedAt,
		UpdatedAt:    a.UpdatedAt,
	}
}

// RuntimeStatus captures a runtime health/activity report from agent-runtime.
type RuntimeStatus struct {
	AgentID      string
	RuntimeState string
	SessionID    string
	LastActiveAt *time.Time
	LastError    string
}

var validRuntimeStates = map[string]bool{
	"provisioning":          true,
	"waiting_configuration": true,
	"idle":                  true,
	"running":               true,
	"disabled":              true,
	"error":                 true,
}

// ValidRuntimeReport checks whether a runtime state report is structurally valid.
func ValidRuntimeReport(agentID, state, sessionID string) bool {
	return validRuntimeStates[state] && sessionID == "agent:"+agentID
}

// SafeRuntimeError normalizes an untrusted runtime error code to the allowlist.
func SafeRuntimeError(raw string) string {
	switch raw {
	case "":
		return ""
	case "model_unconfigured", "gateway_unavailable", "im_unavailable", "sandbox_unavailable", "runtime_interrupted", "tool_failed", "execution_unknown":
		return raw
	default:
		return "runtime_error"
	}
}

// UserProfile represents an authenticated or looked-up IM user identity.
type UserProfile struct {
	ID           string
	DisplayName  string
	Kind         string
	PlatformRole string
}

// ValidKey performs a constant-time service credential check.
func ValidKey(expected, provided string) bool {
	return expected != "" && provided != "" && subtle.ConstantTimeCompare([]byte(expected), []byte(provided)) == 1
}

// NewID generates a random UUIDv4 identifier.
func NewID() string {
	b := make([]byte, 16)
	if _, e := rand.Read(b); e != nil {
		panic(e)
	}
	b[6] = (b[6] & 15) | 64
	b[8] = (b[8] & 63) | 128
	raw := hex.EncodeToString(b)
	return fmt.Sprintf("%s-%s-%s-%s-%s", raw[:8], raw[8:12], raw[12:16], raw[16:20], raw[20:])
}

// DeterministicID derives a stable caller-scoped UUIDv4-formatted identifier from owner and key.
func DeterministicID(owner, key string) string {
	sum := sha256.Sum256([]byte("teamie:agent-create:v1\x00" + owner + "\x00" + key))
	b := sum[:16]
	b[6] = (b[6] & 15) | 64
	b[8] = (b[8] & 63) | 128
	raw := hex.EncodeToString(b)
	return fmt.Sprintf("%s-%s-%s-%s-%s", raw[:8], raw[8:12], raw[12:16], raw[16:20], raw[20:])
}

// Bounded reports whether the trimmed rune length of v is within [min, max].
func Bounded(v string, min, max int) bool {
	n := len([]rune(strings.TrimSpace(v)))
	return n >= min && n <= max
}

// ValidModelID checks whether an optional model profile ID is empty (default) or a safe identifier ([a-zA-Z0-9._-], 1..64 chars).
func ValidModelID(id string) bool {
	if id == "" {
		return true
	}
	if len(id) > 64 {
		return false
	}
	for i := 0; i < len(id); i++ {
		ch := id[i]
		isAlphaNum := (ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') || (ch >= '0' && ch <= '9')
		if !isAlphaNum && ch != '-' && ch != '_' && ch != '.' {
			return false
		}
	}
	return id != "." && id != ".."
}

// ValidateFields validates and normalizes the non-RPC fields of Changes.
func ValidateFields(in *Changes, create bool) error {
	if create && (in.Name == nil || in.WorkspaceIDs == nil) {
		return ErrInvalidRequest("Name and workspace IDs are required")
	}
	if in.Name != nil {
		*in.Name = strings.TrimSpace(*in.Name)
		if !Bounded(*in.Name, 1, 80) {
			return ErrInvalidName()
		}
	}
	if in.Description != nil && len([]rune(*in.Description)) > 1000 {
		return ErrInvalidDescription()
	}
	if in.ModelID != nil {
		*in.ModelID = strings.TrimSpace(*in.ModelID)
		if !ValidModelID(*in.ModelID) {
			return ErrInvalidRequest("Invalid model ID")
		}
	}
	if in.WorkspaceIDs != nil {
		if len(*in.WorkspaceIDs) > 100 {
			return ErrInvalidWorkspaces()
		}
		*in.WorkspaceIDs = Normalized(*in.WorkspaceIDs)
	}
	return nil
}
