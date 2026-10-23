package app

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"teamie/services/sandbox/internal/domain"
)

const maxFileBytes = domain.MaxFileBytes
const maxWorkspaceEntries = domain.MaxWorkspaceEntries
const MaxWorkspaceResponseBytes = domain.MaxWorkspaceResponseBytes
const MaxReadableFileBytes = domain.MaxReadableFileBytes
const MaxPageBytes = domain.MaxPageBytes
const maxPageMatches = domain.MaxPageMatches

type workspaceArgs = domain.WorkspaceArgs
type fileInfo = domain.FileInfo
type grepMatch = domain.GrepMatch
type pageCursor = domain.PageCursor

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

func encodePageCursor(c pageCursor) string {
	return domain.EncodePageCursor(c)
}
func pageFits(v any) bool { return domain.PageFits(v) }
func utf8End(b []byte, end int) int {
	return domain.UTF8End(b, end)
}
func readPageFile(root *os.Root, name string) ([]byte, error) {
	return domain.DefaultWorkspaceStorage.ReadPageFile(root, name)
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
		f, e := domain.DefaultWorkspaceStorage.OpenNonblock(root, name)
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

func (s *Service) workspacePage(ctx context.Context, r *domain.SandboxWorkspaceRequest, in workspaceArgs) *domain.SandboxWorkspaceResponse {
	if in.PageVersion != 1 || in.ByteOffset < 0 || in.MaxBytes < 0 || in.MaxBytes > MaxPageBytes || len(in.Cursor) > 4096 || len(in.ExpectedVersion) > 64 {
		return workspaceError("invalid_request")
	}
	if r.Operation == "output" {
		return s.outputPage(ctx, r, in)
	}
	if r.Operation != "read" && r.Operation != "ls" && r.Operation != "glob" && r.Operation != "grep" {
		return workspaceError("invalid_request")
	}
	name, e := workspacePath(in.Path)
	if e != nil {
		return workspaceError("invalid_path")
	}
	workspace, e := s.WorkspaceDir(r.AgentId)
	if e != nil {
		return workspaceError("file_unavailable")
	}
	root, e := os.OpenRoot(workspace)
	if e != nil {
		return workspaceError("file_unavailable")
	}
	defer root.Close()
	maxBytes := in.MaxBytes
	if maxBytes == 0 {
		maxBytes = 16384
	}
	if r.Operation == "read" {
		b, e := readPageFile(root, name)
		if e != nil {
			return workspaceError(e.Error())
		}
		version := digest(string(b))
		if in.ExpectedVersion != "" && in.ExpectedVersion != version {
			return workspaceError("file_changed")
		}
		start := int(in.ByteOffset)
		if start > len(b) || start < 0 || start < len(b) && !utf8.RuneStart(b[start]) {
			return workspaceError("invalid_request")
		}
		if in.ByteOffset == 0 && in.Offset > 1 {
			for n := 1; n < in.Offset && start < len(b); n++ {
				at := strings.IndexByte(string(b[start:]), '\n')
				if at < 0 {
					start = len(b)
				} else {
					start += at + 1
				}
			}
		}
		end := utf8End(b, start+maxBytes)
		if end == start && start < len(b) {
			return workspaceError("invalid_request")
		}
		if in.Limit > 0 {
			p := start
			for n := 0; n < in.Limit && p < end; n++ {
				at := strings.IndexByte(string(b[p:end]), '\n')
				if at < 0 {
					p = end
					break
				}
				p += at + 1
			}
			if p < end {
				end = p
			}
		}
		var out map[string]any
		for {
			out = map[string]any{"pageVersion": 1, "path": virtual(name), "content": string(b[start:end]), "byteOffset": start, "nextOffset": end, "line": 1 + strings.Count(string(b[:start]), "\n"), "sizeBytes": len(b), "sha256": version, "hasMore": end < len(b), "truncated": end < len(b)}
			if end < len(b) {
				out["resume"] = map[string]any{"operation": "read", "path": virtual(name), "byteOffset": end, "expectedVersion": version, "maxBytes": maxBytes}
			}
			if pageFits(out) {
				break
			}
			end = utf8End(b, start+(end-start)/2)
			if end <= start {
				return workspaceError("file_too_large")
			}
		}
		if ctx.Err() != nil {
			return workspaceError("file_unavailable")
		}
		return workspaceResult(out)
	}
	return s.workspaceSearchPage(ctx, r.Operation, in, name, root)
}

type pageGrepMatch struct {
	Path                 string         `json:"path"`
	Line                 int            `json:"line"`
	EndLine              int            `json:"endLine"`
	ContentStartLine     int            `json:"contentStartLine"`
	MatchByteOffset      int            `json:"matchByteOffset"`
	MatchEndByteOffset   int            `json:"matchEndByteOffset"`
	ContentByteOffset    int            `json:"contentByteOffset"`
	ContentEndByteOffset int            `json:"contentEndByteOffset"`
	SHA256               string         `json:"sha256"`
	Content              string         `json:"content"`
	ContentTruncated     bool           `json:"contentTruncated"`
	ReadResume           map[string]any `json:"readResume,omitempty"`
}

func (s *Service) workspaceSearchPage(ctx context.Context, op string, in workspaceArgs, name string, root *os.Root) *domain.SandboxWorkspaceResponse {
	if len(in.Pattern) > 4096 || in.Limit > 512 {
		return workspaceError("invalid_request")
	}
	if op == "glob" {
		in.Pattern = strings.TrimPrefix(in.Pattern, "/workspace/")
		if _, e := workspacePath(in.Pattern); e != nil || len(in.Pattern) > 512 {
			return workspaceError("invalid_path")
		}
	}
	var re *regexp.Regexp
	if op == "grep" {
		p := in.Pattern
		if in.CaseInsensitive {
			p = "(?i)" + p
		}
		if in.EnableMultiline {
			p = "(?ms)" + p
		}
		var e error
		re, e = regexp.Compile(p)
		if e != nil {
			return workspaceError("invalid_request")
		}
	}
	files := []fileInfo{}
	skipped := 0
	limited := false
	visited := 0
	add := func(current string, entry fs.DirEntry, walkErr error) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		visited++
		if visited > 10000 {
			limited = true
			return fs.SkipAll
		}
		if walkErr != nil {
			skipped++
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
		if op == "glob" && !globMatches(in.Pattern, rel) {
			return nil
		}
		if op == "grep" && (entry.IsDir() || in.Glob != "" && !globMatches(in.Glob, rel) || in.FileType != "" && !strings.HasSuffix(current, "."+in.FileType)) {
			return nil
		}
		i, e := entry.Info()
		if e != nil {
			skipped++
			return nil
		}
		if !i.IsDir() && !i.Mode().IsRegular() {
			return nil
		}
		files = append(files, fileInfo{Path: virtual(current), IsDir: i.IsDir(), Size: i.Size(), ModifiedAt: i.ModTime().UTC().Format(time.RFC3339Nano)})
		return nil
	}
	if op == "ls" {
		f, e := domain.DefaultWorkspaceStorage.OpenNonblock(root, name)
		if os.IsNotExist(e) {
			return workspaceResult(map[string]any{"pageVersion": 1, "files": []fileInfo{}, "hasMore": false, "truncated": false})
		}
		if e != nil {
			return workspaceError("file_unavailable")
		}
		defer f.Close()
		entries, e := f.ReadDir(10001)
		if e != nil && e != io.EOF {
			return workspaceError("file_unavailable")
		}
		for _, entry := range entries {
			if e = add(path.Join(name, entry.Name()), entry, nil); e != nil {
				break
			}
		}
	} else if e := fs.WalkDir(root.FS(), name, add); e != nil {
		return workspaceError("file_unavailable")
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	b, _ := json.Marshal(files)
	version := digest(string(b))
	query, _ := json.Marshal([]any{op, name, in.Pattern, in.Glob, in.FileType, in.CaseInsensitive, in.EnableMultiline, in.BeforeLines, in.AfterLines})
	c := pageCursor{Query: digest(string(query)), Version: version}
	if in.Cursor != "" {
		old := c
		b, e := base64.RawURLEncoding.DecodeString(in.Cursor)
		if e != nil || json.Unmarshal(b, &c) != nil || c.Query != old.Query || c.File < 0 || c.Match < 0 || c.File > len(files) {
			return workspaceError("invalid_cursor")
		}
		if c.Version != version {
			return workspaceError("file_changed")
		}
	}
	if in.ExpectedVersion != "" && in.ExpectedVersion != version {
		return workspaceError("file_changed")
	}
	limit := in.Limit
	if limit == 0 {
		limit = 128
	}
	selected := []fileInfo{}
	matches := []pageGrepMatch{}
	scanned := 0
	abbreviated := false
	coverage := map[string]any{"complete": !limited && skipped == 0, "skippedFiles": skipped, "scanLimited": limited}
	result := func(more bool) map[string]any {
		out := map[string]any{"pageVersion": 1, "version": version, "hasMore": more, "truncated": more || limited || skipped > 0 || abbreviated, "coverage": coverage}
		if op == "grep" {
			out["matches"] = matches
		} else {
			out["files"] = selected
		}
		if more {
			out["nextCursor"] = encodePageCursor(c)
			out["resume"] = map[string]any{"operation": op, "path": virtual(name), "pattern": in.Pattern, "glob": in.Glob, "fileType": in.FileType, "caseInsensitive": in.CaseInsensitive, "enableMultiline": in.EnableMultiline, "beforeLines": in.BeforeLines, "afterLines": in.AfterLines, "cursor": encodePageCursor(c), "limit": limit}
		}
		return out
	}
	for c.File < len(files) {
		if op != "grep" {
			selected = append(selected, files[c.File])
			if len(selected) > 1 && !pageFits(result(true)) {
				selected = selected[:len(selected)-1]
				break
			}
			c.File++
			if len(selected) >= limit {
				break
			}
			continue
		}
		f := files[c.File]
		if f.Size > MaxReadableFileBytes {
			skipped++
			coverage["skippedFiles"] = skipped
			coverage["complete"] = false
			c.File++
			c.Match = 0
			c.FileVersion = ""
			continue
		}
		if scanned > 0 && scanned+int(f.Size) > MaxReadableFileBytes {
			break
		}
		text, e := readPageFile(root, strings.TrimPrefix(f.Path, "/workspace/"))
		if e != nil {
			skipped++
			coverage["skippedFiles"] = skipped
			coverage["complete"] = false
			c.File++
			c.Match = 0
			c.FileVersion = ""
			continue
		}
		scanned += len(text)
		fileVersion := digest(string(text))
		if c.FileVersion != "" && c.FileVersion != fileVersion {
			return workspaceError("file_changed")
		}
		c.FileVersion = fileVersion
		indices := [][2]int{}
		if in.EnableMultiline {
			for _, at := range re.FindAllIndex(text, maxPageMatches+1) {
				indices = append(indices, [2]int{at[0], at[1]})
			}
		} else {
			for p := 0; p < len(text); {
				end := p + strings.IndexByte(string(text[p:]), '\n')
				if end < p {
					end = len(text)
				}
				if at := re.FindIndex(text[p:end]); at != nil {
					indices = append(indices, [2]int{p + at[0], p + at[1]})
					if len(indices) > maxPageMatches {
						break
					}
				}
				p = end + 1
			}
		}
		for c.Match < len(indices) && c.Match < maxPageMatches {
			at := indices[c.Match]
			line := 1 + strings.Count(string(text[:at[0]]), "\n")
			endLine := line + strings.Count(string(text[at[0]:at[1]]), "\n")
			start := at[0]
			for start > 0 && text[start-1] != '\n' {
				start--
			}
			startLine := line
			for n := 0; n < in.BeforeLines && start > 0; n++ {
				start--
				for start > 0 && text[start-1] != '\n' {
					start--
				}
				startLine--
			}
			end := at[1]
			for end < len(text) && text[end] != '\n' {
				end++
			}
			for n := 0; n < in.AfterLines && end < len(text); n++ {
				end++
				for end < len(text) && text[end] != '\n' {
					end++
				}
			}
			clip := utf8End(text, start+4096)
			if clip > end {
				clip = end
			}
			short := clip < end
			m := pageGrepMatch{Path: f.Path, Line: line, EndLine: endLine, ContentStartLine: startLine, MatchByteOffset: at[0], MatchEndByteOffset: at[1], ContentByteOffset: start, ContentEndByteOffset: clip, SHA256: fileVersion, Content: string(text[start:clip]), ContentTruncated: short}
			if short {
				m.ReadResume = map[string]any{"operation": "read", "path": f.Path, "byteOffset": at[0], "expectedVersion": fileVersion, "maxBytes": 16384}
			}
			matches = append(matches, m)
			if len(matches) > 1 && !pageFits(result(true)) {
				matches = matches[:len(matches)-1]
				return workspaceResult(result(true))
			}
			c.Match++
			abbreviated = abbreviated || short
			if len(matches) >= limit {
				return workspaceResult(result(true))
			}
		}
		if len(indices) > maxPageMatches {
			limited = true
			coverage["complete"] = false
			coverage["matchLimited"] = true
			coverage["hint"] = "At most 10000 matches are examined in one file. Narrow the pattern or read that file by byteOffset; no absence claim is made beyond this bound."
		}
		c.File++
		c.Match = 0
		c.FileVersion = ""
	}
	return workspaceResult(result(c.File < len(files)))
}

func (s *Service) outputPage(ctx context.Context, r *domain.SandboxWorkspaceRequest, in workspaceArgs) *domain.SandboxWorkspaceResponse {
	limit := int32(in.MaxBytes)
	if limit == 0 {
		limit = 16384
	}
	offset := in.ByteOffset
	out, e := s.ReadOutput(ctx, &domain.SandboxTaskRequest{ServiceToken: r.ServiceToken, AgentId: r.AgentId, TaskId: in.TaskID, Offset: &offset, Limit: &limit})
	if e != nil {
		return workspaceError("file_unavailable")
	}
	if !out.Ok {
		return workspaceError(out.GetErrorCode())
	}
	data := map[string]any{"pageVersion": 1, "taskId": in.TaskID, "byteOffset": offset, "nextOffset": out.GetNextOffset(), "stdoutBase64": base64.StdEncoding.EncodeToString([]byte(out.GetStdout())), "stderrBase64": base64.StdEncoding.EncodeToString([]byte(out.GetStderr())), "state": out.State, "hasMore": out.GetHasMore(), "truncated": out.GetTruncated(), "outputBytes": out.GetOutputBytes(), "outputLimitBytes": out.GetOutputLimitBytes(), "outputStatus": out.GetOutputStatus(), "outputSha256": out.GetOutputSha256()}
	if out.OutputExpiresAt != nil {
		data["outputExpiresAt"] = out.GetOutputExpiresAt()
	}
	if out.OutputRetentionSeconds != nil {
		data["outputRetentionSeconds"] = out.GetOutputRetentionSeconds()
	}
	if out.OutputAvailability != nil {
		data["outputAvailability"] = out.GetOutputAvailability()
	}
	s.TaskMu.Lock()
	receipt, loadErr := s.LoadTaskLocked(r.AgentId, in.TaskID)
	var segments []outputSegment
	if loadErr == nil {
		segments, loadErr = s.outputSegmentsLocked(taskKey(r.AgentId, in.TaskID), receipt, offset, out.GetNextOffset())
	}
	s.TaskMu.Unlock()
	if loadErr != nil {
		if errors.Is(loadErr, errOutputExpired) {
			return workspaceError("output_expired")
		}
		return workspaceError("execution_unknown")
	}
	data["segments"] = segments
	if utf8.ValidString(out.GetStdout()) && utf8.ValidString(out.GetStderr()) {
		data["stdout"] = out.GetStdout()
		data["stderr"] = out.GetStderr()
	}
	if !pageFits(data) {
		in.MaxBytes = int(limit) / 2
		return s.outputPage(ctx, r, in)
	}
	if out.GetHasMore() || out.State == "running" || out.State == "not_started" {
		data["resume"] = map[string]any{"operation": "output", "taskId": in.TaskID, "byteOffset": out.GetNextOffset(), "maxBytes": limit}
	}
	return workspaceResult(data)
}
