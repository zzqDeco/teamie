package sandbox

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path"
	"regexp"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"teamie/agent-infra/internal/api"
	"teamie/agent-protocol/kitex_gen/agent"
)

const maxFileBytes = 262144
const maxWorkspaceEntries = 512

type workspaceArgs struct {
	PageVersion     int    `json:"pageVersion,omitempty"`
	ByteOffset      int64  `json:"byteOffset,omitempty"`
	MaxBytes        int    `json:"maxBytes,omitempty"`
	Cursor          string `json:"cursor,omitempty"`
	ExpectedVersion string `json:"expectedVersion,omitempty"`
	TaskID          string `json:"taskId,omitempty"`
	Path            string `json:"path"`
	Content         string `json:"content"`
	Old             string `json:"old"`
	New             string `json:"new"`
	ReplaceAll      bool   `json:"replaceAll"`
	Pattern         string `json:"pattern"`
	Offset          int    `json:"offset"`
	Limit           int    `json:"limit"`
	Glob            string `json:"glob"`
	FileType        string `json:"fileType"`
	CaseInsensitive bool   `json:"caseInsensitive"`
	EnableMultiline bool   `json:"enableMultiline"`
	AfterLines      int    `json:"afterLines"`
	BeforeLines     int    `json:"beforeLines"`
}
type fileInfo struct {
	Path       string `json:"path"`
	IsDir      bool   `json:"isDir"`
	Size       int64  `json:"size"`
	ModifiedAt string `json:"modifiedAt"`
}
type grepMatch struct {
	Path    string `json:"path"`
	Line    int    `json:"line"`
	Content string `json:"content"`
}

func virtual(p string) string {
	if p == "." {
		return "/workspace"
	}
	return "/workspace/" + p
}
func workspacePath(p string) (string, error) {
	if p == "/workspace" {
		p = "."
	} else if strings.HasPrefix(p, "/workspace/") {
		p = strings.TrimPrefix(p, "/workspace/")
	}
	if p == "" {
		p = "."
	}
	if len(p) > 4096 || strings.ContainsAny(p, "\x00\\") || strings.HasPrefix(p, "/") {
		return "", errors.New("invalid path")
	}
	for _, part := range strings.Split(p, "/") {
		if part == ".." {
			return "", errors.New("path escapes workspace")
		}
	}
	return path.Clean(p), nil
}
func workspaceError(code string) *agent.SandboxWorkspaceResponse {
	message := map[string]string{"unauthorized": "Invalid service credential", "invalid_request": "Invalid workspace operation", "file_conflict": "Edit must match exactly one occurrence unless replaceAll is true", "file_too_large": "Workspace file exceeds the operation limit", "invalid_path": "Path must stay inside /workspace"}[code]
	if message == "" {
		message = "Workspace file is unavailable or not a regular file"
	}
	return &agent.SandboxWorkspaceResponse{ErrorCode: &code, ErrorMessage: &message}
}
func workspaceResult(data any) *agent.SandboxWorkspaceResponse {
	b, e := json.Marshal(data)
	if e != nil || len(b) > MaxWorkspaceResponseBytes {
		return workspaceError("file_too_large")
	}
	value := string(b)
	return &agent.SandboxWorkspaceResponse{Ok: true, DataJson: &value}
}

const MaxWorkspaceResponseBytes = 1024 * 1024

func regularRead(root *os.Root, name string) ([]byte, error) {
	f, e := root.OpenFile(name, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if e != nil {
		return nil, e
	}
	defer f.Close()
	info, e := f.Stat()
	if e != nil || !info.Mode().IsRegular() {
		return nil, errors.New("not a regular file")
	}
	if info.Size() > maxFileBytes {
		return nil, errors.New("file_too_large")
	}
	b, e := io.ReadAll(io.LimitReader(f, maxFileBytes+1))
	if len(b) > maxFileBytes {
		return nil, errors.New("file_too_large")
	}
	if !utf8.Valid(b) {
		return nil, errors.New("binary file")
	}
	return b, e
}

// O_DIRECTORY and O_NONBLOCK prevent a concurrently substituted FIFO from
// blocking a service operation while it holds the file mutation lock. Root may
// resolve confined symlinks even with O_NOFOLLOW, so also match the opened fd to
// the directory inode validated by Lstat before using it for chmod or fsync.
func openWorkspaceDirectory(root *os.Root, name string) (*os.File, error) {
	before, e := root.Lstat(name)
	if e != nil {
		return nil, e
	}
	if !before.IsDir() || before.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("not a directory")
	}
	f, e := root.OpenFile(name, os.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if e != nil {
		return nil, e
	}
	info, e := f.Stat()
	if e != nil || !info.IsDir() || !os.SameFile(before, info) {
		f.Close()
		if e == nil {
			e = errors.New("not a directory")
		}
		return nil, e
	}
	return f, nil
}

func ensureWorkspaceParents(root *os.Root, parent string) error {
	current := ""
	if parent == "." {
		return nil
	}
	for _, part := range strings.Split(parent, "/") {
		current = path.Join(current, part)
		if e := root.Mkdir(current, 0777); e != nil && !os.IsExist(e) {
			return e
		}
		f, e := openWorkspaceDirectory(root, current)
		if e != nil {
			return e
		}
		e = f.Chmod(0777)
		f.Close()
		if e != nil {
			return e
		}
	}
	return nil
}

func atomicWorkspaceWrite(root *os.Root, name, content string) error {
	if name == "." || len(content) > maxFileBytes {
		return errors.New("file_too_large")
	}
	parent := path.Dir(name)
	if e := ensureWorkspaceParents(root, parent); e != nil {
		return e
	}
	if info, e := root.Lstat(name); e == nil && !info.Mode().IsRegular() {
		return errors.New("not a regular file")
	}
	nameTmp := path.Join(parent, ".teamie-write-"+digest(time.Now().UTC().String() + name)[:24])
	f, e := root.OpenFile(nameTmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY|syscall.O_NOFOLLOW, 0666)
	if e != nil {
		return e
	}
	defer root.Remove(nameTmp)
	if e = f.Chmod(0666); e == nil {
		_, e = io.WriteString(f, content)
	}
	if e == nil {
		e = f.Sync()
	}
	closeErr := f.Close()
	if e == nil {
		e = closeErr
	}
	if e == nil {
		e = root.Rename(nameTmp, name)
	}
	if e == nil {
		dir, openErr := openWorkspaceDirectory(root, parent)
		if openErr != nil {
			return openErr
		}
		e = dir.Sync()
		dir.Close()
	}
	return e
}
func globMatches(pattern, name string) bool {
	a, b := strings.Split(strings.TrimPrefix(pattern, "/"), "/"), strings.Split(name, "/")
	memo := make(map[[2]int]bool)
	seen := make(map[[2]int]bool)
	var match func(int, int) bool
	match = func(i, j int) (result bool) {
		key := [2]int{i, j}
		if seen[key] {
			return memo[key]
		}
		seen[key] = true
		defer func() { memo[key] = result }()
		if i == len(a) {
			return j == len(b)
		}
		if a[i] == "**" {
			return match(i+1, j) || j < len(b) && match(i, j+1)
		}
		if j == len(b) {
			return false
		}
		yes, e := path.Match(a[i], b[j])
		return e == nil && yes && match(i+1, j+1)
	}
	return match(0, 0)
}

func (s *Service) Workspace(ctx context.Context, r *agent.SandboxWorkspaceRequest) (*agent.SandboxWorkspaceResponse, error) {
	if r == nil || !api.EqualSecret(r.ServiceToken, s.Token) {
		return workspaceError("unauthorized"), nil
	}
	if !validID(r.AgentId) || len(r.ArgsJson) > maxFileBytes*2 {
		return workspaceError("invalid_request"), nil
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
	workspace, e := s.workspace(r.AgentId)
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
			files = append(files, fileInfo{virtual(path.Join(name, entry.Name())), info.IsDir(), info.Size(), info.ModTime().UTC().Format(time.RFC3339Nano)})
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
					files = append(files, fileInfo{virtual(current), info.IsDir(), info.Size(), info.ModTime().UTC().Format(time.RFC3339Nano)})
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
					matches = append(matches, grepMatch{virtual(current), line + 1, content})
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
					matches = append(matches, grepMatch{virtual(current), line + 1, content})
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
