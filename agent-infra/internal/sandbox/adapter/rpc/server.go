// Package rpc implements the inbound Kitex Thrift RPC adapter for the Sandbox bounded context.
package rpc

import (
	"context"

	"teamie/agent-infra/internal/sandbox/app"
	"teamie/agent-protocol/kitex_gen/agent"
)

var _ agent.SandboxService = (*Server)(nil)

// Handler defines the full Sandbox RPC contract delegated by Server.
type Handler interface {
	agent.SandboxService
}

// Server adapts Kitex RPC calls to the Sandbox service implementation.
type Server struct {
	App     *app.Service
	Handler Handler
}

// New creates a new Kitex RPC Server for the Sandbox service.
func New(appSvc *app.Service, handler Handler) *Server {
	return &Server{App: appSvc, Handler: handler}
}

func (s *Server) Ping(ctx context.Context, r *agent.PingRequest) (*agent.PingResponse, error) {
	if s.Handler != nil {
		return s.Handler.Ping(ctx, r)
	}
	ready := r != nil && s.App != nil && s.App.Authorize(r.GetServiceToken()) && s.App.Ready(ctx) == nil
	return &agent.PingResponse{Ready: ready}, nil
}

func (s *Server) Exec(ctx context.Context, r *agent.SandboxExecRequest) (*agent.SandboxExecResponse, error) {
	return s.Handler.Exec(ctx, r)
}

func (s *Server) Workspace(ctx context.Context, r *agent.SandboxWorkspaceRequest) (*agent.SandboxWorkspaceResponse, error) {
	return s.Handler.Workspace(ctx, r)
}

func (s *Server) ImportFile(ctx context.Context, r *agent.SandboxImportRequest) (*agent.SandboxWorkspaceResponse, error) {
	return s.Handler.ImportFile(ctx, r)
}

func (s *Server) ExportFile(ctx context.Context, r *agent.SandboxExportRequest) (*agent.SandboxExportResponse, error) {
	return s.Handler.ExportFile(ctx, r)
}

func (s *Server) Start(ctx context.Context, r *agent.SandboxStartRequest) (*agent.SandboxTaskResponse, error) {
	return s.Handler.Start(ctx, r)
}

func (s *Server) Get(ctx context.Context, r *agent.SandboxTaskRequest) (*agent.SandboxTaskResponse, error) {
	return s.Handler.Get(ctx, r)
}

func (s *Server) ReadOutput(ctx context.Context, r *agent.SandboxTaskRequest) (*agent.SandboxTaskResponse, error) {
	return s.Handler.ReadOutput(ctx, r)
}

func (s *Server) CancelTask(ctx context.Context, r *agent.SandboxTaskRequest) (*agent.SandboxTaskResponse, error) {
	return s.Handler.CancelTask(ctx, r)
}

func (s *Server) Cancel(ctx context.Context, r *agent.CancelRequest) (*agent.Ack, error) {
	if s.Handler != nil {
		return s.Handler.Cancel(ctx, r)
	}
	if r == nil || s.App == nil {
		return &agent.Ack{Ok: false}, nil
	}
	return &agent.Ack{Ok: s.App.Cancel(r.ServiceToken, r.AgentId, r.RunId)}, nil
}
