package sandbox

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"teamie/agent-protocol/kitex_gen/agent"
	"testing"
	"time"
)

// This opt-in test runs only on disposable CI Docker. It never contacts production.
func TestRealDockerExecutionSeparationAndCleanup(t *testing.T) {
	runner := os.Getenv("SANDBOX_TEST_RUNNER_IMAGE")
	if runner == "" {
		t.Skip("SANDBOX_TEST_RUNNER_IMAGE is unset")
	}
	// Deliberately seed supervisor-only fixture credentials; Docker must not inherit them.
	fixtureSecret := "sandbox-parent-private-fixture-sentinel"
	fixtureMaster := strings.Repeat("cd", 32)
	for _, name := range []string{"AGENT_RUNTIME_TOKEN", "IM_PRINCIPAL_CONTROL_TOKEN", "IM_PRINCIPAL_RUNTIME_TOKEN", "GATEWAY_RUNTIME_TOKEN", "DATABASE_URL", "POSTGRES_PASSWORD"} {
		t.Setenv(name, fixtureSecret)
	}
	t.Setenv("SANDBOX_RUNTIME_TOKEN", token)
	t.Setenv("GATEWAY_MASTER_KEY", fixtureMaster)
	docker := NewDocker("/var/run/docker.sock")
	root := t.TempDir()
	s, e := New(docker, token, image, filepath.Join(root, "workspace"), filepath.Join(root, "state"))
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	// CI builds a local immutable image ID; production only accepts the GHCR manifest digest.
	s.Image = runner
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if e = s.Ready(ctx); e != nil {
		t.Fatal(e)
	}
	r := request()
	r.Command = fmt.Sprintf(`python3 - <<'PY'
import os,socket,resource
assert int(open("/sys/fs/cgroup/memory.max").read())==134217728
assert int(open("/sys/fs/cgroup/pids.max").read())==32
quota,period=map(int,open("/sys/fs/cgroup/cpu.max").read().split())
assert quota/period==0.5
assert resource.getrlimit(resource.RLIMIT_FSIZE)==(8388608,8388608)
assert os.getuid()==10001
assert not os.path.exists('/var/run/docker.sock')
# Official Python image metadata may include public GPG_KEY; only platform
# credential names and private supervisor fixture values are forbidden.
forbidden_names = {
 'AGENT_RUNTIME_TOKEN','IM_PRINCIPAL_CONTROL_TOKEN','IM_PRINCIPAL_RUNTIME_TOKEN',
 'GATEWAY_RUNTIME_TOKEN','GATEWAY_MASTER_KEY','SANDBOX_RUNTIME_TOKEN',
 'DATABASE_URL','DATABASE_URL_TEST','TEAMIE_RUNTIME_TEST_DATABASE_URL',
 'IDENTITY_DATABASE_URL','CHAT_DATABASE_URL','AGENT_CONTROL_DATABASE_URL',
 'AGENT_RUNTIME_DATABASE_URL','GATEWAY_DATABASE_URL','DOCKER_SOCKET','DOCKER_HOST',
}

assert forbidden_names.isdisjoint(os.environ)
assert not any(k.startswith('POSTGRES_') and k.endswith('PASSWORD') for k in os.environ)
fixture_credentials = (%q,%q,%q)
assert not any(secret in value for value in os.environ.values() for secret in fixture_credentials)
try:
 open('/root-write','w').write('bad')
 raise AssertionError('root fs writable')
except OSError: pass
try:
 socket.create_connection(('1.1.1.1',443),timeout=1)
 raise AssertionError('network reachable')
except OSError: pass
open('persistent.txt','w').write('retained')
print('isolated')
PY`, token, fixtureSecret, fixtureMaster)
	first, _ := s.Exec(ctx, r)
	if !first.Ok || first.ExitCode != 0 || !strings.Contains(first.Stdout, "isolated") {
		t.Fatal(first)
	}
	r.RequestId = "read"
	r.Command = "cat persistent.txt"
	read, _ := s.Exec(ctx, r)
	if read.Stdout != "retained" {
		t.Fatal(read)
	}
	r.AgentId = "agent-b"
	r.RequestId = "separate"
	r.Command = "test ! -e persistent.txt"
	other, _ := s.Exec(ctx, r)
	if other.ExitCode != 0 {
		t.Fatal(other)
	}
	r.RequestId = "limit"
	r.Command = "python3 -c 'print(\"x\"*200000)'"
	out, _ := s.Exec(ctx, r)
	if !out.Ok || !out.Truncated || len(out.Stdout)+len(out.Stderr) > OutputLimit {
		t.Fatal(out)
	}
	r.RequestId = "timeout"
	r.Command = "sleep 10"
	r.TimeoutSeconds = 1
	timed, _ := s.Exec(ctx, r)
	if timed.GetErrorCode() != "execution_timeout" {
		t.Fatalf("timeout phase: code=%q exit=%d stdout=%q stderr=%q", timed.GetErrorCode(), timed.ExitCode, timed.Stdout, timed.Stderr)
	}
	res, e := docker.request(ctx, "GET", "/containers/json?all=true&filters=%7B%22label%22%3A%5B%22io.teamie.sandbox%3Dtrue%22%5D%7D", nil)
	if e != nil {
		t.Fatal(e)
	}
	defer res.Body.Close()
	var list []any
	if json.NewDecoder(res.Body).Decode(&list) != nil || len(list) != 0 {
		t.Fatal("ephemeral sandbox containers were not cleaned")
	}
}

// This runs in the same required disposable-Docker CI gate as isolation tests.
// Closing the supervisor models lost transport/process ownership while the
// Docker daemon retains a live command; recovery must adopt it exactly once.
func TestRealDockerAsyncSupervisorRestartAndStableTask(t *testing.T) {
	runner := os.Getenv("SANDBOX_TEST_RUNNER_IMAGE")
	if runner == "" {
		t.Skip("SANDBOX_TEST_RUNNER_IMAGE is unset; real Docker async recovery is unverified locally")
	}
	engine := NewDocker("/var/run/docker.sock")
	root := t.TempDir()
	s, e := New(engine, token, image, filepath.Join(root, "workspaces"), filepath.Join(root, "state"))
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	s.Image = runner
	r := startRequest()
	r.Command = "printf first; sleep 2; printf second; printf once >> side-effect.txt"
	res, _ := s.Start(context.Background(), r)
	if !res.Ok {
		t.Fatal(res)
	}
	awaitTask(t, s, func(r *agent.SandboxTaskResponse) bool { return r.GetStdout() == "first" })
	s.Close()
	recovered, e := New(engine, token, image, s.WorkspaceRoot, s.StateRoot)
	if e != nil {
		t.Fatal(e)
	}
	defer recovered.Close()
	recovered.Image = runner
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if e = recovered.Recover(ctx); e != nil {
		t.Fatal(e)
	}
	res, _ = recovered.Start(ctx, r)
	if !res.Ok {
		t.Fatal(res)
	}
	res = awaitTask(t, recovered, completed)
	if res.GetStdout() != "firstsecond" {
		t.Fatal(res)
	}
	file, _ := recovered.Workspace(ctx, &agent.SandboxWorkspaceRequest{ServiceToken: token, AgentId: r.AgentId, Operation: "read", ArgsJson: `{"path":"side-effect.txt"}`})
	if !file.Ok || !strings.Contains(file.GetDataJson(), `"content":"once"`) {
		t.Fatal(file)
	}
}
