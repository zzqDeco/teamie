package domain

import (
	"sort"
	"strings"
)

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
