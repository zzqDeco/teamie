package sandbox

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"teamie/contracts/gen/agent/kitex_gen/agent"
)

func TestFileMutationReceiptsReplayCrashReconciliationAndConflict(t *testing.T) {
	s := fixture(t, &fakeEngine{})
	id := "stable-write"
	r := &agent.SandboxWorkspaceRequest{ServiceToken: token, AgentId: "a", Operation: "write", ArgsJson: `{"path":"f","content":"a"}`, RequestId: &id}
	call := func() *agent.SandboxWorkspaceResponse {
		t.Helper()
		res, _ := s.Workspace(context.Background(), r)
		return res
	}
	if res := call(); !res.Ok {
		t.Fatal(res)
	}
	id = "stable-edit"
	r.Operation = "edit"
	r.ArgsJson = `{"path":"f","old":"a","new":"aa"}`
	if res := call(); !res.Ok {
		t.Fatal(res)
	}
	if res := call(); !res.Ok {
		t.Fatal("edit did not replay receipt", res)
	}
	workspace, _ := s.workspace("a")
	b, _ := os.ReadFile(filepath.Join(workspace, "f"))
	if string(b) != "aa" {
		t.Fatal("edit was applied twice", string(b))
	}
	receiptPath := filepath.Join(s.StateRoot, digest(r.AgentId+"\x00file\x00"+id)+".file.json")
	b, e := os.ReadFile(receiptPath)
	if e != nil {
		t.Fatal(e)
	}
	var intent fileReceipt
	if json.Unmarshal(b, &intent) != nil {
		t.Fatal("invalid file receipt")
	}
	intent.State = "intent"
	if e = writeAtomicJSON(receiptPath, intent); e != nil {
		t.Fatal(e)
	}
	if res := call(); !res.Ok {
		t.Fatal("fsynced mutation could not reconcile lost result", res)
	}
	intent.State = "intent"
	if e = writeAtomicJSON(receiptPath, intent); e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(filepath.Join(workspace, "f"), []byte("intervening changes"), 0666); e != nil {
		t.Fatal(e)
	}
	if res := call(); res.GetErrorCode() != "execution_unknown" {
		t.Fatal("ambiguous mutation was rerun", res)
	}
	r.ArgsJson = `{"path":"f","old":"a","new":"different"}`
	if res := call(); res.GetErrorCode() != "request_conflict" {
		t.Fatal(res)
	}
	id = "stable-delete"
	r.Operation = "delete"
	r.ArgsJson = `{"path":"f"}`
	if res := call(); !res.Ok {
		t.Fatal(res)
	}
	if e = os.WriteFile(filepath.Join(workspace, "f"), []byte("later content"), 0666); e != nil {
		t.Fatal(e)
	}
	if res := call(); !res.Ok {
		t.Fatal(res)
	}
	b, _ = os.ReadFile(filepath.Join(workspace, "f"))
	if string(b) != "later content" {
		t.Fatal("delete replay deleted new content")
	}
	info, e := os.Stat(receiptPath)
	if e != nil || info.Mode().Perm() != 0600 {
		t.Fatal(info, e)
	}
}

func TestWorkspaceMultilineAndContextSearch(t *testing.T) {
	s := fixture(t, &fakeEngine{})
	write := &agent.SandboxWorkspaceRequest{ServiceToken: token, AgentId: "a", Operation: "write", ArgsJson: `{"path":"file.txt","content":"first\nHELLO\nworld\nlast\n"}`}
	res, _ := s.Workspace(context.Background(), write)
	if !res.Ok {
		t.Fatal(res)
	}
	r := &agent.SandboxWorkspaceRequest{ServiceToken: token, AgentId: "a", Operation: "grep", ArgsJson: `{"path":"/workspace","pattern":"hello\nworld","caseInsensitive":true,"enableMultiline":true,"beforeLines":1,"afterLines":1,"glob":"**/*.txt","fileType":"txt"}`}
	res, _ = s.Workspace(context.Background(), r)
	if !res.Ok || !strings.Contains(res.GetDataJson(), `first\nHELLO\nworld\nlast`) || !strings.Contains(res.GetDataJson(), `"line":2`) {
		t.Fatal(res)
	}
}

func TestTaskOutputReceiptPreservesRawBytes(t *testing.T) {
	s := fixture(t, &fakeEngine{})
	key := taskKey("a", "binary-output")
	raw := string([]byte{0xff, 0x00, 'a', 0xfe})
	value := &taskReceipt{Version: 1, AgentID: "a", TaskID: "binary-output", State: "completed", Output: []outputPart{{Stream: 1, Data: raw}, {Stream: 2, Data: "stderr"}}}
	if e := writeAtomicJSON(s.taskPath(key), value); e != nil {
		t.Fatal(e)
	}
	s.TaskMu.Lock()
	loaded, e := s.loadTaskLocked("a", "binary-output")
	s.TaskMu.Unlock()
	if e != nil || loaded.Result.Stdout != raw || loaded.Result.Stderr != "stderr" {
		t.Fatal("raw output changed across receipt replay", e)
	}
}

func TestWorkspaceLsMissingNativePlanDirectoryIsEmpty(t *testing.T) {
	s := fixture(t, &fakeEngine{})
	request := &agent.SandboxWorkspaceRequest{ServiceToken: token, AgentId: "agent-a", Operation: "ls", ArgsJson: `{"path":"/workspace/.teamie/plan"}`}
	response, _ := s.Workspace(context.Background(), request)
	if !response.Ok || response.GetDataJson() != `{"files":[],"truncated":false}` {
		t.Fatal("first native plan directory listing must be empty")
	}
	request.Operation = "read"
	response, _ = s.Workspace(context.Background(), request)
	if response.Ok {
		t.Fatal("missing file read must still report failure")
	}
}
