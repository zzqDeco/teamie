package sandbox

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// This opt-in test runs only on disposable CI Docker. It never contacts production.
func TestRealDockerExecutionSeparationAndCleanup(t *testing.T) {
	runner := os.Getenv("SANDBOX_TEST_RUNNER_IMAGE")
	if runner == "" {
		t.Skip("SANDBOX_TEST_RUNNER_IMAGE is unset")
	}
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
	r.Command = `python3 - <<'PY'
import os,socket,resource
assert int(open("/sys/fs/cgroup/memory.max").read())==134217728
assert int(open("/sys/fs/cgroup/pids.max").read())==32
quota,period=map(int,open("/sys/fs/cgroup/cpu.max").read().split())
assert quota/period==0.5
assert resource.getrlimit(resource.RLIMIT_FSIZE)==(8388608,8388608)
assert os.getuid()==10001
assert not os.path.exists('/var/run/docker.sock')
assert not any('TOKEN' in k or 'KEY' in k or 'PASSWORD' in k for k in os.environ)
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
PY`
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
		t.Fatal(timed)
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
