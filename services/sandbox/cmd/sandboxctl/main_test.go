package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"teamie/services/sandbox/internal/domain"
	"testing"
)

func TestIsolationRejectsActualUnsafeContainerSettings(t *testing.T) {
	good := `{"Config":{"User":"10001:10001","Env":["PATH=/bin","HOME=/workspace"]},"HostConfig":{"NetworkMode":"none","ReadonlyRootfs":true,"CapDrop":["ALL"],"SecurityOpt":["no-new-privileges:true"]},"Mounts":[{"Type":"bind","Source":"/owned","Destination":"/workspace"}]}`
	var c inspected
	if json.Unmarshal([]byte(good), &c) != nil || !isolated(c, "/owned") {
		t.Fatal("valid policy rejected")
	}
	for _, bad := range []string{strings.Replace(good, `"none"`, `"host"`, 1), strings.Replace(good, `"10001:10001"`, `"0:0"`, 1), strings.Replace(good, `"ReadonlyRootfs":true`, `"ReadonlyRootfs":false`, 1), strings.Replace(good, `"PATH=/bin"`, `"GATEWAY_RUNTIME_TOKEN=secret"`, 1), strings.Replace(good, `"/owned"`, `"/host"`, 1), strings.Replace(good, `"/workspace"`, `"/var/run/docker.sock"`, 1)} {
		var changed inspected
		_ = json.Unmarshal([]byte(bad), &changed)
		if isolated(changed, "/owned") {
			t.Fatal("unsafe policy accepted")
		}
	}
}
func TestTaskEvidenceRequiresCorrectScopeAndSuccessfulCompletion(t *testing.T) {
	root := t.TempDir()
	t.Setenv("SANDBOX_STATE_ROOT", root)
	path := filepath.Join(root, domain.TaskKey("agent", "task")+".task.json")
	for _, state := range []string{"running", "completed", "failed"} {
		raw, _ := json.Marshal(domain.TaskReceipt{Version: 2, AgentID: "agent", RunID: "run", TaskID: "task", State: state, Command: "PRIVATE-COMMAND", Result: domain.Result{Stdout: "PRIVATE-OUTPUT"}})
		if err := os.WriteFile(path, raw, 0600); err != nil {
			t.Fatal(err)
		}
		out, err := run(context.Background(), []string{"task", "--agent", "agent", "--run", "run", "--task", "task"})
		if err != nil {
			t.Fatal(err)
		}
		v := out.(map[string]any)
		if v["success"] != (state == "completed") {
			t.Fatal("unfinished task accepted")
		}
		projected, _ := json.Marshal(v)
		if strings.Contains(string(projected), "PRIVATE") {
			t.Fatal("private task detail leaked")
		}
	}
	if _, err := run(context.Background(), []string{"task", "--agent", "agent", "--run", "other", "--task", "task"}); err == nil {
		t.Fatal("wrong run accepted")
	}
}
