// Package domain contains pure Go entities, value objects, business invariants,
// domain errors, and outbound repository port interfaces for the Chat bounded
// context. It has zero dependencies on HTTP, RPC, or SQL frameworks.
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

func ErrInviteUnavailable() error {
	return Err(404, "not_found", "Invitation is unavailable or expired")
}

func ErrPendingFileUnavailable() error {
	return Err(404, "not_found", "This pending file is unavailable")
}

func ErrAttachmentUploadNotFound() error {
	return Err(404, "attachment_upload_not_found", "No upload with this key")
}

func ErrFeatureDisabled() error {
	return Err(403, "feature_disabled", "This action is currently disabled")
}

func ErrWorkspaceOwnerRequired() error {
	return Err(403, "forbidden", "Workspace owner permission is required")
}

func ErrOwnerCannotBeRemoved() error {
	return Err(403, "forbidden", "The workspace owner cannot be removed")
}

func ErrGrantorMembershipRequired() error {
	return Err(403, "forbidden", "Selected workspaces must be current memberships of the granting human")
}

func ErrGroupManageForbidden() error {
	return Err(403, "forbidden", "Only group creator or workspace owner can manage group membership")
}

func ErrGroupCurrentMembershipRequired() error {
	return Err(403, "forbidden", "Current group membership is required to manage this group")
}

func ErrAuthorOnlyMessageChange() error {
	return Err(403, "forbidden", "Only the author can change a message")
}

func ErrHumanWorkspaceManagementRequired() error {
	return Err(403, "forbidden", "A human manages workspace membership")
}

func ErrInvalidWorkspaceName() error {
	return Err(400, "invalid_name", "Workspace name must contain 1 to 80 characters")
}

func ErrInvalidConversationType() error {
	return Err(400, "invalid_type", "Conversation type must be channel, direct or group")
}

func ErrInvalidConversationName() error {
	return Err(400, "invalid_name", "Conversation name must contain 1 to 80 characters")
}

func ErrInvalidMembersCount() error {
	return Err(400, "invalid_members", "Choose one participant for a direct chat or 2 to 50 group participants")
}

func ErrInvalidWorkspaceMembers() error {
	return Err(400, "invalid_members", "All participants must be current workspace members")
}

func ErrInvalidGroupParticipant() error {
	return Err(400, "invalid_members", "Participant must be a current workspace member")
}

func ErrGroupMemberLimit() error {
	return Err(400, "invalid_members", "Groups support at most 50 participants")
}

func ErrWorkspaceMemberLimit() error {
	return Err(400, "invalid_members", "Workspaces support at most 500 participants")
}

func ErrOnlyGroupMembershipChanged() error {
	return Err(400, "invalid_conversation", "Only group membership can be changed")
}

func ErrInvalidDelegatedMembership() error {
	return Err(400, "invalid_membership", "Invalid delegated membership request")
}

func ErrInvalidSyncCursor() error {
	return Err(400, "invalid_cursor", "Cursor must be a non-negative decimal")
}

func ErrFutureSyncCursor() error {
	return Err(400, "invalid_cursor", "Cursor is ahead of this user's stream")
}

func ErrInvalidAttentionCursor() error {
	return Err(400, "invalid_cursor", "Invalid attention cursor")
}

func ErrInvalidAttentionPageSize() error {
	return Err(400, "invalid_query", "Invalid attention page size")
}

func ErrInvalidSearchCursor() error {
	return Err(400, "invalid_query", "Invalid search cursor")
}

func ErrInvalidSearchQuery() error {
	return Err(400, "invalid_query", "Invalid search query")
}

func ErrInvalidHistoryQuery() error {
	return Err(400, "invalid_query", "Invalid history query")
}

func ErrInvalidBodyRange() error {
	return Err(400, "invalid_body", "Message text must contain 1 to 8000 characters")
}

func ErrInvalidBodyMax() error {
	return Err(400, "invalid_body", "Message text must be at most 8000 characters")
}

func ErrInvalidMentionsRange() error {
	return Err(400, "invalid_mentions", "Use ordered, non-overlapping @ mention ranges in UTF-16 offsets")
}

func ErrInvalidMentionRecipients() error {
	return Err(400, "invalid_mentions", "Mention recipients must be current conversation members")
}

func ErrInvalidClientMessageID() error {
	return Err(400, "invalid_client_message_id", "clientMessageId is required and must be at most 128 characters")
}

func ErrInvalidReplyTarget() error {
	return Err(400, "invalid_reply", "Reply target must belong to this conversation")
}

func ErrIdempotencyConflict() error {
	return Err(409, "idempotency_conflict", "This clientMessageId was used for a different message")
}

func ErrMessageRetracted() error {
	return Err(409, "message_retracted", "A retracted message cannot be edited")
}

func ErrInvalidReadSeq() error {
	return Err(400, "invalid_seq", "Read sequence cannot be negative")
}

func ErrTooManyAttachments() error {
	return Err(400, "too_many_attachments", "A message supports at most 8 files")
}

func ErrDuplicateAttachmentID() error {
	return Err(400, "invalid_attachment", "Attachment IDs must be unique")
}

func ErrInvalidAttachmentUnavailable() error {
	return Err(400, "invalid_attachment", "A file is unavailable, expired, or already attached")
}

func ErrInvalidFilename() error {
	return Err(400, "invalid_filename", "Use a filename of 1 to 180 characters without path separators")
}

func ErrInvalidUploadKeyFormat() error {
	return Err(400, "invalid_upload_key", "Use an upload key of 1 to 128 ASCII letters, digits, dot, colon, underscore or hyphen")
}

func ErrInvalidUploadKey() error {
	return Err(400, "invalid_upload_key", "Invalid upload key")
}

func ErrAttachmentTooLarge() error {
	return Err(413, "attachment_too_large", "Files must contain 1 byte to 4 MiB")
}

func ErrUploadConflict() error {
	return Err(409, "upload_conflict", "This upload key was already used for a different file")
}

func ErrAttachmentUploadUnavailable(retryHint bool) error {
	if retryHint {
		return Err(410, "attachment_upload_unavailable", "This upload was removed or expired; use a new upload key")
	}
	return Err(410, "attachment_upload_unavailable", "This upload was removed or expired")
}

func ErrAttachmentCapacity() error {
	return Err(429, "attachment_capacity", "Remove pending files or wait for them to expire")
}

func ErrAttachmentBusy() error {
	return Err(429, "attachment_busy", "File transfers are busy; please retry")
}

func ErrInvalidReaction() error {
	return Err(400, "invalid_reaction", "Choose one supported Unicode emoji")
}

func ErrReactionLimit() error {
	return Err(409, "reaction_limit", "A message supports at most 20 distinct emoji reactions")
}

func ErrInvalidNotificationLevel() error {
	return Err(400, "invalid_notification_level", "Notification level must be all, mentions or none")
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

func ErrMarketplaceGroupOnly() error {
	return Err(403, "marketplace_group_only", "Marketplace apps are only available in group conversations")
}

func ErrMarketplaceAppUnavailable() error {
	return Err(404, "not_found", "Marketplace app is unavailable")
}
