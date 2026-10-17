package sandbox

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"teamie/agent-infra/internal/api"
	"teamie/agent-protocol/kitex_gen/agent"
)

type taskReceipt struct {
	Version            int          `json:"version"`
	AgentID            string       `json:"agentId"`
	RunID              string       `json:"runId"`
	TaskID             string       `json:"taskId"`
	Command            string       `json:"command"`
	Fingerprint        string       `json:"fingerprint"`
	ContainerName      string       `json:"containerName"`
	ContainerID        string       `json:"containerId,omitempty"`
	CleanupDone        bool         `json:"cleanupDone,omitempty"`
	State              string       `json:"state"`
	TimeoutSeconds     int32        `json:"timeoutSeconds"`
	Deadline           time.Time    `json:"deadline,omitempty"`
	CancelRequested    bool         `json:"cancelRequested"`
	CancelCode         string       `json:"cancelCode,omitempty"`
	Result             Result       `json:"result"`
	Output             []outputPart `json:"output,omitempty"`
	OutputBytes        int64        `json:"outputBytes,omitempty"`
	OutputFileBytes    int64        `json:"outputFileBytes,omitempty"`
	OutputFrames       int          `json:"outputFrames,omitempty"`
	OutputStatus       string       `json:"outputStatus,omitempty"`
	OutputSHA256       string       `json:"outputSha256,omitempty"`
	OutputAnchor       string       `json:"outputAnchor,omitempty"`
	OutputAnchorTime   string       `json:"outputAnchorTime,omitempty"`
	OutputGateReleased bool         `json:"outputGateReleased,omitempty"`
	OutputExpiresAt    *time.Time   `json:"outputExpiresAt,omitempty"`
	OutputExpiredAt    *time.Time   `json:"outputExpiredAt,omitempty"`
	UpdatedAt          time.Time    `json:"updatedAt"`
}

func taskKey(agentID, taskID string) string   { return digest(agentID + "\x00" + taskID) }
func (s *Service) taskPath(key string) string { return filepath.Join(s.StateRoot, key+".task.json") }
func terminalTask(state string) bool {
	return state == "completed" || state == "failed" || state == "canceled" || state == "unknown"
}
func taskFailure(taskID, code string) *agent.SandboxTaskResponse {
	message := map[string]string{"unauthorized": "Invalid service credential", "invalid_request": "Invalid sandbox request", "request_conflict": "Task ID already identifies a different command", "sandbox_busy": "The sandbox execution slot is busy", "execution_unknown": "Execution state is unknown; inspect its side effects before retrying"}[code]
	if message == "" {
		message = "Sandbox operation could not be completed"
	}
	return &agent.SandboxTaskResponse{TaskId: taskID, State: "unknown", ErrorCode: &code, ErrorMessage: &message}
}
func taskResponse(t *taskReceipt) *agent.SandboxTaskResponse {
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
	r := &agent.SandboxTaskResponse{Ok: true, TaskId: t.TaskID, State: t.State, Stdout: &out, Stderr: &errout, Truncated: &truncated, NextOffset: &next}
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
func (s *Service) saveTaskLocked(key string, t *taskReceipt) error {
	t.UpdatedAt = time.Now().UTC()
	return writeAtomicJSON(s.taskPath(key), t)
}
func (s *Service) loadTaskLocked(agentID, taskID string) (*taskReceipt, error) {
	key := taskKey(agentID, taskID)
	if t := s.tasks[key]; t != nil {
		return t, nil
	}
	b, e := os.ReadFile(s.taskPath(key))
	if e != nil {
		return nil, e
	}
	var t taskReceipt
	if len(b) > 2*1024*1024 || json.Unmarshal(b, &t) != nil || (t.Version != 1 && t.Version != 2) || t.AgentID != agentID || t.TaskID != taskID {
		return nil, errors.New("invalid task receipt")
	}
	if t.Version == 2 {
		if e = s.loadSpoolLocked(key, &t); e != nil {
			return nil, e
		}
	} else {
		restoreOutput(&t)
	}
	s.tasks[key] = &t
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
func (s *Service) workspace(agentID string) (string, error) {
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
	var fs syscall.Statfs_t
	return syscall.Statfs(path, &fs) == nil && fs.Bavail > 0 && uint64(fs.Bavail)*uint64(fs.Bsize) >= 2*1024*1024*1024
}

func (s *Service) Start(_ context.Context, r *agent.SandboxStartRequest) (*agent.SandboxTaskResponse, error) {
	if r == nil || !api.EqualSecret(r.ServiceToken, s.Token) {
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
	s.taskMu.Lock()
	defer s.taskMu.Unlock()
	if s.closed {
		return taskFailure(r.TaskId, "sandbox_unavailable"), nil
	}
	old, e := s.loadTaskLocked(r.AgentId, r.TaskId)
	if e == nil {
		if old.Fingerprint != fp {
			return taskFailure(r.TaskId, "request_conflict"), nil
		}
		if e = s.retainOutputLocked(key, old, false); e != nil {
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
	for _, pending := range s.tasks {
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
	if _, e = s.workspace(r.AgentId); e != nil {
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
		used, err := s.outputUsedLocked()
		if err != nil || used >= SharedOutputLimit || !diskAvailable(s.StateRoot) {
			<-s.slots
			return taskFailure(r.TaskId, "sandbox_capacity"), nil
		}
	}
	if e = s.saveTaskLocked(key, t); e != nil {
		<-s.slots
		return taskFailure(r.TaskId, "receipt_unavailable"), nil
	}
	s.tasks[key] = t
	s.observeLocked(key, t, false)
	return taskResponse(t), nil
}

func (s *Service) Get(_ context.Context, r *agent.SandboxTaskRequest) (*agent.SandboxTaskResponse, error) {
	if r == nil || !api.EqualSecret(r.ServiceToken, s.Token) {
		return taskFailure("", "unauthorized"), nil
	}
	if !validID(r.AgentId) || !validID(r.TaskId) {
		return taskFailure(r.TaskId, "invalid_request"), nil
	}
	s.taskMu.Lock()
	defer s.taskMu.Unlock()
	t, e := s.loadTaskLocked(r.AgentId, r.TaskId)
	if os.IsNotExist(e) {
		code, message := "task_not_found", "No durable task receipt exists"
		return &agent.SandboxTaskResponse{TaskId: r.TaskId, State: "not_started", ErrorCode: &code, ErrorMessage: &message}, nil
	}
	if e != nil {
		return taskFailure(r.TaskId, "execution_unknown"), nil
	}
	if e = s.retainOutputLocked(taskKey(r.AgentId, r.TaskId), t, true); e != nil {
		return taskFailure(r.TaskId, "receipt_unavailable"), nil
	}
	return taskResponse(t), nil
}
func (s *Service) ReadOutput(ctx context.Context, r *agent.SandboxTaskRequest) (*agent.SandboxTaskResponse, error) {
	if r == nil || r.GetOffset() < 0 || r.GetLimit() < 0 || r.GetLimit() > OutputLimit {
		return taskFailure("", "invalid_request"), nil
	}
	res, e := s.Get(ctx, r)
	if e != nil || !res.Ok {
		return res, e
	}
	s.taskMu.Lock()
	defer s.taskMu.Unlock()
	t, e := s.loadTaskLocked(r.AgentId, r.TaskId)
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
		parts = []outputPart{{1, t.Result.Stdout}, {2, t.Result.Stderr}}
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
func (s *Service) CancelTask(_ context.Context, r *agent.SandboxTaskRequest) (*agent.SandboxTaskResponse, error) {
	if r == nil || !api.EqualSecret(r.ServiceToken, s.Token) {
		return taskFailure("", "unauthorized"), nil
	}
	if !validID(r.AgentId) || !validID(r.TaskId) {
		return taskFailure(r.TaskId, "invalid_request"), nil
	}
	s.taskMu.Lock()
	defer s.taskMu.Unlock()
	t, e := s.loadTaskLocked(r.AgentId, r.TaskId)
	if os.IsNotExist(e) {
		return &agent.SandboxTaskResponse{Ok: true, TaskId: r.TaskId, State: "not_started"}, nil
	}
	if e != nil {
		return taskFailure(r.TaskId, "execution_unknown"), nil
	}
	if !terminalTask(t.State) || t.State == "unknown" {
		t.CancelRequested = true
		t.CancelCode = "execution_canceled"
		if e = s.saveTaskLocked(taskKey(r.AgentId, r.TaskId), t); e != nil {
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
// It deliberately neither deletes nor starts arbitrary labeled containers.
func (s *Service) Recover(ctx context.Context) error {
	entries, e := os.ReadDir(s.StateRoot)
	if e != nil {
		return e
	}
	s.taskMu.Lock()
	defer s.taskMu.Unlock()
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
			if e = s.loadSpoolLocked(key, &t); e != nil {
				return e
			}
		} else {
			restoreOutput(&t)
		}
		if s.tasks[key] != nil {
			continue
		}
		s.tasks[key] = &t
		if t.Version == 2 && t.State == "unknown" && t.OutputStatus == "unknown" && !t.CancelRequested {
			continue
		}
		if terminalTask(t.State) && t.State != "unknown" {
			// A crash after fsyncing a result but before cleanup may leave only
			// this known terminal container. Never delete a live or foreign one.
			if m, ok := s.Engine.(ManagedEngine); ok && !t.CleanupDone {
				obj, inspectErr := s.inspect(ctx, m, t.ContainerName)
				if errors.Is(inspectErr, errContainerMissing) {
					t.CleanupDone = true
					_ = s.saveTaskLocked(key, &t)
				}
				if inspectErr == nil && !obj.State.Running && obj.Config.Labels[ManagedLabel] == "true" && obj.Config.Labels["io.teamie.task"] == key && obj.Config.Labels["io.teamie.agent"] == digest(t.AgentID) {
					op, cancel := context.WithTimeout(ctx, 10*time.Second)
					if m.Remove(op, obj.ID) == nil {
						t.CleanupDone = true
						_ = s.saveTaskLocked(key, &t)
					}
					cancel()
				}
			}
			if e = s.retainOutputLocked(key, &t, false); e != nil {
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
				_ = s.saveTaskLocked(key, &t)
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
func (s *Service) observeLocked(key string, t *taskReceipt, recovering bool) {
	ctx, cancel := context.WithCancel(context.Background())
	s.observers[key] = cancel
	s.taskWG.Add(1)
	go func() {
		defer s.taskWG.Done()
		defer func() { s.taskMu.Lock(); delete(s.observers, key); s.taskMu.Unlock(); <-s.slots }()
		s.supervise(ctx, key, t, recovering)
	}()
}
func (s *Service) setUnknown(key string, t *taskReceipt) {
	s.taskMu.Lock()
	defer s.taskMu.Unlock()
	t.State = "unknown"
	t.Result.ErrorCode = "execution_unknown"
	t.Result.ExitCode = -1
	if t.Version == 2 {
		t.OutputStatus = "unknown"
		t.Result.Truncated = true
	}
	_ = s.saveTaskLocked(key, t)
}
func (s *Service) inspect(ctx context.Context, m ManagedEngine, name string) (ContainerState, error) {
	op, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	return m.Inspect(op, name)
}
func (s *Service) supervise(ctx context.Context, key string, t *taskReceipt, recovering bool) {
	m := s.Engine.(ManagedEngine)
	s.taskMu.Lock()
	name, agentID, command, state, version, anchor := t.ContainerName, t.AgentID, t.Command, t.State, t.Version, t.OutputAnchor
	s.taskMu.Unlock()
	obj, e := s.inspect(ctx, m, name)
	if errors.Is(e, errContainerMissing) && state == "not_started" {
		workspace, workspaceErr := s.workspace(agentID)
		if workspaceErr != nil {
			s.setUnknown(key, t)
			return
		}
		op, cancel := context.WithTimeout(ctx, 10*time.Second)
		_, e = m.Create(op, Job{Name: name, Image: s.Image, Workspace: workspace, AgentHash: digest(agentID), TaskHash: key, Command: command, OutputAnchor: anchor})
		cancel()
		// A lost create response is reconciled through the stable Docker name.
		obj, e = s.inspect(ctx, m, name)
	}
	if e != nil {
		if ctx.Err() == nil {
			if errors.Is(e, errContainerMissing) {
				s.taskMu.Lock()
				t.ContainerID = ""
				s.taskMu.Unlock()
			}
			s.setUnknown(key, t)
		}
		return
	}
	if obj.Config.Labels[ManagedLabel] != "true" || obj.Config.Labels["io.teamie.agent"] != digest(agentID) || obj.Config.Labels["io.teamie.task"] != key {
		s.setUnknown(key, t)
		return
	}
	s.taskMu.Lock()
	t.ContainerID = obj.ID
	s.taskMu.Unlock()
	var stream io.ReadCloser
	if obj.State.Status == "created" {
		s.taskMu.Lock()
		canceled := t.CancelRequested
		s.taskMu.Unlock()
		if !canceled {
			// Attach first so a fast command cannot outrun bounded output capture.
			if version == 1 {
				stream, e = m.Attach(ctx, obj.ID, false)
			}
			if e != nil {
				if ctx.Err() == nil {
					s.setUnknown(key, t)
				}
				return
			}
			s.taskMu.Lock()
			t.State = "running"
			if t.Deadline.IsZero() && t.TimeoutSeconds > 0 {
				t.Deadline = time.Now().UTC().Add(time.Duration(t.TimeoutSeconds) * time.Second)
			}
			e = s.saveTaskLocked(key, t)
			s.taskMu.Unlock()
			if e != nil {
				if stream != nil {
					stream.Close()
				}
				s.setUnknown(key, t)
				return
			}
			op, cancel := context.WithTimeout(ctx, 10*time.Second)
			e = m.StartContainer(op, obj.ID)
			cancel()
			if e != nil {
				if stream != nil {
					stream.Close()
				}
				if ctx.Err() == nil {
					s.setUnknown(key, t)
				}
				return
			}
			obj.State.Status = "running"
			obj.State.Running = true
		}
	}
	if stream == nil && obj.State.Status != "created" {
		if version == 2 {
			if timestamped, ok := m.(TimestampedEngine); ok {
				stream, e = timestamped.AttachTimestamped(ctx, obj.ID)
			} else {
				e = errors.New("timestamped recovery unavailable")
			}
		} else {
			stream, e = m.Attach(ctx, obj.ID, true)
		}
		if e != nil {
			if ctx.Err() == nil {
				s.setUnknown(key, t)
			}
			return
		}
	}
	s.taskMu.Lock()
	if !t.CancelRequested {
		t.State = "running"
		t.Result.ErrorCode = ""
	}
	_ = s.saveTaskLocked(key, t)
	s.taskMu.Unlock()
	type capture struct {
		result Result
		err    error
	}
	captured := make(chan capture, 1)
	if stream != nil {
		captureStopped := make(chan struct{})
		captureCtx, cancelCapture := context.WithCancel(ctx)
		defer func() { cancelCapture(); stream.Close(); <-captureStopped }()
		go func() {
			defer close(captureStopped)
			if version == 2 {
				e := s.observeSpool(captureCtx, key, t, stream, m.(TimestampedEngine), obj.ID, recovering)
				if errors.Is(e, errOutputLimit) {
					e = nil
				}
				captured <- capture{err: e}
				return
			}
			s.taskMu.Lock()
			priorValue, priorStreams := flattenOutput(t.Output)
			s.taskMu.Unlock()
			result, e := captureFrames(stream, func(result Result, parts []outputPart) error {
				s.taskMu.Lock()
				defer s.taskMu.Unlock()
				if recovering {
					value, streams := flattenOutput(parts)
					common := len(value)
					if common > len(priorValue) {
						common = len(priorValue)
					}
					if value[:common] != priorValue[:common] || streams[:common] != priorStreams[:common] {
						return errors.New("container log recovery prefix changed")
					}
				}
				if len(result.Stdout)+len(result.Stderr) < len(t.Result.Stdout)+len(t.Result.Stderr) {
					return nil
				}
				t.Result.Stdout, t.Result.Stderr, t.Result.Truncated = result.Stdout, result.Stderr, result.Truncated
				t.Output = append([]outputPart(nil), parts...)
				if result.Truncated {
					t.CancelRequested = true
					t.CancelCode = "execution_output_limit"
				}
				return s.saveTaskLocked(key, t)
			})
			captured <- capture{result, e}
		}()
	} else {
		captured <- capture{}
	}
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	var output *capture
	killSent := false
	killEffective := false
	for {
		select {
		case <-ctx.Done():
			return
		case cap := <-captured:
			output = &cap
			captured = nil
			if cap.err != nil && ctx.Err() == nil {
				// Output persistence failed. Stop the known child before exposing
				// unknown state; leaving it running would bypass output bounds.
				op, cancel := operationContext()
				_ = m.Kill(op, obj.ID)
				cancel()
				s.setUnknown(key, t)
				return
			}
		case <-ticker.C:
		}
		s.taskMu.Lock()
		cancelRequested := t.CancelRequested
		deadline := t.Deadline
		s.taskMu.Unlock()
		if !cancelRequested && !deadline.IsZero() && !time.Now().Before(deadline) {
			s.taskMu.Lock()
			t.CancelRequested = true
			t.CancelCode = "execution_timeout"
			_ = s.saveTaskLocked(key, t)
			s.taskMu.Unlock()
			cancelRequested = true
		}
		if !cancelRequested && (!diskAvailable(s.WorkspaceRoot) || !workspaceWithinQuota(filepath.Join(s.WorkspaceRoot, digest(agentID)))) {
			s.taskMu.Lock()
			t.CancelRequested = true
			t.CancelCode = "execution_disk_limit"
			_ = s.saveTaskLocked(key, t)
			s.taskMu.Unlock()
			cancelRequested = true
		}
		if cancelRequested && !killSent {
			if obj.State.Status != "created" {
				op, cancel := context.WithTimeout(ctx, 5*time.Second)
				e = m.Kill(op, obj.ID)
				cancel()
				killEffective = e == nil
				if e != nil {
					obj, e = s.inspect(ctx, m, name)
					if e != nil || obj.State.Running {
						if ctx.Err() == nil {
							s.setUnknown(key, t)
						}
						return
					}
				}
			}
			killSent = true
		}
		obj, e = s.inspect(ctx, m, name)
		if e != nil {
			if ctx.Err() == nil {
				s.setUnknown(key, t)
			}
			return
		}
		if obj.State.Running {
			continue
		}
		if obj.State.Status == "created" && !cancelRequested {
			continue
		}
		if output == nil {
			continue
		}
		s.taskMu.Lock()
		t.Result.ExitCode = obj.State.ExitCode
		t.Result.ErrorCode = ""
		t.State = "completed"
		if obj.State.ExitCode != 0 || obj.State.OOMKilled || obj.State.Error != "" {
			t.State = "failed"
		}
		if t.CancelRequested && (killEffective || obj.State.Status == "created" || t.CancelCode != "execution_canceled") {
			t.State = "canceled"
			t.Result.ExitCode = -1
			t.Result.ErrorCode = t.CancelCode
			if t.CancelCode != "execution_canceled" {
				t.State = "failed"
			}
		}
		if version == 2 {
			e = s.finishOutputLocked(key, t)
		}
		if e == nil {
			e = s.saveTaskLocked(key, t)
		}
		s.taskMu.Unlock()
		if e != nil {
			s.setUnknown(key, t)
			return
		}
		// Persist terminal result before removing the container. A crash before
		// this point leaves an inspectable container for the next supervisor.
		if e == nil {
			op, cancel := operationContext()
			if m.Remove(op, obj.ID) == nil {
				s.taskMu.Lock()
				t.CleanupDone = true
				_ = s.saveTaskLocked(key, t)
				_ = s.retainOutputLocked(key, t, false)
				s.taskMu.Unlock()
			}
			cancel()
		}
		return
	}
}
