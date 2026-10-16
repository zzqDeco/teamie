package sandbox

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"syscall"
	"time"

	"teamie/agent-infra/internal/api"
	"teamie/agent-protocol/kitex_gen/agent"
	"teamie/backend/pkg/imlimits"
)

const MaxWorkspaceBytes int64 = 64 * 1024 * 1024
const MaxWorkspaceFiles = 4096

// WalkDir must also use nonblocking directory opens. os.Root.FS().ReadDir uses
// Root.Open, which can otherwise block if a child replaces a directory by a FIFO
// between reading its DirEntry and descending into it.
type workspaceDirectories struct{ root *os.Root }

func (d workspaceDirectories) Open(name string) (fs.File, error) {
	return openWorkspaceDirectory(d.root, name)
}

// Includes files produced by execution children; symlinks are never followed.
func workspaceCapacity(root *os.Root, name string, replacementBytes int64) bool {
	var total int64
	entries := 0
	e := fs.WalkDir(workspaceDirectories{root}, ".", func(current string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		entries++
		if entries > MaxWorkspaceFiles {
			return errors.New("workspace_capacity")
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return nil
		}
		info, e := entry.Info()
		if e != nil {
			return e
		}
		if info.Mode().IsRegular() && current != name {
			total += info.Size()
		}
		if total+replacementBytes > MaxWorkspaceBytes {
			return errors.New("workspace_capacity")
		}
		return nil
	})
	return e == nil && total+replacementBytes <= MaxWorkspaceBytes && entries < MaxWorkspaceFiles
}

func workspaceWithinQuota(workspace string) bool {
	root, e := os.OpenRoot(workspace)
	if e != nil {
		return false
	}
	defer root.Close()
	return workspaceCapacity(root, "", 0)
}

func binaryHash(root *os.Root, name string) (string, error) {
	f, e := root.OpenFile(name, os.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOFOLLOW, 0)
	if e != nil {
		return "", e
	}
	defer f.Close()
	info, e := f.Stat()
	if e != nil || !info.Mode().IsRegular() || info.Size() > imlimits.AttachmentBytes {
		return "", errors.New("invalid file")
	}
	h := sha256.New()
	n, e := io.Copy(h, io.LimitReader(f, imlimits.AttachmentBytes+1))
	if e != nil || n > imlimits.AttachmentBytes {
		return "", errors.New("invalid file")
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func atomicBinaryImport(root *os.Root, name string, content []byte) error {
	parent := path.Dir(name)
	if e := ensureWorkspaceParents(root, parent); e != nil {
		return e
	}
	tmp := path.Join(parent, ".teamie-import-"+digest(time.Now().UTC().String() + name)[:24])
	f, e := root.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY|syscall.O_NOFOLLOW, 0666)
	if e != nil {
		return e
	}
	defer root.Remove(tmp)
	if e = f.Chmod(0666); e == nil {
		_, e = f.Write(content)
	}
	if e == nil {
		e = f.Sync()
	}
	if closeErr := f.Close(); e == nil {
		e = closeErr
	}
	if e != nil {
		return e
	}
	// An atomic no-replace publication never overwrites a later user-created
	// file, even if a child races the destination between validation and publish.
	if e = root.Link(tmp, name); e != nil {
		return e
	}
	if e = root.Remove(tmp); e != nil {
		return e
	}
	dir, e := openWorkspaceDirectory(root, parent)
	if e != nil {
		return e
	}
	defer dir.Close()
	return dir.Sync()
}

func (s *Service) ImportFile(ctx context.Context, r *agent.SandboxImportRequest) (*agent.SandboxWorkspaceResponse, error) {
	if r == nil || !api.EqualSecret(r.ServiceToken, s.Token) {
		return workspaceError("unauthorized"), nil
	}
	if !validID(r.AgentId) || !validID(r.RunId) || !validID(r.RequestId) {
		return workspaceError("invalid_request"), nil
	}
	if len(r.Content) == 0 || len(r.Content) > imlimits.AttachmentBytes {
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
	s.fileMu.Lock()
	defer s.fileMu.Unlock()
	if ctx.Err() != nil {
		return workspaceError("execution_canceled"), nil
	}
	workspace, e := s.workspace(r.AgentId)
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
			return &agent.SandboxWorkspaceResponse{Ok: true, DataJson: &old.DataJSON}, nil
		}
		observed, err := binaryHash(root, old.Path)
		if err != nil || observed != old.ExpectedHash {
			return workspaceError("execution_unknown"), nil
		}
		old.State = "complete"
		if writeAtomicJSON(receiptPath, old) != nil {
			return workspaceError("receipt_unavailable"), nil
		}
		return &agent.SandboxWorkspaceResponse{Ok: true, DataJson: &old.DataJSON}, nil
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
	// Reject existing parent symlinks before durable intent, so normal invalid
	// destinations remain a defined failure and do not poison a retry receipt.
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
