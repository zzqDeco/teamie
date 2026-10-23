package eino

import (
	"context"
	"time"

	bg "github.com/cloudwego/eino/adk/backgroundtask"
	"teamie/services/agent-runtime/internal/infra/pg"
)

// TaskStore adapts Runtime's PostgreSQL TaskStore implementation in infra/pg
// to Eino's backgroundtask interfaces.
type TaskStore struct {
	Store         *Store
	AgentID       string
	Lease         *Lease
	LeaseDuration time.Duration
	AllowSubmit   func(context.Context) error
}

func terminalTask(v bg.Status) bool {
	return v == bg.StatusCompleted || v == bg.StatusFailed || v == bg.StatusCanceled
}

func (s *TaskStore) pgStore() *pg.TaskStore {
	return &pg.TaskStore{
		Repo:          s.Store.DB,
		AgentID:       s.AgentID,
		Lease:         s.Lease,
		LeaseDuration: s.LeaseDuration,
		AllowSubmit:   s.AllowSubmit,
		CurrentRunID: func(ctx context.Context) *string {
			if r, err := CurrentRun(ctx); err == nil {
				return &r.ID
			}
			return nil
		},
	}
}

func (s *TaskStore) Create(ctx context.Context, r *bg.CreateTaskRequest) (*bg.Task, error) {
	return s.pgStore().Create(ctx, r)
}
func (s *TaskStore) Get(ctx context.Context, id string) (*bg.Task, error) {
	return s.pgStore().Get(ctx, id)
}
func (s *TaskStore) Start(ctx context.Context, r *bg.StartTaskRequest) (*bg.Task, error) {
	return s.pgStore().Start(ctx, r)
}
func (s *TaskStore) Heartbeat(ctx context.Context, r *bg.HeartbeatRequest) (*bg.Task, error) {
	return s.pgStore().Heartbeat(ctx, r)
}
func (s *TaskStore) CommitStart(ctx context.Context, r *bg.CommitStartRequest) (*bg.Task, error) {
	return s.pgStore().CommitStart(ctx, r)
}
func (s *TaskStore) ReportTranscriptFailure(ctx context.Context, r *bg.ReportTranscriptFailureRequest) (*bg.Task, error) {
	return s.pgStore().ReportTranscriptFailure(ctx, r)
}
func (s *TaskStore) Complete(ctx context.Context, r *bg.CompleteTaskRequest) (*bg.Task, error) {
	return s.pgStore().Complete(ctx, r)
}
func (s *TaskStore) Fail(ctx context.Context, r *bg.FailTaskRequest) (*bg.Task, error) {
	return s.pgStore().Fail(ctx, r)
}
func (s *TaskStore) WaitInput(ctx context.Context, r *bg.WaitInputTaskRequest) (*bg.Task, error) {
	return s.pgStore().WaitInput(ctx, r)
}
func (s *TaskStore) Suspend(ctx context.Context, r *bg.SuspendTaskRequest) (*bg.Task, error) {
	return s.pgStore().Suspend(ctx, r)
}
func (s *TaskStore) Yield(ctx context.Context, r *bg.YieldTaskRequest) (*bg.Task, error) {
	return s.pgStore().Yield(ctx, r)
}
func (s *TaskStore) AckCancel(ctx context.Context, r *bg.AckCancelRequest) (*bg.Task, error) {
	return s.pgStore().AckCancel(ctx, r)
}
func (s *TaskStore) RequestCancel(ctx context.Context, r *bg.RequestCancelRequest) (*bg.Task, error) {
	return s.pgStore().RequestCancel(ctx, r)
}
func (s *TaskStore) Resume(ctx context.Context, r *bg.ResumeRequest) (*bg.Task, error) {
	return s.pgStore().Resume(ctx, r)
}
func (s *TaskStore) ReleaseSuspension(ctx context.Context, r *bg.ReleaseSuspensionRequest) (*bg.Task, error) {
	return s.pgStore().ReleaseSuspension(ctx, r)
}
func (s *TaskStore) WaitForTaskVersion(ctx context.Context, r *bg.WaitForTaskVersionRequest) (*bg.Task, error) {
	return s.pgStore().WaitForTaskVersion(ctx, r)
}
func (s *TaskStore) ListPending(ctx context.Context, r *bg.ListPendingRequest) (*bg.ListPendingResult, error) {
	return s.pgStore().ListPending(ctx, r)
}
func (s *TaskStore) ListSuspended(ctx context.Context, r *bg.ListSuspendedRequest) (*bg.ListSuspendedResult, error) {
	return s.pgStore().ListSuspended(ctx, r)
}
func (s *TaskStore) AppendTaskEvent(ctx context.Context, r *bg.AppendTaskEventRequest) (*bg.AppendTaskEventResult, error) {
	return s.pgStore().AppendTaskEvent(ctx, r)
}
func (s *TaskStore) ListTaskEvents(ctx context.Context, r *bg.ListTaskEventsRequest) (*bg.ListTaskEventsResult, error) {
	return s.pgStore().ListTaskEvents(ctx, r)
}
func (s *TaskStore) EnqueueTaskNotification(ctx context.Context, id string, attempt int64, r *bg.NotifyParentRequest) error {
	return s.pgStore().EnqueueTaskNotification(ctx, id, attempt, r)
}
func (s *TaskStore) Receive(ctx context.Context, r *bg.ReceiveNotificationsRequest) (*bg.ReceiveNotificationsResult, error) {
	return s.pgStore().Receive(ctx, r)
}
func (s *TaskStore) Ack(ctx context.Context, r bg.NotificationReceipt) error {
	return s.pgStore().Ack(ctx, r)
}

var _ bg.TaskStore = (*TaskStore)(nil)
var _ bg.TaskEventStore = (*TaskStore)(nil)
var _ bg.NotificationWriter = (*TaskStore)(nil)
var _ bg.NotificationOutbox = (*TaskStore)(nil)
