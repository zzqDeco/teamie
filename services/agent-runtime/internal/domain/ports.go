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

// NormalizeLimit normalizes the query pagination limit (default 50, max 100).
func (q RuntimeQuery) NormalizeLimit() int {
	n := int(q.Limit)
	if n <= 0 {
		return 50
	}
	if n > 100 {
		return 100
	}
	return n
}

// RuntimeResult represents the structured outcome of a runtime inspection or lifecycle operation.
type RuntimeResult struct {
	OK           bool
	DataJSON     string
	StatusCode   int32
	ErrorCode    string
	ErrorMessage string
}

// AgentRuntimeSummary holds the persisted runtime state and counters for an Agent.
type AgentRuntimeSummary struct {
	PendingInputs int
	RunningTasks  int
	ActiveRunID   string
	ActiveStatus  string
	CurrentState  string
	LastError     string
	LastActiveAt  *time.Time
	HasAgentRow   bool
}

// PublicRun is the safe owner-visible projection of a runtime run.
type PublicRun struct {
	ID         string     `json:"id"`
	Status     string     `json:"status"`
	InputCount int        `json:"inputCount"`
	CreatedAt  time.Time  `json:"createdAt"`
	UpdatedAt  time.Time  `json:"updatedAt"`
	StartedAt  time.Time  `json:"startedAt"`
	FinishedAt *time.Time `json:"finishedAt,omitempty"`
	ErrorCode  string     `json:"errorCode,omitempty"`
}

// PublicTask is the safe owner-visible projection of a background task.
type PublicTask struct {
	ID          string     `json:"id"`
	RunID       *string    `json:"runId,omitempty"`
	Kind        string     `json:"kind"`
	Description string     `json:"description"`
	Status      string     `json:"status"`
	CreatedAt   time.Time  `json:"createdAt"`
	UpdatedAt   time.Time  `json:"updatedAt"`
	FinishedAt  *time.Time `json:"finishedAt,omitempty"`
	ExitCode    *int32     `json:"exitCode,omitempty"`
}

// PublicEvent is the safe owner-visible projection of a runtime lifecycle event.
type PublicEvent struct {
	ID        string    `json:"id"`
	Type      string    `json:"type"`
	RunID     *string   `json:"runId,omitempty"`
	TaskID    *string   `json:"taskId,omitempty"`
	ToolName  *string   `json:"toolName,omitempty"`
	Status    *string   `json:"status,omitempty"`
	CreatedAt time.Time `json:"createdAt"`
	ErrorCode *string   `json:"errorCode,omitempty"`
}

// RuntimeEngine defines the outbound port for the Eino TurnLoop runtime engine.
type RuntimeEngine interface {
	Ready(ctx context.Context) error
	OwnerID() string
	IsFeatureEnabled(flag string) bool
	WorkAgent(ctx context.Context, profile AgentProfile, lease *Lease)
	CancelAgentTasks(ctx context.Context, agentID string)
	CancelRunTasks(ctx context.Context, agentID, runID string)
	CancelTaskByID(ctx context.Context, agentID, taskID string) (statusCode int32, errorCode string, err error)
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

// InspectRepository defines persistence operations for runtime inspection and run/task lifecycle control.
type InspectRepository interface {
	GetAgentRuntimeSummary(ctx context.Context, agentID string) (AgentRuntimeSummary, error)
	ListRuns(ctx context.Context, agentID, resourceID, cursor string, limit int) ([]PublicRun, error)
	ListTasks(ctx context.Context, agentID, resourceID, cursor string, limit int) ([]PublicTask, error)
	ListPublicEvents(ctx context.Context, agentID string, afterID int64, limit int) ([]PublicEvent, error)
	RequestRunCancel(ctx context.Context, agentID, runID string) (statusCode int32, errorCode string, err error)
	RequestRunRetry(ctx context.Context, agentID, runID string) (statusCode int32, errorCode string, err error)
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

// ScheduleCoordinator defines the application-level use-case boundary for schedule creation,
// wakeup input authorization, active schedule ACL filtering, and provenance row-source resolution.
type ScheduleCoordinator interface {
	ResolveScheduleRowSources(ctx context.Context, agentID, createdRunID string, sourceRefsJSON []byte, sourceOverflow bool) ([]InputSource, error)
	AuthorizeScheduleInput(ctx context.Context, lease *Lease, in Input) error
	FilterAuthorizedSchedules(ctx context.Context, agentID string, candidates []ScheduleCandidate) ([]ScheduleItem, error)
	AuthorizeScheduleListReceipt(ctx context.Context, agentID string, cached []byte) error
	EvaluateScheduleCreation(ctx context.Context, lease *Lease, runID, callID string, runInputs []Input, spec ScheduleSpec) (*ScheduleItem, string, error)
}

// DefaultScheduleCoordinator is registered by the application layer to construct a ScheduleCoordinator
// over outbound repository and gateway ports.
var DefaultScheduleCoordinator func(
	tx TxManager,
	sessions SessionRepository,
	schedules ScheduleRepository,
	archive ArchiveRepository,
	receipts ToolReceiptRepository,
	im IMGateway,
) ScheduleCoordinator
