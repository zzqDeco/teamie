package sandbox

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path"
	"strings"
	"syscall"

	"teamie/agent-infra/internal/api"
	"teamie/agent-protocol/kitex_gen/agent"
	"teamie/backend/pkg/imlimits"
)

func exportError(code string) *agent.SandboxExportResponse {
	return &agent.SandboxExportResponse{ErrorCode: &code}
}

// Export is a bounded read, with no execution, network, credentials in a child
// or durable side effect. IM's independent upload receipt is the recovery
// boundary once the runtime publishes these bytes outside the workspace.
func (s *Service) ExportFile(ctx context.Context, r *agent.SandboxExportRequest) (*agent.SandboxExportResponse, error) {
	if r == nil || !api.EqualSecret(r.ServiceToken, s.Token) {
		return exportError("unauthorized"), nil
	}
	if !validID(r.AgentId) {
		return exportError("invalid_request"), nil
	}
	name, err := workspacePath(r.Path)
	if err != nil || name == "." {
		return exportError("invalid_path"), nil
	}
	s.fileMu.Lock()
	defer s.fileMu.Unlock()
	if ctx.Err() != nil {
		return exportError("execution_canceled"), nil
	}
	workspace, err := s.workspace(r.AgentId)
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
	if before.Size() < 1 || before.Size() > imlimits.AttachmentBytes {
		return exportError("file_too_large"), nil
	}
	f, err := root.OpenFile(name, os.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return exportError("file_unavailable"), nil
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !opened.Mode().IsRegular() || !os.SameFile(before, opened) {
		return exportError("file_changed"), nil
	}
	content, err := io.ReadAll(io.LimitReader(f, imlimits.AttachmentBytes+1))
	if err != nil {
		return exportError("file_unavailable"), nil
	}
	if len(content) < 1 || len(content) > imlimits.AttachmentBytes {
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
	return &agent.SandboxExportResponse{Ok: true, Content: content, Sha256: &hash}, nil
}
