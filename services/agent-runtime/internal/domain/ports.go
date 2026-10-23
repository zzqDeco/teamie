package domain

import (
	"context"
	"time"
)

// AgentProfile represents the control-plane profile and revision state of an Agent.
type AgentProfile struct {
	ID              string
	OwnerID         string
	DisplayName     string
	RoleDescription string
	SystemPrompt    string
	WorkspaceIDs    []string
	Enabled         bool
	Deleted         bool
	Revision        int64
}

// RuntimeQuery specifies the parameters for runtime inspection and lifecycle operations.
type RuntimeQuery struct {
	ServiceToken string
	AgentID      string
	OwnerID      string
	ResourceID   string
	Cursor       string
	Limit        int32
}

// RuntimeResult represents the structured outcome of a runtime inspection or lifecycle operation.
type RuntimeResult struct {
	OK           bool
	DataJSON     string
	StatusCode   int32
	ErrorCode    string
	ErrorMessage string
}

// RuntimeEngine defines the outbound port for the Eino TurnLoop runtime engine.
type RuntimeEngine interface {
	Ready(ctx context.Context) error
	Run(ctx context.Context)
	State(ctx context.Context, q RuntimeQuery) (RuntimeResult, error)
	Runs(ctx context.Context, q RuntimeQuery) (RuntimeResult, error)
	RunDetail(ctx context.Context, q RuntimeQuery) (RuntimeResult, error)
	Tasks(ctx context.Context, q RuntimeQuery) (RuntimeResult, error)
	TaskDetail(ctx context.Context, q RuntimeQuery) (RuntimeResult, error)
	Events(ctx context.Context, q RuntimeQuery) (RuntimeResult, error)
	CancelRun(ctx context.Context, q RuntimeQuery) (RuntimeResult, error)
	RetryRun(ctx context.Context, q RuntimeQuery) (RuntimeResult, error)
	CancelTask(ctx context.Context, q RuntimeQuery) (RuntimeResult, error)
}

// TxManager coordinates atomic database transactions without exposing SQL driver types
// to the domain or application layers.
type TxManager interface {
	WithinTx(ctx context.Context, fn func(ctx context.Context) error) error
	Ping(ctx context.Context) error
}

// SessionRepository defines persistence operations for Agent worker leases, inbox inputs, and session runs.
type SessionRepository interface {
	AcquireLease(ctx context.Context, agentID, owner string) (*Lease, error)
	RenewLease(ctx context.Context, lease *Lease) error
	ReleaseLease(ctx context.Context, lease *Lease)
	CheckLease(ctx context.Context, lease *Lease) error
	CheckLeaseForUpdate(ctx context.Context, lease *Lease) error
	GetCursor(ctx context.Context, lease *Lease) (string, error)
	IngestInputs(ctx context.Context, lease *Lease, cursor string, inputs []Input) error
	SkipInput(ctx context.Context, lease *Lease, eventID string) error
}

// ScheduleRepository defines persistence operations for durable Agent schedules.
type ScheduleRepository interface {
	ClockNow(ctx context.Context) (time.Time, error)
	CountActiveSchedules(ctx context.Context, agentID string) (int, error)
	FindScheduleByID(ctx context.Context, agentID, scheduleID string) (*ScheduleItem, bool, error)
	CreateSchedule(ctx context.Context, agentID, runID, scheduleID string, spec ValidatedScheduleSpec, sources []InputSource, overflow bool, now time.Time) (*ScheduleItem, error)
	ListActiveScheduleCandidates(ctx context.Context, agentID string, limit int) ([]ScheduleCandidate, error)
	CancelActiveSchedule(ctx context.Context, agentID, scheduleID string) error
	CancelScheduleWithLease(ctx context.Context, lease *Lease, scheduleID string) (status string, found bool, err error)
	GetScheduleStatusAndParent(ctx context.Context, agentID, scheduleID string) (status string, createdRunID string, found bool, err error)
	GetScheduleSourcesMeta(ctx context.Context, agentID, scheduleID string) (createdRunID string, sourceRefsJSON []byte, sourceOverflow bool, found bool, err error)
}

// ArchiveRepository defines source provenance resolution against the private context archive.
type ArchiveRepository interface {
	ResolveRunSources(ctx context.Context, agentID, runID string) ([]InputSource, error)
}

// ToolReceiptRepository defines persistence operations for idempotent tool observations and receipts.
type ToolReceiptRepository interface {
	ReadObservation(ctx context.Context, runID, callID, key string) ([]byte, bool, error)
	SaveObservation(ctx context.Context, lease *Lease, runID, callID, key string, payload []byte) error
}

// IMGateway defines outbound IM service operations for message/conversation ACL verification.
type IMGateway interface {
	AuthorizeSources(ctx context.Context, refs []InputSource) error
}

// ControlGateway defines outbound Agent Control Plane operations.
type ControlGateway interface {
	GetAgentProfile(ctx context.Context, agentID string) (*AgentProfile, error)
	ListAgentProfiles(ctx context.Context) ([]AgentProfile, error)
	ReportRuntimeState(ctx context.Context, agentID, state, lastError string, active bool) error
	Ping(ctx context.Context) error
}

// ModelGateway defines outbound Model Gateway operations.
type ModelGateway interface {
	Ping(ctx context.Context) (ready bool, contextWindow int, configID string, err error)
	CancelRun(ctx context.Context, agentID, executionID string) error
}

// SandboxGateway defines outbound Sandbox execution and workspace operations.
type SandboxGateway interface {
	CancelRun(ctx context.Context, agentID, executionID string) error
}

// TurnExecutor defines the execution boundary for advancing an Agent's serial TurnLoop.
type TurnExecutor interface {
	ExecuteTurn(ctx context.Context, agentID string, inputs []Input) error
}
