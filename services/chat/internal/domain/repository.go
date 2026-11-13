package domain

import (
	"context"
	"time"
)

// TxManager coordinates atomic transactions and transaction-scoped advisory locks
// without exposing SQL driver types to the domain or application layers.
type TxManager interface {
	WithinTx(ctx context.Context, fn func(ctx context.Context) error) error
	LockSenderClientMessage(ctx context.Context, senderID, clientMessageID string) error
	LockAttachmentSender(ctx context.Context, userID string) error
	LockPrincipalMembership(ctx context.Context, principalID string) error
	LockSavedItems(ctx context.Context, userID string) error
	Ping(ctx context.Context) error
}

// FeatureFlags exposes Chat feature toggles to the application layer.
type FeatureFlags interface {
	Enabled(key string) bool
	Snapshot() (revision string, flags map[string]bool)
}

// IdentityDirectory resolves participant profiles across the Identity boundary.
type IdentityDirectory interface {
	Users(ctx context.Context, ids []string) ([]User, error)
	UsersWithActive(ctx context.Context, ids []string) ([]User, map[string]bool, error)
	Ping(ctx context.Context) error
}

// PresenceDirectory resolves live human presence states across the private Realtime boundary.
type PresenceDirectory interface {
	Presence(ctx context.Context, userIDs []string) (map[string]string, error)
}

// OutboxPublisher publishes realtime wakeup hints from the durable outbox.
type OutboxPublisher interface {
	PublishHint(ctx context.Context, channel string, payload []byte) error
}

// WorkspaceRepository defines persistence operations for workspaces and invites.
type WorkspaceRepository interface {
	ListWorkspaces(ctx context.Context, userID string) ([]Workspace, error)
	WorkspaceLock(ctx context.Context, workspaceID, userID string, owner, exclusive bool) error
	WorkspaceMemberExists(ctx context.Context, workspaceID, userID string) (bool, error)
	LockWorkspaceByIDForUpdate(ctx context.Context, workspaceID string) error
	InsertWorkspace(ctx context.Context, ws Workspace) (time.Time, error)
	InsertWorkspaceMember(ctx context.Context, workspaceID, userID, role string, grantedBy *string, onConflictDoNothing bool) (bool, error)
	ListWorkspaceMembers(ctx context.Context, workspaceID string) ([]Member, error)
	WorkspaceMemberIDs(ctx context.Context, workspaceID string) ([]string, error)
	InsertWorkspaceInvite(ctx context.Context, codeHash, workspaceID, createdBy string, expiresAt time.Time) error
	FindValidInvite(ctx context.Context, codeHash string) (Workspace, time.Time, error)
	LockValidInviteWorkspace(ctx context.Context, codeHash string) (Workspace, error)
	GetWorkspaceMemberRole(ctx context.Context, workspaceID, userID string) (role string, found bool, err error)
	GetWorkspaceRoleAndOwner(ctx context.Context, workspaceID, userID string) (ownerID string, role string, found bool, err error)
	FindMemberAndDelegatedIDs(ctx context.Context, workspaceID, targetUserID string) ([]string, error)
	DeleteConversationMembersInWorkspace(ctx context.Context, workspaceID string, userIDs []string) error
	DeleteWorkspaceMembers(ctx context.Context, workspaceID string, userIDs []string) error
	SharedWorkspaceMemberIDs(ctx context.Context, userID string) ([]string, error)
}

// ConversationRepository defines persistence operations for conversations and sync streams.
type ConversationRepository interface {
	ListAuthorizedConversationIDs(ctx context.Context, userID, workspaceID string) ([]string, error)
	ConversationAccess(ctx context.Context, conversationID, userID string) (Conversation, error)
	GetConversationScope(ctx context.Context, conversationID, userID string) (workspaceID string, workspaceOwnerID string, convType string, allowed bool, err error)
	LockConversation(ctx context.Context, conversationID, userID string) (Conversation, error)
	LockConversationForShare(ctx context.Context, conversationID string) error
	LockConversationForUpdate(ctx context.Context, conversationID string) error
	GetConversationWorkspaceID(ctx context.Context, conversationID string) (string, error)
	FindDirectConversationByPairKey(ctx context.Context, workspaceID, pairKey string) (string, bool, error)
	InsertConversation(ctx context.Context, c Conversation, pairKey string) (time.Time, error)
	InsertConversationMember(ctx context.Context, conversationID, userID string, onConflictDoNothing bool) (bool, error)
	DeleteConversationMember(ctx context.Context, conversationID, userID string) error
	DeleteConversationCascade(ctx context.Context, conversationID string) error
	LockConversationWithActorRole(ctx context.Context, conversationID, actorID string) (Conversation, string, error)
	ConversationRecipients(ctx context.Context, c Conversation) ([]string, error)
	CountConversationMembersAndCheckTarget(ctx context.Context, conversationID, targetUserID string) (count int, present bool, err error)
	IncrementConversationSeq(ctx context.Context, conversationID string) (int64, error)
	UpsertReadCursor(ctx context.Context, conversationID, userID string, seq int64) error
	SetManualUnread(ctx context.Context, conversationID, userID string, seq int64) error
	ClearManualUnread(ctx context.Context, conversationID, userID string) (bool, error)
	SetNotificationLevel(ctx context.Context, conversationID, userID, level string) (bool, error)
	QueryReadReceipts(ctx context.Context, c Conversation, callerUserID string) (ReadReceipts, error)
	EnqueueOutboxHint(ctx context.Context, payload any) error
	SyncHead(ctx context.Context, userID string) (int64, error)
	EnsureAndLockSyncHeads(ctx context.Context, userIDs []string) error
	Fanout(ctx context.Context, userIDs []string, event Event) error
	QuerySyncEvents(ctx context.Context, userID string, afterID, upToID int64, limit int) ([]Event, int64, error)
}

// MembershipRepository defines persistence operations for principal workspace delegation.
type MembershipRepository interface {
	ParticipantWorkspaces(ctx context.Context, userID string) ([]string, error)
	EnsureAndLockPrincipalRevision(ctx context.Context, principalID string) (int64, error)
	UpdatePrincipalRevision(ctx context.Context, principalID string, revision int64) error
}

// MessageRepository defines persistence operations for messages, history, search, and attention.
type MessageRepository interface {
	GetMessage(ctx context.Context, conversationID, messageID string) (Message, error)
	FindMessageBySenderClientID(ctx context.Context, senderID, clientMessageID string) (msg Message, identity MessageIdentity, found bool, err error)
	MessageExistsInConversation(ctx context.Context, messageID, conversationID string) (bool, error)
	InsertMessage(ctx context.Context, m Message, originalBodyHash string, forwardOf *ForwardSource) (time.Time, error)
	CopyForwardAttachments(ctx context.Context, sourceConversationID, sourceMessageID, targetConversationID, targetMessageID, forwarderID string, descriptors []Attachment) ([]Attachment, error)
	SetMessageForwarded(ctx context.Context, messageID string, snapshot ForwardedSnapshot) error
	SetMessageInitialMentions(ctx context.Context, messageID string, mentions, originalMentions []Mention, broadcastRecipientIDs []string) error
	SetMessageInitialAttachments(ctx context.Context, messageID string, attachments []Attachment, originalIDs []string) error
	RetractMessageAndDeleteAttachments(ctx context.Context, messageID string, retractedAt time.Time) error
	UpdateMessageBodyAndMentions(ctx context.Context, messageID, body string, editedAt time.Time, mentions []Mention, broadcastRecipientIDs []string) error
	AddReaction(ctx context.Context, conversationID, messageID, userID, emoji string) (ReactionState, bool, error)
	RemoveReaction(ctx context.Context, conversationID, messageID, userID, emoji string) (ReactionState, bool, error)
	QueryMessageHistory(ctx context.Context, conversationID string, before, around int64, query string, limit int, filesOnly bool) ([]Message, bool, error)
	SearchWorkspaceCandidateIDs(ctx context.Context, userID, workspaceID, query string, before any, cursorID string, limit int) ([]string, error)
	LockSearchConversationsForShare(ctx context.Context, userID, workspaceID string, candidateMessageIDs []string) ([]string, error)
	QueryVerifiedWorkspaceSearchItems(ctx context.Context, userID, query string, before any, cursorID string, limit int, candidateMessageIDs, conversationIDs []string) ([]WorkspaceSearchItem, error)
	LockUserWorkspacesForShare(ctx context.Context, userID string) ([]string, error)
	LockUserConversationsForShare(ctx context.Context, userID string, workspaceIDs []string) ([]string, error)
	QueryAttentionProjection(ctx context.Context, userID string, before any, cursorID string, unreadOnly bool, limit int, conversationIDs []string, messageID string) (unreadCount, channelUnreadCount int64, items []AttentionItem, unreadByConversation []AttentionUnread, err error)

	// Pins: mutations run under the conversation row lock so the counter,
	// pin rows and durable event commit together.
	FindPin(ctx context.Context, conversationID, messageID string) (Pin, bool, error)
	CountPins(ctx context.Context, conversationID string) (int, error)
	InsertPin(ctx context.Context, conversationID, messageID, pinnedBy, pinnedByName string) (Pin, int64, error)
	DeletePin(ctx context.Context, conversationID, messageID string) (version int64, deleted bool, err error)
	PinsVersion(ctx context.Context, conversationID string) (int64, error)
	ListPins(ctx context.Context, conversationID string) ([]Pin, error)

	// Saved items are private to userID.
	FindSaved(ctx context.Context, userID, messageID string) (SavedMarker, bool, error)
	CountSaved(ctx context.Context, userID string) (int, error)
	InsertSaved(ctx context.Context, userID, conversationID, messageID string) (SavedMarker, error)
	DeleteSaved(ctx context.Context, userID, conversationID, messageID string) (bool, error)
	DeleteSavedForMessage(ctx context.Context, messageID string) error
	SavedMessageIDs(ctx context.Context, userID, conversationID string) ([]string, error)
	QuerySavedPage(ctx context.Context, userID string, before any, cursorID string, limit int, conversationIDs []string) ([]SavedItem, error)
}

// AttachmentRepository defines persistence operations for file attachments and upload receipts.
type AttachmentRepository interface {
	FindAttachmentUpload(ctx context.Context, userID, key string) (AttachmentUpload, bool, error)
	DeleteExpiredPendingForSender(ctx context.Context, userID string) error
	CountPendingForSender(ctx context.Context, userID string) (int, error)
	InsertAttachment(ctx context.Context, a Attachment, conversationID, uploaderID string, content []byte, thumb *AttachmentThumbnail) error
	InsertAttachmentUpload(ctx context.Context, uploaderID, key, conversationID, fingerprint, attachmentID string) error
	DeleteAllExpiredPending(ctx context.Context) error
	LockAttachmentForUpdate(ctx context.Context, attachmentID, conversationID string) (ownerID string, messageID *string, found bool, err error)
	DeleteAttachmentByID(ctx context.Context, attachmentID string) error
	AttachFilesToMessage(ctx context.Context, userID, conversationID, messageID string, attachmentIDs []string) ([]Attachment, error)
	GetAuthorizedAttachmentContent(ctx context.Context, attachmentID, conversationID, userID string) (Attachment, []byte, error)
	GetAuthorizedAttachmentThumbnail(ctx context.Context, attachmentID, conversationID, userID string) (mediaType string, content []byte, sha256 string, err error)
}

// OutboxRepository defines persistence operations for the durable realtime hint outbox.
type OutboxRepository interface {
	PrunePublishedOutbox(ctx context.Context) (int64, error)
	ClaimNextUnpublishedOutbox(ctx context.Context) (id int64, payload []byte, found bool, err error)
	DeleteOutboxByID(ctx context.Context, id int64) error
}

// ConversationMutation stores no request bodies or historical member lists.
type ConversationMutation struct{ RequestHash, ConversationID string }
type MutationRepository interface {
	LockMutation(context.Context, string, string) (ConversationMutation, bool, error)
	SaveMutation(context.Context, string, string, ConversationMutation) error
}

// AnnouncementRepository defines persistence operations for platform announcements and per-user read cursors.
type AnnouncementRepository interface {
	InsertAnnouncement(ctx context.Context, a Announcement) (Announcement, error)
	ListAnnouncements(ctx context.Context, userID string, beforeSeq int64, limit int) (AnnouncementsPage, error)
	MarkAnnouncementsRead(ctx context.Context, userID string, seq int64) (int64, error)
	DeleteAnnouncement(ctx context.Context, id string) (bool, error)
}
