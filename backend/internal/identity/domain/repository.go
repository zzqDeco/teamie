package domain

import (
	"context"
	"time"
)

// TxManager coordinates atomic transactions and transaction-scoped advisory locks
// without exposing SQL driver types to the domain or application layers.
type TxManager interface {
	WithinTx(ctx context.Context, fn func(ctx context.Context) error) error
	WithinReadCommittedTx(ctx context.Context, fn func(ctx context.Context) error) error
	LockRegistration(ctx context.Context) error
	LockPrincipal(ctx context.Context, principalID string) error
	Ping(ctx context.Context) error
}

// FeatureFlags exposes identity feature toggles to the application layer.
type FeatureFlags interface {
	PasswordResetEnabled() bool
}

// UserRepository defines persistence operations for human and agent identity rows.
type UserRepository interface {
	AnyUserExists(ctx context.Context) (bool, error)
	CreateHuman(ctx context.Context, u User, passwordHash string) (time.Time, error)
	FindHumanForLogin(ctx context.Context, email string) (user User, passwordHash string, found bool, err error)
	LockHumanForShare(ctx context.Context, userID string) (passwordHash string, found bool, err error)
	LockHumanForUpdate(ctx context.Context, userID string) (passwordHash string, found bool, err error)
	LockHumanByEmailForUpdate(ctx context.Context, email string) (userID string, found bool, err error)
	LockUsersForAdminReset(ctx context.Context, adminID, targetID string) (adminFound, adminEnabled bool, adminKind, adminRole string, targetFound bool, err error)
	GetActiveHumanPlatformRole(ctx context.Context, userID string) (role string, found bool, err error)
	UpdateDisplayName(ctx context.Context, userID, displayName string) (User, error)
	UpdatePasswordHash(ctx context.Context, userID, passwordHash string) error
	FindByIDs(ctx context.Context, ids []string) ([]User, error)
	ListAdminHumans(ctx context.Context, afterID, query, likePattern string, limit int) ([]User, error)
}

// SessionRepository defines persistence operations for browser sessions.
type SessionRepository interface {
	RetireExpiredBatchAndInsert(ctx context.Context, userID, tokenHash, csrfToken string, expiresAt time.Time) error
	FindActiveHumanSession(ctx context.Context, tokenHash string) (Session, bool, error)
	LockActiveHumanSessionForShare(ctx context.Context, userID, tokenHash string) (bool, error)
	DeleteByTokenHash(ctx context.Context, tokenHash string) error
	DeleteAllForUser(ctx context.Context, userID string) error
}

// InviteRepository defines persistence operations for registration invitations.
type InviteRepository interface {
	FindValidInviteID(ctx context.Context, codeHash string, lockForUpdate bool) (string, bool, error)
	MarkInviteUsed(ctx context.Context, inviteID, usedByUserID string) error
	CreateInvite(ctx context.Context, id, codeHash, createdBy string) (RegistrationInvite, error)
	ListAllInvites(ctx context.Context) ([]RegistrationInvite, error)
	RevokeUnusedInvite(ctx context.Context, inviteID string) error
}

// PasswordResetRepository defines persistence operations for password recovery links.
type PasswordResetRepository interface {
	RevokeActiveForUser(ctx context.Context, userID string) error
	IssuePasswordReset(ctx context.Context, id, userID, tokenHash string, createdBy *string) (PasswordReset, error)
	InspectActive(ctx context.Context, tokenHash string) (userID, displayName, email string, expiresAt time.Time, found bool, err error)
	FindUserIDByTokenHash(ctx context.Context, tokenHash string) (string, bool, error)
	LockActiveForUpdate(ctx context.Context, tokenHash, userID string) (resetID string, found bool, err error)
	MarkPasswordResetUsed(ctx context.Context, resetID string) error
}

// PrincipalRepository defines persistence operations for machine principals and tokens.
type PrincipalRepository interface {
	LockPrincipalForUpdate(ctx context.Context, id string) (user User, enabled bool, deletedAt *time.Time, revision int64, found bool, err error)
	CreateAgentPrincipal(ctx context.Context, id, name, kind string, enabled, deleted bool, revision int64) (time.Time, error)
	UpdateAgentPrincipal(ctx context.Context, id, name string, enabled, deleted bool, revision int64) error
	DeletePrincipalTokens(ctx context.Context, principalID string) error
	LockAgentEnabled(ctx context.Context, id string) (enabled bool, found bool, err error)
	UpsertPrincipalToken(ctx context.Context, principalID, tokenHash string, expiresAt time.Time) error
	FindActiveAgentByTokenHash(ctx context.Context, tokenHash string) (User, bool, error)
}
