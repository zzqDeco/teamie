// Package rpc implements the inbound Kitex Thrift RPC adapter for the Model Gateway.
package rpc

import (
	"context"
	"errors"

	"teamie/contracts/gen/agent/kitex_gen/agent"
	"teamie/services/llm-gateway/internal/app"
	"teamie/services/llm-gateway/internal/domain"
)

var _ agent.ModelGatewayService = (*Server)(nil)

// Server adapts Kitex RPC requests to the Model Gateway application service.
type Server struct {
	App *app.Service
}

// New creates a new Kitex RPC Server backed by the gateway application service.
func New(svc *app.Service) *Server {
	return &Server{App: svc}
}

func ptr[T any](v T) *T { return &v }

func failedChatResponse(err error) *agent.GatewayChatResponse {
	var de *domain.Error
	if errors.As(err, &de) {
		return &agent.GatewayChatResponse{
			Ok:           false,
			ErrorCode:    &de.Code,
			ErrorMessage: &de.Message,
			StatusCode:   ptr(int32(de.Status)),
		}
	}
	code := "gateway_unavailable"
	msg := "Model gateway is temporarily unavailable"
	return &agent.GatewayChatResponse{
		Ok:           false,
		ErrorCode:    &code,
		ErrorMessage: &msg,
		StatusCode:   ptr(int32(500)),
	}
}

// Ping handles the Kitex Ping RPC.
func (s *Server) Ping(ctx context.Context, r *agent.PingRequest) (*agent.PingResponse, error) {
	if r == nil {
		return &agent.PingResponse{Ready: false}, nil
	}
	st := s.App.Ping(ctx, r.GetServiceToken(), r.GetModelId())
	if !st.Ready && st.Configured == nil {
		return &agent.PingResponse{Ready: false}, nil
	}
	return &agent.PingResponse{
		Ready:               st.Ready,
		Configured:          st.Configured,
		Enabled:             st.Enabled,
		ContextWindowTokens: st.ContextWindowTokens,
		ModelConfigId:       st.ModelConfigID,
	}, nil
}

// Chat handles the Kitex Chat RPC (rejected in favor of private HTTP transport).
func (s *Server) Chat(_ context.Context, r *agent.GatewayChatRequest) (*agent.GatewayChatResponse, error) {
	if r == nil {
		return failedChatResponse(domain.Err(401, "unauthorized", "Invalid service credential")), nil
	}
	return failedChatResponse(s.App.ValidateUnaryChat(r.ServiceToken, r.OwnerId, r.AgentId, r.SessionId, r.RunId)), nil
}

// Cancel handles the Kitex Cancel RPC.
func (s *Server) Cancel(_ context.Context, r *agent.CancelRequest) (*agent.Ack, error) {
	if r == nil {
		return &agent.Ack{Ok: false}, nil
	}
	return &agent.Ack{Ok: s.App.Cancel(r.ServiceToken, r.AgentId, r.RunId)}, nil
}
