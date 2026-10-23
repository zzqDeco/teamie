// Package rpc implements the inbound Kitex Thrift RPC adapter for the Sandbox bounded context.
package rpc

import (
	"context"

	"go.uber.org/zap"
	"teamie/contracts/gen/agent/kitex_gen/agent"
	"teamie/pkg/observability"
	"teamie/services/sandbox/internal/app"
	"teamie/services/sandbox/internal/domain"
)

var _ agent.SandboxService = (*Server)(nil)

// Server adapts Kitex RPC calls to the Sandbox application service.
type Server struct {
	*app.Service
}

// New creates a new Kitex RPC Server for the Sandbox service and attaches the task supervision observability hook.
func New(appSvc *app.Service) *Server {
	if appSvc == nil {
		panic("sandbox/adapter/rpc: nil app.Service")
	}
	if appSvc.SuperviseHook == nil {
		appSvc.SuperviseHook = superviseObservabilityHook
	}
	return &Server{Service: appSvc}
}

func superviseObservabilityHook(ctx context.Context, agentID, runID, taskID string, recovering bool) (context.Context, func(state, code string, outputBytes int64, ctxErr error)) {
	ctx, operation := observability.Begin(ctx, "sandbox-task",
		zap.String("custom.agent.id", observability.ID(agentID)),
		zap.String("custom.runtime.run_id", observability.ID(runID)),
		zap.String("custom.task.id", observability.ID(taskID)),
		zap.Bool("custom.task.recovering", recovering),
	)
	observability.L(ctx).Info("Sandbox task supervision started", zap.String("event.action", "sandbox-task-started"))
	return ctx, func(state, code string, outputBytes int64, ctxErr error) {
		var err error
		if code != "" || state == "failed" || state == "unknown" {
			err = observability.Diagnostic("sandbox_task_failed", nil)
		}
		if ctxErr != nil {
			err = ctxErr
		}
		operation.End(err,
			zap.String("custom.task.state", state),
			zap.String("custom.error.code", observability.ID(code)),
			zap.Int64("custom.sandbox.output_bytes", outputBytes),
		)
	}
}

func toExecResponse(r *domain.SandboxExecResponse) *agent.SandboxExecResponse {
	if r == nil {
		return nil
	}
	return &agent.SandboxExecResponse{
		Ok:           r.Ok,
		ExitCode:     r.ExitCode,
		Stdout:       r.Stdout,
		Stderr:       r.Stderr,
		Truncated:    r.Truncated,
		ErrorCode:    r.ErrorCode,
		ErrorMessage: r.ErrorMessage,
	}
}

func toWorkspaceResponse(r *domain.SandboxWorkspaceResponse) *agent.SandboxWorkspaceResponse {
	if r == nil {
		return nil
	}
	return &agent.SandboxWorkspaceResponse{
		Ok:           r.Ok,
		DataJson:     r.DataJson,
		ErrorCode:    r.ErrorCode,
		ErrorMessage: r.ErrorMessage,
	}
}

func toTaskResponse(r *domain.SandboxTaskResponse) *agent.SandboxTaskResponse {
	if r == nil {
		return nil
	}
	return &agent.SandboxTaskResponse{
		Ok:                     r.Ok,
		TaskId:                 r.TaskId,
		State:                  r.State,
		ExitCode:               r.ExitCode,
		Stdout:                 r.Stdout,
		Stderr:                 r.Stderr,
		Truncated:              r.Truncated,
		NextOffset:             r.NextOffset,
		ErrorCode:              r.ErrorCode,
		ErrorMessage:           r.ErrorMessage,
		OutputBytes:            r.OutputBytes,
		OutputLimitBytes:       r.OutputLimitBytes,
		OutputStatus:           r.OutputStatus,
		HasMore:                r.HasMore,
		OutputSha256:           r.OutputSha256,
		OutputExpiresAt:        r.OutputExpiresAt,
		OutputRetentionSeconds: r.OutputRetentionSeconds,
		OutputAvailability:     r.OutputAvailability,
	}
}

func (s *Server) Ping(ctx context.Context, r *agent.PingRequest) (*agent.PingResponse, error) {
	var in *domain.PingRequest
	if r != nil {
		in = &domain.PingRequest{ServiceToken: r.GetServiceToken()}
	}
	res, err := s.Service.Ping(ctx, in)
	if res == nil {
		return &agent.PingResponse{}, err
	}
	return &agent.PingResponse{Ready: res.Ready}, err
}

func (s *Server) Exec(ctx context.Context, r *agent.SandboxExecRequest) (reply *agent.SandboxExecResponse, executionErr error) {
	var in *domain.SandboxExecRequest
	if r != nil {
		in = &domain.SandboxExecRequest{
			ServiceToken:   r.ServiceToken,
			AgentId:        r.AgentId,
			RunId:          r.RunId,
			RequestId:      r.RequestId,
			Command:        r.Command,
			TimeoutSeconds: r.TimeoutSeconds,
			ExecutionId:    r.ExecutionId,
		}
	}
	if code := s.Service.ValidateExecRequest(in); code != "" {
		res, err := s.Service.Exec(ctx, in)
		return toExecResponse(res), err
	}
	ctx, operation := observability.Begin(ctx, "sandbox-execution",
		zap.String("custom.agent.id", observability.ID(r.AgentId)),
		zap.String("custom.runtime.run_id", observability.ID(r.RunId)),
		zap.String("custom.sandbox.receipt_id", observability.ID(r.RequestId)),
	)
	defer func() {
		err := executionErr
		code := ""
		outputBytes := 0
		if reply != nil {
			outputBytes = len(reply.Stdout) + len(reply.Stderr)
			if !reply.Ok {
				code = reply.GetErrorCode()
				if err == nil {
					err = observability.Diagnostic("sandbox_execution_failed", nil)
				}
			}
		}
		operation.End(err, zap.String("custom.error.code", observability.ID(code)), zap.Int("custom.sandbox.output_bytes", outputBytes))
	}()
	res, err := s.Service.Exec(ctx, in)
	return toExecResponse(res), err
}

func (s *Server) Workspace(ctx context.Context, r *agent.SandboxWorkspaceRequest) (result *agent.SandboxWorkspaceResponse, failure error) {
	var in *domain.SandboxWorkspaceRequest
	if r != nil {
		in = &domain.SandboxWorkspaceRequest{
			ServiceToken: r.ServiceToken,
			AgentId:      r.AgentId,
			Operation:    r.Operation,
			ArgsJson:     r.ArgsJson,
			RequestId:    r.RequestId,
		}
	}
	if code := s.Service.ValidateWorkspaceRequest(in); code != "" {
		res, err := s.Service.Workspace(ctx, in)
		return toWorkspaceResponse(res), err
	}
	operationName := "unsupported"
	switch r.Operation {
	case "ls", "read", "write", "edit", "delete", "glob", "grep", "output":
		operationName = r.Operation
	}
	ctx, operation := observability.Begin(ctx, "sandbox-workspace",
		zap.String("custom.agent.id", observability.ID(r.AgentId)),
		zap.String("custom.sandbox.operation", operationName),
	)
	defer func() {
		err := failure
		code := ""
		if result != nil && !result.Ok {
			code = result.GetErrorCode()
			if err == nil {
				err = observability.Diagnostic("sandbox_workspace_failed", nil)
			}
		}
		operation.End(err, zap.String("custom.error.code", observability.ID(code)))
	}()
	res, err := s.Service.Workspace(ctx, in)
	return toWorkspaceResponse(res), err
}

func (s *Server) ImportFile(ctx context.Context, r *agent.SandboxImportRequest) (*agent.SandboxWorkspaceResponse, error) {
	var in *domain.SandboxImportRequest
	if r != nil {
		in = &domain.SandboxImportRequest{
			ServiceToken: r.ServiceToken,
			AgentId:      r.AgentId,
			RunId:        r.RunId,
			RequestId:    r.RequestId,
			Path:         r.Path,
			Content:      r.Content,
			Sha256:       r.Sha256,
		}
	}
	res, err := s.Service.ImportFile(ctx, in)
	return toWorkspaceResponse(res), err
}

func (s *Server) ExportFile(ctx context.Context, r *agent.SandboxExportRequest) (*agent.SandboxExportResponse, error) {
	var in *domain.SandboxExportRequest
	if r != nil {
		in = &domain.SandboxExportRequest{
			ServiceToken: r.ServiceToken,
			AgentId:      r.AgentId,
			Path:         r.Path,
		}
	}
	res, err := s.Service.ExportFile(ctx, in)
	if res == nil {
		return nil, err
	}
	return &agent.SandboxExportResponse{
		Ok:        res.Ok,
		Content:   res.Content,
		Sha256:    res.Sha256,
		ErrorCode: res.ErrorCode,
	}, err
}

func (s *Server) Start(ctx context.Context, r *agent.SandboxStartRequest) (*agent.SandboxTaskResponse, error) {
	var in *domain.SandboxStartRequest
	if r != nil {
		in = &domain.SandboxStartRequest{
			ServiceToken:   r.ServiceToken,
			AgentId:        r.AgentId,
			RunId:          r.RunId,
			TaskId:         r.TaskId,
			Command:        r.Command,
			TimeoutSeconds: r.TimeoutSeconds,
			OutputVersion:  r.OutputVersion,
		}
	}
	res, err := s.Service.Start(ctx, in)
	return toTaskResponse(res), err
}

func (s *Server) Get(ctx context.Context, r *agent.SandboxTaskRequest) (*agent.SandboxTaskResponse, error) {
	var in *domain.SandboxTaskRequest
	if r != nil {
		in = &domain.SandboxTaskRequest{
			ServiceToken: r.ServiceToken,
			AgentId:      r.AgentId,
			TaskId:       r.TaskId,
			Offset:       r.Offset,
			Limit:        r.Limit,
		}
	}
	res, err := s.Service.Get(ctx, in)
	return toTaskResponse(res), err
}

func (s *Server) ReadOutput(ctx context.Context, r *agent.SandboxTaskRequest) (*agent.SandboxTaskResponse, error) {
	var in *domain.SandboxTaskRequest
	if r != nil {
		in = &domain.SandboxTaskRequest{
			ServiceToken: r.ServiceToken,
			AgentId:      r.AgentId,
			TaskId:       r.TaskId,
			Offset:       r.Offset,
			Limit:        r.Limit,
		}
	}
	res, err := s.Service.ReadOutput(ctx, in)
	return toTaskResponse(res), err
}

func (s *Server) CancelTask(ctx context.Context, r *agent.SandboxTaskRequest) (*agent.SandboxTaskResponse, error) {
	var in *domain.SandboxTaskRequest
	if r != nil {
		in = &domain.SandboxTaskRequest{
			ServiceToken: r.ServiceToken,
			AgentId:      r.AgentId,
			TaskId:       r.TaskId,
			Offset:       r.Offset,
			Limit:        r.Limit,
		}
	}
	res, err := s.Service.CancelTask(ctx, in)
	return toTaskResponse(res), err
}

func (s *Server) Cancel(ctx context.Context, r *agent.CancelRequest) (*agent.Ack, error) {
	var in *domain.CancelRequest
	if r != nil {
		in = &domain.CancelRequest{
			ServiceToken: r.ServiceToken,
			AgentId:      r.AgentId,
			RunId:        r.RunId,
		}
	}
	res, err := s.Service.Cancel(ctx, in)
	if res == nil {
		return &agent.Ack{}, err
	}
	return &agent.Ack{Ok: res.Ok}, err
}
