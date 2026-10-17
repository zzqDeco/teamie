package sandbox

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"teamie/agent-protocol/kitex_gen/agent"
)

func pagedCall(t *testing.T, s *Service, op string, args map[string]any) map[string]any {
	t.Helper()
	args["pageVersion"] = 1
	b, _ := json.Marshal(args)
	r, _ := s.Workspace(context.Background(), &agent.SandboxWorkspaceRequest{ServiceToken: token, AgentId: "paged-agent", Operation: op, ArgsJson: string(b)})
	if !r.Ok {
		t.Fatalf("%s: %s", op, r.GetErrorCode())
	}
	if len(r.GetDataJson()) > MaxPageBytes {
		t.Fatal("unbounded page", len(r.GetDataJson()))
	}
	var out map[string]any
	if e := json.Unmarshal([]byte(r.GetDataJson()), &out); e != nil {
		t.Fatal(e)
	}
	return out
}
func TestPagedReadLargeUTF8LongLineVersionAndLegacyLimit(t *testing.T) {
	s := fixture(t, &fakeEngine{})
	workspace, _ := s.workspace("paged-agent")
	content := strings.Repeat("你好\x00x", 60000) + "\nlast\n"
	if e := os.WriteFile(filepath.Join(workspace, "large.txt"), []byte(content), 0666); e != nil {
		t.Fatal(e)
	}
	old, _ := s.Workspace(context.Background(), &agent.SandboxWorkspaceRequest{ServiceToken: token, AgentId: "paged-agent", Operation: "read", ArgsJson: `{"path":"large.txt"}`})
	if old.GetErrorCode() != "file_too_large" {
		t.Fatal("legacy read limit changed", old)
	}
	args := map[string]any{"path": "large.txt", "maxBytes": 32768}
	var result strings.Builder
	pages := 0
	var first map[string]any
	for {
		out := pagedCall(t, s, "read", args)
		if first == nil {
			first = out
		}
		result.WriteString(out["content"].(string))
		pages++
		if !out["hasMore"].(bool) {
			break
		}
		resume := out["resume"].(map[string]any)
		if resume["byteOffset"].(float64) <= float64(result.Len()-len(out["content"].(string))) {
			t.Fatal("cursor did not advance")
		}
		delete(resume, "operation")
		args = resume
		if pages > 100 {
			t.Fatal("page loop")
		}
	}
	if result.String() != content || pages < 2 {
		t.Fatal("large/long-line content lost", result.Len(), len(content), pages)
	}
	if e := os.WriteFile(filepath.Join(workspace, "large.txt"), []byte("changed"), 0666); e != nil {
		t.Fatal(e)
	}
	b, _ := json.Marshal(map[string]any{"pageVersion": 1, "path": "large.txt", "expectedVersion": first["sha256"], "byteOffset": 0})
	r, _ := s.Workspace(context.Background(), &agent.SandboxWorkspaceRequest{ServiceToken: token, AgentId: "paged-agent", Operation: "read", ArgsJson: string(b)})
	if r.GetErrorCode() != "file_changed" {
		t.Fatal("mixed file versions", r)
	}
}

func TestPagedListingsAndGrepContinuePastLegacy512(t *testing.T) {
	s := fixture(t, &fakeEngine{})
	workspace, _ := s.workspace("paged-agent")
	for i := 0; i < 620; i++ {
		if e := os.WriteFile(filepath.Join(workspace, fmt.Sprintf("f-%04d.txt", i)), []byte("target\n"), 0666); e != nil {
			t.Fatal(e)
		}
	}
	for _, op := range []string{"ls", "glob", "grep"} {
		args := map[string]any{"path": "/workspace", "limit": 37}
		if op == "glob" {
			args["pattern"] = "**/*.txt"
		}
		if op == "grep" {
			args["pattern"] = "target"
		}
		seen := map[string]bool{}
		pages := 0
		for {
			out := pagedCall(t, s, op, args)
			key := "files"
			if op == "grep" {
				key = "matches"
			}
			for _, entry := range out[key].([]any) {
				p := entry.(map[string]any)["path"].(string)
				if seen[p] {
					t.Fatal("duplicate", op, p)
				}
				seen[p] = true
			}
			pages++
			if !out["hasMore"].(bool) {
				break
			}
			args = out["resume"].(map[string]any)
			delete(args, "operation")
			if pages > 30 {
				t.Fatal("page loop", op)
			}
		}
		if len(seen) != 620 {
			t.Fatal("silent listing truncation", op, len(seen))
		}
	}
}

func TestPagedSearchReportsSkippedCoverageAndPreciseContext(t *testing.T) {
	s := fixture(t, &fakeEngine{})
	workspace, _ := s.workspace("paged-agent")
	if e := os.WriteFile(filepath.Join(workspace, "context.txt"), []byte("first\nHELLO\nworld\nlast\n"), 0666); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(filepath.Join(workspace, "binary"), []byte{0xff, 0x00}, 0666); e != nil {
		t.Fatal(e)
	}
	out := pagedCall(t, s, "grep", map[string]any{"path": "/workspace", "pattern": "hello.world", "caseInsensitive": true, "enableMultiline": true, "beforeLines": 1, "afterLines": 1})
	matches := out["matches"].([]any)
	if len(matches) != 1 {
		t.Fatal(matches)
	}
	m := matches[0].(map[string]any)
	if m["line"] != float64(2) || m["endLine"] != float64(3) || m["contentStartLine"] != float64(1) || m["content"] != "first\nHELLO\nworld\nlast" {
		t.Fatal("imprecise context", m)
	}
	c := out["coverage"].(map[string]any)
	if c["complete"] != false || c["skippedFiles"] != float64(1) || out["truncated"] != true {
		t.Fatal("hidden skipped file", out)
	}
	outside := filepath.Join(t.TempDir(), "private")
	os.WriteFile(outside, []byte("PRIVATE_OUTSIDE"), 0600)
	os.Symlink(outside, filepath.Join(workspace, "escape"))
	for _, op := range []string{"read", "ls", "glob", "grep"} {
		args := map[string]any{"pageVersion": 1, "path": "escape", "pattern": "PRIVATE"}
		b, _ := json.Marshal(args)
		r, _ := s.Workspace(context.Background(), &agent.SandboxWorkspaceRequest{ServiceToken: token, AgentId: "paged-agent", Operation: op, ArgsJson: string(b)})
		if strings.Contains(r.GetDataJson(), "PRIVATE_OUTSIDE") {
			t.Fatal("outside os.Root", op)
		}
	}
}

func TestPagedGrepLongLineReferenceReadsActualMatchAndDetectsChangedContinuation(t *testing.T) {
	s := fixture(t, &fakeEngine{})
	workspace, _ := s.workspace("paged-agent")
	name := filepath.Join(workspace, "long.txt")
	content := strings.Repeat("x", 300000) + "needle\nneedle again\n"
	os.WriteFile(name, []byte(content), 0666)
	out := pagedCall(t, s, "grep", map[string]any{"path": "long.txt", "pattern": "needle", "limit": 1})
	m := out["matches"].([]any)[0].(map[string]any)
	if m["matchByteOffset"] != float64(300000) || m["matchEndByteOffset"] != float64(300006) || m["contentTruncated"] != true || m["sha256"] != digest(content) {
		t.Fatal(m)
	}
	resume := m["readResume"].(map[string]any)
	delete(resume, "operation")
	read := pagedCall(t, s, "read", resume)
	if !strings.HasPrefix(read["content"].(string), "needle") {
		t.Fatal("match could not be read precisely", read)
	}
	info, _ := os.Stat(name)
	changed := strings.Replace(content, "again", "other", 1)
	os.WriteFile(name, []byte(changed), 0666)
	os.Chtimes(name, info.ModTime(), info.ModTime())
	args := out["resume"].(map[string]any)
	delete(args, "operation")
	args["pageVersion"] = 1
	b, _ := json.Marshal(args)
	response, _ := s.Workspace(context.Background(), &agent.SandboxWorkspaceRequest{ServiceToken: token, AgentId: "paged-agent", Operation: "grep", ArgsJson: string(b)})
	if response.GetErrorCode() != "file_changed" {
		t.Fatal("same metadata hid changed grep file", response)
	}
}
