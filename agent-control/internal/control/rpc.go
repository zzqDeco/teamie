package control

import (
	"context"
	"fmt"
	"teamie/agent-protocol/kitex_gen/agent"
	"time"
)

type RPC struct{ Store *Store }

func (s *Store) definition(a Agent) *agent.AgentDefinition {
	d := &agent.AgentDefinition{Id: a.ID, OwnerId: a.OwnerID, Name: a.Name, Description: a.Description, WorkspaceIds: a.WorkspaceIDs, Enabled: a.Enabled && a.PolicyEnabled && s.Flags.Enabled("agents.enabled") && a.DeletedAt == nil && a.AppliedRevision == a.Revision, Revision: a.Revision, CreatedAt: a.CreatedAt.Format(time.RFC3339Nano), UpdatedAt: a.UpdatedAt.Format(time.RFC3339Nano)}
	if a.DeletedAt != nil {
		v := a.DeletedAt.Format(time.RFC3339Nano)
		d.DeletedAt = &v
	}
	return d
}
func (h *RPC) authorized(token string) error {
	if !validKey(h.Store.RuntimeToken, token) {
		return fmt.Errorf("service authentication required")
	}
	return nil
}
func (h *RPC) ListAgents(ctx context.Context, r *agent.AgentListRequest) (*agent.AgentListResponse, error) {
	if e := h.authorized(r.ServiceToken); e != nil {
		return nil, e
	}
	all, e := h.Store.list(ctx, "")
	if e != nil {
		return nil, e
	}
	out := &agent.AgentListResponse{Agents: []*agent.AgentDefinition{}}
	for _, a := range all {
		out.Agents = append(out.Agents, h.Store.definition(a))
	}
	return out, nil
}
func (h *RPC) GetAgent(ctx context.Context, r *agent.AgentRequest) (*agent.AgentResponse, error) {
	if e := h.authorized(r.ServiceToken); e != nil {
		return nil, e
	}
	all, e := h.Store.list(ctx, "WHERE id=$1", r.AgentId)
	if e != nil {
		return nil, e
	}
	out := &agent.AgentResponse{}
	if len(all) > 0 {
		out.Agent = h.Store.definition(all[0])
	}
	return out, nil
}
func safeError(raw string) string {
	switch raw {
	case "":
		return ""
	case "model_unconfigured", "gateway_unavailable", "im_unavailable", "sandbox_unavailable", "runtime_interrupted", "tool_failed", "execution_unknown":
		return raw
	default:
		return "runtime_error"
	}
}
func (h *RPC) ReportRuntime(ctx context.Context, r *agent.RuntimeReport) (*agent.Ack, error) {
	if e := h.authorized(r.ServiceToken); e != nil {
		return nil, e
	}
	valid := map[string]bool{"provisioning": true, "waiting_configuration": true, "idle": true, "running": true, "disabled": true, "error": true}
	if !valid[r.RuntimeState] || r.SessionId != "agent:"+r.AgentId {
		return &agent.Ack{Ok: false}, nil
	}
	var active *time.Time
	if r.LastActiveAt != nil {
		v, e := time.Parse(time.RFC3339Nano, *r.LastActiveAt)
		if e != nil {
			return &agent.Ack{Ok: false}, nil
		}
		v = v.UTC()
		active = &v
	}
	tag, e := h.Store.DB.Exec(ctx, "UPDATE agents SET runtime_state=CASE WHEN NOT enabled OR NOT policy_enabled OR deleted_at IS NOT NULL THEN 'disabled' ELSE $2 END,last_active_at=COALESCE($3,last_active_at),last_error=$4 WHERE id=$1", r.AgentId, r.RuntimeState, active, safeError(r.GetLastError()))
	return &agent.Ack{Ok: e == nil && tag.RowsAffected() > 0}, e
}
func (h *RPC) Ping(ctx context.Context, r *agent.PingRequest) (*agent.PingResponse, error) {
	return &agent.PingResponse{Ready: h.Store.DB.Ping(ctx) == nil}, nil
}
