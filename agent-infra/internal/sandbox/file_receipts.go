package sandbox

import (
	"encoding/json"
	"os"
	"path"
	"path/filepath"
	"strings"

	"teamie/agent-infra/internal/sandbox/domain"
	"teamie/agent-protocol/kitex_gen/agent"
)

type fileReceipt = domain.FileReceipt

// A file mutation intent precedes the atomic filesystem effect. If the process
// dies before the result receipt, reconciliation recognizes the intended bytes
// without applying an edit twice. Ambiguous intervening edits remain unknown.
func (s *Service) mutateWorkspace(r *agent.SandboxWorkspaceRequest, in workspaceArgs, name string, root *os.Root) *agent.SandboxWorkspaceResponse {
	s.fileMu.Lock()
	defer s.fileMu.Unlock()
	requestID := r.GetRequestId()
	if requestID != "" && !validID(requestID) {
		return workspaceError("invalid_request")
	}
	receiptPath := ""
	fp := digest(r.Operation + "\x00" + r.ArgsJson)
	if requestID != "" {
		receiptPath = filepath.Join(s.StateRoot, digest(r.AgentId+"\x00file\x00"+requestID)+".file.json")
		b, e := os.ReadFile(receiptPath)
		if e == nil {
			var old fileReceipt
			if len(b) > 65536 || json.Unmarshal(b, &old) != nil || old.Version != 1 {
				return workspaceError("execution_unknown")
			}
			if old.Fingerprint != fp {
				return workspaceError("request_conflict")
			}
			if old.State == "complete" {
				return &agent.SandboxWorkspaceResponse{Ok: true, DataJson: &old.DataJSON}
			}
			matched := false
			if old.Operation == "delete" {
				_, e = root.Lstat(old.Path)
				matched = os.IsNotExist(e)
			} else {
				b, e := regularRead(root, old.Path)
				matched = e == nil && digest(string(b)) == old.ExpectedHash
			}
			if !matched {
				return workspaceError("execution_unknown")
			}
			old.State = "complete"
			if writeAtomicJSON(receiptPath, old) != nil {
				return workspaceError("receipt_unavailable")
			}
			return &agent.SandboxWorkspaceResponse{Ok: true, DataJson: &old.DataJSON}
		}
		if !os.IsNotExist(e) {
			return workspaceError("receipt_unavailable")
		}
		entries, e := os.ReadDir(s.StateRoot)
		if e != nil || len(entries) >= 10001 {
			return workspaceError("sandbox_capacity")
		}
	}
	content := in.Content
	if r.Operation == "edit" {
		b, e := regularRead(root, name)
		if e != nil {
			return workspaceError("file_unavailable")
		}
		text := string(b)
		if in.Old == "" || in.Old == in.New {
			return workspaceError("file_conflict")
		}
		count := strings.Count(text, in.Old)
		if count == 0 || !in.ReplaceAll && count != 1 {
			return workspaceError("file_conflict")
		}
		n := 1
		if in.ReplaceAll {
			n = -1
		}
		content = strings.Replace(text, in.Old, in.New, n)
	}
	if r.Operation == "delete" {
		info, e := root.Lstat(name)
		if e != nil && !os.IsNotExist(e) {
			return workspaceError("file_unavailable")
		}
		if e == nil && !info.Mode().IsRegular() {
			return workspaceError("file_unavailable")
		}
	}
	if len(content) > maxFileBytes {
		return workspaceError("file_too_large")
	}
	if !diskAvailable(s.WorkspaceRoot) || r.Operation != "delete" && !workspaceCapacity(root, name, int64(len(content))) {
		return workspaceError("workspace_capacity")
	}
	data := workspaceResult(map[string]any{"path": virtual(name), "bytes": len(content)})
	if r.Operation == "delete" {
		data = workspaceResult(map[string]any{"path": virtual(name), "deleted": true})
	}
	intent := fileReceipt{Version: 1, Fingerprint: fp, State: "intent", Operation: r.Operation, Path: name, ExpectedHash: digest(content), DataJSON: data.GetDataJson()}
	if receiptPath != "" && writeAtomicJSON(receiptPath, intent) != nil {
		return workspaceError("receipt_unavailable")
	}
	var e error
	if r.Operation == "delete" {
		e = root.Remove(name)
		if os.IsNotExist(e) {
			e = nil
		}
		if e == nil {
			dir, openErr := openWorkspaceDirectory(root, path.Dir(name))
			if openErr != nil {
				e = openErr
			} else {
				e = dir.Sync()
				dir.Close()
			}
		}
	} else {
		e = atomicWorkspaceWrite(root, name, content)
	}
	if e != nil {
		if receiptPath != "" {
			return workspaceError("execution_unknown")
		}
		return workspaceError("file_unavailable")
	}
	intent.State = "complete"
	if receiptPath != "" && writeAtomicJSON(receiptPath, intent) != nil {
		return workspaceError("execution_unknown")
	}
	return data
}
