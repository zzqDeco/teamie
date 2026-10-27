// Package rpc implements the inbound Kitex AgentControlService RPC adapter.
package rpc

import (
	"context"
	"fmt"
	"time"

	"teamie/services/agent-control/internal/app"
	"teamie/services/agent-control/internal/domain"
	"teamie/contracts/gen/agent/kitex_gen/agent"
)

// Server adapts the Agent Control application service to the generated Kitex AgentControlService interface.
type Server struct {
	App *app.Service
}

var _ agent.AgentControlService = (*Server)(nil)

// ToDefinition converts a domain.Agent into a wire *agent.AgentDefinition.
func ToDefinition(a domain.Agent, globalEnabled bool) *agent.AgentDefinition {
	d := &agent.AgentDefinition{
		Id:           a.ID,
		OwnerId:      a.OwnerID,
		Name:         a.Name,
		Description:  a.Description,
		WorkspaceIds: a.WorkspaceIDs,
		Enabled:      a.EffectiveEnabled(globalEnabled),
		Revision:     a.Revision,
		CreatedAt:    a.CreatedAt.Format(time.RFC3339Nano),
		UpdatedAt:    a.UpdatedAt.Format(time.RFC3339Nano),
	}
	if a.DeletedAt != nil {
		v := a.DeletedAt.Format(time.RFC3339Nano)
		d.DeletedAt = &v
	}
	if a.ModelID != "" {
		v := a.ModelID
		d.ModelId = &v
	}
	return d
}

func (s *Server) authorized(token string) error {
	if s.App == nil || !domain.ValidKey(s.App.RuntimeToken, token) {
		return fmt.Errorf("service authentication required")
	}
	return nil
}

func (s *Server) ListAgents(ctx context.Context, r *agent.AgentListRequest) (*agent.AgentListResponse, error) {
	if err := s.authorized(r.ServiceToken); err != nil {
		return nil, err
	}
	all, err := s.App.ListAll(ctx)
	if err != nil {
		return nil, err
	}
	globalEnabled := s.App.GlobalEnabled()
	out := &agent.AgentListResponse{Agents: make([]*agent.AgentDefinition, 0, len(all))}
	for _, a := range all {
		out.Agents = append(out.Agents, ToDefinition(a, globalEnabled))
	}
	return out, nil
}

func (s *Server) GetAgent(ctx context.Context, r *agent.AgentRequest) (*agent.AgentResponse, error) {
	if err := s.authorized(r.ServiceToken); err != nil {
		return nil, err
	}
	a, found, err := s.App.GetByID(ctx, r.AgentId)
	if err != nil {
		return nil, err
	}
	out := &agent.AgentResponse{}
	if found {
		out.Agent = ToDefinition(a, s.App.GlobalEnabled())
	}
	return out, nil
}

func (s *Server) ReportRuntime(ctx context.Context, r *agent.RuntimeReport) (*agent.Ack, error) {
	if err := s.authorized(r.ServiceToken); err != nil {
		return nil, err
	}
	var active *time.Time
	if r.LastActiveAt != nil {
		v, err := time.Parse(time.RFC3339Nano, *r.LastActiveAt)
		if err != nil {
			return &agent.Ack{Ok: false}, nil
		}
		v = v.UTC()
		active = &v
	}
	ok, err := s.App.ReportRuntime(ctx, domain.RuntimeStatus{
		AgentID:      r.AgentId,
		RuntimeState: r.RuntimeState,
		SessionID:    r.SessionId,
		LastActiveAt: active,
		LastError:    r.GetLastError(),
	})
	return &agent.Ack{Ok: ok}, err
}

func (s *Server) Ping(ctx context.Context, _ *agent.PingRequest) (*agent.PingResponse, error) {
	return &agent.PingResponse{Ready: s.App != nil && s.App.Ping(ctx) == nil}, nil
}
