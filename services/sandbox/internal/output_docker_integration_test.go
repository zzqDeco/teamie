package sandbox

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"teamie/contracts/gen/agent/kitex_gen/agent"
)

// The required disposable-Docker CI gate executes this test with the existing
// runner-image environment. A local skip is not evidence of daemon acceptance.
func TestRealDockerDurableOutputV2RestartAndExactBinaryPages(t *testing.T) {
	runner := os.Getenv("SANDBOX_TEST_RUNNER_IMAGE")
	if runner == "" {
		t.Skip("SANDBOX_TEST_RUNNER_IMAGE is unset; real Docker v2 output recovery is unverified locally")
	}
	engine := NewDocker("/var/run/docker.sock")
	root := t.TempDir()
	s, e := New(engine, token, image, filepath.Join(root, "workspaces"), filepath.Join(root, "state"))
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	s.Image = runner
	// Remove only this test's named, label-verified child if an assertion fails.
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		obj, e := engine.Inspect(ctx, taskName())
		if e == nil && obj.Config.Labels[ManagedLabel] == "true" && obj.Config.Labels["io.teamie.task"] == taskKey("agent-a", "native-task") {
			if obj.State.Running {
				engine.Kill(ctx, obj.ID)
			}
			engine.Remove(ctx, obj.ID)
		}
	}()
	r := progressiveStart()
	r.Command = `python3 - <<'PY'
import os,sys,time
sys.stdout.buffer.write(('你好-Teamie-'*15000+'\n').encode('utf-8'))
sys.stdout.buffer.flush()
for _ in range(300):
    if os.path.exists('v2-released.txt'):
        break
    time.sleep(.1)
else:
    raise RuntimeError('test supervisor did not resume')
sys.stderr.buffer.write(bytes([0,255,254])+b'tail-end\n')
sys.stderr.buffer.flush()
with open('v2-side-effect.txt','ab') as f:
    f.write(b'once')
PY`
	wantOut := strings.Repeat("你好-Teamie-", 15000) + "\n"
	wantErr := string([]byte{0, 255, 254}) + "tail-end\n"
	accepted, e := s.Start(context.Background(), r)
	if e != nil || !accepted.Ok {
		t.Fatal(accepted, e)
	}
	awaitDiskTask(t, s, func(r *agent.SandboxTaskResponse) bool {
		return r.State == "running" && r.GetOutputBytes() == int64(len(wantOut))
	})
	original, e := engine.Inspect(context.Background(), taskName())
	if e != nil || !original.State.Running {
		t.Fatal("real child was not running before detach", original, e)
	}
	inspection, e := engine.request(context.Background(), "GET", "/containers/"+original.ID+"/json", nil)
	if e != nil {
		t.Fatal(e)
	}
	var config struct {
		HostConfig struct {
			LogConfig struct {
				Type   string
				Config map[string]string
			}
		}
	}
	e = json.NewDecoder(inspection.Body).Decode(&config)
	inspection.Body.Close()
	if e != nil || config.HostConfig.LogConfig.Type != "local" || config.HostConfig.LogConfig.Config["max-size"] != "16m" || config.HostConfig.LogConfig.Config["max-file"] != "1" {
		t.Fatal("v2 daemon replay log configuration changed", config, e)
	}
	s.Close()
	recovered, e := New(engine, token, image, s.WorkspaceRoot, s.StateRoot)
	if e != nil {
		t.Fatal(e)
	}
	defer recovered.Close()
	recovered.Image = runner
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if e = recovered.Recover(ctx); e != nil {
		t.Fatal(e)
	}
	accepted, e = recovered.Start(ctx, r)
	if e != nil || !accepted.Ok {
		t.Fatal(accepted, e)
	}
	adopted, e := engine.Inspect(ctx, taskName())
	if e != nil || !adopted.State.Running || adopted.ID != original.ID {
		t.Fatal("supervisor did not adopt the same running local-driver child", adopted, e)
	}
	released, e := recovered.Workspace(ctx, &agent.SandboxWorkspaceRequest{ServiceToken: token, AgentId: r.AgentId, Operation: "write", ArgsJson: `{"path":"v2-released.txt","content":"resume"}`})
	if e != nil || !released.Ok {
		t.Fatal("test supervisor could not resume the existing child", released, e)
	}
	result := awaitDiskTask(t, recovered, completed)
	if result.GetOutputStatus() != "complete" || result.GetOutputBytes() != int64(len(wantOut)+len(wantErr)) || result.GetOutputLimitBytes() != DurableOutputLimit || result.GetOutputSha256() == "" || !result.GetHasMore() || !result.GetTruncated() {
		t.Fatalf("v2 metadata: state=%s status=%s bytes=%d", result.State, result.GetOutputStatus(), result.GetOutputBytes())
	}
	var stdout, stderr strings.Builder
	offset := int64(0)
	for {
		args, _ := json.Marshal(map[string]any{"pageVersion": 1, "taskId": r.TaskId, "byteOffset": offset, "maxBytes": 16384})
		response, e := recovered.Workspace(ctx, &agent.SandboxWorkspaceRequest{ServiceToken: token, AgentId: r.AgentId, Operation: "output", ArgsJson: string(args)})
		if e != nil || !response.Ok || len(response.GetDataJson()) > MaxPageBytes {
			t.Fatal("bounded output page unavailable", response, e)
		}
		var page struct {
			NextOffset   int64
			HasMore      bool
			OutputStatus string
			OutputSha256 string
			Segments     []outputSegment
			StdoutBase64 []byte
			StderrBase64 []byte
		}
		if e = json.Unmarshal([]byte(response.GetDataJson()), &page); e != nil {
			t.Fatal(e)
		}
		if page.NextOffset <= offset || page.OutputStatus != "complete" || page.OutputSha256 != result.GetOutputSha256() {
			t.Fatal("output reference changed across pages", response)
		}
		var pageOut, pageErr strings.Builder
		position := offset
		for _, segment := range page.Segments {
			if segment.ByteOffset != position || segment.NextOffset != position+int64(len(segment.Data)) {
				t.Fatal("noncontiguous canonical segment", segment)
			}
			position = segment.NextOffset
			if segment.Stream == "stdout" {
				pageOut.Write(segment.Data)
			} else if segment.Stream == "stderr" {
				pageErr.Write(segment.Data)
			} else {
				t.Fatal("unknown output stream", segment.Stream)
			}
		}
		if position != page.NextOffset || pageOut.String() != string(page.StdoutBase64) || pageErr.String() != string(page.StderrBase64) {
			t.Fatal("segments disagree with raw stream page")
		}
		stdout.WriteString(pageOut.String())
		stderr.WriteString(pageErr.String())
		offset = page.NextOffset
		if !page.HasMore {
			break
		}
	}
	if stdout.String() != wantOut || stderr.String() != wantErr {
		t.Fatalf("daemon changed canonical bytes: stdout=%d/%d stderr=%x/%x", stdout.Len(), len(wantOut), stderr.String(), wantErr)
	}
	spool, e := os.ReadFile(recovered.outputPath(taskKey(r.AgentId, r.TaskId)))
	if e != nil {
		t.Fatal(e)
	}
	hash := sha256.Sum256(spool)
	if hex.EncodeToString(hash[:]) != result.GetOutputSha256() {
		t.Fatal("terminal canonical spool hash changed after restart")
	}
	file, e := recovered.Workspace(ctx, &agent.SandboxWorkspaceRequest{ServiceToken: token, AgentId: r.AgentId, Operation: "read", ArgsJson: `{"path":"v2-side-effect.txt"}`})
	if e != nil || !file.Ok || !strings.Contains(file.GetDataJson(), `"content":"once"`) {
		t.Fatal("logical task reran its side effect", file, e)
	}
}
