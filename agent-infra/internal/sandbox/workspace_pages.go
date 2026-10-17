package sandbox

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
	"syscall"
	"time"
	"unicode/utf8"

	"teamie/agent-protocol/kitex_gen/agent"
)

const MaxReadableFileBytes = 8 * 1024 * 1024
const MaxPageBytes = 65536
const maxPageMatches = 10000

type pageCursor struct {
	Query   string `json:"q"`
	Version string `json:"v"`
	File    int    `json:"f"`
	Match   int    `json:"m,omitempty"`
}

func encodePageCursor(c pageCursor) string {
	b, _ := json.Marshal(c)
	return base64.RawURLEncoding.EncodeToString(b)
}
func pageFits(v any) bool { b, e := json.Marshal(v); return e == nil && len(b) <= MaxPageBytes }
func utf8End(b []byte, end int) int {
	if end > len(b) {
		end = len(b)
	}
	for end > 0 && end < len(b) && !utf8.RuneStart(b[end]) {
		end--
	}
	return end
}
func readPageFile(root *os.Root, name string) ([]byte, error) {
	f, e := root.OpenFile(name, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if e != nil {
		return nil, e
	}
	defer f.Close()
	i, e := f.Stat()
	if e != nil || !i.Mode().IsRegular() {
		return nil, errors.New("file_unavailable")
	}
	if i.Size() > MaxReadableFileBytes {
		return nil, errors.New("file_too_large")
	}
	b, e := io.ReadAll(io.LimitReader(f, MaxReadableFileBytes+1))
	if len(b) > MaxReadableFileBytes {
		return nil, errors.New("file_too_large")
	}
	if !utf8.Valid(b) {
		return nil, errors.New("binary_file")
	}
	return b, e
}

// Paging is an explicit new protocol. Legacy callers retain their original
// limits and serialized receipts, including native PlanTask file operations.
func (s *Service) workspacePage(ctx context.Context, r *agent.SandboxWorkspaceRequest, in workspaceArgs) *agent.SandboxWorkspaceResponse {
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
	workspace, e := s.workspace(r.AgentId)
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
	Path             string `json:"path"`
	Line             int    `json:"line"`
	EndLine          int    `json:"endLine"`
	ContentStartLine int    `json:"contentStartLine"`
	MatchByteOffset  int    `json:"matchByteOffset"`
	Content          string `json:"content"`
	ContentTruncated bool   `json:"contentTruncated"`
}

func (s *Service) workspaceSearchPage(ctx context.Context, op string, in workspaceArgs, name string, root *os.Root) *agent.SandboxWorkspaceResponse {
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
		files = append(files, fileInfo{virtual(current), i.IsDir(), i.Size(), i.ModTime().UTC().Format(time.RFC3339Nano)})
		return nil
	}
	if op == "ls" {
		f, e := root.OpenFile(name, os.O_RDONLY|syscall.O_NONBLOCK, 0)
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
			continue
		}
		scanned += len(text)
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
				if re.Match(text[p:end]) {
					indices = append(indices, [2]int{p, end})
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
			m := pageGrepMatch{f.Path, line, endLine, startLine, at[0], string(text[start:clip]), short}
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
		}
		c.File++
		c.Match = 0
	}
	return workspaceResult(result(c.File < len(files)))
}

func (s *Service) outputPage(ctx context.Context, r *agent.SandboxWorkspaceRequest, in workspaceArgs) *agent.SandboxWorkspaceResponse {
	limit := int32(in.MaxBytes)
	if limit == 0 {
		limit = 16384
	}
	offset := in.ByteOffset
	out, e := s.ReadOutput(ctx, &agent.SandboxTaskRequest{ServiceToken: r.ServiceToken, AgentId: r.AgentId, TaskId: in.TaskID, Offset: &offset, Limit: &limit})
	if e != nil {
		return workspaceError("file_unavailable")
	}
	if !out.Ok {
		return workspaceError(out.GetErrorCode())
	}
	// Base64 is always available for an exact binary-safe reference; UTF-8 text
	// is convenience data only and never replaces the canonical stored bytes.
	data := map[string]any{"pageVersion": 1, "taskId": in.TaskID, "byteOffset": offset, "nextOffset": out.GetNextOffset(), "stdoutBase64": base64.StdEncoding.EncodeToString([]byte(out.GetStdout())), "stderrBase64": base64.StdEncoding.EncodeToString([]byte(out.GetStderr())), "state": out.State, "hasMore": out.GetHasMore(), "truncated": out.GetTruncated(), "outputBytes": out.GetOutputBytes(), "outputLimitBytes": out.GetOutputLimitBytes(), "outputStatus": out.GetOutputStatus(), "outputSha256": out.GetOutputSha256()}
	s.taskMu.Lock()
	receipt, loadErr := s.loadTaskLocked(r.AgentId, in.TaskID)
	var segments []outputSegment
	if loadErr == nil {
		segments, loadErr = s.outputSegmentsLocked(taskKey(r.AgentId, in.TaskID), receipt, offset, out.GetNextOffset())
	}
	s.taskMu.Unlock()
	if loadErr != nil {
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
