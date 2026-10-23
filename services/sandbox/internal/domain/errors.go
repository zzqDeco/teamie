// Package domain contains pure Go entities, value objects, business invariants,
// and outbound port interfaces for the Sandbox bounded context. It has zero
// dependencies on HTTP, RPC, Docker client libraries, or external packages.
package domain

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"regexp"
	"strings"
)

const (
	acrRegionPattern    = `[a-z]{2}(?:-[a-z0-9]+)+`
	acrInstancePattern  = `[a-z0-9](?:[a-z0-9-]*[a-z0-9])?`
	acrNamespacePattern = `[a-z0-9]+(?:[._-][a-z0-9]+)*`
	acrRegistryPattern  = `(?:registry(?:-vpc)?\.` + acrRegionPattern + `\.aliyuncs\.com|crpi-[a-z0-9]+(?:-vpc)?\.` + acrRegionPattern + `\.personal\.cr\.aliyuncs\.com|` + acrInstancePattern + `-registry(?:-vpc)?\.` + acrRegionPattern + `\.cr\.aliyuncs\.com)`
)

var imagePattern = regexp.MustCompile(`^(?:ghcr\.io/zzqdeco/teamie-sandbox-runner@|` + acrRegistryPattern + `/` + acrNamespacePattern + `/teamie-sandbox-runner@)?sha256:[0-9a-f]{64}$`)

var (
	ErrContainerMissing = errors.New("managed container does not exist")
	ErrOutputLimit      = errors.New("durable output limit")
	ErrOutputGap        = errors.New("container output replay gap")
	ErrOutputExpired    = errors.New("output_expired")
)

// Error represents a coded sandbox domain error with a safe code, diagnostic stage/reason, and internal cause.
type Error struct {
	Status  int
	Code    string
	Stage   string
	Reason  string
	Message string
	Cause   error
}

func (e *Error) Error() string             { return e.Message }
func (e *Error) StatusCode() int           { return e.Status }
func (e *Error) ErrorCode() string         { return e.Code }
func (e *Error) DiagnosticStage() string   { return e.Stage }
func (e *Error) DiagnosticReason() string  { return e.Reason }
func (e *Error) DiagnosticMessage() string { return e.Code + ": " + e.Message }
func (e *Error) Unwrap() error             { return e.Cause }

// ValidRunnerImage validates that image is an immutable platform runner image digest.
func ValidRunnerImage(image string) bool {
	if !imagePattern.MatchString(image) {
		return false
	}
	if strings.HasPrefix(image, "sha256:") || strings.HasPrefix(image, "ghcr.io/") {
		return true
	}
	parts := strings.SplitN(image, "/", 3)
	if len(parts[1]) > 63 {
		return false
	}
	for _, label := range strings.Split(parts[0], ".") {
		if len(label) > 63 {
			return false
		}
	}
	return true
}

// Digest returns the hex-encoded SHA-256 hash of v.
func Digest(v string) string {
	b := sha256.Sum256([]byte(v))
	return hex.EncodeToString(b[:])
}

// ValidID checks that an identifier is non-empty, <= 160 bytes, and free of control characters.
func ValidID(v string) bool {
	return v != "" && len(v) <= 160 && !strings.ContainsAny(v, "\x00\r\n")
}

// EqualSecret compares two secret strings of length >= 24 in constant time.
func EqualSecret(a, b string) bool {
	x, y := sha256.Sum256([]byte(a)), sha256.Sum256([]byte(b))
	return len(a) >= 24 && len(b) >= 24 && subtle.ConstantTimeCompare(x[:], y[:]) == 1
}

// ExecErrorMessage maps an execution error code to its safe public message.
func ExecErrorMessage(code string) string {
	msg := map[string]string{
		"execution_unknown":  "A prior execution was interrupted; its side effects must be inspected before retrying",
		"execution_canceled": "Execution canceled",
		"execution_timeout":  "Execution exceeded its time limit",
	}[code]
	if msg == "" {
		msg = "Execution could not be completed"
	}
	return msg
}

// WorkspaceErrorMessage maps a workspace error code to its safe public message.
func WorkspaceErrorMessage(code string) string {
	msg := map[string]string{
		"unauthorized":    "Invalid service credential",
		"invalid_request": "Invalid workspace operation",
		"file_conflict":   "Edit must match exactly one occurrence unless replaceAll is true",
		"file_too_large":  "Workspace file exceeds the operation limit",
		"invalid_path":    "Path must stay inside /workspace",
	}[code]
	if msg == "" {
		msg = "Workspace file is unavailable or not a regular file"
	}
	return msg
}

// TaskErrorMessage maps a task error code to its safe public message.
func TaskErrorMessage(code string) string {
	msg := map[string]string{
		"unauthorized":      "Invalid service credential",
		"invalid_request":   "Invalid sandbox request",
		"request_conflict":  "Task ID already identifies a different command",
		"sandbox_busy":      "The sandbox execution slot is busy",
		"execution_unknown": "Execution state is unknown; inspect its side effects before retrying",
	}[code]
	if msg == "" {
		msg = "Sandbox operation could not be completed"
	}
	return msg
}
