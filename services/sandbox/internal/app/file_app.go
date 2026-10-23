package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"

	"teamie/services/sandbox/internal/domain"
)

const MaxWorkspaceBytes int64 = domain.MaxWorkspaceBytes
const MaxWorkspaceFiles = domain.MaxWorkspaceFiles

type fileReceipt = domain.FileReceipt
type workspaceDirectories struct{ root *os.Root }

func (d workspaceDirectories) Open(name string) (fs.File, error) {
	return openWorkspaceDirectory(d.root, name)
}

func workspaceCapacity(root *os.Root, name string, replacementBytes int64) bool {
	return domain.DefaultWorkspaceStorage.WorkspaceCapacity(root, name, replacementBytes)
}

func workspaceWithinQuota(workspace string) bool {
	return domain.DefaultWorkspaceStorage.WorkspaceWithinQuota(workspace)
}

func binaryHash(root *os.Root, name string) (string, error) {
	return domain.DefaultWorkspaceStorage.BinaryHash(root, name)
}

func atomicBinaryImport(root *os.Root, name string, content []byte) error {
	return domain.DefaultWorkspaceStorage.AtomicBinaryImport(root, name, content)
}

func exportError(code string) *domain.SandboxExportResponse {
	return &domain.SandboxExportResponse{ErrorCode: &code}
}

func (s *Service) mutateWorkspace(r *domain.SandboxWorkspaceRequest, in workspaceArgs, name string, root *os.Root) *domain.SandboxWorkspaceResponse {
	s.FileMu.Lock()
	defer s.FileMu.Unlock()
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
				return &domain.SandboxWorkspaceResponse{Ok: true, DataJson: &old.DataJSON}
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
			return &domain.SandboxWorkspaceResponse{Ok: true, DataJson: &old.DataJSON}
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

// ImportFile atomically imports a binary attachment into the agent's workspace with durable receipt idempotency.
func (s *Service) ImportFile(ctx context.Context, r *domain.SandboxImportRequest) (*domain.SandboxWorkspaceResponse, error) {
	if r == nil || !domain.EqualSecret(r.ServiceToken, s.Token) {
		return workspaceError("unauthorized"), nil
	}
	if !validID(r.AgentId) || !validID(r.RunId) || !validID(r.RequestId) {
		return workspaceError("invalid_request"), nil
	}
	if len(r.Content) == 0 || len(r.Content) > domain.AttachmentBytes {
		return workspaceError("file_too_large"), nil
	}
	h := sha256.Sum256(r.Content)
	contentHash := hex.EncodeToString(h[:])
	if r.Sha256 != contentHash {
		return workspaceError("content_mismatch"), nil
	}
	name, e := workspacePath(r.Path)
	if e != nil || name == "." {
		return workspaceError("invalid_path"), nil
	}
	s.FileMu.Lock()
	defer s.FileMu.Unlock()
	if ctx.Err() != nil {
		return workspaceError("execution_canceled"), nil
	}
	workspace, e := s.WorkspaceDir(r.AgentId)
	if e != nil {
		return workspaceError("file_unavailable"), nil
	}
	root, e := os.OpenRoot(workspace)
	if e != nil {
		return workspaceError("file_unavailable"), nil
	}
	defer root.Close()
	receiptPath := filepath.Join(s.StateRoot, digest(r.AgentId+"\x00import\x00"+r.RequestId)+".file.json")
	fp := digest(r.RunId + "\x00" + name + "\x00" + contentHash)
	if b, readErr := os.ReadFile(receiptPath); readErr == nil {
		var old fileReceipt
		if len(b) > 65536 || json.Unmarshal(b, &old) != nil || old.Version != 1 {
			return workspaceError("execution_unknown"), nil
		}
		if old.Fingerprint != fp {
			return workspaceError("request_conflict"), nil
		}
		if old.State == "complete" {
			return &domain.SandboxWorkspaceResponse{Ok: true, DataJson: &old.DataJSON}, nil
		}
		observed, err := binaryHash(root, old.Path)
		if err != nil || observed != old.ExpectedHash {
			return workspaceError("execution_unknown"), nil
		}
		old.State = "complete"
		if writeAtomicJSON(receiptPath, old) != nil {
			return workspaceError("receipt_unavailable"), nil
		}
		return &domain.SandboxWorkspaceResponse{Ok: true, DataJson: &old.DataJSON}, nil
	} else if !os.IsNotExist(readErr) {
		return workspaceError("receipt_unavailable"), nil
	}
	entries, e := os.ReadDir(s.StateRoot)
	if e != nil || len(entries) >= 10001 {
		return workspaceError("sandbox_capacity"), nil
	}
	if _, e = root.Lstat(name); !os.IsNotExist(e) {
		return workspaceError("file_exists"), nil
	}
	if !diskAvailable(s.WorkspaceRoot) || !workspaceCapacity(root, name, int64(len(r.Content))) {
		return workspaceError("workspace_capacity"), nil
	}
	if e = ensureWorkspaceParents(root, path.Dir(name)); e != nil {
		return workspaceError("invalid_path"), nil
	}
	data := workspaceResult(map[string]any{"path": virtual(name), "bytes": len(r.Content), "sha256": contentHash})
	intent := fileReceipt{Version: 1, Fingerprint: fp, State: "intent", Operation: "import", Path: name, ExpectedHash: contentHash, DataJSON: data.GetDataJson()}
	if writeAtomicJSON(receiptPath, intent) != nil {
		return workspaceError("receipt_unavailable"), nil
	}
	if ctx.Err() != nil {
		return workspaceError("execution_canceled"), nil
	}
	if e = atomicBinaryImport(root, name, r.Content); e != nil {
		return workspaceError("execution_unknown"), nil
	}
	intent.State = "complete"
	if writeAtomicJSON(receiptPath, intent) != nil {
		return workspaceError("execution_unknown"), nil
	}
	return data, nil
}

// ExportFile performs a bounded, confined read of a regular workspace file for attachment export.
func (s *Service) ExportFile(ctx context.Context, r *domain.SandboxExportRequest) (*domain.SandboxExportResponse, error) {
	if r == nil || !domain.EqualSecret(r.ServiceToken, s.Token) {
		return exportError("unauthorized"), nil
	}
	if !validID(r.AgentId) {
		return exportError("invalid_request"), nil
	}
	name, err := workspacePath(r.Path)
	if err != nil || name == "." {
		return exportError("invalid_path"), nil
	}
	s.FileMu.Lock()
	defer s.FileMu.Unlock()
	if ctx.Err() != nil {
		return exportError("execution_canceled"), nil
	}
	workspace, err := s.WorkspaceDir(r.AgentId)
	if err != nil {
		return exportError("file_unavailable"), nil
	}
	root, err := os.OpenRoot(workspace)
	if err != nil {
		return exportError("file_unavailable"), nil
	}
	defer root.Close()
	parent := ""
	if dir := path.Dir(name); dir != "." {
		for _, component := range strings.Split(dir, "/") {
			parent = path.Join(parent, component)
			info, err := root.Lstat(parent)
			if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
				return exportError("invalid_path"), nil
			}
		}
	}
	before, err := root.Lstat(name)
	if err != nil || !before.Mode().IsRegular() {
		return exportError("file_unavailable"), nil
	}
	if before.Size() < 1 || before.Size() > domain.AttachmentBytes {
		return exportError("file_too_large"), nil
	}
	f, err := domain.DefaultWorkspaceStorage.OpenNonblockNoFollow(root, name)
	if err != nil {
		return exportError("file_unavailable"), nil
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !opened.Mode().IsRegular() || !os.SameFile(before, opened) {
		return exportError("file_changed"), nil
	}
	content, err := io.ReadAll(io.LimitReader(f, domain.AttachmentBytes+1))
	if err != nil {
		return exportError("file_unavailable"), nil
	}
	if len(content) < 1 || len(content) > domain.AttachmentBytes {
		return exportError("file_too_large"), nil
	}
	after, err := f.Stat()
	current, pathErr := root.Lstat(name)
	if err != nil || pathErr != nil || !current.Mode().IsRegular() || !os.SameFile(opened, current) || int64(len(content)) != opened.Size() || after.Size() != opened.Size() || !after.ModTime().Equal(opened.ModTime()) {
		return exportError("file_changed"), nil
	}
	if ctx.Err() != nil {
		return exportError("execution_canceled"), nil
	}
	sum := sha256.Sum256(content)
	hash := hex.EncodeToString(sum[:])
	return &domain.SandboxExportResponse{Ok: true, Content: content, Sha256: &hash}, nil
}
