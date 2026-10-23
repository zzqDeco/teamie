package control

import "teamie/agent-control/internal/control/domain"

type publicAgent = domain.PublicAgent

func publicProfile(a Agent) publicAgent {
	return domain.PublicProfile(a)
}

func publicProfiles(agents []Agent, workspaceID string) []publicAgent {
	return domain.PublicProfiles(agents, workspaceID)
}
