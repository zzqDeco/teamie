// Package httpx provides shared HTTP transport helpers, strict JSON decoding,
// session/CSRF/origin authentication, Hertz server bootstrap, and domain-error-to-HTTP-response mapping.
package httpx

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/cloudwego/hertz/pkg/app"
	"teamie/pkg/observability"
)

const CookieName = "teamie_session"

var BuildVersion = "unreleased"
var BuildSHA = "unknown"

// User represents an authenticated principal profile on the HTTP transport boundary.
type User struct {
	ID           string    `json:"id"`
	Email        string    `json:"email"`
	DisplayName  string    `json:"displayName"`
	Kind         string    `json:"kind"`
	PlatformRole string    `json:"platformRole"`
	CreatedAt    time.Time `json:"createdAt"`
}

// Session represents a validated browser or service principal session.
type Session struct {
	User             User   `json:"user"`
	CSRFToken        string `json:"csrfToken"`
	ServicePrincipal bool   `json:"-"`
}

// CodedError is implemented by domain and transport errors that carry a safe
// HTTP status code and machine-readable error code. Notice that domain packages
// satisfy this interface implicitly via Go structural typing without importing httpx.
type CodedError interface {
	error
	StatusCode() int
	ErrorCode() string
}

// Error represents a transport or domain error with an HTTP status code, machine-readable code, and safe message.
type Error struct {
	Status        int
	Code, Message string
	Cause         error
}

func (e *Error) Error() string {
	if e.Message != "" {
		return e.Message
	}
	return e.Code
}
func (e *Error) StatusCode() int           { return e.Status }
func (e *Error) ErrorCode() string         { return e.Code }
func (e *Error) DiagnosticMessage() string { return e.Code + ": " + e.Message }
func (e *Error) Unwrap() error             { return e.Cause }

// TransportError is an alias for Error for backward compatibility.
type TransportError = Error

// Err constructs an Error with the given HTTP status, code, and safe message.
func Err(status int, code, message string) error {
	return &Error{Status: status, Code: code, Message: message}
}

// Responder maps application/domain results and CodedErrors onto native Hertz request contexts.
type Responder struct{}

// Error writes a CodedError or generic 500 internal_error JSON response to a native Hertz RequestContext.
func (Responder) Error(_ context.Context, c *app.RequestContext, err error) {
	var ce CodedError
	if errors.As(err, &ce) {
		c.JSON(ce.StatusCode(), map[string]any{
			"error": map[string]string{
				"code":    ce.ErrorCode(),
				"message": ce.Error(),
			},
		})
		return
	}
	c.JSON(http.StatusInternalServerError, map[string]any{
		"error": map[string]string{
			"code":    "internal_error",
			"message": "The request could not be completed",
		},
	})
}

// WriteJSON serializes value as application/json with the given HTTP status code.
func WriteJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if value != nil {
		_ = json.NewEncoder(w).Encode(value)
	}
}

// Write is an alias for WriteJSON.
func Write(w http.ResponseWriter, status int, value any) {
	WriteJSON(w, status, value)
}

// WriteNoStoreJSON serializes value as application/json with Cache-Control: no-store.
func WriteNoStoreJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	if value != nil {
		_ = json.NewEncoder(w).Encode(value)
	}
}

// WriteError records the error for observability and writes a safe JSON error response.
func WriteError(w http.ResponseWriter, err error) {
	observability.RecordError(w, err)
	var ae *Error
	if errors.As(err, &ae) {
		WriteJSON(w, ae.Status, map[string]any{
			"error": map[string]string{
				"code":    ae.Code,
				"message": ae.Message,
			},
		})
		return
	}
	var coded CodedError
	if errors.As(err, &coded) {
		msg := coded.Error()
		if sm, ok := coded.(interface{ SafeMessage() string }); ok && sm.SafeMessage() != "" {
			msg = sm.SafeMessage()
		}
		WriteJSON(w, coded.StatusCode(), map[string]any{
			"error": map[string]string{
				"code":    coded.ErrorCode(),
				"message": msg,
			},
		})
		return
	}
	WriteJSON(w, http.StatusInternalServerError, map[string]any{
		"error": map[string]string{
			"code":    "internal_error",
			"message": "The request could not be completed",
		},
	})
}

// Fail is an alias for WriteError.
func Fail(w http.ResponseWriter, err error) {
	WriteError(w, err)
}

// DecodeJSON strictly decodes a single JSON object of at most 128 KiB with no unknown fields.
func DecodeJSON(w http.ResponseWriter, r *http.Request, dst any) error {
	r.Body = http.MaxBytesReader(w, r.Body, 131072)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if err := d.Decode(dst); err != nil {
		return Err(http.StatusBadRequest, "invalid_request", "Invalid JSON request")
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return Err(http.StatusBadRequest, "invalid_request", "Expected a single JSON object")
	}
	return nil
}

// Decode is an alias for DecodeJSON.
func Decode(w http.ResponseWriter, r *http.Request, dst any) error {
	return DecodeJSON(w, r, dst)
}

// DecodeJSONMaxBytes strictly decodes a single JSON object bounded by maxBytes with no unknown fields.
func DecodeJSONMaxBytes(w http.ResponseWriter, r *http.Request, maxBytes int64, dst any) error {
	r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if d.Decode(dst) != nil || d.Decode(new(any)) != io.EOF {
		return Err(http.StatusBadRequest, "invalid_request", "Expected one valid JSON object")
	}
	return nil
}

// RequireJSONMediaType verifies that the request Content-Type is application/json.
func RequireJSONMediaType(r *http.Request) error {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		return Err(http.StatusUnsupportedMediaType, "unsupported_media_type", "Use an application/json request")
	}
	return nil
}

// Random returns n cryptographically random bytes encoded as lowercase hex.
func Random(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

// ID generates a new random UUIDv4 string.
func ID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[:4], b[4:6], b[6:8], b[8:10], b[10:])
}

// Hash returns the lowercase SHA-256 hex digest of v.
func Hash(v string) string {
	h := sha256.Sum256([]byte(v))
	return hex.EncodeToString(h[:])
}

// Token extracts the session cookie value from r.
func Token(r *http.Request) string {
	c, err := r.Cookie(CookieName)
	if err != nil {
		return ""
	}
	return c.Value
}

// TrustedOrigin checks whether r originates from origin or is a non-cross-site request without Origin.
func TrustedOrigin(r *http.Request, origin string) bool {
	o := r.Header.Get("Origin")
	return o == origin || (o == "" && r.Header.Get("Sec-Fetch-Site") != "cross-site")
}

// CheckOrigin returns a 403 untrusted_origin error if r does not satisfy TrustedOrigin.
func CheckOrigin(r *http.Request, origin string) error {
	if !TrustedOrigin(r, origin) {
		return Err(http.StatusForbidden, "untrusted_origin", "Untrusted request origin")
	}
	return nil
}

// Mutating reports whether r is a state-changing HTTP method.
func Mutating(r *http.Request) bool {
	return r.Method != "GET" && r.Method != "HEAD" && r.Method != "OPTIONS"
}

// Env returns the environment variable k or fallback if unset/empty.
func Env(k, fallback string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return fallback
}

// BoundedString checks whether the trimmed rune length of v is within [min, max].
func BoundedString(v string, minLen, maxLen int) bool {
	n := len([]rune(strings.TrimSpace(v)))
	return n >= minLen && n <= maxLen
}

// EqualSecret performs constant-time comparison of two secrets that must each be at least 24 bytes.
func EqualSecret(a, b string) bool {
	x, y := sha256.Sum256([]byte(a)), sha256.Sum256([]byte(b))
	return len(a) >= 24 && len(b) >= 24 && subtle.ConstantTimeCompare(x[:], y[:]) == 1
}

// ValidServiceKey performs constant-time comparison of two non-empty service keys.
func ValidServiceKey(expected, provided string) bool {
	return expected != "" && provided != "" && subtle.ConstantTimeCompare([]byte(expected), []byte(provided)) == 1
}

// Operations registers /healthz, /readyz, and /version on m.
func Operations(m *http.ServeMux, ready func(*http.Request) error) {
	m.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	m.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		if err := ready(r); err != nil {
			WriteJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "unavailable"})
			return
		}
		WriteJSON(w, http.StatusOK, map[string]string{"status": "ready"})
	})
	m.HandleFunc("GET /version", func(w http.ResponseWriter, _ *http.Request) {
		WriteJSON(w, http.StatusOK, map[string]string{"version": BuildVersion, "sha": BuildSHA})
	})
}

// OperationsContext registers /healthz, /readyz (with a 2s context timeout), and /version on m.
func OperationsContext(m *http.ServeMux, ready func(context.Context) error) {
	Operations(m, func(r *http.Request) error {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		return ready(ctx)
	})
}
