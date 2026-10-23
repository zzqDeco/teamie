package sandbox

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"teamie/contracts/gen/agent/kitex_gen/agent"
	"testing"
	"time"
)

const token = "sandbox-fixture-token-long-enough"

var image = "ghcr.io/zzqdeco/teamie-sandbox-runner@sha256:" + strings.Repeat("a", 64)

type fakeEngine struct {
	calls   atomic.Int32
	block   bool
	started chan struct{}
}

func (f *fakeEngine) Ready(context.Context, string) error { return nil }
func (f *fakeEngine) Cleanup(context.Context) error       { return nil }
func (f *fakeEngine) Run(ctx context.Context, j Job) (Result, error) {
	f.calls.Add(1)
	if f.started != nil {
		close(f.started)
	}
	if f.block {
		<-ctx.Done()
		return Result{ExitCode: -1, ErrorCode: "execution_canceled"}, nil
	}
	return Result{ExitCode: 0, Stdout: j.Workspace}, nil
}
func fixture(t *testing.T, e Engine) *Service {
	t.Helper()
	root := t.TempDir()
	s, err := New(e, token, image, filepath.Join(root, "workspaces"), filepath.Join(root, "state"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	return s
}
func request() *agent.SandboxExecRequest {
	return &agent.SandboxExecRequest{ServiceToken: token, AgentId: "agent-a", RunId: "run-a", RequestId: "request-a", Command: "pwd", TimeoutSeconds: 30}
}
func TestReceiptIdempotenceConflictAndCrashRecovery(t *testing.T) {
	engine := &fakeEngine{}
	s := fixture(t, engine)
	r := request()
	first, _ := s.Exec(context.Background(), r)
	again, _ := s.Exec(context.Background(), r)
	if !first.Ok || !again.Ok || first.Stdout != again.Stdout || engine.calls.Load() != 1 {
		t.Fatal(first, again)
	}
	r.Command = "side-effect"
	conflict, _ := s.Exec(context.Background(), r)
	if conflict.GetErrorCode() != "request_conflict" || engine.calls.Load() != 1 {
		t.Fatal(conflict)
	}
	r.RequestId = "interrupted"
	path := s.receiptPath(r.AgentId, r.RequestId)
	fp := digest(r.RunId + "\x00" + r.Command + "\x0030")
	if e := writeReceipt(path, Receipt{Fingerprint: fp, State: "running"}); e != nil {
		t.Fatal(e)
	}
	unknown, _ := s.Exec(context.Background(), r)
	if unknown.GetErrorCode() != "execution_unknown" || engine.calls.Load() != 1 {
		t.Fatal(unknown)
	}
	mode, _ := os.Stat(path)
	if mode.Mode().Perm() != 0600 {
		t.Fatal(mode.Mode())
	}
	if strings.HasPrefix(path, first.Stdout+string(os.PathSeparator)) {
		t.Fatal("receipt is accessible inside workspace")
	}
}
func TestWorkspaceIsolationAndAuthentication(t *testing.T) {
	e := &fakeEngine{}
	s := fixture(t, e)
	r := request()
	r.ServiceToken = "invalid"
	bad, _ := s.Exec(context.Background(), r)
	if bad.GetErrorCode() != "unauthorized" {
		t.Fatal(bad)
	}
	r.ServiceToken = token
	a, _ := s.Exec(context.Background(), r)
	r.AgentId = "../../escape"
	r.RequestId = "other"
	b, _ := s.Exec(context.Background(), r)
	if a.Stdout == b.Stdout || !strings.HasPrefix(b.Stdout, s.WorkspaceRoot+string(os.PathSeparator)) || strings.Contains(b.Stdout, "..") {
		t.Fatal(a, b)
	}
	r.TimeoutSeconds = 61
	bad, _ = s.Exec(context.Background(), r)
	if bad.GetErrorCode() != "invalid_request" {
		t.Fatal(bad)
	}
	if e.calls.Load() != 2 {
		t.Fatal(e.calls.Load())
	}
}
func TestCancellationSingleConcurrencyAndLock(t *testing.T) {
	e := &fakeEngine{block: true, started: make(chan struct{})}
	s := fixture(t, e)
	done := make(chan *agent.SandboxExecResponse, 1)
	go func() { r, _ := s.Exec(context.Background(), request()); done <- r }()
	<-e.started
	busy, _ := s.Exec(context.Background(), request())
	if busy.GetErrorCode() != "sandbox_busy" {
		t.Fatal(busy)
	}
	if second, err := New(e, token, image, s.WorkspaceRoot, s.StateRoot); err == nil {
		second.Close()
		t.Fatal("second supervisor acquired same receipt state")
	}
	ack, _ := s.Cancel(context.Background(), &agent.CancelRequest{ServiceToken: token, AgentId: "agent-a", RunId: "run-a"})
	if !ack.Ok {
		t.Fatal(ack)
	}
	select {
	case r := <-done:
		if r.GetErrorCode() != "execution_canceled" {
			t.Fatal(r)
		}
	case <-time.After(time.Second):
		t.Fatal("job did not cancel")
	}
}
func TestChildPolicyAndOutputBounds(t *testing.T) {
	config := containerConfig(Job{Image: image, Workspace: "/fixed/agent", Command: "python3 -c 'print(1)'"})
	b, _ := json.Marshal(config)
	text := string(b)
	for _, required := range []string{`"NetworkMode":"none"`, `"ReadonlyRootfs":true`, `"CapDrop":["ALL"]`, `"User":"10001:10001"`, `"Memory":134217728`, `"MemorySwap":134217728`, `"PidsLimit":32`, `"NanoCpus":500000000`, `no-new-privileges:true`} {
		if !strings.Contains(text, required) {
			t.Fatal(required, text)
		}
	}
	if strings.Contains(text, "docker.sock") || strings.Contains(text, token) {
		t.Fatal("job contains host credential or socket")
	}
	var wire bytes.Buffer
	frame := func(stream byte, data string) {
		header := make([]byte, 8)
		header[0] = stream
		binary.BigEndian.PutUint32(header[4:], uint32(len(data)))
		wire.Write(header)
		wire.WriteString(data)
	}
	frame(1, strings.Repeat("a", 40000))
	frame(2, strings.Repeat("b", 40000))
	out, errout, truncated, e := demux(&wire, OutputLimit)
	if e != nil || !truncated || len(out)+len(errout) != OutputLimit {
		t.Fatal(len(out), len(errout), truncated, e)
	}
}

func TestExecutionAttemptCancellationKeepsDurableReceipts(t *testing.T) {
	engine := &fakeEngine{}
	s := fixture(t, engine)
	firstAttempt, secondAttempt := "run-a:attempt-1", "run-a:attempt-2"
	r := request()
	r.ExecutionId = &firstAttempt
	completed, _ := s.Exec(context.Background(), r)
	if !completed.Ok || engine.calls.Load() != 1 {
		t.Fatal(completed)
	}
	ack, _ := s.Cancel(context.Background(), &agent.CancelRequest{ServiceToken: token, AgentId: r.AgentId, RunId: firstAttempt})
	if !ack.Ok {
		t.Fatal(ack)
	}
	// A completed durable request survives cancellation of its prior execution attempt.
	r.ExecutionId = &secondAttempt
	replay, _ := s.Exec(context.Background(), r)
	if !replay.Ok || replay.Stdout != completed.Stdout || engine.calls.Load() != 1 {
		t.Fatal(replay, engine.calls.Load())
	}
	// A new tool request in the retry must not inherit the old attempt's tombstone.
	r.RequestId = "request-new"
	next, _ := s.Exec(context.Background(), r)
	if !next.Ok || engine.calls.Load() != 2 {
		t.Fatal(next, engine.calls.Load())
	}
}

func TestCanceledExecutionReceiptIsNotRerunInLaterAttempt(t *testing.T) {
	engine := &fakeEngine{block: true, started: make(chan struct{})}
	s := fixture(t, engine)
	firstAttempt, secondAttempt := "run-a:attempt-1", "run-a:attempt-2"
	r := request()
	r.ExecutionId = &firstAttempt
	done := make(chan *agent.SandboxExecResponse, 1)
	go func() { res, _ := s.Exec(context.Background(), r); done <- res }()
	<-engine.started
	_, _ = s.Cancel(context.Background(), &agent.CancelRequest{ServiceToken: token, AgentId: r.AgentId, RunId: firstAttempt})
	canceled := <-done
	if canceled.GetErrorCode() != "execution_canceled" {
		t.Fatal(canceled)
	}
	r.ExecutionId = &secondAttempt
	replay, _ := s.Exec(context.Background(), r)
	if replay.GetErrorCode() != "execution_canceled" || engine.calls.Load() != 1 {
		t.Fatal(replay, engine.calls.Load())
	}
	engine.block = false
	engine.started = nil
	r.RequestId = "request-next-attempt"
	next, _ := s.Exec(context.Background(), r)
	if !next.Ok || engine.calls.Load() != 2 {
		t.Fatal(next, engine.calls.Load())
	}
	empty := ""
	r.ExecutionId = &empty
	r.RequestId = "invalid-attempt"
	invalid, _ := s.Exec(context.Background(), r)
	if invalid.GetErrorCode() != "invalid_request" || engine.calls.Load() != 2 {
		t.Fatal(invalid)
	}
}
