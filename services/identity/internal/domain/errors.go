// Package domain contains pure Go entities, value objects, business invariants,
// domain errors, and outbound repository port interfaces for the Identity
// bounded context. It has zero dependencies on HTTP, RPC, or SQL frameworks.
package domain

// Error represents a domain-level business rule violation with a canonical
// status classification, safe code, diagnostic stage/reason, and internal cause.
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

// Err creates a new domain Error.
func Err(status int, code, message string) *Error {
	return &Error{Status: status, Code: code, Message: message}
}

func ErrInvalidEmail() error {
	return Err(400, "invalid_email", "A valid email address is required")
}

func ErrInvalidName() error {
	return Err(400, "invalid_name", "Display name must contain 1 to 80 characters")
}

func ErrInvalidPassword() error {
	return Err(400, "invalid_password", "Password must contain at least 12 characters and at most 72 bytes")
}

func ErrEmailInUse() error {
	return Err(409, "email_in_use", "This email is already registered")
}

func ErrInvalidCredentials() error {
	return Err(401, "invalid_credentials", "Email or password is incorrect")
}

func ErrUnauthenticatedSignIn() error {
	return Err(401, "unauthenticated", "Sign in to continue")
}

func ErrSessionExpiredOrRevoked() error {
	return Err(401, "unauthenticated", "Session expired or revoked")
}

func ErrInvalidParticipantCredential() error {
	return Err(401, "unauthenticated", "Invalid participant credential")
}

func ErrParticipantCredentialExpired() error {
	return Err(401, "unauthenticated", "Participant credential expired or revoked")
}

func ErrTooManyUsers() error {
	return Err(400, "invalid_request", "Too many users")
}

func ErrInvalidInvite() error {
	return Err(403, "invalid_invite", "This registration invitation is invalid, expired, revoked, or already used")
}

func ErrAdminPermissionRequired() error {
	return Err(403, "forbidden", "Platform administrator permission is required")
}

func ErrHumanSessionRequired() error {
	return Err(403, "forbidden", "A human login session is required")
}

func ErrInvalidCurrentPassword() error {
	return Err(400, "invalid_current_password", "Current password is incorrect")
}

func ErrPasswordUnchanged() error {
	return Err(400, "password_unchanged", "Choose a different new password")
}

func ErrInvalidResetLink() error {
	return Err(400, "invalid_reset_link", "This password reset link is invalid, expired, revoked, or already used")
}

func ErrUserNotFound() error {
	return Err(404, "user_not_found", "This account is unavailable")
}

func ErrInvalidAdminUsersRequest() error {
	return Err(400, "invalid_request", "Use a limit from 1 to 100 and a search of at most 200 characters")
}

func ErrInvalidAdminUsersLimit() error {
	return Err(400, "invalid_request", "Use a limit from 1 to 100")
}

func ErrInvalidUsersCursor() error {
	return Err(400, "invalid_cursor", "Invalid users cursor")
}

func ErrPasswordResetDisabled() error {
	return Err(403, "feature_disabled", "Password reset link generation is disabled")
}

func ErrInvalidPrincipalLifecycle() error {
	return Err(400, "invalid_principal", "Invalid principal lifecycle request")
}

func ErrHumanNotMachinePrincipal() error {
	return Err(409, "invalid_principal", "Human identities cannot be managed as machine principals")
}

func ErrPrincipalRevisionConflict() error {
	return Err(409, "revision_conflict", "Principal revision conflicts with persisted state")
}

func ErrPrincipalDeleted() error {
	return Err(410, "principal_deleted", "Deleted principals cannot be reactivated")
}

func ErrPrincipalDisabled() error {
	return Err(403, "principal_disabled", "Principal is unavailable or disabled")
}

func ErrRateLimited() error {
	return Err(429, "rate_limited", "Try again in a minute")
}
