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

func ErrNotFound() error {
	return Err(404, "not_found", "This resource is unavailable")
}

func ErrWorkspaceUnavailable() error {
	return Err(404, "not_found", "Workspace is unavailable")
}

func ErrFeatureDisabled() error {
	return Err(403, "feature_disabled", "This action is currently disabled")
}

func ErrWorkspaceOwnerRequired() error {
	return Err(403, "forbidden", "Workspace owner permission is required")
}

func ErrHumanWorkspaceManagementRequired() error {
	return Err(403, "forbidden", "A human manages workspace membership")
}

func ErrPlatformAdminRequired() error {
	return Err(403, "forbidden", "Platform administrator permission is required")
}

func ErrInvalidMarketplaceSlug() error {
	return Err(400, "invalid_slug", "App slug must be 1 to 64 lowercase letters, digits, hyphens or underscores")
}

func ErrDuplicateMarketplaceSlug() error {
	return Err(409, "duplicate_slug", "An app with this slug already exists")
}

func ErrInvalidMarketplaceName() error {
	return Err(400, "invalid_name", "App name must contain 1 to 80 characters")
}

func ErrInvalidMarketplaceDescription() error {
	return Err(400, "invalid_description", "App description must contain at most 500 characters")
}

func ErrInvalidMarketplaceURL() error {
	return Err(400, "invalid_url", "Provide valid http:// or https:// URLs for webUrl and mcpUrl")
}

func ErrInvalidMarketplaceMCPURL() error {
	return Err(400, "invalid_mcp_url", "MCP Server URL must be a valid http(s) endpoint without credentials or URL fragments")
}

func ErrSameOriginMarketplaceURL() error {
	return Err(400, "same_origin_web_url", "App webUrl must be hosted on an independent origin separate from Teamie")
}

func ErrMarketplaceHTTPSRequired() error {
	return Err(400, "https_required", "Production Marketplace endpoints must use https://")
}

func ErrForbiddenMarketplaceTarget() error {
	return Err(400, "forbidden_endpoint_host", "Marketplace endpoints must target public network addresses")
}

func ErrMarketplaceGroupOnly() error {
	return Err(403, "marketplace_group_only", "Marketplace apps are only available in group conversations")
}

func ErrMarketplaceAppUnavailable() error {
	return Err(404, "not_found", "Marketplace app is unavailable")
}

func ErrHumanDeveloperRequired() error {
	return Err(403, "human_developer_required", "Only human Teamie accounts can manage developer applications")
}

func ErrDeveloperAppLimitReached() error {
	return Err(409, "developer_app_limit_reached", "Each developer account can register at most 20 applications")
}

func ErrMarketplaceSlugImmutable() error {
	return Err(409, "slug_immutable", "App slug cannot be changed after submission or publication")
}

func ErrMarketplaceReviewPending() error {
	return Err(409, "marketplace_app_review_pending", "Withdraw the pending review before editing draft fields")
}

func ErrMarketplaceInvalidState(msg string) error {
	return Err(409, "invalid_review_state", msg)
}

func ErrMarketplaceRevisionConflict() error {
	return Err(409, "revision_conflict", "Submission revision changed; refresh and review the latest submission")
}

func ErrMarketplaceSuspended() error {
	return Err(403, "marketplace_app_suspended", "This application has been suspended by a platform administrator")
}

func ErrMarketplaceNoteLength() error {
	return Err(400, "invalid_review_note", "Review note must be at most 1000 characters")
}

func ErrMarketplaceNoteRequired() error {
	return Err(400, "review_note_required", "Provide a clear reason when rejecting or suspending an application")
}

func ErrMarketplaceVerificationFailed(summary string) error {
	if summary == "" {
		summary = "Deployed Web UI and MCP Server endpoints failed platform protocol verification"
	}
	return Err(422, "endpoint_verification_failed", summary)
}

func ErrMarketplaceVerifyRateLimited() error {
	return Err(429, "verify_rate_limited", "Please wait a few seconds before probing endpoints again")
}
