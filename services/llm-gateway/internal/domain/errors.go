// Package domain contains pure Go entities, value objects, business invariants,
// domain errors, and outbound port interfaces for the Model Gateway bounded
// context. It has zero dependencies on HTTP, RPC, Eino, or SQL frameworks.
package domain

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
)

// Error represents a domain or upstream boundary violation with a canonical
// HTTP status code and machine-readable error code.
type Error struct {
	Status        int
	Code, Message string
	Cause         error
}

func (e *Error) Error() string             { return e.Code }
func (e *Error) StatusCode() int           { return e.Status }
func (e *Error) ErrorCode() string         { return e.Code }
func (e *Error) SafeMessage() string       { return e.Message }
func (e *Error) DiagnosticMessage() string { return e.Code + ": " + e.Message }
func (e *Error) Unwrap() error             { return e.Cause }

// Err creates a new domain Error.
func Err(status int, code, message string) *Error {
	return &Error{Status: status, Code: code, Message: message}
}

// ErrWithCause creates a new domain Error preserving the underlying cause.
func ErrWithCause(status int, code, message string, cause error) *Error {
	return &Error{Status: status, Code: code, Message: message, Cause: cause}
}

// AsError extracts a *Error from err, or returns a default 500 gateway_unavailable *Error.
func AsError(err error) *Error {
	var de *Error
	if errors.As(err, &de) {
		return de
	}
	return ErrWithCause(500, "gateway_unavailable", "Model gateway is temporarily unavailable", err)
}

// StreamError maps context cancellation/deadline or a fallback code to a domain Error.
func StreamError(ctx context.Context, fallback string) error {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return ErrWithCause(504, "upstream_timeout", "Model request exceeded its deadline", ctx.Err())
	}
	if ctx.Err() != nil {
		return ErrWithCause(499, "canceled", "Model request canceled", ctx.Err())
	}
	return Err(502, fallback, "Provider stream ended without a complete response")
}

// Provider text is inspected only inside this boundary and is never forwarded
// or persisted. A general invalid-parameter/auth/rate-limit error must not cause
// context rewriting. DashScope documents this exact input-range failure:
// https://www.alibabacloud.com/help/en/model-studio/error-code
var inputRangeError = regexp.MustCompile(`^(?:InternalError\.Algo\.InvalidParameter: ?)?Range of input length should be\s*\[1,\s*[1-9][0-9]*\]\.?(?:$|\s)`)

// ProviderContextError classifies structured upstream context-window overflow responses.
func ProviderContextError(raw []byte) error {
	var envelope struct {
		Error struct {
			Code    string `json:"code"`
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if len(raw) > 16384 || json.Unmarshal(raw, &envelope) != nil {
		return nil
	}
	e := envelope.Error
	code := strings.ToLower(e.Code)
	overflow := code == "context_length_exceeded" || code == "context_window_exceeded" || code == "max_context_length_exceeded"
	if !overflow && (code == "invalidparameter" || code == "internalerror.algo.invalidparameter" || code == "invalid_parameter_error") {
		overflow = inputRangeError.MatchString(e.Message)
	}
	if !overflow {
		return nil
	}
	return Err(413, "context_overflow", "Model input exceeds the provider context window")
}
