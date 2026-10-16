package sandbox

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"teamie/agent-protocol/kitex_gen/agent"
)

// Managed-engine fixture models daemon-owned processes independently of the
// supervisor and RPC connections. It exercises restart adoption without Docker.
type fixtureContainer struct {
	state   ContainerState
	wire    []byte
	changed chan struct{}
}
type managedFixture struct {
	mu                              sync.Mutex
	containers                      map[string]*fixtureContainer
	creates, starts, kills, removes int
	lostCreateReply                 bool
}

func newManagedFixture() *managedFixture {
	return &managedFixture{containers: map[string]*fixtureContainer{}}
}
func (f *managedFixture) Ready(context.Context, string) error { return nil }
func (f *managedFixture) Cleanup(context.Context) error {
	return errors.New("indiscriminate cleanup forbidden")
}
func (f *managedFixture) Run(context.Context, Job) (Result, error) {
	return Result{}, errors.New("legacy exec not used")
}
func (f *managedFixture) Create(_ context.Context, j Job) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.containers[j.Name] != nil {
		return "", errors.New("already exists")
	}
	f.creates++
	c := &fixtureContainer{changed: make(chan struct{})}
	c.state.ID = j.Name
	c.state.Config.Labels = map[string]string{ManagedLabel: "true", "io.teamie.agent": j.AgentHash, "io.teamie.task": j.TaskHash}
	c.state.State.Status = "created"
	f.containers[j.Name] = c
	if f.lostCreateReply {
		return "", errors.New("fixture transport loss after create")
	}
	return j.Name, nil
}
func (f *managedFixture) signal(c *fixtureContainer) {
	close(c.changed)
	c.changed = make(chan struct{})
}
func (f *managedFixture) StartContainer(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	c := f.containers[id]
	if c == nil {
		return errContainerMissing
	}
	if c.state.State.Running {
		return errors.New("duplicate start")
	}
	f.starts++
	c.state.State.Running = true
	c.state.State.Status = "running"
	f.signal(c)
	return nil
}
func (f *managedFixture) Inspect(_ context.Context, id string) (ContainerState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if c := f.containers[id]; c != nil {
		return c.state, nil
	}
	return ContainerState{}, errContainerMissing
}
func (f *managedFixture) Attach(ctx context.Context, id string, logs bool) (io.ReadCloser, error) {
	f.mu.Lock()
	c := f.containers[id]
	if c == nil {
		f.mu.Unlock()
		return nil, errContainerMissing
	}
	offset := 0
	if !logs {
		offset = len(c.wire)
	}
	f.mu.Unlock()
	r, w := io.Pipe()
	go func() { <-ctx.Done(); r.CloseWithError(ctx.Err()) }()
	go func() {
		defer w.Close()
		for {
			f.mu.Lock()
			wire := append([]byte(nil), c.wire[offset:]...)
			offset = len(c.wire)
			finished := c.state.State.Status == "exited"
			changed := c.changed
			f.mu.Unlock()
			if len(wire) > 0 {
				if _, e := w.Write(wire); e != nil {
					return
				}
			}
			if finished {
				return
			}
			select {
			case <-ctx.Done():
				return
			case <-changed:
			}
		}
	}()
	return r, nil
}
func (f *managedFixture) Kill(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	c := f.containers[id]
	if c == nil {
		return errContainerMissing
	}
	if !c.state.State.Running && c.state.State.Status == "exited" {
		return errors.New("container already finished")
	}
	f.kills++
	c.state.State.Running = false
	c.state.State.Status = "exited"
	c.state.State.ExitCode = 137
	f.signal(c)
	return nil
}
func (f *managedFixture) Remove(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.containers, id)
	f.removes++
	return nil
}
func (f *managedFixture) output(id string, stream byte, value string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c := f.containers[id]
	if c == nil {
		panic("fixture container missing")
	}
	header := make([]byte, 8)
	header[0] = stream
	binary.BigEndian.PutUint32(header[4:], uint32(len(value)))
	c.wire = append(c.wire, header...)
	c.wire = append(c.wire, value...)
	f.signal(c)
}
func (f *managedFixture) finish(id string, exit int32) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c := f.containers[id]
	c.state.State.Status = "exited"
	c.state.State.Running = false
	c.state.State.ExitCode = exit
	f.signal(c)
}
func startRequest() *agent.SandboxStartRequest {
	return &agent.SandboxStartRequest{ServiceToken: token, AgentId: "agent-a", RunId: "durable-run", TaskId: "native-task", Command: "long task"}
}
func taskRequest() *agent.SandboxTaskRequest {
	return &agent.SandboxTaskRequest{ServiceToken: token, AgentId: "agent-a", TaskId: "native-task"}
}
func awaitTask(t *testing.T, s *Service, predicate func(*agent.SandboxTaskResponse) bool) *agent.SandboxTaskResponse {
	t.Helper()
	deadline := time.Now().Add(4 * time.Second)
	var res *agent.SandboxTaskResponse
	for time.Now().Before(deadline) {
		res, _ = s.Get(context.Background(), taskRequest())
		if predicate(res) {
			return res
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("task never reached expected state: %+v", res)
	return nil
}
func running(r *agent.SandboxTaskResponse) bool   { return r.State == "running" }
func completed(r *agent.SandboxTaskResponse) bool { return r.State == "completed" }
func taskName() string                            { return "teamie-task-" + taskKey("agent-a", "native-task")[:32] }

func TestTaskLookupDistinguishesMissingReceiptFromAcceptedQueuedTask(t *testing.T) {
	engine := newManagedFixture()
	s := fixture(t, engine)
	request := taskRequest()
	missing, _ := s.Get(context.Background(), request)
	if missing.Ok || missing.State != "not_started" || missing.GetErrorCode() != "task_not_found" {
		t.Fatal("missing receipt was reported as an accepted task")
	}
	output, _ := s.ReadOutput(context.Background(), request)
	if output.Ok || output.GetErrorCode() != "task_not_found" {
		t.Fatal("missing output lookup lost task_not_found")
	}
	cancel, _ := s.CancelTask(context.Background(), request)
	if !cancel.Ok || cancel.State != "not_started" {
		t.Fatal("cancel of missing task must remain idempotent")
	}
	queued := &taskReceipt{Version: 1, AgentID: request.AgentId, TaskID: request.TaskId, State: "not_started"}
	if e := writeAtomicJSON(s.taskPath(taskKey(request.AgentId, request.TaskId)), queued); e != nil {
		t.Fatal(e)
	}
	known, _ := s.Get(context.Background(), request)
	if !known.Ok || known.State != "not_started" || known.GetErrorCode() != "" {
		t.Fatal("accepted queued task was confused with a missing receipt")
	}
	engine.mu.Lock()
	defer engine.mu.Unlock()
	if engine.creates != 0 || engine.starts != 0 {
		t.Fatal("task lookup started an execution")
	}
}

func TestAsyncTaskDetachRecoveryIdempotenceAndOutputCursor(t *testing.T) {
	engine := newManagedFixture()
	engine.lostCreateReply = true
	s := fixture(t, engine)
	r := startRequest()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	res, e := s.Start(ctx, r)
	if e != nil || !res.Ok {
		t.Fatal(res, e)
	}
	awaitTask(t, s, running)
	engine.output(taskName(), 1, "a")
	engine.output(taskName(), 2, "b")
	engine.output(taskName(), 1, "c")
	awaitTask(t, s, func(r *agent.SandboxTaskResponse) bool { return r.GetNextOffset() == 3 })
	offset, limit := int64(1), int32(1)
	read := taskRequest()
	read.Offset = &offset
	read.Limit = &limit
	piece, _ := s.ReadOutput(context.Background(), read)
	if piece.GetStdout() != "" || piece.GetStderr() != "b" || piece.GetNextOffset() != 2 {
		t.Fatal(piece)
	}
	s.Close()
	engine.mu.Lock()
	if engine.kills != 0 || engine.starts != 1 {
		t.Fatal("RPC/supervisor lifetime killed or restarted process", engine.kills, engine.starts)
	}
	engine.mu.Unlock()
	reopened, e := New(engine, token, image, s.WorkspaceRoot, s.StateRoot)
	if e != nil {
		t.Fatal(e)
	}
	defer reopened.Close()
	if e = reopened.Recover(context.Background()); e != nil {
		t.Fatal(e)
	}
	awaitTask(t, reopened, running)
	replay, _ := reopened.Start(context.Background(), r)
	if !replay.Ok {
		t.Fatal(replay)
	}
	engine.output(taskName(), 1, "d")
	engine.finish(taskName(), 0)
	result := awaitTask(t, reopened, completed)
	if result.GetStdout() != "acd" || result.GetStderr() != "b" || result.GetNextOffset() != 4 {
		t.Fatal(result)
	}
	engine.mu.Lock()
	if engine.starts != 1 || engine.creates != 1 {
		t.Fatal(engine.starts, engine.creates)
	}
	engine.mu.Unlock()
	r.Command = "different side effect"
	conflict, _ := reopened.Start(context.Background(), r)
	if conflict.GetErrorCode() != "request_conflict" {
		t.Fatal(conflict)
	}
	path := reopened.taskPath(taskKey(r.AgentId, r.TaskId))
	info, e := os.Stat(path)
	if e != nil || info.Mode().Perm() != 0600 || strings.HasPrefix(path, reopened.WorkspaceRoot+string(os.PathSeparator)) {
		t.Fatal("receipt protection", info, e)
	}
}

func TestAsyncTaskCancelTimeoutBoundsAndUnknownNeverRerun(t *testing.T) {
	for _, test := range []struct {
		name     string
		timeout  int32
		overflow bool
		expected string
	}{{"cancel", 0, false, "execution_canceled"}, {"command deadline", 1, false, "execution_timeout"}, {"output bound", 0, true, "execution_output_limit"}} {
		t.Run(test.name, func(t *testing.T) {
			engine := newManagedFixture()
			s := fixture(t, engine)
			r := startRequest()
			r.TimeoutSeconds = &test.timeout
			res, _ := s.Start(context.Background(), r)
			if !res.Ok {
				t.Fatal(res)
			}
			awaitTask(t, s, running)
			if test.overflow {
				engine.output(taskName(), 1, strings.Repeat("x", OutputLimit+5))
			} else if test.timeout == 0 {
				res, _ = s.CancelTask(context.Background(), taskRequest())
				if !res.Ok {
					t.Fatal(res)
				}
			}
			result := awaitTask(t, s, func(r *agent.SandboxTaskResponse) bool { return terminalTask(r.State) })
			if result.GetErrorCode() != test.expected || len(result.GetStdout())+len(result.GetStderr()) > OutputLimit {
				t.Fatal(result)
			}
			for {
				engine.mu.Lock()
				removed := engine.removes > 0
				engine.mu.Unlock()
				if removed {
					break
				}
				time.Sleep(time.Millisecond)
			}
			replay, _ := s.Start(context.Background(), r)
			if replay.State != result.State {
				t.Fatal(replay)
			}
			engine.mu.Lock()
			defer engine.mu.Unlock()
			if engine.starts != 1 {
				t.Fatal("canceled logical command rerun", engine.starts)
			}
		})
	}
	engine := newManagedFixture()
	s := fixture(t, engine)
	r := startRequest()
	seconds := int32(86400)
	r.TimeoutSeconds = &seconds
	res, _ := s.Start(context.Background(), r)
	if !res.Ok {
		t.Fatal("long command inherited legacy60s", res)
	}
	awaitTask(t, s, running)
	s.Close()
	engine.Remove(context.Background(), taskName())
	reopened, e := New(engine, token, image, s.WorkspaceRoot, s.StateRoot)
	if e != nil {
		t.Fatal(e)
	}
	defer reopened.Close()
	if e = reopened.Recover(context.Background()); e != nil {
		t.Fatal(e)
	}
	unknown := awaitTask(t, reopened, func(r *agent.SandboxTaskResponse) bool { return r.State == "unknown" })
	if unknown.GetErrorCode() != "execution_unknown" {
		t.Fatal(unknown)
	}
	_, _ = reopened.Start(context.Background(), r)
	engine.mu.Lock()
	defer engine.mu.Unlock()
	if engine.starts != 1 {
		t.Fatal("missing uncertain execution restarted")
	}
}

func TestWorkspaceOperationsPathSymlinkAndFileIsolation(t *testing.T) {
	s := fixture(t, &fakeEngine{})
	call := func(operation string, args any) *agent.SandboxWorkspaceResponse {
		t.Helper()
		b, _ := json.Marshal(args)
		res, _ := s.Workspace(context.Background(), &agent.SandboxWorkspaceRequest{ServiceToken: token, AgentId: "a", Operation: operation, ArgsJson: string(b)})
		return res
	}
	write := call("write", map[string]any{"path": "/workspace/dir/file.txt", "content": "one\ntwo two\nthree\n"})
	if !write.Ok {
		t.Fatal(write)
	}
	read := call("read", map[string]any{"path": "dir/file.txt", "offset": 2, "limit": 1})
	if !read.Ok || !strings.Contains(read.GetDataJson(), `two two\n`) {
		t.Fatal(read)
	}
	badEdit := call("edit", map[string]any{"path": "dir/file.txt", "old": "two", "new": "TWO"})
	if badEdit.GetErrorCode() != "file_conflict" {
		t.Fatal(badEdit)
	}
	edited := call("edit", map[string]any{"path": "dir/file.txt", "old": "two", "new": "TWO", "replaceAll": true})
	if !edited.Ok {
		t.Fatal(edited)
	}
	for _, op := range []string{"ls", "glob", "grep"} {
		args := map[string]any{"path": "/workspace"}
		if op == "ls" {
			args["path"] = "/workspace/dir"
		}
		if op == "glob" {
			args["pattern"] = "**/*.txt"
		}
		if op == "grep" {
			args["pattern"] = "TWO"
		}
		res := call(op, args)
		if !res.Ok || !strings.Contains(res.GetDataJson(), "file.txt") {
			t.Fatal(op, res)
		}
	}
	outside := filepath.Join(t.TempDir(), "private.txt")
	if e := os.WriteFile(outside, []byte("secret outside workspace"), 0600); e != nil {
		t.Fatal(e)
	}
	workspace, _ := s.workspace("a")
	if e := os.Symlink(outside, filepath.Join(workspace, "escape")); e != nil {
		t.Fatal(e)
	}
	if e := os.Symlink(filepath.Dir(outside), filepath.Join(workspace, "escape-dir")); e != nil {
		t.Fatal(e)
	}
	for _, path := range []string{"../private.txt", "/etc/passwd", "/workspace/../private.txt", "escape", "escape-dir/private.txt"} {
		for _, op := range []string{"read", "write", "edit"} {
			res := call(op, map[string]any{"path": path, "content": "bad", "old": "secret", "new": "bad"})
			if res.Ok {
				t.Fatal("escaped root", op, path)
			}
		}
	}
	other, _ := s.Workspace(context.Background(), &agent.SandboxWorkspaceRequest{ServiceToken: token, AgentId: "b", Operation: "read", ArgsJson: `{"path":"dir/file.txt"}`})
	if other.Ok {
		t.Fatal("cross agent file exposed")
	}
	b, _ := os.ReadFile(outside)
	if string(b) != "secret outside workspace" {
		t.Fatal("outside file changed")
	}
	large := call("write", map[string]any{"path": "large", "content": strings.Repeat("x", maxFileBytes+1)})
	if large.Ok {
		t.Fatal("unbounded file write")
	}
}
