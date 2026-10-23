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
// It panics if handler is nil because core Sandbox RPC operations require a
// non-nil Handler implementation.
func New(appSvc *app.Service, handler Handler) *Server {
	if handler == nil {
		panic("sandbox/adapter/rpc: nil Handler")
	}
	return &Server{App: appSvc, Handler: handler}
}

func strPtr(v string) *string { return &v }

func (s *Server) Ping(ctx context.Context, r *agent.PingRequest) (*agent.PingResponse, error) {
	if s != nil && s.Handler != nil {
		return s.Handler.Ping(ctx, r)
	}
	ready := r != nil && s != nil && s.App != nil && s.App.Authorize(r.GetServiceToken()) && s.App.Ready(ctx) == nil
	return &agent.PingResponse{Ready: ready}, nil
}

func (s *Server) Exec(ctx context.Context, r *agent.SandboxExecRequest) (*agent.SandboxExecResponse, error) {
	if s == nil || s.Handler == nil {
		return &agent.SandboxExecResponse{Ok: false, ExitCode: 1, ErrorCode: strPtr("sandbox_unavailable"), ErrorMessage: strPtr("Sandbox is temporarily unavailable")}, nil
	}
	return s.Handler.Exec(ctx, r)
}

func (s *Server) Workspace(ctx context.Context, r *agent.SandboxWorkspaceRequest) (*agent.SandboxWorkspaceResponse, error) {
	if s == nil || s.Handler == nil {
		return &agent.SandboxWorkspaceResponse{Ok: false, ErrorCode: strPtr("sandbox_unavailable"), ErrorMessage: strPtr("Sandbox is temporarily unavailable")}, nil
	}
	return s.Handler.Workspace(ctx, r)
}

func (s *Server) ImportFile(ctx context.Context, r *agent.SandboxImportRequest) (*agent.SandboxWorkspaceResponse, error) {
	if s == nil || s.Handler == nil {
		return &agent.SandboxWorkspaceResponse{Ok: false, ErrorCode: strPtr("sandbox_unavailable"), ErrorMessage: strPtr("Sandbox is temporarily unavailable")}, nil
	}
	return s.Handler.ImportFile(ctx, r)
}

func (s *Server) ExportFile(ctx context.Context, r *agent.SandboxExportRequest) (*agent.SandboxExportResponse, error) {
	if s == nil || s.Handler == nil {
		return &agent.SandboxExportResponse{Ok: false, ErrorCode: strPtr("sandbox_unavailable")}, nil
	}
	return s.Handler.ExportFile(ctx, r)
}

func (s *Server) Start(ctx context.Context, r *agent.SandboxStartRequest) (*agent.SandboxTaskResponse, error) {
	if s == nil || s.Handler == nil {
		return &agent.SandboxTaskResponse{Ok: false, ErrorCode: strPtr("sandbox_unavailable"), ErrorMessage: strPtr("Sandbox is temporarily unavailable")}, nil
	}
	return s.Handler.Start(ctx, r)
}

func (s *Server) Get(ctx context.Context, r *agent.SandboxTaskRequest) (*agent.SandboxTaskResponse, error) {
	if s == nil || s.Handler == nil {
		return &agent.SandboxTaskResponse{Ok: false, ErrorCode: strPtr("sandbox_unavailable"), ErrorMessage: strPtr("Sandbox is temporarily unavailable")}, nil
	}
	return s.Handler.Get(ctx, r)
}

func (s *Server) ReadOutput(ctx context.Context, r *agent.SandboxTaskRequest) (*agent.SandboxTaskResponse, error) {
	if s == nil || s.Handler == nil {
		return &agent.SandboxTaskResponse{Ok: false, ErrorCode: strPtr("sandbox_unavailable"), ErrorMessage: strPtr("Sandbox is temporarily unavailable")}, nil
	}
	return s.Handler.ReadOutput(ctx, r)
}

func (s *Server) CancelTask(ctx context.Context, r *agent.SandboxTaskRequest) (*agent.SandboxTaskResponse, error) {
	if s == nil || s.Handler == nil {
		return &agent.SandboxTaskResponse{Ok: false, ErrorCode: strPtr("sandbox_unavailable"), ErrorMessage: strPtr("Sandbox is temporarily unavailable")}, nil
	}
	return s.Handler.CancelTask(ctx, r)
}

func (s *Server) Cancel(ctx context.Context, r *agent.CancelRequest) (*agent.Ack, error) {
	if s != nil && s.Handler != nil {
		return s.Handler.Cancel(ctx, r)
	}
	if r == nil || s == nil || s.App == nil {
		return &agent.Ack{Ok: false}, nil
	}
	return &agent.Ack{Ok: s.App.Cancel(r.ServiceToken, r.AgentId, r.RunId)}, nil
}
