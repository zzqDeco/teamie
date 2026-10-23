package domain

import (
	"context"
	"time"
)

// TxManager coordinates atomic persistence operations.
type TxManager interface {
	WithinTx(ctx context.Context, fn func(ctx context.Context) error) error
	Ping(ctx context.Context) error
}

// AgentRepository defines the persistence port for Agent definitions.
type AgentRepository interface {
	Insert(ctx context.Context, a Agent) (Agent, error)
	GetOwned(ctx context.Context, ownerID, id string) (Agent, error)
	GetForUpdate(ctx context.Context, ownerID, id string) (Agent, error)
	GetByIDForUpdate(ctx context.Context, id string) (Agent, error)
	UpdateProfile(ctx context.Context, a Agent) error
	MarkDeleted(ctx context.Context, ownerID, id string) (bool, error)
	ListOwned(ctx context.Context, ownerID string) ([]Agent, error)
	ListWorkspaceAgents(ctx context.Context, workspaceID string, participantIDs []string) ([]Agent, error)
	ListAll(ctx context.Context) ([]Agent, error)
	GetByID(ctx context.Context, id string) (Agent, bool, error)
	UpdatePolicyEnabled(ctx context.Context, id string, policyEnabled bool) error
	UpdateWorkspaces(ctx context.Context, id string, workspaceIDs []string) error
	MarkReconciled(ctx context.Context, id string, enabled bool) error
	MarkReconcileFailed(ctx context.Context, id string) error
	ReportRuntime(ctx context.Context, agentID, runtimeState string, active *time.Time, safeErr string) (bool, error)
}

// IMAuthority defines the outbound port to the independent IM Identity and Chat services.
type IMAuthority interface {
	ValidateSession(ctx context.Context, token string) (*UserProfile, string, error)
	GetUsers(ctx context.Context, ids []string) (map[string]UserProfile, error)
	GetWorkspaces(ctx context.Context, userID string) ([]string, error)
	GetParticipants(ctx context.Context, workspaceID, requesterID string) ([]string, error)
	ProvisionPrincipal(ctx context.Context, a Agent, revision int64, enabled, deleted bool) error
	SyncMemberships(ctx context.Context, a Agent, workspaceIDs []string) ([]string, error)
	Ping(ctx context.Context) error
}

// RuntimeNotifier is an optional outbound port for observing reconciliation lifecycle events.
type RuntimeNotifier interface {
	BeginReconcile(ctx context.Context, agentID string) (context.Context, func(changed bool, attempts int, err error))
}

// FeatureFlags provides dynamic feature flag evaluation.
type FeatureFlags interface {
	Enabled(key string) bool
}
