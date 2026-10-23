// Package domain contains pure Go entities, value objects, business invariants,
// domain errors, and outbound repository/authority port interfaces for the
// Agent Control Plane bounded context. It has zero dependencies on HTTP, RPC,
// or SQL frameworks.
package domain

// Error represents a domain-level business rule or availability violation with
// a canonical HTTP status classification and machine-readable error code.
type Error struct {
	Status        int
	Code, Message string
	Cause         error
}

func (e *Error) Error() string             { return e.Message }
func (e *Error) StatusCode() int           { return e.Status }
func (e *Error) ErrorCode() string         { return e.Code }
func (e *Error) DiagnosticMessage() string { return e.Code }
func (e *Error) Unwrap() error             { return e.Cause }

// Err constructs a new domain Error.
func Err(status int, code, message string) *Error {
	return &Error{Status: status, Code: code, Message: message}
}

func ErrUnauthenticated(message string) error {
	if message == "" {
		message = "Sign in to continue"
	}
	return Err(401, "unauthenticated", message)
}

func ErrSessionChanged() error {
	return Err(401, "session_changed", "Browser session changed")
}

func ErrForbidden(message string) error {
	return Err(403, "forbidden", message)
}

func ErrUntrustedOrigin() error {
	return Err(403, "untrusted_origin", "Untrusted origin")
}

func ErrCSRFFailed() error {
	return Err(403, "csrf_failed", "A valid CSRF token is required")
}

func ErrFeatureDisabled() error {
	return Err(403, "feature_disabled", "This Agent action is currently disabled")
}

func ErrNotFound(message string) error {
	if message == "" {
		message = "Agent is unavailable"
	}
	return Err(404, "not_found", message)
}

func ErrInvalidRequest(message string) error {
	return Err(400, "invalid_request", message)
}

func ErrInvalidName() error {
	return Err(400, "invalid_name", "Agent name must contain 1 to 80 characters")
}

func ErrInvalidDescription() error {
	return Err(400, "invalid_description", "Description must contain at most 1000 characters")
}

func ErrInvalidWorkspaces() error {
	return Err(400, "invalid_workspaces", "Select at most 100 workspaces")
}

func ErrControlUnavailable(message string) error {
	if message == "" {
		message = "Agent control is temporarily unavailable"
	}
	return Err(503, "control_unavailable", message)
}

func ErrSchemaUnavailable(cause ...error) error {
	var err error
	if len(cause) > 0 {
		err = cause[0]
	}
	return &Error{Status: 503, Code: "schema_unavailable", Message: "Unsupported control schema", Cause: err}
}
