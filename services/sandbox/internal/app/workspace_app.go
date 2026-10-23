package app

import (
	"context"
	"encoding/json"
	"io"
	"io/fs"
	"os"
	"path"
	"regexp"
	"strings"
	"syscall"
	"time"

	"teamie/services/sandbox/internal/domain"
)

const maxFileBytes = domain.MaxFileBytes
const maxWorkspaceEntries = domain.MaxWorkspaceEntries
const MaxWorkspaceResponseBytes = domain.MaxWorkspaceResponseBytes

type workspaceArgs = domain.WorkspaceArgs
type fileInfo = domain.FileInfo
type grepMatch = domain.GrepMatch

func virtual(p string) string {
	return domain.VirtualPath(p)
}
func workspacePath(p string) (string, error) {
	return domain.WorkspacePath(p)
}
func workspaceError(code string) *domain.SandboxWorkspaceResponse {
	message := domain.WorkspaceErrorMessage(code)
	return &domain.SandboxWorkspaceResponse{ErrorCode: &code, ErrorMessage: &message}
}
func workspaceResult(data any) *domain.SandboxWorkspaceResponse {
	b, e := json.Marshal(data)
	if e != nil || len(b) > MaxWorkspaceResponseBytes {
		return workspaceError("file_too_large")
	}
	value := string(b)
	return &domain.SandboxWorkspaceResponse{Ok: true, DataJson: &value}
}

func regularRead(root *os.Root, name string) ([]byte, error) {
	return domain.DefaultWorkspaceStorage.RegularRead(root, name)
}

func openWorkspaceDirectory(root *os.Root, name string) (*os.File, error) {
	return domain.DefaultWorkspaceStorage.OpenWorkspaceDirectory(root, name)
}

func ensureWorkspaceParents(root *os.Root, parent string) error {
	return domain.DefaultWorkspaceStorage.EnsureWorkspaceParents(root, parent)
}

func atomicWorkspaceWrite(root *os.Root, name, content string) error {
	return domain.DefaultWorkspaceStorage.AtomicWorkspaceWrite(root, name, content)
}

func globMatches(pattern, name string) bool {
	return domain.GlobMatches(pattern, name)
}

// ValidateWorkspaceRequest validates a workspace request before execution.
func (s *Service) ValidateWorkspaceRequest(r *domain.SandboxWorkspaceRequest) string {
	if r == nil || !domain.EqualSecret(r.ServiceToken, s.Token) {
		return "unauthorized"
	}
	if !validID(r.AgentId) || len(r.ArgsJson) > maxFileBytes*2 {
		return "invalid_request"
	}
	return ""
}

// Workspace executes a confined workspace operation.
func (s *Service) Workspace(ctx context.Context, r *domain.SandboxWorkspaceRequest) (*domain.SandboxWorkspaceResponse, error) {
	if code := s.ValidateWorkspaceRequest(r); code != "" {
		return workspaceError(code), nil
	}
	var in workspaceArgs
	dec := json.NewDecoder(strings.NewReader(r.ArgsJson))
	dec.DisallowUnknownFields()
	if dec.Decode(&in) != nil || dec.Decode(new(any)) != io.EOF || in.Offset < 0 || in.Limit < 0 || in.Limit > 10000 || in.AfterLines < 0 || in.BeforeLines < 0 || in.AfterLines > 20 || in.BeforeLines > 20 {
		return workspaceError("invalid_request"), nil
	}
	if in.PageVersion != 0 {
		return s.workspacePage(ctx, r, in), nil
	}
	name, e := workspacePath(in.Path)
	if e != nil {
		return workspaceError("invalid_path"), nil
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
	switch r.Operation {
	case "read":
		b, e := regularRead(root, name)
		if e != nil {
			return workspaceError(e.Error()), nil
		}
		offset := in.Offset
		if offset < 1 {
			offset = 1
		}
		lines := strings.SplitAfter(string(b), "\n")
		if len(lines) > 0 && lines[len(lines)-1] == "" {
			lines = lines[:len(lines)-1]
		}
		start := offset - 1
		if start > len(lines) {
			start = len(lines)
		}
		end := len(lines)
		if in.Limit > 0 && start+in.Limit < end {
			end = start + in.Limit
		}
		return workspaceResult(map[string]any{"content": strings.Join(lines[start:end], ""), "offset": offset, "nextOffset": end + 1, "truncated": end < len(lines)}), nil
	case "write", "edit", "delete":
		return s.mutateWorkspace(r, in, name, root), nil
	case "ls":
		f, e := root.OpenFile(name, os.O_RDONLY|syscall.O_NONBLOCK, 0)
		if os.IsNotExist(e) {
			return workspaceResult(map[string]any{"files": []fileInfo{}, "truncated": false}), nil
		}
		if e != nil {
			return workspaceError("file_unavailable"), nil
		}
		defer f.Close()
		entries, e := f.ReadDir(maxWorkspaceEntries + 1)
		if e != nil && e != io.EOF {
			return workspaceError("file_unavailable"), nil
		}
		truncated := len(entries) > maxWorkspaceEntries
		if truncated {
			entries = entries[:maxWorkspaceEntries]
		}
		files := make([]fileInfo, 0, len(entries))
		for _, entry := range entries {
			if entry.Type()&os.ModeSymlink != 0 {
				continue
			}
			info, e := entry.Info()
			if e != nil || !info.Mode().IsRegular() && !info.IsDir() {
				continue
			}
			files = append(files, fileInfo{Path: virtual(path.Join(name, entry.Name())), IsDir: info.IsDir(), Size: info.Size(), ModifiedAt: info.ModTime().UTC().Format(time.RFC3339Nano)})
		}
		return workspaceResult(map[string]any{"files": files, "truncated": truncated}), nil
	case "glob", "grep":
		pattern := in.Pattern
		if r.Operation == "glob" {
			if strings.HasPrefix(pattern, "/workspace/") {
				pattern = strings.TrimPrefix(pattern, "/workspace/")
			}
			if _, e = workspacePath(pattern); e != nil {
				return workspaceError("invalid_path"), nil
			}
			if len(pattern) > 512 {
				return workspaceError("invalid_request"), nil
			}
		}
		var re *regexp.Regexp
		if r.Operation == "grep" {
			if len(pattern) > 4096 {
				return workspaceError("invalid_request"), nil
			}
			if in.CaseInsensitive {
				pattern = "(?i)" + pattern
			}
			if in.EnableMultiline {
				pattern = "(?m)" + pattern
			}
			re, e = regexp.Compile(pattern)
			if e != nil {
				return workspaceError("invalid_request"), nil
			}
		}
		files := make([]fileInfo, 0)
		matches := make([]grepMatch, 0)
		visited, scanned := 0, 0
		truncated := false
		e = fs.WalkDir(root.FS(), name, func(current string, entry fs.DirEntry, walkErr error) error {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			visited++
			if visited > 10000 {
				truncated = true
				return fs.SkipAll
			}
			if walkErr != nil {
				return nil
			}
			if entry.Type()&os.ModeSymlink != 0 {
				return nil
			}
			if current == name && entry.IsDir() {
				return nil
			}
			rel := strings.TrimPrefix(strings.TrimPrefix(current, name), "/")
			if name == "." {
				rel = current
			}
			if r.Operation == "glob" {
				if !globMatches(pattern, rel) {
					return nil
				}
				if len(files) >= maxWorkspaceEntries {
					truncated = true
					return fs.SkipAll
				}
				info, e := entry.Info()
				if e == nil && (info.IsDir() || info.Mode().IsRegular()) {
					files = append(files, fileInfo{Path: virtual(current), IsDir: info.IsDir(), Size: info.Size(), ModifiedAt: info.ModTime().UTC().Format(time.RFC3339Nano)})
				}
				return nil
			}
			if entry.IsDir() || in.Glob != "" && !globMatches(in.Glob, rel) || in.FileType != "" && !strings.HasSuffix(current, "."+in.FileType) {
				return nil
			}
			b, e := regularRead(root, current)
			if e != nil {
				return nil
			}
			scanned += len(b)
			if scanned > 8*1024*1024 {
				truncated = true
				return fs.SkipAll
			}
			text := string(b)
			lines := strings.Split(text, "\n")
			if in.EnableMultiline {
				indices := re.FindAllStringIndex(text, maxWorkspaceEntries+1)
				for _, at := range indices {
					if len(matches) >= maxWorkspaceEntries {
						truncated = true
						return fs.SkipAll
					}
					line := strings.Count(text[:at[0]], "\n")
					endLine := line + strings.Count(text[at[0]:at[1]], "\n")
					start := line - in.BeforeLines
					if start < 0 {
						start = 0
					}
					end := endLine + in.AfterLines + 1
					if end > len(lines) {
						end = len(lines)
					}
					content := strings.Join(lines[start:end], "\n")
					if len(content) > 4096 {
						content = content[:4096]
						truncated = true
					}
					matches = append(matches, grepMatch{Path: virtual(current), Line: line + 1, Content: content})
				}
			} else {
				for line, content := range lines {
					if !re.MatchString(content) {
						continue
					}
					if len(matches) >= maxWorkspaceEntries {
						truncated = true
						return fs.SkipAll
					}
					start := line - in.BeforeLines
					if start < 0 {
						start = 0
					}
					end := line + in.AfterLines + 1
					if end > len(lines) {
						end = len(lines)
					}
					content = strings.Join(lines[start:end], "\n")
					if len(content) > 4096 {
						content = content[:4096]
						truncated = true
					}
					matches = append(matches, grepMatch{Path: virtual(current), Line: line + 1, Content: content})
				}
			}
			return nil
		})
		if e != nil {
			return workspaceError("file_unavailable"), nil
		}
		if r.Operation == "glob" {
			return workspaceResult(map[string]any{"files": files, "truncated": truncated}), nil
		}
		return workspaceResult(map[string]any{"matches": matches, "truncated": truncated}), nil
	default:
		return workspaceError("invalid_request"), nil
	}
}
