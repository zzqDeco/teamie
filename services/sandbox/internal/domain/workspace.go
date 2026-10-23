package domain

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"path"
	"strings"
	"unicode/utf8"
)

const (
	MaxFileBytes              = 262144
	MaxWorkspaceEntries       = 512
	MaxWorkspaceResponseBytes = 1024 * 1024
	MaxReadableFileBytes      = 8 * 1024 * 1024
	MaxPageBytes              = 65536
	MaxPageMatches            = 10000
	MaxWorkspaceBytes         = int64(64 * 1024 * 1024)
	MaxWorkspaceFiles         = 4096
	AttachmentBytes           = 4 * 1024 * 1024
)

// WorkspacePolicy groups workspace path confinement, globbing, and paging invariants.
type WorkspacePolicy struct{}

// WorkspaceArgs represents decoded workspace operation arguments.
type WorkspaceArgs struct {
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

// FileInfo describes a single entry inside an agent's workspace.
type FileInfo struct {
	Path       string `json:"path"`
	IsDir      bool   `json:"isDir"`
	Size       int64  `json:"size"`
	ModifiedAt string `json:"modifiedAt"`
}

// GrepMatch describes a single line/block match from workspace grep.
type GrepMatch struct {
	Path    string `json:"path"`
	Line    int    `json:"line"`
	Content string `json:"content"`
}

// PageCursor represents an opaque pagination cursor for workspace queries.
type PageCursor struct {
	Query       string `json:"q"`
	Version     string `json:"v"`
	File        int    `json:"f"`
	Match       int    `json:"m,omitempty"`
	FileVersion string `json:"h,omitempty"`
}

// EncodePageCursor serializes a PageCursor as raw URL-safe base64.
func EncodePageCursor(c PageCursor) string {
	b, _ := json.Marshal(c)
	return base64.RawURLEncoding.EncodeToString(b)
}

// PageFits reports whether v serializes within MaxPageBytes.
func PageFits(v any) bool {
	b, err := json.Marshal(v)
	return err == nil && len(b) <= MaxPageBytes
}

// UTF8End backs up end to the nearest UTF-8 rune boundary in b.
func UTF8End(b []byte, end int) int {
	if end > len(b) {
		end = len(b)
	}
	for end > 0 && end < len(b) && !utf8.RuneStart(b[end]) {
		end--
	}
	return end
}

// VirtualPath formats a relative workspace path as /workspace or /workspace/<p>.
func VirtualPath(p string) string {
	if p == "." {
		return "/workspace"
	}
	return "/workspace/" + p
}

// WorkspacePath validates and normalizes a path inside /workspace.
func WorkspacePath(p string) (string, error) {
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

// GlobMatches matches name against a slash-separated glob pattern supporting "**".
func GlobMatches(pattern, name string) bool {
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
		yes, err := path.Match(a[i], b[j])
		return err == nil && yes && match(i+1, j+1)
	}
	return match(0, 0)
}
