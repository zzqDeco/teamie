package control

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strings"
	"teamie/backend/pkg/observability"
	"time"
)

var Version = "unreleased"
var SHA = "unknown"

type Agent struct {
	ID                        string     `json:"id"`
	PrincipalID               string     `json:"principalId"`
	OwnerID                   string     `json:"ownerId"`
	OwnerName                 string     `json:"ownerName"`
	Name                      string     `json:"name"`
	Description               string     `json:"description"`
	WorkspaceIDs              []string   `json:"workspaceIds"`
	Enabled                   bool       `json:"enabled"`
	Revision                  int64      `json:"revision"`
	CreatedAt                 time.Time  `json:"createdAt"`
	UpdatedAt                 time.Time  `json:"updatedAt"`
	DeletedAt                 *time.Time `json:"deletedAt,omitempty"`
	RuntimeState              string     `json:"runtimeState"`
	SessionID                 string     `json:"sessionId"`
	LastActiveAt              *time.Time `json:"lastActiveAt,omitempty"`
	LastError                 string     `json:"lastError,omitempty"`
	MembershipRevision        int64      `json:"-"`
	AppliedRevision           int64      `json:"-"`
	AppliedMembershipRevision int64      `json:"-"`
	PolicyEnabled             bool       `json:"-"`
}
type Changes struct {
	Name         *string   `json:"name"`
	Description  *string   `json:"description"`
	WorkspaceIDs *[]string `json:"workspaceIds"`
	Enabled      *bool     `json:"enabled"`
}
type Error struct {
	Status        int
	Code, Message string
}

func (e *Error) Error() string                      { return e.Message }
func (e *Error) DiagnosticMessage() string          { return e.Code }
func errAPI(status int, code, message string) error { return &Error{status, code, message} }
func fail(w http.ResponseWriter, e error) {
	observability.RecordError(w, e)
	var a *Error
	if !errors.As(e, &a) {
		a = &Error{503, "control_unavailable", "Agent control is temporarily unavailable"}
	}
	write(w, a.Status, map[string]any{"error": map[string]string{"code": a.Code, "message": a.Message}})
}
func write(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	if v != nil {
		_ = json.NewEncoder(w).Encode(v)
	}
}
func env(key, otherwise string) string {
	if s := os.Getenv(key); s != "" {
		return s
	}
	return otherwise
}
func validKey(expected, provided string) bool {
	return expected != "" && provided != "" && subtle.ConstantTimeCompare([]byte(expected), []byte(provided)) == 1
}
func id() string {
	b := make([]byte, 16)
	if _, e := rand.Read(b); e != nil {
		panic(e)
	}
	b[6] = (b[6] & 15) | 64
	b[8] = (b[8] & 63) | 128
	raw := hex.EncodeToString(b)
	return fmt.Sprintf("%s-%s-%s-%s-%s", raw[:8], raw[8:12], raw[12:16], raw[16:20], raw[20:])
}
func normalized(ids []string) []string {
	m := map[string]bool{}
	for _, id := range ids {
		if id != "" {
			m[id] = true
		}
	}
	out := []string{}
	for id := range m {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}
func bounded(v string, min, max int) bool {
	n := len([]rune(strings.TrimSpace(v)))
	return n >= min && n <= max
}
func same(a, b []string) bool {
	return strings.Join(normalized(a), "\x00") == strings.Join(normalized(b), "\x00")
}
