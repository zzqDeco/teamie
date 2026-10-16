package control

import "time"

// publicAgent is the complete browser contract. Runtime state stays in Agent
// and the private RPC definitions, never in owner or workspace responses.
type publicAgent struct {
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

func publicProfile(a Agent) publicAgent {
	return publicAgent{
		ID: a.ID, PrincipalID: a.PrincipalID, OwnerID: a.OwnerID,
		OwnerName: a.OwnerName, Name: a.Name, Description: a.Description,
		WorkspaceIDs: append([]string{}, a.WorkspaceIDs...), Enabled: a.Enabled,
		CreatedAt: a.CreatedAt, UpdatedAt: a.UpdatedAt,
	}
}

func publicProfiles(agents []Agent, workspaceID string) []publicAgent {
	out := make([]publicAgent, len(agents))
	for n, a := range agents {
		out[n] = publicProfile(a)
		if workspaceID != "" {
			// Discovery proves membership in this workspace only. Other selected
			// workspaces may be private to the owner or their other teams.
			out[n].WorkspaceIDs = []string{workspaceID}
		}
	}
	return out
}
