package eino

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	bg "github.com/cloudwego/eino/adk/backgroundtask"
	"teamie/pkg/postgres"
)

// TaskStore implements Eino's durable backgroundtask.TaskStore, TaskEventStore,
// NotificationWriter, and NotificationOutbox protocols backed by RuntimeRepository.
type TaskStore struct {
	Store         *Store
	AgentID       string
	Lease         *Lease
	LeaseDuration time.Duration
	AllowSubmit   func(context.Context) error
}

func (s *TaskStore) duration() time.Duration {
	if s.LeaseDuration > 0 {
		return s.LeaseDuration
	}
	return 30 * time.Second
}

func terminalTask(v bg.Status) bool {
	return v == bg.StatusCompleted || v == bg.StatusFailed || v == bg.StatusCanceled
}

func taskBound(b []byte) error {
	if len(b) > 1<<20 {
		return errors.New("task value exceeds 1 MiB")
	}
	return nil
}

func lifecycle(v bg.Status) bg.NotificationKind {
	switch v {
	case bg.StatusWaitingInput:
		return bg.NotificationWaitingInput
	case bg.StatusCompleted:
		return bg.NotificationCompleted
	case bg.StatusFailed:
		return bg.NotificationFailed
	case bg.StatusCanceled:
		return bg.NotificationCanceled
	}
	return ""
}

func (s *TaskStore) withFencedTx(ctx context.Context, fn func(txCtx context.Context) error) error {
	return s.Store.DB.WithinFencedTx(ctx, s.Lease, fn)
}

func (s *TaskStore) save(ctx context.Context, t *bg.Task, until *time.Time) error {
	b, err := json.Marshal(t)
	if err != nil {
		return err
	}
	return s.Store.DB.UpdateTaskRow(ctx, t.Spec.ID, s.AgentID, b, until)
}

func (s *TaskStore) load(ctx context.Context, id string) (*bg.Task, *time.Time, time.Time, error) {
	b, until, now, err := s.Store.DB.LoadTaskRowForUpdate(ctx, s.AgentID, id)
	if postgres.IsNoRows(err) {
		err = bg.ErrNotFound
	}
	if err != nil {
		return nil, nil, now, err
	}
	t := new(bg.Task)
	if err = json.Unmarshal(b, t); err != nil {
		return nil, nil, now, err
	}
	if t.Status == bg.StatusRunning && (until == nil || !now.Before(*until)) {
		until = nil
		t.Version++
		t.UpdatedAt = now
		if t.LeaseExpiryPolicy == bg.LeaseExpiryRetry {
			t.Status = bg.StatusPending
		} else {
			t.Status = bg.StatusFailed
			t.ResultError = "execution lease expired and retry is disabled"
			if t.CancelRequestedAt != nil {
				t.Status = bg.StatusCanceled
				t.ResultError = t.CancelReason
				if t.ResultError == "" {
					t.ResultError = "canceled"
				}
			}
			t.PendingResume = nil
			t.DoneAt = &now
		}
		if err = s.save(ctx, t, until); err == nil {
			err = s.enqueue(ctx, t, lifecycle(t.Status))
		}
		if err == nil {
			err = s.public(ctx, t)
		}
		if err != nil {
			return nil, nil, now, err
		}
	}
	return t, until, now, nil
}

func (s *TaskStore) Create(ctx context.Context, r *bg.CreateTaskRequest) (*bg.Task, error) {
	if r == nil || r.Spec.ID == "" || r.Spec.ExecutorKey == "" || r.Spec.NotifySession && r.Spec.SessionID == "" || (r.LeaseExpiryPolicy != bg.LeaseExpiryRetry && r.LeaseExpiryPolicy != bg.LeaseExpiryFail) {
		return nil, errors.New("invalid task specification")
	}
	for _, b := range [][]byte{r.Checkpoint, r.ContextSnapshot} {
		if err := taskBound(b); err != nil {
			return nil, err
		}
	}
	if s.AllowSubmit != nil {
		if err := s.AllowSubmit(ctx); err != nil {
			return nil, err
		}
	}
	var b []byte
	err := s.withFencedTx(ctx, func(txCtx context.Context) error {
		now, err := s.Store.DB.ClockNow(txCtx)
		if err != nil {
			return err
		}
		t := &bg.Task{Spec: r.Spec, LeaseExpiryPolicy: r.LeaseExpiryPolicy, Checkpoint: r.Checkpoint, ContextSnapshot: r.ContextSnapshot, Status: bg.StatusPending, Version: 1, CreatedAt: now, UpdatedAt: now}
		b, err = json.Marshal(t)
		if err != nil {
			return err
		}
		var runID *string
		if run, runErr := CurrentRun(txCtx); runErr == nil {
			runID = &run.ID
		}
		inserted, err := s.Store.DB.InsertTaskRow(txCtx, r.Spec.ID, s.AgentID, runID, b)
		if err != nil {
			return err
		}
		if !inserted {
			return bg.ErrAlreadyExists
		}
		if err = s.enqueue(txCtx, t, bg.NotificationTaskCreated); err == nil {
			err = s.public(txCtx, t)
		}
		return err
	})
	if err != nil {
		return nil, err
	}
	var out bg.Task
	err = json.Unmarshal(b, &out)
	return &out, err
}

func (s *TaskStore) transition(ctx context.Context, id string, version int64, active, uncanceled bool, fn func(*bg.Task, *time.Time, time.Time) (*time.Time, error)) (*bg.Task, error) {
	var (
		t     *bg.Task
		opErr error
	)
	err := s.withFencedTx(ctx, func(txCtx context.Context) error {
		var (
			until   *time.Time
			now     time.Time
			loadErr error
		)
		t, until, now, loadErr = s.load(txCtx, id)
		if loadErr != nil {
			return loadErr
		}
		if t.Version != version {
			opErr = bg.ErrVersionConflict
		} else if active && (t.Status != bg.StatusRunning || until == nil || !now.Before(*until)) {
			opErr = bg.ErrLeaseLost
		} else if uncanceled && t.CancelRequestedAt != nil {
			opErr = bg.ErrLeaseLost
		} else {
			until, opErr = fn(t, until, now)
		}
		if opErr != nil {
			return nil
		}
		saveErr := s.save(txCtx, t, until)
		if saveErr == nil {
			saveErr = s.enqueue(txCtx, t, lifecycle(t.Status))
		}
		if saveErr == nil {
			saveErr = s.public(txCtx, t)
		}
		return saveErr
	})
	if err != nil {
		return nil, err
	}
	if opErr != nil {
		return nil, opErr
	}
	var out bg.Task
	b, err := json.Marshal(t)
	if err == nil {
		err = json.Unmarshal(b, &out)
	}
	return &out, err
}

func advanceTask(t *bg.Task, now time.Time) { t.Version++; t.UpdatedAt = now }
func finishTask(t *bg.Task, now time.Time) {
	advanceTask(t, now)
	t.PendingResume = nil
	t.DoneAt = &now
}

func (s *TaskStore) Start(ctx context.Context, r *bg.StartTaskRequest) (*bg.Task, error) {
	if r == nil {
		return nil, errors.New("nil start")
	}
	return s.transition(ctx, r.TaskID, r.ExpectedVersion, false, false, func(t *bg.Task, _ *time.Time, n time.Time) (*time.Time, error) {
		if t.Status != bg.StatusPending {
			return nil, bg.ErrIllegalTransition
		}
		t.Status = bg.StatusRunning
		t.Attempt++
		advanceTask(t, n)
		v := n.Add(s.duration())
		return &v, nil
	})
}

func (s *TaskStore) Heartbeat(ctx context.Context, r *bg.HeartbeatRequest) (*bg.Task, error) {
	if r == nil {
		return nil, errors.New("nil heartbeat")
	}
	return s.transition(ctx, r.TaskID, r.ExpectedVersion, true, true, func(t *bg.Task, _ *time.Time, n time.Time) (*time.Time, error) {
		advanceTask(t, n)
		v := n.Add(s.duration())
		return &v, nil
	})
}

func (s *TaskStore) CommitStart(ctx context.Context, r *bg.CommitStartRequest) (*bg.Task, error) {
	if r == nil || len(r.Checkpoint) == 0 {
		return nil, errors.New("start checkpoint required")
	}
	if err := taskBound(r.Checkpoint); err != nil {
		return nil, err
	}
	return s.transition(ctx, r.TaskID, r.ExpectedVersion, true, true, func(t *bg.Task, u *time.Time, n time.Time) (*time.Time, error) {
		if len(t.Checkpoint) > 0 {
			return u, bg.ErrIllegalTransition
		}
		t.Checkpoint = r.Checkpoint
		advanceTask(t, n)
		return u, nil
	})
}

func (s *TaskStore) ReportTranscriptFailure(ctx context.Context, r *bg.ReportTranscriptFailureRequest) (*bg.Task, error) {
	if r == nil || r.Error == "" || len(r.Error) > 4096 {
		return nil, errors.New("invalid transcript failure")
	}
	return s.transition(ctx, r.TaskID, r.ExpectedVersion, true, true, func(t *bg.Task, u *time.Time, n time.Time) (*time.Time, error) {
		if t.OutputFileErr == "" {
			t.OutputFileErr = r.Error
			advanceTask(t, n)
		}
		return u, nil
	})
}

func (s *TaskStore) Complete(ctx context.Context, r *bg.CompleteTaskRequest) (*bg.Task, error) {
	if r == nil {
		return nil, errors.New("nil complete")
	}
	if err := taskBound(r.Data); err != nil {
		return nil, err
	}
	return s.transition(ctx, r.TaskID, r.ExpectedVersion, true, true, func(t *bg.Task, _ *time.Time, n time.Time) (*time.Time, error) {
		t.Status = bg.StatusCompleted
		t.ResultData = r.Data
		t.ResultError = ""
		finishTask(t, n)
		return nil, nil
	})
}

func (s *TaskStore) Fail(ctx context.Context, r *bg.FailTaskRequest) (*bg.Task, error) {
	if r == nil || r.Error == "" || len(r.Error) > 4096 {
		return nil, bg.ErrInvalidExecutionResult
	}
	return s.transition(ctx, r.TaskID, r.ExpectedVersion, true, true, func(t *bg.Task, _ *time.Time, n time.Time) (*time.Time, error) {
		t.Status = bg.StatusFailed
		t.ResultData = nil
		t.ResultError = r.Error
		finishTask(t, n)
		return nil, nil
	})
}

func (s *TaskStore) pause(ctx context.Context, id string, v int64, b []byte, status bg.Status) (*bg.Task, error) {
	if len(b) == 0 {
		return nil, errors.New("pause checkpoint required")
	}
	if err := taskBound(b); err != nil {
		return nil, err
	}
	return s.transition(ctx, id, v, true, true, func(t *bg.Task, _ *time.Time, n time.Time) (*time.Time, error) {
		t.Status = status
		t.Checkpoint = b
		t.PendingResume = nil
		advanceTask(t, n)
		return nil, nil
	})
}

func (s *TaskStore) WaitInput(ctx context.Context, r *bg.WaitInputTaskRequest) (*bg.Task, error) {
	if r == nil {
		return nil, errors.New("nil wait")
	}
	return s.pause(ctx, r.TaskID, r.ExpectedVersion, r.Checkpoint, bg.StatusWaitingInput)
}

func (s *TaskStore) Suspend(ctx context.Context, r *bg.SuspendTaskRequest) (*bg.Task, error) {
	if r == nil {
		return nil, errors.New("nil suspend")
	}
	return s.pause(ctx, r.TaskID, r.ExpectedVersion, r.Checkpoint, bg.StatusSuspended)
}

func (s *TaskStore) Yield(ctx context.Context, r *bg.YieldTaskRequest) (*bg.Task, error) {
	if r == nil {
		return nil, errors.New("nil yield")
	}
	if err := taskBound(r.Checkpoint); err != nil {
		return nil, err
	}
	return s.transition(ctx, r.TaskID, r.ExpectedVersion, true, true, func(t *bg.Task, _ *time.Time, n time.Time) (*time.Time, error) {
		t.Status = bg.StatusPending
		if len(r.Checkpoint) > 0 {
			t.Checkpoint = r.Checkpoint
		}
		advanceTask(t, n)
		return nil, nil
	})
}

func (s *TaskStore) AckCancel(ctx context.Context, r *bg.AckCancelRequest) (*bg.Task, error) {
	if r == nil || len(r.Reason) > 4096 {
		return nil, errors.New("invalid cancel ack")
	}
	return s.transition(ctx, r.TaskID, r.ExpectedVersion, true, false, func(t *bg.Task, _ *time.Time, n time.Time) (*time.Time, error) {
		reason := t.CancelReason
		if reason == "" {
			reason = r.Reason
		}
		if reason == "" {
			reason = "canceled"
		}
		t.Status = bg.StatusCanceled
		t.ResultData = nil
		t.ResultError = reason
		finishTask(t, n)
		return nil, nil
	})
}

func (s *TaskStore) RequestCancel(ctx context.Context, r *bg.RequestCancelRequest) (*bg.Task, error) {
	if r == nil || len(r.Reason) > 4096 {
		return nil, errors.New("invalid cancellation")
	}
	return s.transition(ctx, r.TaskID, r.ExpectedVersion, false, false, func(t *bg.Task, u *time.Time, n time.Time) (*time.Time, error) {
		if terminalTask(t.Status) {
			return u, bg.ErrAlreadyTerminal
		}
		if t.CancelRequestedAt != nil {
			return u, nil
		}
		t.CancelRequestedAt = &n
		t.CancelReason = r.Reason
		if t.Status == bg.StatusRunning {
			advanceTask(t, n)
			v := n.Add(s.duration())
			return &v, nil
		}
		if t.Status == bg.StatusPending && t.LeaseExpiryPolicy == bg.LeaseExpiryRetry && t.Attempt > 0 {
			advanceTask(t, n)
			return nil, nil
		}
		t.Status = bg.StatusCanceled
		t.ResultError = r.Reason
		if t.ResultError == "" {
			t.ResultError = "canceled"
		}
		finishTask(t, n)
		return nil, nil
	})
}

func (s *TaskStore) Resume(ctx context.Context, r *bg.ResumeRequest) (*bg.Task, error) {
	if r == nil {
		return nil, errors.New("nil resume")
	}
	for _, b := range [][]byte{r.Data, r.ContextSnapshot} {
		if err := taskBound(b); err != nil {
			return nil, err
		}
	}
	return s.transition(ctx, r.TaskID, r.ExpectedVersion, false, false, func(t *bg.Task, u *time.Time, n time.Time) (*time.Time, error) {
		if t.Status != bg.StatusWaitingInput {
			return u, bg.ErrIllegalTransition
		}
		t.Status = bg.StatusPending
		t.PendingResume = r.Data
		if r.ContextSnapshot != nil {
			t.ContextSnapshot = r.ContextSnapshot
		}
		advanceTask(t, n)
		return nil, nil
	})
}

func (s *TaskStore) ReleaseSuspension(ctx context.Context, r *bg.ReleaseSuspensionRequest) (*bg.Task, error) {
	if r == nil {
		return nil, errors.New("nil release")
	}
	if err := taskBound(r.ContextSnapshot); err != nil {
		return nil, err
	}
	return s.transition(ctx, r.TaskID, r.ExpectedVersion, false, false, func(t *bg.Task, u *time.Time, n time.Time) (*time.Time, error) {
		if t.Status != bg.StatusSuspended {
			return u, bg.ErrIllegalTransition
		}
		t.Status = bg.StatusPending
		if r.ContextSnapshot != nil {
			t.ContextSnapshot = r.ContextSnapshot
		}
		advanceTask(t, n)
		return nil, nil
	})
}

var _ bg.TaskStore = (*TaskStore)(nil)
