package domain

// Error represents a coded domain error whose HTTP status and safe message map cleanly at the transport layer.
type Error struct {
	Status  int
	Code    string
	Message string
	Cause   error
}

func (e *Error) Error() string {
	if e.Message != "" {
		return e.Message
	}
	return e.Code
}

func (e *Error) StatusCode() int     { return e.Status }
func (e *Error) ErrorCode() string   { return e.Code }
func (e *Error) SafeMessage() string { return e.Message }
func (e *Error) Unwrap() error       { return e.Cause }

func Err(status int, code, message string) error {
	return &Error{Status: status, Code: code, Message: message}
}

func ErrUnauthenticated() error {
	return Err(401, "unauthenticated", "Sign in to continue")
}

func ErrForbidden() error {
	return Err(403, "forbidden", "Web push subscriptions are only available to human accounts")
}

func ErrFeatureDisabled() error {
	return Err(403, "feature_disabled", "Web push notifications are currently disabled")
}

func ErrInvalidEndpoint() error {
	return Err(400, "invalid_push_endpoint", "Push endpoint must be a valid HTTPS URL")
}

func ErrForbiddenEndpoint() error {
	return Err(400, "forbidden_push_endpoint", "Push endpoint cannot target a private or internal address")
}

func ErrInvalidSubscriptionKeys() error {
	return Err(400, "invalid_push_keys", "Push subscription keys must be valid P-256 p256dh and 16-byte auth values")
}
