package app

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"teamie/services/sandbox/internal/domain"
)

type taskReceipt = domain.TaskReceipt

func taskKey(agentID, taskID string) string   { return domain.TaskKey(agentID, taskID) }
func (s *Service) TaskPath(key string) string { return filepath.Join(s.StateRoot, key+".task.json") }
func terminalTask(state string) bool {
	return domain.TerminalTask(state)
}
func taskFailure(taskID, code string) *domain.SandboxTaskResponse {
	message := domain.TaskErrorMessage(code)
	return &domain.SandboxTaskResponse{TaskId: taskID, State: "unknown", ErrorCode: &code, ErrorMessage: &message}
}
func taskResponse(t *taskReceipt) *domain.SandboxTaskResponse {
	out, errout, truncated, exit := t.Result.Stdout, t.Result.Stderr, t.Result.Truncated, t.Result.ExitCode
	next := int64(len(out) + len(errout))
	length, limit, status, hash := next, int64(OutputLimit), "recording", ""
	if terminalTask(t.State) {
		status = "complete"
	}
	if truncated {
		status = "legacy_limited"
	}
	if t.Version == 2 {
		length, limit, status, hash = t.OutputBytes, DurableOutputLimit, t.OutputStatus, t.OutputSHA256
		truncated = truncated || next < length || status == "unknown" || status == "limited"
	}
	more := next < length
	r := &domain.SandboxTaskResponse{Ok: true, TaskId: t.TaskID, State: t.State, Stdout: &out, Stderr: &errout, Truncated: &truncated, NextOffset: &next}
	r.OutputBytes = &length
	r.OutputLimitBytes = &limit
	r.OutputStatus = &status
	r.HasMore = &more
	if hash != "" {
		r.OutputSha256 = &hash
	}
	if terminalTask(t.State) {
		r.ExitCode = &exit
	}
	if t.Result.ErrorCode != "" {
		code := t.Result.ErrorCode
		r.ErrorCode = &code
		message := "Execution did not complete successfully"
		r.ErrorMessage = &message
	}
	outputRetentionResponse(t, r)
	return r
}
func (s *Service) SaveTaskLocked(key string, t *taskReceipt) error {
	t.UpdatedAt = time.Now().UTC()
	return writeAtomicJSON(s.TaskPath(key), t)
}
func (s *Service) LoadTaskLocked(agentID, taskID string) (*taskReceipt, error) {
	key := taskKey(agentID, taskID)
	if t := s.Tasks[key]; t != nil {
		return t, nil
	}
	b, e := os.ReadFile(s.TaskPath(key))
	if e != nil {
		return nil, e
	}
	var t taskReceipt
	if len(b) > 2*1024*1024 || json.Unmarshal(b, &t) != nil || (t.Version != 1 && t.Version != 2) || t.AgentID != agentID || t.TaskID != taskID {
		return nil, errors.New("invalid task receipt")
	}
	if t.Version == 2 {
		if e = s.LoadSpoolLocked(key, &t); e != nil {
			return nil, e
		}
	} else {
		restoreOutput(&t)
	}
	s.Tasks[key] = &t
	return &t, nil
}
func restoreOutput(t *taskReceipt) {
	if len(t.Output) == 0 {
		return
	}
	var out, errout strings.Builder
	for _, part := range t.Output {
		if part.Stream == 1 {
			out.WriteString(part.Data)
		} else {
			errout.WriteString(part.Data)
		}
	}
	t.Result.Stdout, t.Result.Stderr = out.String(), errout.String()
}
func (s *Service) WorkspaceDir(agentID string) (string, error) {
	path := filepath.Join(s.WorkspaceRoot, digest(agentID))
	if e := os.Mkdir(path, 0777); e != nil && !os.IsExist(e) {
		return "", e
	}
	info, e := os.Lstat(path)
	if e != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("invalid workspace")
	}
	if e = os.Chmod(path, 0777); e != nil {
		return "", e
	}
	return path, nil
}
func diskAvailable(path string) bool {
	return domain.DefaultWorkspaceStorage.DiskAvailable(path)
}

func (s *Service) Start(_ context.Context, r *domain.SandboxStartRequest) (*domain.SandboxTaskResponse, error) {
	if r == nil || !domain.EqualSecret(r.ServiceToken, s.Token) {
		return taskFailure("", "unauthorized"), nil
	}
	if !validID(r.AgentId) || !validID(r.RunId) || !validID(r.TaskId) || r.Command == "" || len(r.Command) > 16384 || strings.ContainsRune(r.Command, 0) || r.GetTimeoutSeconds() < 0 || r.GetTimeoutSeconds() > 86400 || r.GetOutputVersion() < 0 || r.GetOutputVersion() > 1 {
		return taskFailure(r.TaskId, "invalid_request"), nil
	}
	key := taskKey(r.AgentId, r.TaskId)
	fp := digest(r.Command + "\x00" + time.Duration(r.GetTimeoutSeconds()).String())
	if r.GetOutputVersion() == 1 {
		fp = digest(fp + "/output-v1")
	}
	s.TaskMu.Lock()
	defer s.TaskMu.Unlock()
	if s.closed {
		return taskFailure(r.TaskId, "sandbox_unavailable"), nil
	}
	old, e := s.LoadTaskLocked(r.AgentId, r.TaskId)
	if e == nil {
		if old.Fingerprint != fp {
			return taskFailure(r.TaskId, "request_conflict"), nil
		}
		if e = s.RetainOutputLocked(key, old, false); e != nil {
			return taskFailure(r.TaskId, "receipt_unavailable"), nil
		}
		return taskResponse(old), nil
	}
	if !os.IsNotExist(e) {
		return taskFailure(r.TaskId, "execution_unknown"), nil
	}
	if _, ok := s.Engine.(ManagedEngine); !ok {
		return taskFailure(r.TaskId, "sandbox_unavailable"), nil
	}
	if r.GetOutputVersion() == 1 {
		if _, ok := s.Engine.(TimestampedEngine); !ok {
			return taskFailure(r.TaskId, "sandbox_unavailable"), nil
		}
	}
	for _, pending := range s.Tasks {
		if pending.State == "unknown" && pending.ContainerID != "" {
			return taskFailure(r.TaskId, "sandbox_busy"), nil
		}
	}
	select {
	case s.slots <- struct{}{}:
	default:
		return taskFailure(r.TaskId, "sandbox_busy"), nil
	}
	if !diskAvailable(s.WorkspaceRoot) {
		<-s.slots
		return taskFailure(r.TaskId, "workspace_capacity"), nil
	}
	entries, e := os.ReadDir(s.StateRoot)
	if e != nil || len(entries) >= 10001 {
		<-s.slots
		return taskFailure(r.TaskId, "sandbox_capacity"), nil
	}
	if _, e = s.WorkspaceDir(r.AgentId); e != nil {
		<-s.slots
		return taskFailure(r.TaskId, "workspace_unavailable"), nil
	}
	t := &taskReceipt{Version: 1, AgentID: r.AgentId, RunID: r.RunId, TaskID: r.TaskId, Command: r.Command, Fingerprint: fp, ContainerName: "teamie-task-" + key[:32], State: "not_started", TimeoutSeconds: r.GetTimeoutSeconds()}
	if r.GetOutputVersion() == 1 {
		t.Version = 2
		t.OutputStatus = "recording"
		t.OutputAnchor, e = newOutputAnchor()
		if e != nil {
			<-s.slots
			return taskFailure(r.TaskId, "receipt_unavailable"), nil
		}
		used, err := s.OutputUsedLocked()
		if err != nil || used >= SharedOutputLimit || !diskAvailable(s.StateRoot) {
			<-s.slots
			return taskFailure(r.TaskId, "sandbox_capacity"), nil
		}
	}
	if e = s.SaveTaskLocked(key, t); e != nil {
		<-s.slots
		return taskFailure(r.TaskId, "receipt_unavailable"), nil
	}
	s.Tasks[key] = t
	s.observeLocked(key, t, false)
	return taskResponse(t), nil
}

func (s *Service) Get(_ context.Context, r *domain.SandboxTaskRequest) (*domain.SandboxTaskResponse, error) {
	if r == nil || !domain.EqualSecret(r.ServiceToken, s.Token) {
		return taskFailure("", "unauthorized"), nil
	}
	if !validID(r.AgentId) || !validID(r.TaskId) {
		return taskFailure(r.TaskId, "invalid_request"), nil
	}
	s.TaskMu.Lock()
	defer s.TaskMu.Unlock()
	t, e := s.LoadTaskLocked(r.AgentId, r.TaskId)
	if os.IsNotExist(e) {
		code, message := "task_not_found", "No durable task receipt exists"
		return &domain.SandboxTaskResponse{TaskId: r.TaskId, State: "not_started", ErrorCode: &code, ErrorMessage: &message}, nil
	}
	if e != nil {
		return taskFailure(r.TaskId, "execution_unknown"), nil
	}
	if e = s.RetainOutputLocked(taskKey(r.AgentId, r.TaskId), t, true); e != nil {
		return taskFailure(r.TaskId, "receipt_unavailable"), nil
	}
	return taskResponse(t), nil
}

func (s *Service) ReadOutput(ctx context.Context, r *domain.SandboxTaskRequest) (*domain.SandboxTaskResponse, error) {
	if r == nil || r.GetOffset() < 0 || r.GetLimit() < 0 || r.GetLimit() > OutputLimit {
		return taskFailure("", "invalid_request"), nil
	}
	res, e := s.Get(ctx, r)
	if e != nil || !res.Ok {
		return res, e
	}
	s.TaskMu.Lock()
	defer s.TaskMu.Unlock()
	t, e := s.LoadTaskLocked(r.AgentId, r.TaskId)
	if os.IsNotExist(e) {
		return res, nil
	}
	if e != nil {
		return taskFailure(r.TaskId, "execution_unknown"), nil
	}
	res = taskResponse(t)
	if t.Version == 2 {
		if t.OutputExpiredAt != nil {
			return expiredOutputResponse(t), nil
		}
		if r.GetOffset() > t.OutputBytes {
			return taskFailure(r.TaskId, "invalid_request"), nil
		}
		limit := r.GetLimit()
		if limit == 0 {
			limit = 16384
		}
		out, errout, next, e := s.readSpoolLocked(taskKey(r.AgentId, r.TaskId), t, r.GetOffset(), limit)
		if e != nil {
			return taskFailure(r.TaskId, "execution_unknown"), nil
		}
		res.Stdout = &out
		res.Stderr = &errout
		res.NextOffset = &next
		more := next < t.OutputBytes
		res.HasMore = &more
		truncated := more || t.OutputStatus == "limited" || t.OutputStatus == "unknown"
		res.Truncated = &truncated
		return res, nil
	}
	parts := t.Output
	if len(parts) == 0 {
		parts = []outputPart{{Stream: 1, Data: t.Result.Stdout}, {Stream: 2, Data: t.Result.Stderr}}
	}
	length := int64(len(t.Result.Stdout) + len(t.Result.Stderr))
	offset := r.GetOffset()
	if offset > length {
		return taskFailure(r.TaskId, "invalid_request"), nil
	}
	limit := r.GetLimit()
	if limit == 0 {
		limit = 16384
	}
	remaining := int64(limit)
	var out, errout strings.Builder
	position := int64(0)
	for _, part := range parts {
		end := position + int64(len(part.Data))
		if end > offset && remaining > 0 {
			start := int64(0)
			if offset > position {
				start = offset - position
			}
			take := int64(len(part.Data)) - start
			if take > remaining {
				take = remaining
			}
			if part.Stream == 1 {
				out.WriteString(part.Data[start : start+take])
			} else {
				errout.WriteString(part.Data[start : start+take])
			}
			remaining -= take
		}
		position = end
	}
	stdout, stderr := out.String(), errout.String()
	next := offset + int64(len(stdout)+len(stderr))
	res.Stdout = &stdout
	res.Stderr = &stderr
	res.NextOffset = &next
	more := next < length
	res.HasMore = &more
	return res, nil
}

func (s *Service) CancelTask(_ context.Context, r *domain.SandboxTaskRequest) (*domain.SandboxTaskResponse, error) {
	if r == nil || !domain.EqualSecret(r.ServiceToken, s.Token) {
		return taskFailure("", "unauthorized"), nil
	}
	if !validID(r.AgentId) || !validID(r.TaskId) {
		return taskFailure(r.TaskId, "invalid_request"), nil
	}
	s.TaskMu.Lock()
	defer s.TaskMu.Unlock()
	t, e := s.LoadTaskLocked(r.AgentId, r.TaskId)
	if os.IsNotExist(e) {
		return &domain.SandboxTaskResponse{Ok: true, TaskId: r.TaskId, State: "not_started"}, nil
	}
	if e != nil {
		return taskFailure(r.TaskId, "execution_unknown"), nil
	}
	if !terminalTask(t.State) || t.State == "unknown" {
		t.CancelRequested = true
		t.CancelCode = "execution_canceled"
		if e = s.SaveTaskLocked(taskKey(r.AgentId, r.TaskId), t); e != nil {
			return taskFailure(r.TaskId, "receipt_unavailable"), nil
		}
		key := taskKey(r.AgentId, r.TaskId)
		if t.State == "unknown" && s.observers[key] == nil && !s.closed {
			select {
			case s.slots <- struct{}{}:
				s.observeLocked(key, t, true)
			default:
			}
		}
	}
	return taskResponse(t), nil
}

// Recover adopts only containers named and labeled by a durable task receipt.
func (s *Service) Recover(ctx context.Context) error {
	entries, e := os.ReadDir(s.StateRoot)
	if e != nil {
		return e
	}
	s.TaskMu.Lock()
	defer s.TaskMu.Unlock()
	if s.closed {
		return errors.New("sandbox supervisor closed")
	}
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".task.json") {
			continue
		}
		b, e := os.ReadFile(filepath.Join(s.StateRoot, entry.Name()))
		if e != nil {
			return e
		}
		var t taskReceipt
		if len(b) > 2*1024*1024 || json.Unmarshal(b, &t) != nil || (t.Version != 1 && t.Version != 2) || !validID(t.AgentID) || !validID(t.TaskID) || entry.Name() != taskKey(t.AgentID, t.TaskID)+".task.json" {
			return errors.New("invalid durable task receipt")
		}
		key := taskKey(t.AgentID, t.TaskID)
		if t.Version == 2 {
			if e = s.LoadSpoolLocked(key, &t); e != nil {
				return e
			}
		} else {
			restoreOutput(&t)
		}
		if s.Tasks[key] != nil {
			continue
		}
		s.Tasks[key] = &t
		if t.Version == 2 && t.State == "unknown" && t.OutputStatus == "unknown" && !t.CancelRequested {
			continue
		}
		if terminalTask(t.State) && t.State != "unknown" {
			if m, ok := s.Engine.(ManagedEngine); ok && !t.CleanupDone {
				obj, inspectErr := s.inspect(ctx, m, t.ContainerName)
				if errors.Is(inspectErr, errContainerMissing) {
					t.CleanupDone = true
					_ = s.SaveTaskLocked(key, &t)
				}
				if inspectErr == nil && !obj.State.Running && obj.Config.Labels[ManagedLabel] == "true" && obj.Config.Labels["io.teamie.task"] == key && obj.Config.Labels["io.teamie.agent"] == digest(t.AgentID) {
					op, cancel := context.WithTimeout(ctx, 10*time.Second)
					if m.Remove(op, obj.ID) == nil {
						t.CleanupDone = true
						_ = s.SaveTaskLocked(key, &t)
					}
					cancel()
				}
			}
			if e = s.RetainOutputLocked(key, &t, false); e != nil {
				return e
			}
			continue
		}
		if _, ok := s.Engine.(ManagedEngine); !ok {
			return errors.New("managed execution recovery unavailable")
		}
		if t.State == "unknown" {
			obj, inspectErr := s.inspect(ctx, s.Engine.(ManagedEngine), t.ContainerName)
			if errors.Is(inspectErr, errContainerMissing) {
				t.ContainerID = ""
				_ = s.SaveTaskLocked(key, &t)
				continue
			}
			if inspectErr != nil {
				continue
			}
			t.ContainerID = obj.ID
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		select {
		case s.slots <- struct{}{}:
		default:
			return errors.New("sandbox recovery exceeds execution capacity")
		}
		s.observeLocked(key, &t, true)
	}
	return nil
}
