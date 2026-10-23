// Package httpx provides shared HTTP transport helpers, strict JSON decoding,
// and domain-error-to-HTTP-response mapping for Hertz and net/http adapters.
package httpx

import (
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"teamie/backend/pkg/observability"
)

// CodedError is implemented by domain and transport errors that carry a safe
// HTTP status code and machine-readable error code. Notice that domain packages
// satisfy this interface implicitly via Go structural typing without importing httpx.
type CodedError interface {
	error
	StatusCode() int
	ErrorCode() string
}

// TransportError represents a protocol-level error (e.g. malformed JSON or media type).
type TransportError struct {
	Status  int
	Code    string
	Message string
}

func (e *TransportError) Error() string             { return e.Message }
func (e *TransportError) StatusCode() int           { return e.Status }
func (e *TransportError) ErrorCode() string         { return e.Code }
func (e *TransportError) DiagnosticMessage() string { return e.Code + ": " + e.Message }

// Err constructs a TransportError with the given HTTP status, code, and safe message.
func Err(status int, code, message string) error {
	return &TransportError{Status: status, Code: code, Message: message}
}

// WriteJSON serializes value as application/json with the given HTTP status code.
func WriteJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if value != nil {
		_ = json.NewEncoder(w).Encode(value)
	}
}

// WriteError records the error for observability and writes a safe JSON error response.
func WriteError(w http.ResponseWriter, err error) {
	observability.RecordError(w, err)
	var coded CodedError
	if errors.As(err, &coded) {
		WriteJSON(w, coded.StatusCode(), map[string]any{
			"error": map[string]string{
				"code":    coded.ErrorCode(),
				"message": coded.Error(),
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

// RequireJSONMediaType verifies that the request Content-Type is application/json.
func RequireJSONMediaType(r *http.Request) error {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		return Err(http.StatusUnsupportedMediaType, "unsupported_media_type", "Use an application/json request")
	}
	return nil
}
