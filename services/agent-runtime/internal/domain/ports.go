package domain

import (
	"context"
	"encoding/json"
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
// wakeup input authorization, active schedule ACL filtering, provenance row-source resolution,
// worker lease validation, inbox ingestion, and context policy resolution.
type ScheduleCoordinator interface {
	ResolveScheduleRowSources(ctx context.Context, agentID, createdRunID string, sourceRefsJSON []byte, sourceOverflow bool) ([]InputSource, error)
	AuthorizeScheduleInput(ctx context.Context, lease *Lease, in Input) error
	FilterAuthorizedSchedules(ctx context.Context, agentID string, candidates []ScheduleCandidate) ([]ScheduleItem, error)
	AuthorizeScheduleListReceipt(ctx context.Context, agentID string, cached []byte) error
	EvaluateScheduleCreation(ctx context.Context, lease *Lease, runID, callID string, runInputs []Input, spec ScheduleSpec) (*ScheduleItem, string, error)
	ValidateLease(ctx context.Context, lease *Lease) error
	CheckRevisionPreemption(enabled, deleted bool, currentRevision, workerRevision int64) bool
	ResolveRunContextPolicy(profile string, raw json.RawMessage) (string, json.RawMessage, error)
	GetInboxCursor(ctx context.Context, lease *Lease) (string, error)
	IngestInboxInputs(ctx context.Context, lease *Lease, cursor string, inputs []Input) error
	SkipInboxInput(ctx context.Context, lease *Lease, eventID string) error
	ResolveRunSources(ctx context.Context, agentID, runID string) ([]InputSource, error)
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

// DefaultRuntimeRepository is registered by the PostgreSQL infrastructure package to open a RuntimeRepository.
var DefaultRuntimeRepository func(ctx context.Context, url string) (RuntimeRepository, error)

// ExecTag abstracts a SQL command execution tag without importing driver packages.
type ExecTag interface {
	RowsAffected() int64
}

// RowScanner abstracts a single-row SQL scanner without importing driver packages.
type RowScanner interface {
	Scan(dest ...any) error
}

// RowsScanner abstracts a multi-row SQL cursor without importing driver packages.
type RowsScanner interface {
	Next() bool
	Scan(dest ...any) error
	Close()
	Err() error
}

// SessionEventWrite holds a serialized session event ready for persistence.
type SessionEventWrite struct {
	EventID string
	Kind    string
	Payload []byte
	Hash    string
}

// ContextArtifactWrite holds the parameters for persisting a context artifact and its source version proof.
type ContextArtifactWrite struct {
	AgentID       string
	ActiveRunID   string
	ActiveAttempt string
	RunID         string
	Attempt       string
	Key           string
	Kind          string
	ToolCallID    string
	ToolName      string
	Content       []byte
	ContentHash   string
	SourceJSON    []byte
	ParentJSON    []byte
	EventSeq      int64
	VersionKey    string
	VersionBytes  []byte
}

// AcceptedToolCallCandidate holds a committed tool call candidate from an accepted or current Run.
type AcceptedToolCallCandidate struct {
	RunID string
	Name  string
	Raw   []byte
}

// ToolCallResultRecord holds a persisted tool call receipt row.
type ToolCallResultRecord struct {
	CallID string
	Name   string
	Result []byte
}

// AttemptEventPayload holds an attempt_id and serialized session event payload.
type AttemptEventPayload struct {
	AttemptID string
	Payload   []byte
}

// DrainedScheduleLog records the outcome of draining a due schedule wakeup into runtime_inputs.
type DrainedScheduleLog struct {
	ScheduleID     string
	NotificationID string
	EventID        string
	ParentRunID    string
	Inserted       bool
}

// LoadedContextArtifactRow holds the raw columns of a loaded context artifact.
type LoadedContextArtifactRow struct {
	Attempt    string
	Key        string
	Kind       string
	ToolCallID string
	ToolName   string
	Raw        []byte
	Hash       string
	Sources    []byte
	Parents    []byte
	EventSeq   int64
	CreatedAt  time.Time
	Version    []byte
}

// ArchiveSessionEventRow holds a raw session event row from runtime_session_events.
type ArchiveSessionEventRow struct {
	Seq     int64
	RunID   string
	Attempt string
	EventID string
	Payload []byte
}

// SearchArtifactCandidate holds a candidate context artifact row for search scanning.
type SearchArtifactCandidate struct {
	Seq   int64
	RunID string
	Kind  string
	Key   string
}

// SeqPayload holds a sequence number and serialized event payload.
type SeqPayload struct {
	Seq     int64
	Payload []byte
}

// RetrievalObservationRow holds a committed context retrieval tool call and its observation payload.
type RetrievalObservationRow struct {
	OriginRunID string
	Receipt     []byte
	Observation []byte
}

// MessageSourceArtifactRow holds a message source index artifact row.
type MessageSourceArtifactRow struct {
	Key       string
	Sources   []byte
	Inherited []byte
}

// WorkspaceWriterRow holds a prior workspace writer tool call and its optional source snapshot observation.
type WorkspaceWriterRow struct {
	Run      string
	Call     string
	Name     string
	Hash     string
	Result   []byte
	Snapshot []byte
	At       time.Time
}

// RunEventPayloadRow holds a run_id and serialized session event payload.
type RunEventPayloadRow struct {
	RunID   string
	Payload []byte
}

// TaskEventRow holds a persisted task progress event row from runtime_task_events.
type TaskEventRow struct {
	Seq       int64
	EventID   string
	Data      []byte
	CreatedAt time.Time
}

// NotificationDeliveryRow holds a claimed task notification row from runtime_task_notifications.
type NotificationDeliveryRow struct {
	ID       string
	Snapshot []byte
	Receipt  []byte
}

// RuntimeRepository defines the full outbound persistence contract for the Agent Runtime engine.
type RuntimeRepository interface {
	TxManager
	SessionRepository
	ScheduleRepository
	ArchiveRepository
	ToolReceiptRepository
	InspectRepository
	Close()
	Exec(ctx context.Context, sql string, args ...any) (ExecTag, error)
	Query(ctx context.Context, sql string, args ...any) (RowsScanner, error)
	QueryRow(ctx context.Context, sql string, args ...any) RowScanner
	WithinFencedTx(ctx context.Context, lease *Lease, fn func(txCtx context.Context) error) error
	ReportAgentState(ctx context.Context, agentID, owner, state, code string, active bool) error
	ListPendingInputs(ctx context.Context, agentID string) ([][]byte, error)
	ListFreshPendingInputs(ctx context.Context, agentID string) ([][]byte, error)
	FilterEligibleInputIDs(ctx context.Context, agentID string, ids []string) (map[string]bool, error)
	StartOrResumeRun(
		ctx context.Context,
		l *Lease,
		firstEventID, newRunID, newAttemptID string,
		prepareNew func() (key string, inputsJSON []byte, canonProfile string, canonBudget json.RawMessage, inputIDs []string, err error),
	) (runID string, inputsJSON []byte, contextHead, acceptanceHead int64, contextProfile string, contextBudget json.RawMessage, resumed bool, err error)
	FinishRun(ctx context.Context, l *Lease, runID, state, safeError string, retry bool) error
	InvalidateRun(ctx context.Context, l *Lease, runID string, invalid []string) error
	ConfigureRun(ctx context.Context, l *Lease, runID string, revision, generation int64, resumeAttempt string, hasResume bool) error
	PreemptRun(ctx context.Context, l *Lease, runID string) error
	IsRunCanceled(ctx context.Context, runID string) bool
	CheckpointExists(ctx context.Context, id string) bool
	GetCheckpointMetadata(ctx context.Context, id string) ([]byte, error)
	GetCheckpoint(ctx context.Context, id string) ([]byte, []byte, bool, error)
	SetCheckpoint(ctx context.Context, l *Lease, id string, payload, metadata []byte) error
	DeleteCheckpoint(ctx context.Context, l *Lease, id string) error
	AppendSessionEvents(ctx context.Context, l *Lease, sessionID, runID, attemptID string, items []SessionEventWrite) (bool, error)
	LoadSessionEventPayloads(
		ctx context.Context,
		sessionID, runID, attemptID string,
		contextHead, acceptanceHead int64,
		afterEventID string,
		kinds []string,
		limit int,
		reverse bool,
	) ([][]byte, bool, error)
	SessionEventExists(ctx context.Context, sessionID, eventID string) (bool, error)
	GetCachedModelCall(ctx context.Context, runID string, step int, hash string) ([]byte, error)
	GetModelStepAndRecovery(ctx context.Context, runID string, step int) ([]byte, []byte, error)
	SaveModelCall(ctx context.Context, l *Lease, runID string, step int, hash string, b []byte) error
	RecordToolIntent(ctx context.Context, l *Lease, runID, id, name, hash string) ([]byte, error)
	RecordToolResult(ctx context.Context, l *Lease, runID, id string, b []byte) error
	ListModelCallResponses(ctx context.Context, runID string) ([][]byte, error)
	ListIMToolCallResults(ctx context.Context, runID string) ([]ToolCallResultRecord, error)
	ListOtherAttemptMessageEvents(ctx context.Context, runID, excludeAttemptID string) ([]AttemptEventPayload, error)
	GetContextRecovery(ctx context.Context, runID string, step int) (hash, profile, code string, streaming bool, version int, decisionJSON []byte, err error)
	SaveContextRecovery(
		ctx context.Context,
		lease *Lease,
		runID string,
		step int,
		profile, hash string,
		streaming bool,
		code string,
		decision []byte,
		checkConflict func(oldProfile, oldHash string, oldMode bool, oldCode string, oldVersion int, oldDecision []byte) error,
	) error
	GetObservation(ctx context.Context, runID, callID, key string) ([]byte, error)
	CancelAllActiveSchedules(ctx context.Context, agentID string) error
	DrainDueSchedules(ctx context.Context, l *Lease) ([]DrainedScheduleLog, error)
	CheckRunAttemptRunning(ctx context.Context, runID, agentID, attemptID string) (bool, error)
	ListVisibleSessionRunIDsThrough(ctx context.Context, sessionID, attemptID, runID string, contextHead, acceptanceHead, eventHead int64) ([]string, error)
	PutContextArtifact(ctx context.Context, l *Lease, w ContextArtifactWrite, mergeSources func(oldSources []byte) ([]byte, error)) error
	LoadVisibleContextArtifact(
		ctx context.Context,
		agentID, attemptID, activeRunID string,
		contextHead, acceptanceHead int64,
		targetRunID, key string,
	) (*LoadedContextArtifactRow, bool, error)
	SaveRunningRunObservation(ctx context.Context, l *Lease, runID, agentID, attemptID, call, key string, response []byte) error
	ListVisibleSessionEventPage(ctx context.Context, sessionID, attemptID, runID string, contextHead, acceptanceHead, after, head int64, limit int) ([]ArchiveSessionEventRow, error)
	GetContextSearchHeads(ctx context.Context, agentID, sessionID, attemptID, runID string, contextHead, acceptanceHead int64) (int64, int64, error)
	ListVisibleSearchArtifactCandidates(ctx context.Context, agentID, attemptID, runID string, contextHead, acceptanceHead, after, artifactHead int64, limit int) ([]SearchArtifactCandidate, error)
	GetUploadedFileToolObservation(ctx context.Context, runID, call, obsKey string) (string, string, []byte, bool, error)
	GetToolCallObservationWithMeta(ctx context.Context, runID, call, obsKey string) (string, string, []byte, error)
	ListAcceptedToolCallCandidates(ctx context.Context, agentID, callID string, names []string, runID string, acceptanceHead int64, originRun string) ([]AcceptedToolCallCandidate, error)
	ListVisibleToolReductionRefs(ctx context.Context, agentID, attemptID, runID string, contextHead, acceptanceHead int64, toolName, callID, originRun string) ([]string, error)
	GetRunContextBudget(ctx context.Context, runID string) ([]byte, error)
	ListVisibleRunEventPayloads(ctx context.Context, sessionID, attemptID, activeRunID string, contextHead, acceptanceHead int64, targetRunID string, after, eventHead int64) ([]SeqPayload, error)
	ListMatchingToolCallReceipts(ctx context.Context, agentID, callID string, names, hashes []string, runID string, acceptanceHead int64, currentOnly bool) ([]AcceptedToolCallCandidate, error)
	GetAcceptedInputPayload(ctx context.Context, agentID, eventID, runID string, acceptanceHead int64) ([]byte, string, bool, error)
	ListAcceptedRetrievalObservations(ctx context.Context, agentID, callID, toolName, runID string, acceptanceHead int64) ([]RetrievalObservationRow, error)
	ListVisibleMessageSourceArtifacts(ctx context.Context, agentID, attemptID, runID string, contextHead, acceptanceHead int64, keys []string) ([]MessageSourceArtifactRow, error)
	ListVisibleWantedSessionEvents(ctx context.Context, sessionID, attemptID, runID string, contextHead, acceptanceHead int64, needsLegacy bool, ids []string, after int64) ([]ArchiveSessionEventRow, error)
	ListRunToolReductionArtifactKeys(ctx context.Context, agentID, runID, callID, toolName string) ([]string, error)
	GetSessionEventPayloadBySeq(ctx context.Context, sessionID, runID string, seq int64) ([]byte, error)
	HasLegacyWorkspaceToolActivity(ctx context.Context, agentID, ownerRunID string, createdAt time.Time, names []string, sessionID string) (bool, error)
	GetAgentToolCallMeta(ctx context.Context, agentID, runID, callID string) (string, string, time.Time, []byte, error)
	ListPriorWorkspaceWriters(ctx context.Context, agentID string, cutoff time.Time, names []string, excludeRunID, excludeCallID, obsKey string) ([]WorkspaceWriterRow, error)
	GetTaskOriginBeforeCutoff(ctx context.Context, agentID, taskID string, cutoff time.Time) (string, time.Time, error)
	GetRunHeads(ctx context.Context, runID, agentID string) (string, int64, int64, error)
	ListRunToolCallsBeforeCutoff(ctx context.Context, runID string, cutoff time.Time) ([]ToolCallResultRecord, error)
	GetArtifactProvenanceRaw(ctx context.Context, agentID, runID, key string) ([]byte, []byte, error)
	IsValidWorkspaceSourceSnapshotArtifact(ctx context.Context, agentID, runID, key string) (bool, error)
	HasUnknownLegacyWorkspaceWriterView(ctx context.Context, agentID, runID string) (bool, error)
	ListVisibleMessageUpdatedEventsForCall(ctx context.Context, sessionID, attemptID, runID string, contextHead, acceptanceHead int64, originRun, callID string) ([]RunEventPayloadRow, error)
	LoadTaskRowForUpdate(ctx context.Context, agentID, taskID string) ([]byte, *time.Time, time.Time, error)
	InsertTaskRow(ctx context.Context, taskID, agentID string, runID *string, snapshot []byte) (bool, error)
	UpdateTaskRow(ctx context.Context, taskID, agentID string, snapshot []byte, until *time.Time) error
	InsertTaskNotificationRow(ctx context.Context, id, agentID, taskID string, snapshot []byte) error
	InsertTaskPublicEvent(ctx context.Context, taskID, agentID, status string) error
	ListTaskIDsAfter(ctx context.Context, agentID, lastID string) ([]string, error)
	UpsertTaskEventRow(ctx context.Context, taskID, eventID string, payload []byte) (bool, []byte, time.Time, error)
	MaxTaskEventSeq(ctx context.Context, taskID string) (int64, error)
	ListTaskEventRows(ctx context.Context, taskID string, endSeq, posSeq int64, newestFirst bool, limit int) ([]TaskEventRow, error)
	UpsertCustomNotificationRow(ctx context.Context, taskID, eventID, kind string, data []byte) (bool, string, []byte, error)
	ClaimNotificationRowsForUpdate(ctx context.Context, agentID string, limit int, leaseSeconds float64) ([]NotificationDeliveryRow, error)
	AckNotificationRow(ctx context.Context, agentID string, receipt []byte) (bool, error)
	PersistNotificationInput(
		ctx context.Context,
		lease *Lease,
		agentID, taskID, notificationID, status, createdAt string,
		extractSources func(rawInputs []byte) ([]InputSource, bool, error),
	) (seq, parentRunID string, inserted bool, err error)
	ListIncompleteAgentTaskIDs(ctx context.Context, agentID string) ([]string, error)
	ListIncompleteRunTaskIDs(ctx context.Context, agentID, runID string) ([]string, error)
}
