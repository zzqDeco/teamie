package api

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"
)

const CookieName = "teamie_session"

type User struct {
	ID           string    `json:"id"`
	Email        string    `json:"email"`
	DisplayName  string    `json:"displayName"`
	Kind         string    `json:"kind"`
	PlatformRole string    `json:"platformRole"`
	CreatedAt    time.Time `json:"createdAt"`
}
type Session struct {
	User             User   `json:"user"`
	CSRFToken        string `json:"csrfToken"`
	ServicePrincipal bool   `json:"-"`
}
type Error struct {
	Status        int
	Code, Message string
}

func (e *Error) Error() string                   { return e.Message }
func Err(status int, code, message string) error { return &Error{status, code, message} }
func Write(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if value != nil {
		_ = json.NewEncoder(w).Encode(value)
	}
}
func Fail(w http.ResponseWriter, err error) {
	var ae *Error
	if !errors.As(err, &ae) {
		slog.Error("request failed", "error", err)
		ae = &Error{500, "internal_error", "The request could not be completed"}
	}
	Write(w, ae.Status, map[string]any{"error": map[string]string{"code": ae.Code, "message": ae.Message}})
}
func Decode(w http.ResponseWriter, r *http.Request, v any) error {
	r.Body = http.MaxBytesReader(w, r.Body, 131072)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		return Err(400, "invalid_request", "Invalid JSON request")
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return Err(400, "invalid_request", "Expected a single JSON object")
	}
	return nil
}
func Random(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}
func ID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[:4], b[4:6], b[6:8], b[8:10], b[10:])
}
func Hash(v string) string { h := sha256.Sum256([]byte(v)); return hex.EncodeToString(h[:]) }
func Token(r *http.Request) string {
	c, e := r.Cookie(CookieName)
	if e != nil {
		return ""
	}
	return c.Value
}
func TrustedOrigin(r *http.Request, origin string) bool {
	o := r.Header.Get("Origin")
	return o == origin || (o == "" && r.Header.Get("Sec-Fetch-Site") != "cross-site")
}
func CheckOrigin(r *http.Request, origin string) error {
	if !TrustedOrigin(r, origin) {
		return Err(403, "untrusted_origin", "Untrusted request origin")
	}
	return nil
}
func Mutating(r *http.Request) bool {
	return r.Method != "GET" && r.Method != "HEAD" && r.Method != "OPTIONS"
}
func Env(k, fallback string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return fallback
}
func BoundedString(v string, min, max int) bool {
	n := len([]rune(strings.TrimSpace(v)))
	return n >= min && n <= max
}

var BuildVersion = "unreleased"
var BuildSHA = "unknown"

func Operations(m *http.ServeMux, ready func(*http.Request) error) {
	m.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { Write(w, 200, map[string]string{"status": "ok"}) })
	m.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		if e := ready(r); e != nil {
			Write(w, 503, map[string]string{"status": "unavailable"})
			return
		}
		Write(w, 200, map[string]string{"status": "ready"})
	})
	m.HandleFunc("GET /version", func(w http.ResponseWriter, r *http.Request) {
		Write(w, 200, map[string]string{"version": BuildVersion, "sha": BuildSHA})
	})
}
