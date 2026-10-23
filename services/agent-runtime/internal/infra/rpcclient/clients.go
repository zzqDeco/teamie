// Package rpcclient implements outbound Kitex RPC and HTTP client adapters
// for Identity, Chat, AgentControl, ModelGateway, and Sandbox.
package rpcclient

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/cloudwego/kitex/client"
	wire "teamie/contracts/gen/agent/kitex_gen/agent"
	"teamie/contracts/gen/agent/kitex_gen/agent/agentcontrolservice"
	"teamie/contracts/gen/agent/kitex_gen/agent/modelgatewayservice"
	"teamie/contracts/gen/agent/kitex_gen/agent/sandboxservice"
	"teamie/contracts/gen/im/kitex_gen/im/identityservice"
	"teamie/pkg/rpcx"
	"teamie/services/agent-runtime/internal/domain"
)

func dialOptions(addr string, timeout time.Duration) []client.Option {
	return rpcx.ClientOptions(addr, timeout, time.Second)
}

// DialAgentControl creates a Kitex RPC client for teamie.agent-control.
func DialAgentControl(addr string) (agentcontrolservice.Client, error) {
	return agentcontrolservice.NewClient("teamie.agent-control", dialOptions(addr, 4*time.Second)...)
}

// DialModelGateway creates a Kitex RPC client for teamie.model-gateway.
func DialModelGateway(addr string) (modelgatewayservice.Client, error) {
	return modelgatewayservice.NewClient("teamie.model-gateway", dialOptions(addr, 4*time.Second)...)
}

// DialSandbox creates a Kitex RPC client for teamie.sandbox.
func DialSandbox(addr string) (sandboxservice.Client, error) {
	return sandboxservice.NewClient("teamie.sandbox", dialOptions(addr, 70*time.Second)...)
}

// DialIdentity creates a Kitex RPC client for teamie.identity.
func DialIdentity(addr string) (identityservice.Client, error) {
	return identityservice.NewClient("teamie.identity", dialOptions(addr, 4*time.Second)...)
}

// ControlAdapter adapts agentcontrolservice.Client to domain.ControlGateway.
type ControlAdapter struct {
	Client agentcontrolservice.Client
	Token  string
}

var _ domain.ControlGateway = (*ControlAdapter)(nil)

func toDomainProfile(a *wire.AgentDefinition) domain.AgentProfile {
	return domain.AgentProfile{
		ID:              a.Id,
		OwnerID:         a.OwnerId,
		DisplayName:     a.Name,
		RoleDescription: a.Description,
		WorkspaceIDs:    append([]string(nil), a.WorkspaceIds...),
		Enabled:         a.Enabled,
		Deleted:         a.DeletedAt != nil,
		Revision:        a.Revision,
	}
}

// GetAgentProfile fetches the current Agent profile from AgentControl.
func (a *ControlAdapter) GetAgentProfile(ctx context.Context, agentID string) (*domain.AgentProfile, error) {
	pc, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	r, err := a.Client.GetAgent(pc, &wire.AgentRequest{ServiceToken: a.Token, AgentId: agentID})
	if err != nil {
		return nil, err
	}
	if r == nil || r.Agent == nil {
		return nil, nil
	}
	p := toDomainProfile(r.Agent)
	return &p, nil
}

// ListAgentProfiles lists all Agent profiles from AgentControl.
func (a *ControlAdapter) ListAgentProfiles(ctx context.Context) ([]domain.AgentProfile, error) {
	r, err := a.Client.ListAgents(ctx, &wire.AgentListRequest{ServiceToken: a.Token})
	if err != nil {
		return nil, err
	}
	out := make([]domain.AgentProfile, 0, len(r.Agents))
	for _, item := range r.Agents {
		if item != nil {
			out = append(out, toDomainProfile(item))
		}
	}
	return out, nil
}

// ReportRuntimeState sends a runtime state heartbeat report to AgentControl.
func (a *ControlAdapter) ReportRuntimeState(ctx context.Context, agentID, state, lastError string, active bool) error {
	code := domain.PublicErrorCodeOptional(lastError)
	rep := &wire.RuntimeReport{
		ServiceToken: a.Token,
		AgentId:      agentID,
		RuntimeState: state,
		SessionId:    "agent:" + agentID,
	}
	if code != "" {
		rep.LastError = &code
	}
	if active {
		v := time.Now().UTC().Format(time.RFC3339Nano)
		rep.LastActiveAt = &v
	}
	_, err := a.Client.ReportRuntime(ctx, rep)
	return err
}

// Ping checks whether AgentControl is ready.
func (a *ControlAdapter) Ping(ctx context.Context) error {
	r, err := a.Client.Ping(ctx, &wire.PingRequest{})
	if err != nil {
		return err
	}
	if !r.Ready {
		return errors.New("control_unavailable")
	}
	return nil
}

// ModelGatewayAdapter adapts modelgatewayservice.Client to domain.ModelGateway.
type ModelGatewayAdapter struct {
	Client modelgatewayservice.Client
	Token  string
}

var _ domain.ModelGateway = (*ModelGatewayAdapter)(nil)

// Ping checks model gateway readiness, context window size, and active model config ID.
func (m *ModelGatewayAdapter) Ping(ctx context.Context) (ready bool, contextWindow int, configID string, err error) {
	if m == nil || m.Client == nil {
		return false, 0, "", errors.New("nil gateway client")
	}
	defer func() {
		if r := recover(); r != nil {
			ready = false
			contextWindow = 0
			configID = ""
			err = errors.New("gateway ping unimplemented")
		}
	}()
	token := m.Token
	resp, callErr := m.Client.Ping(ctx, &wire.PingRequest{ServiceToken: &token})
	if callErr != nil {
		return false, 0, "", callErr
	}
	isReady := resp != nil && resp.Ready && resp.GetConfigured() && resp.GetEnabled()
	win := 0
	if resp != nil && resp.ContextWindowTokens != nil {
		win = int(resp.GetContextWindowTokens())
	}
	cfgID := ""
	if resp != nil {
		cfgID = strings.TrimSpace(resp.GetModelConfigId())
	}
	return isReady, win, cfgID, nil
}

// CancelRun requests cancellation of an active model generation on the gateway.
func (m *ModelGatewayAdapter) CancelRun(ctx context.Context, agentID, executionID string) error {
	_, err := m.Client.Cancel(ctx, &wire.CancelRequest{ServiceToken: m.Token, AgentId: agentID, RunId: executionID})
	return err
}

// SandboxAdapter adapts sandboxservice.Client to domain.SandboxGateway.
type SandboxAdapter struct {
	Client sandboxservice.Client
	Token  string
}

var _ domain.SandboxGateway = (*SandboxAdapter)(nil)

// CancelRun requests cancellation of active sandbox execution for executionID.
func (s *SandboxAdapter) CancelRun(ctx context.Context, agentID, executionID string) error {
	_, err := s.Client.Cancel(ctx, &wire.CancelRequest{ServiceToken: s.Token, AgentId: agentID, RunId: executionID})
	return err
}
