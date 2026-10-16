package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	bg "github.com/cloudwego/eino/adk/backgroundtask"
	bgshell "github.com/cloudwego/eino/adk/backgroundtask/shell"
	bgtool "github.com/cloudwego/eino/adk/backgroundtask/tool"
	fs "github.com/cloudwego/eino/adk/filesystem"
	"github.com/cloudwego/eino/adk/middlewares/plantask"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
	"github.com/jackc/pgx/v5"
	wire "teamie/agent-protocol/kitex_gen/agent"
	"teamie/agent-protocol/kitex_gen/agent/sandboxservice"
)

// SandboxBackend is the sole workspace boundary. The Sandbox service confines
// paths and execution; no host filesystem or credentials enter its children.
type SandboxBackend struct {
	Client         sandboxservice.Client
	Token, AgentID string
	Authorize      func(context.Context) error
	Store          *Store
	Lease          *Lease
}

func (s *SandboxBackend) check(ctx context.Context) error {
	if s.Authorize != nil {
		if err := s.Authorize(ctx); err != nil {
			return err
		}
	}
	if s.Store != nil && s.Lease != nil {
		return s.Store.CheckLease(ctx, s.Lease)
	}
	return nil
}
func (s *SandboxBackend) workspace(ctx context.Context, op string, args any, out any) error {
	if err := s.check(ctx); err != nil {
		return err
	}
	b, err := json.Marshal(args)
	if err != nil {
		return err
	}
	var observedRun *Run
	var observedCall, observedKey string
	if op == "ls" || op == "read" || op == "glob" || op == "grep" {
		observedRun, _ = CurrentRun(ctx)
		observedCall = compose.GetToolCallID(ctx)
		observedKey = op + ":" + digest(b)
		if observedRun != nil && observedCall != "" && s.Store != nil && s.Lease != nil {
			cached, err := s.Store.readObservation(ctx, observedRun, observedCall, observedKey)
			if err == nil {
				if out != nil {
					return json.Unmarshal(cached, out)
				}
				return nil
			}
			if !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
		}
	}
	pc, c := context.WithTimeout(ctx, 15*time.Second)
	defer c()
	request := &wire.SandboxWorkspaceRequest{ServiceToken: s.Token, AgentId: s.AgentID, Operation: op, ArgsJson: string(b)}
	if op == "write" || op == "edit" || op == "delete" {
		run, err := CurrentRun(ctx)
		if err != nil {
			return err
		}
		callID := compose.GetToolCallID(ctx)
		if callID == "" {
			return errors.New("file mutation missing tool identity")
		}
		id := "file-" + digest([]byte(run.ID+"/"+callID+"/"+op+"/"+string(b)))
		request.RequestId = &id
	}
	r, err := s.Client.Workspace(pc, request)
	if err != nil {
		return &ModelError{Code: "sandbox_unavailable", Retry: true}
	}
	if !r.Ok {
		return &ModelError{Code: r.GetErrorCode(), Retry: false}
	}
	if observedRun != nil && observedCall != "" && s.Store != nil && s.Lease != nil {
		if err = s.Store.saveObservation(ctx, s.Lease, observedRun, observedCall, observedKey, []byte(r.GetDataJson())); err != nil {
			return err
		}
	}
	if out != nil {
		return json.Unmarshal([]byte(r.GetDataJson()), out)
	}
	return nil
}
func (s *SandboxBackend) LsInfo(ctx context.Context, r *fs.LsInfoRequest) ([]fs.FileInfo, error) {
	var out struct {
		Files []fs.FileInfo `json:"files"`
	}
	err := s.workspace(ctx, "ls", map[string]any{"path": r.Path}, &out)
	return out.Files, err
}
func (s *SandboxBackend) Read(ctx context.Context, r *fs.ReadRequest) (*fs.FileContent, error) {
	var out fs.FileContent
	err := s.workspace(ctx, "read", map[string]any{"path": r.FilePath, "offset": r.Offset, "limit": r.Limit}, &out)
	return &out, err
}
func (s *SandboxBackend) Write(ctx context.Context, r *fs.WriteRequest) error {
	return s.workspace(ctx, "write", map[string]any{"path": r.FilePath, "content": r.Content}, nil)
}
func (s *SandboxBackend) Edit(ctx context.Context, r *fs.EditRequest) error {
	return s.workspace(ctx, "edit", map[string]any{"path": r.FilePath, "old": r.OldString, "new": r.NewString, "replaceAll": r.ReplaceAll}, nil)
}
func (s *SandboxBackend) GlobInfo(ctx context.Context, r *fs.GlobInfoRequest) ([]fs.FileInfo, error) {
	var out struct {
		Files []fs.FileInfo `json:"files"`
	}
	err := s.workspace(ctx, "glob", map[string]any{"path": r.Path, "pattern": r.Pattern}, &out)
	return out.Files, err
}
func (s *SandboxBackend) GrepRaw(ctx context.Context, r *fs.GrepRequest) ([]fs.GrepMatch, error) {
	var out struct {
		Matches []fs.GrepMatch `json:"matches"`
	}
	err := s.workspace(ctx, "grep", map[string]any{"path": r.Path, "pattern": r.Pattern, "glob": r.Glob, "fileType": r.FileType, "caseInsensitive": r.CaseInsensitive, "enableMultiline": r.EnableMultiline, "afterLines": r.AfterLines, "beforeLines": r.BeforeLines}, &out)
	return out.Matches, err
}
func (s *SandboxBackend) StartCommand(ctx context.Context, r *bgshell.StartCommandRequest) (bgtool.Run, error) {
	if err := s.check(ctx); err != nil {
		return nil, err
	}
	run, err := CurrentRun(ctx)
	if err != nil {
		return nil, err
	}
	// A foreground command hands its front slot back as soon as it enters an
	// independently supervised operation. A later model call reacquires the slot.
	run.releaseFront()
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		if err = s.check(ctx); err != nil {
			return nil, err
		}
		if s.Store != nil {
			task, taskErr := (&TaskStore{Store: s.Store, AgentID: s.AgentID}).Get(ctx, r.TaskID)
			if taskErr != nil {
				return nil, taskErr
			}
			if task.CancelRequestedAt != nil {
				return nil, errors.New("task_cancelled")
			}
		}
		pc, c := context.WithTimeout(ctx, 10*time.Second)
		response, callErr := s.Client.Start(pc, &wire.SandboxStartRequest{ServiceToken: s.Token, AgentId: s.AgentID, RunId: run.ID, TaskId: r.TaskID, Command: r.Command})
		c()
		if callErr != nil {
			// An ambiguous Start is observed by its stable identity; it is never
			// repeated speculatively. Missing/unknown state remains an unknown effect.
			for observe := 0; observe < 10; observe++ {
				pc, c := context.WithTimeout(ctx, 5*time.Second)
				known, getErr := s.Client.Get(pc, &wire.SandboxTaskRequest{ServiceToken: s.Token, AgentId: s.AgentID, TaskId: r.TaskID})
				c()
				if getErr == nil && known.Ok && known.State != "unknown" {
					return &sandboxRun{backend: s, id: r.TaskID, ctx: ctx}, nil
				}
				select {
				case <-ctx.Done():
					return nil, ctx.Err()
				case <-ticker.C:
				}
			}
			return nil, errors.New("sandbox_execution_unknown")
		}
		if response.Ok {
			return &sandboxRun{backend: s, id: r.TaskID, ctx: ctx}, nil
		}
		if response.GetErrorCode() != "sandbox_busy" {
			return nil, &ModelError{Code: response.GetErrorCode(), Retry: false}
		}
		// Busy explicitly confirms that this logical operation has not started.
		// Its durable native task waits without occupying the foreground slot.
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-ticker.C:
		}
	}

}
func (s *SandboxBackend) RecoverCommand(ctx context.Context, r *bgshell.RecoverCommandRequest) (bgtool.Run, error) {
	if err := s.check(ctx); err != nil {
		return nil, err
	}
	if run, err := CurrentRun(ctx); err == nil {
		run.releaseFront()
	}
	pc, c := context.WithTimeout(ctx, 10*time.Second)
	defer c()
	response, err := s.Client.Get(pc, &wire.SandboxTaskRequest{ServiceToken: s.Token, AgentId: s.AgentID, TaskId: r.TaskID})
	if err != nil {
		return nil, &ModelError{Code: "sandbox_unavailable", Retry: true}
	}
	if !response.Ok {
		if response.GetErrorCode() == "task_not_found" {
			return nil, errors.New("sandbox_execution_unknown")
		}
		return nil, &ModelError{Code: response.GetErrorCode(), Retry: false}
	}
	if response.State == "unknown" {
		return nil, errors.New("sandbox_execution_unknown")
	}
	return &sandboxRun{backend: s, id: r.TaskID, ctx: ctx}, nil
}

type sandboxRun struct {
	backend *SandboxBackend
	id      string
	ctx     context.Context
}

func (r *sandboxRun) Wait(ctx context.Context) (*bgtool.Outcome, error) {
	timer := time.NewTicker(250 * time.Millisecond)
	defer timer.Stop()
	for {
		if err := r.backend.check(ctx); err != nil {
			return nil, err
		}
		pc, c := context.WithTimeout(ctx, 10*time.Second)
		v, err := r.backend.Client.Get(pc, &wire.SandboxTaskRequest{ServiceToken: r.backend.Token, AgentId: r.backend.AgentID, TaskId: r.id})
		c()
		if err != nil {
			return nil, &ModelError{Code: "sandbox_unavailable", Retry: true}
		}
		if !v.Ok {
			if v.GetErrorCode() == "task_not_found" {
				return &bgtool.Outcome{Status: bg.StatusFailed, Error: "sandbox_execution_unknown"}, nil
			}
			return nil, &ModelError{Code: v.GetErrorCode(), Retry: false}
		}
		switch v.State {
		case "completed":
			b, _ := json.Marshal(map[string]any{"exitCode": v.GetExitCode(), "stdout": v.GetStdout(), "stderr": v.GetStderr(), "truncated": v.GetTruncated()})
			return &bgtool.Outcome{Status: bg.StatusCompleted, Data: b}, nil
		case "failed":
			return &bgtool.Outcome{Status: bg.StatusFailed, Error: "sandbox_command_failed"}, nil
		case "canceled":
			return &bgtool.Outcome{Status: bg.StatusCanceled, Error: "canceled"}, nil
		case "unknown":
			return &bgtool.Outcome{Status: bg.StatusFailed, Error: "sandbox_execution_unknown"}, nil
		case "running", "not_started":
		default:
			return nil, fmt.Errorf("invalid sandbox state")
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}
func (r *sandboxRun) Stop(ctx context.Context) error {
	pc, c := context.WithTimeout(ctx, 10*time.Second)
	defer c()
	v, err := r.backend.Client.CancelTask(pc, &wire.SandboxTaskRequest{ServiceToken: r.backend.Token, AgentId: r.backend.AgentID, TaskId: r.id})
	if err != nil {
		return &ModelError{Code: "sandbox_unavailable", Retry: true}
	}
	if !v.Ok {
		return &ModelError{Code: v.GetErrorCode(), Retry: false}
	}
	return nil
}

var _ fs.Backend = (*SandboxBackend)(nil)
var _ bgshell.RecoverableShell = (*SandboxBackend)(nil)

func (s *SandboxBackend) Delete(ctx context.Context, r *plantask.DeleteRequest) error {
	return s.workspace(ctx, "delete", map[string]any{"path": r.FilePath}, nil)
}

var _ plantask.Backend = (*SandboxBackend)(nil)

// Fixed byte boundaries give progress events stable replay identities across
// worker handoff. Partial output is emitted only once the command is terminal.
func (r *sandboxRun) Updates() *schema.StreamReader[*bgtool.Update] {
	reader, writer := schema.Pipe[*bgtool.Update](4)
	go func() {
		defer writer.Close()
		ctx := r.ctx
		if ctx == nil {
			ctx = context.Background()
		}
		ticker := time.NewTicker(250 * time.Millisecond)
		defer ticker.Stop()
		offset := int64(0)
		limit := int32(32768)
		for {
			if err := r.backend.check(ctx); err != nil {
				writer.Send(nil, err)
				return
			}
			pc, c := context.WithTimeout(ctx, 10*time.Second)
			out, err := r.backend.Client.ReadOutput(pc, &wire.SandboxTaskRequest{ServiceToken: r.backend.Token, AgentId: r.backend.AgentID, TaskId: r.id, Offset: &offset, Limit: &limit})
			c()
			if err != nil {
				writer.Send(nil, &ModelError{Code: "sandbox_unavailable", Retry: true})
				return
			}
			if !out.Ok {
				writer.Send(nil, &ModelError{Code: out.GetErrorCode()})
				return
			}
			next := out.GetNextOffset()
			terminal := out.State != "running" && out.State != "not_started"
			if next > offset && (next-offset >= int64(limit) || terminal) {
				b, _ := json.Marshal(map[string]string{"stdout": out.GetStdout(), "stderr": out.GetStderr()})
				if writer.Send(&bgtool.Update{EventID: fmt.Sprintf("sandbox-output:%d:%d", offset, next), Kind: "command.output", Data: b}, nil) {
					return
				}
				offset = next
				continue
			}
			if terminal {
				return
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	return reader
}
