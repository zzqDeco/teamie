package domain

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Agent is the authoritative domain entity for an owned Agent definition and
// its internal reconciliation/runtime state.
type Agent struct {
	ID                        string     `json:"id"`
	PrincipalID               string     `json:"principalId"`
	OwnerID                   string     `json:"ownerId"`
	OwnerName                 string     `json:"ownerName"`
	Name                      string     `json:"name"`
	Description               string     `json:"description"`
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

// Changes represents a partial or complete owner mutation to an Agent profile.
type Changes struct {
	Name         *string   `json:"name"`
	Description  *string   `json:"description"`
	WorkspaceIDs *[]string `json:"workspaceIds"`
	Enabled      *bool     `json:"enabled"`
}

// PublicAgent is the 10-field browser contract. Internal runtime state, session
// IDs, revisions, and unrequested workspace memberships never enter PublicAgent.
type PublicAgent struct {
	ID           string    `json:"id"`
	PrincipalID  string    `json:"principalId"`
	OwnerID      string    `json:"ownerId"`
	OwnerName    string    `json:"ownerName"`
	Name         string    `json:"name"`
	Description  string    `json:"description"`
	WorkspaceIDs []string  `json:"workspaceIds"`
	Enabled      bool      `json:"enabled"`
	CreatedAt    time.Time `json:"createdAt"`
	UpdatedAt    time.Time `json:"updatedAt"`
}

// PublicProfile projects an Agent into its safe 10-field public representation.
func PublicProfile(a Agent) PublicAgent {
	return PublicAgent{
		ID:           a.ID,
		PrincipalID:  a.PrincipalID,
		OwnerID:      a.OwnerID,
		OwnerName:    a.OwnerName,
		Name:         a.Name,
		Description:  a.Description,
		WorkspaceIDs: append([]string{}, a.WorkspaceIDs...),
		Enabled:      a.Enabled,
		CreatedAt:    a.CreatedAt,
		UpdatedAt:    a.UpdatedAt,
	}
}

// PublicProfiles projects a list of Agents for owner listing (workspaceID == "")
// or workspace discovery (workspaceID != "", stripping all other workspace IDs).
func PublicProfiles(agents []Agent, workspaceID string) []PublicAgent {
	out := make([]PublicAgent, len(agents))
	for n, a := range agents {
		out[n] = PublicProfile(a)
		if workspaceID != "" {
			// Discovery proves membership in this workspace only. Other selected
			// workspaces may be private to the owner or their other teams.
			out[n].WorkspaceIDs = []string{workspaceID}
		}
	}
	return out
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

// Normalized deduplicates, filters empty strings, and sorts workspace/user IDs.
func Normalized(ids []string) []string {
	m := map[string]bool{}
	for _, id := range ids {
		if id != "" {
			m[id] = true
		}
	}
	out := []string{}
	for id := range m {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// Bounded reports whether the trimmed rune length of v is within [min, max].
func Bounded(v string, min, max int) bool {
	n := len([]rune(strings.TrimSpace(v)))
	return n >= min && n <= max
}

// Same reports whether two ID slices contain the exact same normalized set.
func Same(a, b []string) bool {
	return strings.Join(Normalized(a), "\x00") == strings.Join(Normalized(b), "\x00")
}

// Intersect filters selected IDs to those present in every provided set.
func Intersect(selected []string, sets ...[]string) []string {
	out := Normalized(selected)
	for _, set := range sets {
		have := map[string]bool{}
		for _, id := range set {
			have[id] = true
		}
		next := []string{}
		for _, id := range out {
			if have[id] {
				next = append(next, id)
			}
		}
		out = next
	}
	return out
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
	if in.WorkspaceIDs != nil {
		if len(*in.WorkspaceIDs) > 100 {
			return ErrInvalidWorkspaces()
		}
		*in.WorkspaceIDs = Normalized(*in.WorkspaceIDs)
	}
	return nil
}

// ValidateWorkspaceSelection ensures all requested workspaces belong to allowed.
func ValidateWorkspaceSelection(requested, allowed []string) error {
	set := map[string]bool{}
	for _, id := range allowed {
		set[id] = true
	}
	for _, id := range requested {
		if !set[id] {
			return ErrForbidden("Only current workspace memberships may be selected")
		}
	}
	return nil
}
