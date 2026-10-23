package eino

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	bg "github.com/cloudwego/eino/adk/backgroundtask"
	"teamie/pkg/postgres"
	"teamie/services/agent-runtime/internal/domain"
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

func (s *TaskStore) enqueue(ctx context.Context, t *bg.Task, k bg.NotificationKind) error {
	if k == "" || t.Spec.SessionID == "" || (k != bg.NotificationTaskCreated && !t.Spec.NotifySession) {
		return nil
	}
	n := bg.Notification{ID: fmt.Sprintf("%s:%d:%s", t.Spec.ID, t.Version, k), TaskID: t.Spec.ID, SessionID: t.Spec.SessionID, Version: t.Version, Kind: k, CreatedAt: t.UpdatedAt}
	return s.insertNotification(ctx, n)
}

func (s *TaskStore) insertNotification(ctx context.Context, n bg.Notification) error {
	b, err := json.Marshal(n)
	if err != nil {
		return err
	}
	return s.Store.DB.InsertTaskNotificationRow(ctx, n.ID, s.AgentID, n.TaskID, b)
}

func (s *TaskStore) public(ctx context.Context, t *bg.Task) error {
	return s.Store.DB.InsertTaskPublicEvent(ctx, t.Spec.ID, s.AgentID, string(t.Status))
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

func (s *TaskStore) Get(ctx context.Context, id string) (*bg.Task, error) {
	var t *bg.Task
	err := s.withFencedTx(ctx, func(txCtx context.Context) error {
		var loadErr error
		t, _, _, loadErr = s.load(txCtx, id)
		return loadErr
	})
	return t, err
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

func (s *TaskStore) WaitForTaskVersion(ctx context.Context, r *bg.WaitForTaskVersionRequest) (*bg.Task, error) {
	if r == nil {
		return nil, errors.New("nil wait version")
	}
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		t, err := s.Get(ctx, r.TaskID)
		if err != nil {
			return nil, err
		}
		if t.Version > r.AfterVersion {
			return t, nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-ticker.C:
		}
	}
}

type pendingCursor struct {
	Agent  string
	Status bg.Status
	Keys   []string
	Last   string
}

func encodeCursor(v any) string {
	b, _ := json.Marshal(v)
	return base64.RawURLEncoding.EncodeToString(b)
}

func decodeCursor(v string, out any) error {
	b, e := base64.RawURLEncoding.DecodeString(v)
	if e != nil || json.Unmarshal(b, out) != nil {
		return bg.ErrInvalidCursor
	}
	return nil
}

func (s *TaskStore) list(ctx context.Context, keys []string, cursor string, limit int, status bg.Status) ([]*bg.Task, string, error) {
	if len(keys) == 0 || slices.Contains(keys, "") {
		return nil, "", errors.New("executor keys required")
	}
	keys = slices.Clone(keys)
	sort.Strings(keys)
	keys = slices.Compact(keys)
	c := pendingCursor{Agent: s.AgentID, Status: status, Keys: keys}
	if cursor != "" {
		if err := decodeCursor(cursor, &c); err != nil {
			return nil, "", err
		}
		if c.Agent != s.AgentID || c.Status != status || !slices.Equal(c.Keys, keys) || c.Last == "" {
			return nil, "", bg.ErrInvalidCursor
		}
	}
	if limit <= 0 {
		limit = 100
	}
	if limit > 1000 {
		limit = 1000
	}
	ids, err := s.Store.DB.ListTaskIDsAfter(ctx, s.AgentID, c.Last)
	if err != nil {
		return nil, "", err
	}
	tasks := []*bg.Task{}
	next := ""
	for _, id := range ids {
		t, e := s.Get(ctx, id)
		if e != nil {
			return nil, "", e
		}
		if t.Status != status || !slices.Contains(keys, t.Spec.ExecutorKey) {
			continue
		}
		if len(tasks) == limit {
			c.Last = tasks[len(tasks)-1].Spec.ID
			next = encodeCursor(c)
			break
		}
		tasks = append(tasks, t)
	}
	return tasks, next, nil
}

func (s *TaskStore) ListPending(ctx context.Context, r *bg.ListPendingRequest) (*bg.ListPendingResult, error) {
	if r == nil {
		return nil, errors.New("nil pending")
	}
	t, c, e := s.list(ctx, r.ExecutorKeys, r.Cursor, r.Limit, bg.StatusPending)
	return &bg.ListPendingResult{Tasks: t, NextCursor: c}, e
}

func (s *TaskStore) ListSuspended(ctx context.Context, r *bg.ListSuspendedRequest) (*bg.ListSuspendedResult, error) {
	if r == nil {
		return nil, errors.New("nil suspended")
	}
	t, c, e := s.list(ctx, r.ExecutorKeys, r.Cursor, r.Limit, bg.StatusSuspended)
	return &bg.ListSuspendedResult{Tasks: t, NextCursor: c}, e
}

func (s *TaskStore) authorizeOutput(t *bg.Task, u *time.Time, n time.Time, attempt int64) error {
	if t.Status != bg.StatusRunning || t.CancelRequestedAt != nil || t.Attempt != attempt || u == nil || !n.Before(*u) {
		return bg.ErrLeaseLost
	}
	return nil
}

func (s *TaskStore) AppendTaskEvent(ctx context.Context, r *bg.AppendTaskEventRequest) (*bg.AppendTaskEventResult, error) {
	if r == nil || r.EventID == "" || len(r.EventID) > 1024 {
		return nil, errors.New("event id required")
	}
	if err := taskBound(r.Data); err != nil {
		return nil, err
	}
	var out *bg.AppendTaskEventResult
	var opErr error
	if err := s.withFencedTx(ctx, func(txCtx context.Context) error {
		t, u, n, err := s.load(txCtx, r.TaskID)
		if err == nil {
			err = s.authorizeOutput(t, u, n, r.Attempt)
		}
		if err != nil {
			opErr = err
			return nil
		}
		inserted, stored, createdAt, err := s.Store.DB.UpsertTaskEventRow(txCtx, r.TaskID, r.EventID, r.Data)
		if err != nil {
			return err
		}
		if !bytes.Equal(r.Data, stored) {
			return bg.ErrTaskEventIDConflict
		}
		out = &bg.AppendTaskEventResult{
			Inserted: inserted,
			Event: &bg.TaskEvent{
				TaskID:    r.TaskID,
				EventID:   r.EventID,
				Data:      stored,
				CreatedAt: createdAt,
			},
		}
		return nil
	}); err != nil {
		return nil, err
	}
	if opErr != nil {
		return nil, opErr
	}
	return out, nil
}

type progressCursor struct {
	Agent, Task   string
	Newest        bool
	End, Position int64
}

func (s *TaskStore) ListTaskEvents(ctx context.Context, r *bg.ListTaskEventsRequest) (*bg.ListTaskEventsResult, error) {
	if r == nil {
		return nil, errors.New("nil task events")
	}
	if _, err := s.Get(ctx, r.TaskID); err != nil {
		return nil, err
	}
	c := progressCursor{Agent: s.AgentID, Task: r.TaskID, Newest: r.NewestFirst}
	if r.Cursor != "" {
		if err := decodeCursor(r.Cursor, &c); err != nil {
			return nil, err
		}
		if c.Agent != s.AgentID || c.Task != r.TaskID || c.Newest != r.NewestFirst || c.End <= 0 || c.Position <= 0 || c.Position > c.End {
			return nil, bg.ErrInvalidCursor
		}
	} else {
		var err error
		if c.End, err = s.Store.DB.MaxTaskEventSeq(ctx, r.TaskID); err != nil {
			return nil, err
		}
		if c.Newest {
			c.Position = c.End + 1
		}
	}
	limit := r.Limit
	if limit <= 0 {
		limit = 100
	}
	if limit > 1000 {
		limit = 1000
	}
	rows, err := s.Store.DB.ListTaskEventRows(ctx, r.TaskID, c.End, c.Position, c.Newest, limit+1)
	if err != nil {
		return nil, err
	}
	out := &bg.ListTaskEventsResult{Events: []*bg.TaskEvent{}}
	for _, row := range rows {
		if len(out.Events) == limit {
			out.NextCursor = encodeCursor(c)
			break
		}
		out.Events = append(out.Events, &bg.TaskEvent{
			TaskID:    r.TaskID,
			EventID:   row.EventID,
			Data:      row.Data,
			CreatedAt: row.CreatedAt,
		})
		c.Position = row.Seq
	}
	return out, nil
}

func (s *TaskStore) EnqueueTaskNotification(ctx context.Context, id string, attempt int64, r *bg.NotifyParentRequest) error {
	if r == nil || r.EventID == "" || len(r.EventID) > 1024 || r.Kind == "" || len(r.Kind) > 64 || len(r.Data) > 256<<10 || strings.HasPrefix(string(r.Kind), "eino.") || slices.Contains([]bg.NotificationKind{bg.NotificationTaskCreated, bg.NotificationWaitingInput, bg.NotificationCompleted, bg.NotificationFailed, bg.NotificationCanceled}, r.Kind) {
		return errors.New("invalid task notification")
	}
	var opErr error
	if err := s.withFencedTx(ctx, func(txCtx context.Context) error {
		t, u, n, err := s.load(txCtx, id)
		if err == nil {
			err = s.authorizeOutput(t, u, n, attempt)
		}
		if err != nil {
			opErr = err
			return nil
		}
		if t.Spec.SessionID == "" {
			return bg.ErrNotificationUnavailable
		}
		inserted, storedKind, storedData, err := s.Store.DB.UpsertCustomNotificationRow(txCtx, id, r.EventID, string(r.Kind), r.Data)
		if err != nil {
			return err
		}
		if !inserted {
			if storedKind != string(r.Kind) || !bytes.Equal(storedData, r.Data) {
				return bg.ErrNotificationEventIDConflict
			}
			return nil
		}
		return s.insertNotification(txCtx, bg.Notification{ID: "custom:" + domain.Digest([]byte(id+"\x00"+r.EventID)), TaskID: id, SessionID: t.Spec.SessionID, Version: t.Version, Kind: r.Kind, Data: r.Data, CreatedAt: n})
	}); err != nil {
		return err
	}
	return opErr
}

func (s *TaskStore) Receive(ctx context.Context, r *bg.ReceiveNotificationsRequest) (*bg.ReceiveNotificationsResult, error) {
	if r == nil || r.LeaseDuration <= 0 {
		return nil, errors.New("positive notification lease required")
	}
	limit := r.Limit
	if limit <= 0 {
		limit = 100
	}
	if limit > 1000 {
		limit = 1000
	}
	var out *bg.ReceiveNotificationsResult
	if err := s.withFencedTx(ctx, func(txCtx context.Context) error {
		rows, err := s.Store.DB.ClaimNotificationRowsForUpdate(txCtx, s.AgentID, limit, r.LeaseDuration.Seconds())
		if err != nil {
			return err
		}
		res := &bg.ReceiveNotificationsResult{Deliveries: make([]bg.NotificationDelivery, 0, len(rows))}
		for _, row := range rows {
			var n bg.Notification
			if err = json.Unmarshal(row.Snapshot, &n); err != nil {
				return err
			}
			res.Deliveries = append(res.Deliveries, bg.NotificationDelivery{Record: n, Receipt: bg.NotificationReceipt(row.Receipt)})
		}
		out = res
		return nil
	}); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *TaskStore) Ack(ctx context.Context, r bg.NotificationReceipt) error {
	acked, err := s.Store.DB.AckNotificationRow(ctx, s.AgentID, []byte(r))
	if err == nil && !acked {
		return bg.ErrLeaseLost
	}
	return err
}

func (s *TaskStore) PersistNotificationInput(
	ctx context.Context,
	agentID, taskID, notificationID, status, createdAt string,
	extractSources func(rawInputs []byte) ([]InputSource, bool, error),
) (seq, parentRunID string, inserted bool, err error) {
	return s.Store.DB.PersistNotificationInput(ctx, s.Lease, agentID, taskID, notificationID, status, createdAt, extractSources)
}

var _ bg.TaskStore = (*TaskStore)(nil)
var _ bg.TaskEventStore = (*TaskStore)(nil)
var _ bg.NotificationWriter = (*TaskStore)(nil)
var _ bg.NotificationOutbox = (*TaskStore)(nil)
