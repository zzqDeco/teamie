package control

import (
	"context"

	rpcadapter "teamie/agent-control/internal/control/adapter/rpc"
	"teamie/agent-protocol/kitex_gen/agent"
)

// RPC delegates Kitex AgentControlService RPC calls to the inbound RPC adapter.
type RPC struct {
	Store *Store
}

func (s *Store) definition(a Agent) *agent.AgentDefinition {
	return rpcadapter.ToDefinition(a, s.Flags.Enabled("agents.enabled"))
}

func (h *RPC) server() *rpcadapter.Server {
	return &rpcadapter.Server{App: h.Store.App()}
}

func (h *RPC) ListAgents(ctx context.Context, r *agent.AgentListRequest) (*agent.AgentListResponse, error) {
	return h.server().ListAgents(ctx, r)
}

func (h *RPC) GetAgent(ctx context.Context, r *agent.AgentRequest) (*agent.AgentResponse, error) {
	return h.server().GetAgent(ctx, r)
}

func (h *RPC) ReportRuntime(ctx context.Context, r *agent.RuntimeReport) (*agent.Ack, error) {
	return h.server().ReportRuntime(ctx, r)
}

func (h *RPC) Ping(ctx context.Context, r *agent.PingRequest) (*agent.PingResponse, error) {
	return h.server().Ping(ctx, r)
}
