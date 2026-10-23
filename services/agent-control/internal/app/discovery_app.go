package app

import (
	"context"

	"teamie/services/agent-control/internal/domain"
)

// Discover returns visible Agents in workspace for requester user.
func (s *Service) Discover(ctx context.Context, workspace, user string) ([]domain.Agent, error) {
	ids, err := s.IM.GetParticipants(ctx, workspace, user)
	if err != nil {
		return nil, err
	}
	if !s.flagEnabled("agents.discover") {
		return []domain.Agent{}, nil
	}
	return s.Agents.ListWorkspaceAgents(ctx, workspace, ids)
}

// Decorate resolves owner display names via IM and normalizes disabled runtime state.
func (s *Service) Decorate(ctx context.Context, agents []domain.Agent) ([]domain.Agent, error) {
	ids := make([]string, 0, len(agents))
	for _, a := range agents {
		ids = append(ids, a.OwnerID)
	}
	profiles, err := s.IM.GetUsers(ctx, ids)
	if err != nil {
		return nil, err
	}
	globalEnabled := s.flagEnabled("agents.enabled")
	for n := range agents {
		if p, ok := profiles[agents[n].OwnerID]; ok {
			agents[n].OwnerName = p.DisplayName
		}
		if !agents[n].Enabled || !globalEnabled || agents[n].DeletedAt != nil {
			agents[n].RuntimeState = "disabled"
		}
	}
	return agents, nil
}

// ListAll returns all Agents (including disabled/deleted) for internal runtime consumption.
func (s *Service) ListAll(ctx context.Context) ([]domain.Agent, error) {
	return s.Agents.ListAll(ctx)
}

// GetByID returns a single Agent by ID for internal runtime consumption.
func (s *Service) GetByID(ctx context.Context, id string) (domain.Agent, bool, error) {
	return s.Agents.GetByID(ctx, id)
}
