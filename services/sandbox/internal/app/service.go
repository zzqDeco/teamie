// Package app implements the application use cases for the Sandbox bounded
// context. It orchestrates domain entities, validation policies, and outbound
// ports without importing HTTP, RPC, or external infrastructure libraries.
package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"teamie/services/sandbox/internal/domain"
)

const OutputLimit = domain.OutputLimit
const MemoryLimit = domain.MemoryLimit
const ManagedLabel = domain.ManagedLabel

var errContainerMissing = domain.ErrContainerMissing

type Job = domain.Job
type Result = domain.Result
type Engine = domain.Engine
type ManagedEngine = domain.ManagedEngine
type TimestampedEngine = domain.TimestampedEngine
type ContainerState = domain.ContainerState
type outputPart = domain.OutputPart
type Receipt = domain.Receipt

func flattenOutput(parts []outputPart) (string, string) {
	return domain.FlattenOutput(parts)
}

func captureFrames(r *os.File, _ any) {} // unused placeholder

func operationContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 10*time.Second)
}

// SuperviseHook is an optional observability hook invoked when a background task supervision goroutine starts and finishes.
type SuperviseHook func(ctx context.Context, agentID, runID, taskID string, recovering bool) (context.Context, func(state, code string, outputBytes int64, ctxErr error))

// Service coordinates Sandbox use cases, slot admission, workspace mutations, and durable task supervision.
type Service struct {
	Engine                                 Engine
	Storage                                domain.WorkspaceStorage
	Token, Image, WorkspaceRoot, StateRoot string
	slots                                  chan struct{}
	mu                                     sync.Mutex
	active                                 map[string]context.CancelFunc
	lock                                   *os.File
	canceled                               map[string]time.Time
	TaskMu                                 sync.Mutex
	Tasks                                  map[string]*taskReceipt
	observers                              map[string]context.CancelFunc
	taskWG                                 sync.WaitGroup
	closed                                 bool
	FileMu                                 sync.Mutex
	OutputNow                              func() time.Time
	SuperviseHook                          SuperviseHook
}

// New constructs a new Sandbox application Service and acquires the exclusive supervisor state lock.
func New(engine Engine, token, image, workspace, state string) (*Service, error) {
	if !domain.ValidRunnerImage(image) {
		return nil, errors.New("SANDBOX_RUNNER_IMAGE must be an immutable platform image digest")
	}
	for _, pair := range [][2]string{{workspace, state}, {state, workspace}} {
		rel, err := filepath.Rel(pair[0], pair[1])
		if err != nil || rel == "." || rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
			return nil, errors.New("workspace and receipt roots must be disjoint")
		}
	}
	for _, p := range []string{workspace, state} {
		if !filepath.IsAbs(p) || filepath.Clean(p) != p || strings.Contains(p, ":") {
			return nil, errors.New("sandbox roots must be canonical absolute paths")
		}
		if e := os.MkdirAll(p, 0700); e != nil {
			return nil, e
		}
		info, e := os.Lstat(p)
		if e != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return nil, errors.New("sandbox root is not a directory")
		}
		if e = os.Chmod(p, 0700); e != nil {
			return nil, e
		}
	}
	// Compare physical roots as well: aliases in ancestor directories must not
	// put trusted receipts inside the child-accessible workspace mount.
	var resolveErr error
	workspace, resolveErr = filepath.EvalSymlinks(workspace)
	if resolveErr != nil {
		return nil, resolveErr
	}
	state, resolveErr = filepath.EvalSymlinks(state)
	if resolveErr != nil {
		return nil, resolveErr
	}
	for _, pair := range [][2]string{{workspace, state}, {state, workspace}} {
		rel, e := filepath.Rel(pair[0], pair[1])
		if e != nil || rel == "." || rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
			return nil, errors.New("workspace and receipt roots must be physically disjoint")
		}
	}
	lock, e := os.OpenFile(filepath.Join(state, "supervisor.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if e != nil {
		return nil, e
	}
	if e = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); e != nil {
		lock.Close()
		return nil, errors.New("another sandbox supervisor owns this state")
	}
	return &Service{
		Engine:        engine,
		Token:         token,
		Image:         image,
		WorkspaceRoot: workspace,
		StateRoot:     state,
		slots:         make(chan struct{}, 1),
		active:        make(map[string]context.CancelFunc),
		canceled:      make(map[string]time.Time),
		lock:          lock,
		Tasks:         make(map[string]*taskReceipt),
		observers:     make(map[string]context.CancelFunc),
	}, nil
}

// Close detaches background task observers and releases the supervisor lock.
func (s *Service) Close() {
	s.TaskMu.Lock()
	s.closed = true
	for _, cancel := range s.observers {
		cancel()
	}
	s.TaskMu.Unlock()
	s.taskWG.Wait() // Close detaches observers; durable container children continue.
	s.mu.Lock()
	for _, cancel := range s.active {
		cancel()
	}
	s.mu.Unlock()
	if s.lock != nil {
		_ = syscall.Flock(int(s.lock.Fd()), syscall.LOCK_UN)
		_ = s.lock.Close()
	}
}

func digest(v string) string { return domain.Digest(v) }
func validID(v string) bool  { return domain.ValidID(v) }

// Ready checks whether the underlying container engine is ready for the configured runner image.
func (s *Service) Ready(ctx context.Context) error {
	return s.Engine.Ready(ctx, s.Image)
}

// Authorize checks the service bearer token in constant time.
func (s *Service) Authorize(serviceToken string) bool {
	return domain.EqualSecret(serviceToken, s.Token)
}

// Ping handles readiness pings from authorized callers.
func (s *Service) Ping(ctx context.Context, r *domain.PingRequest) (*domain.PingResponse, error) {
	return &domain.PingResponse{Ready: r != nil && domain.EqualSecret(r.GetServiceToken(), s.Token) && s.Ready(ctx) == nil}, nil
}

func response(r Result) *domain.SandboxExecResponse {
	res := &domain.SandboxExecResponse{Ok: r.ErrorCode == "", ExitCode: r.ExitCode, Stdout: r.Stdout, Stderr: r.Stderr, Truncated: r.Truncated}
	if r.ErrorCode != "" {
		res.ErrorCode = &r.ErrorCode
		message := domain.ExecErrorMessage(r.ErrorCode)
		res.ErrorMessage = &message
	}
	return res
}

func failure(code string) *domain.SandboxExecResponse {
	return response(Result{ExitCode: -1, ErrorCode: code})
}

// ReceiptPath returns the durable unary execution receipt path for (agentID, requestID).
func (s *Service) ReceiptPath(agentID, requestID string) string {
	return filepath.Join(s.StateRoot, digest(agentID+"\x00"+requestID)+".json")
}

func writeReceipt(path string, r Receipt) error { return writeAtomicJSON(path, r) }
func writeAtomicJSON(path string, r any) error {
	return domain.DefaultWorkspaceStorage.WriteAtomicJSON(path, r)
}

// ValidateExecRequest validates an execution request without running it.
func (s *Service) ValidateExecRequest(r *domain.SandboxExecRequest) string {
	if r == nil || !domain.EqualSecret(r.ServiceToken, s.Token) {
		return "unauthorized"
	}
	if !validID(r.AgentId) || !validID(r.RunId) || !validID(r.RequestId) || len(r.Command) == 0 || len(r.Command) > 16384 || strings.ContainsRune(r.Command, 0) || r.TimeoutSeconds < 0 || r.TimeoutSeconds > 60 {
		return "invalid_request"
	}
	return ""
}

// Exec executes or replays a unary sandbox command.
func (s *Service) Exec(ctx context.Context, r *domain.SandboxExecRequest) (*domain.SandboxExecResponse, error) {
	if code := s.ValidateExecRequest(r); code != "" {
		return failure(code), nil
	}
	executionID := r.RunId
	if r.ExecutionId != nil {
		executionID = r.GetExecutionId()
		if !validID(executionID) {
			return failure("invalid_request"), nil
		}
	}
	timeout := r.TimeoutSeconds
	if timeout == 0 {
		timeout = 30
	}
	fp := digest(fmt.Sprintf("%s\x00%s\x00%d", r.RunId, r.Command, timeout))
	path := s.ReceiptPath(r.AgentId, r.RequestId)
	select {
	case s.slots <- struct{}{}:
		defer func() { <-s.slots }()
	default:
		return failure("sandbox_busy"), nil
	}
	if b, e := os.ReadFile(path); e == nil {
		var old Receipt
		if json.Unmarshal(b, &old) != nil {
			return failure("execution_unknown"), nil
		}
		if old.Fingerprint != fp {
			return failure("request_conflict"), nil
		}
		if old.State != "complete" {
			return failure("execution_unknown"), nil
		}
		return response(old.Result), nil
	} else if !os.IsNotExist(e) {
		return failure("receipt_unavailable"), nil
	}
	entries, readErr := os.ReadDir(s.StateRoot)
	if readErr != nil {
		return failure("receipt_unavailable"), nil
	}
	if len(entries) >= 10001 {
		return failure("sandbox_capacity"), nil
	}
	var filesystem syscall.Statfs_t
	if syscall.Statfs(s.WorkspaceRoot, &filesystem) != nil || filesystem.Bavail <= 0 || uint64(filesystem.Bavail)*uint64(filesystem.Bsize) < 2*1024*1024*1024 {
		return failure("workspace_capacity"), nil
	}
	agentHash := digest(r.AgentId)
	workspace := filepath.Join(s.WorkspaceRoot, agentHash)
	if e := os.Mkdir(workspace, 0777); e != nil && !os.IsExist(e) {
		return failure("workspace_unavailable"), nil
	}
	info, e := os.Lstat(workspace)
	if e != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return failure("workspace_unavailable"), nil
	}
	if e = os.Chmod(workspace, 0777); e != nil {
		return failure("workspace_unavailable"), nil
	}
	intent := Receipt{Fingerprint: fp, State: "running", UpdatedAt: time.Now().UTC()}
	if writeReceipt(path, intent) != nil {
		return failure("receipt_unavailable"), nil
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
	defer cancel()
	index := r.AgentId + "/" + executionID
	s.mu.Lock()
	s.active[index] = cancel
	if at, ok := s.canceled[index]; ok && time.Since(at) < 2*time.Minute {
		cancel()
	}
	s.mu.Unlock()
	defer func() { s.mu.Lock(); delete(s.active, index); s.mu.Unlock() }()
	monitorDone := make(chan struct{})
	monitorStopped := make(chan struct{})
	var diskLimited bool
	var monitorMu sync.Mutex
	go func() {
		defer close(monitorStopped)
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-monitorDone:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				var fs syscall.Statfs_t
				if syscall.Statfs(s.WorkspaceRoot, &fs) != nil || fs.Bavail <= 0 || uint64(fs.Bavail)*uint64(fs.Bsize) < 2*1024*1024*1024 || !workspaceWithinQuota(workspace) {
					monitorMu.Lock()
					diskLimited = true
					monitorMu.Unlock()
					cancel()
					return
				}
			}
		}
	}()
	result, e := s.Engine.Run(ctx, Job{Name: "teamie-job-" + digest(r.AgentId+"/"+r.RequestId)[:32], Image: s.Image, Workspace: workspace, AgentHash: agentHash, Command: r.Command, Timeout: int(timeout)})
	close(monitorDone)
	<-monitorStopped
	monitorMu.Lock()
	limited := diskLimited
	monitorMu.Unlock()
	if e != nil {
		return failure("execution_unknown"), nil
	}
	if limited {
		result.ErrorCode = "execution_disk_limit"
		result.ExitCode = -1
	}
	intent.State = "complete"
	intent.Result = result
	intent.UpdatedAt = time.Now().UTC()
	if writeReceipt(path, intent) != nil {
		return failure("execution_unknown"), nil
	}
	return response(result), nil
}

// Cancel cancels an active foreground execution attempt.
func (s *Service) Cancel(_ context.Context, r *domain.CancelRequest) (*domain.Ack, error) {
	if r == nil || !domain.EqualSecret(r.ServiceToken, s.Token) || !validID(r.AgentId) || !validID(r.RunId) {
		return &domain.Ack{Ok: false}, nil
	}
	s.mu.Lock()
	index := r.AgentId + "/" + r.RunId
	now := time.Now()
	for key, at := range s.canceled {
		if now.Sub(at) > 2*time.Minute {
			delete(s.canceled, key)
		}
	}
	if len(s.canceled) >= 256 {
		var oldestKey string
		var oldest time.Time
		for key, at := range s.canceled {
			if oldestKey == "" || at.Before(oldest) {
				oldestKey = key
				oldest = at
			}
		}
		delete(s.canceled, oldestKey)
	}
	s.canceled[index] = now
	cancel := s.active[index]
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	return &domain.Ack{Ok: true}, nil
}
