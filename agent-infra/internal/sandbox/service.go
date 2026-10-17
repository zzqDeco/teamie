package sandbox

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"

	"teamie/agent-infra/internal/api"
	"teamie/agent-protocol/kitex_gen/agent"
)

const (
	acrRegionPattern    = `[a-z]{2}(?:-[a-z0-9]+)+`
	acrInstancePattern  = `[a-z0-9](?:[a-z0-9-]*[a-z0-9])?`
	acrNamespacePattern = `[a-z0-9]+(?:[._-][a-z0-9]+)*`
	acrRegistryPattern  = `(?:registry(?:-vpc)?\.` + acrRegionPattern + `\.aliyuncs\.com|crpi-[a-z0-9]+(?:-vpc)?\.` + acrRegionPattern + `\.personal\.cr\.aliyuncs\.com|` + acrInstancePattern + `-registry(?:-vpc)?\.` + acrRegionPattern + `\.cr\.aliyuncs\.com)`
)

var imagePattern = regexp.MustCompile(`^(?:ghcr\.io/zzqdeco/teamie-sandbox-runner@|` + acrRegistryPattern + `/` + acrNamespacePattern + `/teamie-sandbox-runner@)?sha256:[0-9a-f]{64}$`)

func validRunnerImage(image string) bool {
	if !imagePattern.MatchString(image) {
		return false
	}
	// Bare IDs support the disposable CI launcher; GHCR digests preserve existing
	// release recovery. ACR names remain one namespace and a valid DNS hostname.
	if strings.HasPrefix(image, "sha256:") || strings.HasPrefix(image, "ghcr.io/") {
		return true
	}
	parts := strings.SplitN(image, "/", 3)
	if len(parts[1]) > 63 {
		return false
	}
	for _, label := range strings.Split(parts[0], ".") {
		if len(label) > 63 {
			return false
		}
	}
	return true
}

type Receipt struct {
	Fingerprint string    `json:"fingerprint"`
	State       string    `json:"state"`
	Result      Result    `json:"result"`
	UpdatedAt   time.Time `json:"updatedAt"`
}
type Service struct {
	Engine                                 Engine
	Token, Image, WorkspaceRoot, StateRoot string
	slots                                  chan struct{}
	mu                                     sync.Mutex
	active                                 map[string]context.CancelFunc
	lock                                   *os.File
	canceled                               map[string]time.Time
	taskMu                                 sync.Mutex
	tasks                                  map[string]*taskReceipt
	observers                              map[string]context.CancelFunc
	taskWG                                 sync.WaitGroup
	closed                                 bool
	fileMu                                 sync.Mutex
	outputNow                              func() time.Time
}

func New(engine Engine, token, image, workspace, state string) (*Service, error) {
	if !validRunnerImage(image) {
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
	return &Service{Engine: engine, Token: token, Image: image, WorkspaceRoot: workspace, StateRoot: state, slots: make(chan struct{}, 1), active: make(map[string]context.CancelFunc), canceled: make(map[string]time.Time), lock: lock, tasks: make(map[string]*taskReceipt), observers: make(map[string]context.CancelFunc)}, nil
}
func (s *Service) Close() {
	s.taskMu.Lock()
	s.closed = true
	for _, cancel := range s.observers {
		cancel()
	}
	s.taskMu.Unlock()
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
func digest(v string) string                       { b := sha256.Sum256([]byte(v)); return hex.EncodeToString(b[:]) }
func validID(v string) bool                        { return v != "" && len(v) <= 160 && !strings.ContainsAny(v, "\x00\r\n") }
func (s *Service) Ready(ctx context.Context) error { return s.Engine.Ready(ctx, s.Image) }
func (s *Service) Ping(ctx context.Context, r *agent.PingRequest) (*agent.PingResponse, error) {
	return &agent.PingResponse{Ready: r != nil && api.EqualSecret(r.GetServiceToken(), s.Token) && s.Ready(ctx) == nil}, nil
}
func response(r Result) *agent.SandboxExecResponse {
	res := &agent.SandboxExecResponse{Ok: r.ErrorCode == "", ExitCode: r.ExitCode, Stdout: r.Stdout, Stderr: r.Stderr, Truncated: r.Truncated}
	if r.ErrorCode != "" {
		res.ErrorCode = &r.ErrorCode
		message := map[string]string{"execution_unknown": "A prior execution was interrupted; its side effects must be inspected before retrying", "execution_canceled": "Execution canceled", "execution_timeout": "Execution exceeded its time limit"}[r.ErrorCode]
		if message == "" {
			message = "Execution could not be completed"
		}
		res.ErrorMessage = &message
	}
	return res
}
func failure(code string) *agent.SandboxExecResponse {
	return response(Result{ExitCode: -1, ErrorCode: code})
}
func (s *Service) receiptPath(agentID, requestID string) string {
	return filepath.Join(s.StateRoot, digest(agentID+"\x00"+requestID)+".json")
}
func writeReceipt(path string, r Receipt) error { return writeAtomicJSON(path, r) }
func writeAtomicJSON(path string, r any) error {
	b, e := json.Marshal(r)
	if e != nil {
		return e
	}
	f, e := os.CreateTemp(filepath.Dir(path), ".receipt-")
	if e != nil {
		return e
	}
	name := f.Name()
	defer os.Remove(name)
	if e = f.Chmod(0600); e == nil {
		_, e = f.Write(b)
	}
	if e == nil {
		e = f.Sync()
	}
	closeErr := f.Close()
	if e == nil {
		e = closeErr
	}
	if e == nil {
		e = os.Rename(name, path)
	}
	if e == nil {
		d, openErr := os.Open(filepath.Dir(path))
		if openErr != nil {
			return openErr
		}
		e = d.Sync()
		d.Close()
	}
	return e
}
func (s *Service) Exec(ctx context.Context, r *agent.SandboxExecRequest) (*agent.SandboxExecResponse, error) {
	if r == nil || !api.EqualSecret(r.ServiceToken, s.Token) {
		return failure("unauthorized"), nil
	}
	if !validID(r.AgentId) || !validID(r.RunId) || !validID(r.RequestId) || len(r.Command) == 0 || len(r.Command) > 16384 || strings.ContainsRune(r.Command, 0) || r.TimeoutSeconds < 0 || r.TimeoutSeconds > 60 {
		return failure("invalid_request"), nil
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
	path := s.receiptPath(r.AgentId, r.RequestId)
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
	result, e := s.Engine.Run(ctx, Job{Name: "teamie-job-" + digest(r.AgentId + "/" + r.RequestId)[:32], Image: s.Image, Workspace: workspace, AgentHash: agentHash, Command: r.Command, Timeout: int(timeout)})
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
func (s *Service) Cancel(ctx context.Context, r *agent.CancelRequest) (*agent.Ack, error) {
	if r == nil || !api.EqualSecret(r.ServiceToken, s.Token) || !validID(r.AgentId) || !validID(r.RunId) {
		return &agent.Ack{Ok: false}, nil
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
	return &agent.Ack{Ok: true}, nil
}
func (s *Service) Routes() http.Handler {
	m := http.NewServeMux()
	api.Operations(m, s.Ready)
	return m
}
