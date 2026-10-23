package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io/fs"
	"os"
	"path"
	"path/filepath"

	"teamie/services/sandbox/internal/domain"
)

const MaxWorkspaceBytes int64 = domain.MaxWorkspaceBytes
const MaxWorkspaceFiles = domain.MaxWorkspaceFiles

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
