package pg

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
	"github.com/google/uuid"
	"teamie/pkg/postgres"
	"teamie/services/agent-runtime/internal/domain"
)

// TaskStore implements Eino's durable backgroundtask.TaskStore protocol in PostgreSQL.
type TaskStore struct {
	Repo          *Repository
	AgentID       string
	Lease         *domain.Lease
	LeaseDuration time.Duration
	AllowSubmit   func(context.Context) error
	CurrentRunID  func(context.Context) *string
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

// withFencedTx runs fn inside a database transaction fenced by s.Lease if present.
func (s *TaskStore) withFencedTx(ctx context.Context, fn func(txCtx context.Context) error) error {
	return s.Repo.WithinTx(ctx, func(txCtx context.Context) error {
		if s.Lease != nil {
			if err := s.Repo.CheckLeaseForUpdate(txCtx, s.Lease); err != nil {
				return err
			}
		}
		return fn(txCtx)
	})
}

func (s *TaskStore) save(ctx context.Context, t *bg.Task, until *time.Time) error {
	b, err := json.Marshal(t)
	if err != nil {
		return err
	}
	_, err = s.Repo.db.Conn(ctx).Exec(ctx, `UPDATE runtime_tasks SET snapshot=$3,lease_until=$4 WHERE id=$1 AND agent_id=$2`, t.Spec.ID, s.AgentID, b, until)
	return err
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
	_, err = s.Repo.db.Conn(ctx).Exec(ctx, `INSERT INTO runtime_task_notifications(id,agent_id,task_id,snapshot) VALUES($1,$2,$3,$4) ON CONFLICT DO NOTHING`, n.ID, s.AgentID, n.TaskID, b)
	return err
}

func (s *TaskStore) public(ctx context.Context, t *bg.Task) error {
	_, err := s.Repo.db.Conn(ctx).Exec(ctx, `INSERT INTO runtime_public_events(agent_id,type,run_id,task_id,status) SELECT agent_id,'task.status',run_id,id,$3 FROM runtime_tasks WHERE id=$1 AND agent_id=$2`, t.Spec.ID, s.AgentID, string(t.Status))
	return err
}

func (s *TaskStore) load(ctx context.Context, id string) (*bg.Task, *time.Time, time.Time, error) {
	var b []byte
	var until *time.Time
	var now time.Time
	err := s.Repo.db.Conn(ctx).QueryRow(ctx, `SELECT snapshot,lease_until,clock_timestamp() FROM runtime_tasks WHERE id=$1 AND agent_id=$2 FOR UPDATE`, id, s.AgentID).Scan(&b, &until, &now)
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
		conn := s.Repo.db.Conn(txCtx)
		var now time.Time
		if err := conn.QueryRow(txCtx, `SELECT clock_timestamp()`).Scan(&now); err != nil {
			return err
		}
		t := &bg.Task{Spec: r.Spec, LeaseExpiryPolicy: r.LeaseExpiryPolicy, Checkpoint: r.Checkpoint, ContextSnapshot: r.ContextSnapshot, Status: bg.StatusPending, Version: 1, CreatedAt: now, UpdatedAt: now}
		var err error
		b, err = json.Marshal(t)
		if err != nil {
			return err
		}
		var runID *string
		if s.CurrentRunID != nil {
			runID = s.CurrentRunID(txCtx)
		}
		tag, err := conn.Exec(txCtx, `INSERT INTO runtime_tasks(id,agent_id,run_id,snapshot) VALUES($1,$2,$3,$4) ON CONFLICT DO NOTHING`, r.Spec.ID, s.AgentID, runID, b)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
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
	rows, err := s.Repo.Query(ctx, `SELECT id FROM runtime_tasks WHERE agent_id=$1 AND id>$2 ORDER BY id`, s.AgentID, c.Last)
	if err != nil {
		return nil, "", err
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			break
		}
		ids = append(ids, id)
	}
	rows.Close()
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

func nonnilBytes(b []byte) []byte {
	if b == nil {
		return []byte{}
	}
	return b
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
		conn := s.Repo.db.Conn(txCtx)
		t, u, n, err := s.load(txCtx, r.TaskID)
		if err == nil {
			err = s.authorizeOutput(t, u, n, r.Attempt)
		}
		if err != nil {
			opErr = err
			return nil
		}
		tag, err := conn.Exec(txCtx, `INSERT INTO runtime_task_events(task_id,event_id,payload) VALUES($1,$2,$3) ON CONFLICT DO NOTHING`, r.TaskID, r.EventID, nonnilBytes(r.Data))
		if err != nil {
			return err
		}
		res := &bg.AppendTaskEventResult{Inserted: tag.RowsAffected() == 1, Event: &bg.TaskEvent{TaskID: r.TaskID, EventID: r.EventID}}
		if err = conn.QueryRow(txCtx, `SELECT payload,created_at FROM runtime_task_events WHERE task_id=$1 AND event_id=$2`, r.TaskID, r.EventID).Scan(&res.Event.Data, &res.Event.CreatedAt); err != nil {
			return err
		}
		if !bytes.Equal(r.Data, res.Event.Data) {
			return bg.ErrTaskEventIDConflict
		}
		out = res
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
		if err := s.Repo.QueryRow(ctx, `SELECT COALESCE(max(seq),0) FROM runtime_task_events WHERE task_id=$1`, r.TaskID).Scan(&c.End); err != nil {
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
	cmp, order := ">", "ASC"
	if c.Newest {
		cmp, order = "<", "DESC"
	}
	rows, err := s.Repo.Query(ctx, `SELECT seq,event_id,payload,created_at FROM runtime_task_events WHERE task_id=$1 AND seq<=$2 AND seq `+cmp+` $3 ORDER BY seq `+order+` LIMIT $4`, r.TaskID, c.End, c.Position, limit+1)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := &bg.ListTaskEventsResult{Events: []*bg.TaskEvent{}}
	for rows.Next() {
		var seq int64
		e := &bg.TaskEvent{TaskID: r.TaskID}
		if err = rows.Scan(&seq, &e.EventID, &e.Data, &e.CreatedAt); err != nil {
			return nil, err
		}
		if len(out.Events) == limit {
			out.NextCursor = encodeCursor(c)
			break
		}
		out.Events = append(out.Events, e)
		c.Position = seq
	}
	return out, rows.Err()
}

func (s *TaskStore) EnqueueTaskNotification(ctx context.Context, id string, attempt int64, r *bg.NotifyParentRequest) error {
	if r == nil || r.EventID == "" || len(r.EventID) > 1024 || r.Kind == "" || len(r.Kind) > 64 || len(r.Data) > 256<<10 || strings.HasPrefix(string(r.Kind), "eino.") || slices.Contains([]bg.NotificationKind{bg.NotificationTaskCreated, bg.NotificationWaitingInput, bg.NotificationCompleted, bg.NotificationFailed, bg.NotificationCanceled}, r.Kind) {
		return errors.New("invalid task notification")
	}
	var opErr error
	if err := s.withFencedTx(ctx, func(txCtx context.Context) error {
		conn := s.Repo.db.Conn(txCtx)
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
		tag, err := conn.Exec(txCtx, `INSERT INTO runtime_custom_notifications(task_id,event_id,kind,data) VALUES($1,$2,$3,$4) ON CONFLICT DO NOTHING`, id, r.EventID, string(r.Kind), nonnilBytes(r.Data))
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			var k string
			var b []byte
			if err = conn.QueryRow(txCtx, `SELECT kind,data FROM runtime_custom_notifications WHERE task_id=$1 AND event_id=$2`, id, r.EventID).Scan(&k, &b); err != nil {
				return err
			}
			if k != string(r.Kind) || !bytes.Equal(b, r.Data) {
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
		conn := s.Repo.db.Conn(txCtx)
		rows, err := conn.Query(txCtx, `SELECT id,snapshot FROM runtime_task_notifications WHERE agent_id=$1 AND NOT acked AND (lease_until IS NULL OR lease_until<=clock_timestamp()) ORDER BY seq FOR UPDATE SKIP LOCKED LIMIT $2`, s.AgentID, limit)
		if err != nil {
			return err
		}
		res := &bg.ReceiveNotificationsResult{Deliveries: []bg.NotificationDelivery{}}
		for rows.Next() {
			var id string
			var b []byte
			if err = rows.Scan(&id, &b); err != nil {
				break
			}
			var n bg.Notification
			if err = json.Unmarshal(b, &n); err != nil {
				break
			}
			res.Deliveries = append(res.Deliveries, bg.NotificationDelivery{Record: n, Receipt: []byte(uuid.NewString())})
		}
		rows.Close()
		if err != nil {
			return err
		}
		for _, d := range res.Deliveries {
			if _, err = conn.Exec(txCtx, `UPDATE runtime_task_notifications SET receipt=$2,lease_until=clock_timestamp()+$3*interval '1 second' WHERE id=$1`, d.Record.ID, []byte(d.Receipt), r.LeaseDuration.Seconds()); err != nil {
				return err
			}
		}
		out = res
		return nil
	}); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *TaskStore) Ack(ctx context.Context, r bg.NotificationReceipt) error {
	tag, err := s.Repo.Exec(ctx, `UPDATE runtime_task_notifications SET acked=true WHERE agent_id=$1 AND receipt=$2 AND NOT acked AND lease_until>clock_timestamp()`, s.AgentID, []byte(r))
	if err == nil && tag.RowsAffected() != 1 {
		return bg.ErrLeaseLost
	}
	return err
}

// PersistNotificationInput atomically allocates a notification event sequence, resolves parent task origin,
// and inserts the task.notification input into runtime_inputs.
func (s *TaskStore) PersistNotificationInput(
	ctx context.Context,
	agentID, taskID, notificationID, status, createdAt string,
	extractSources func(rawInputs []byte) ([]domain.InputSource, bool, error),
) (seq, parentRunID string, inserted bool, err error) {
	err = s.withFencedTx(ctx, func(txCtx context.Context) error {
		conn := s.Repo.db.Conn(txCtx)
		var raw []byte
		if qErr := conn.QueryRow(txCtx, `SELECT (90000000000000000000::numeric+nextval('runtime_notification_input_seq'))::text`).Scan(&seq); qErr != nil {
			return qErr
		}
		if qErr := conn.QueryRow(txCtx, `SELECT COALESCE(t.run_id::text,''),COALESCE(r.input_json,'[]'::jsonb)
FROM runtime_tasks t LEFT JOIN runtime_runs r ON r.id=t.run_id AND r.agent_id=t.agent_id
WHERE t.agent_id=$1 AND t.id=$2`, agentID, taskID).Scan(&parentRunID, &raw); qErr != nil {
			return qErr
		}
		refs, overflow, qErr := extractSources(raw)
		if qErr != nil {
			return qErr
		}
		in := domain.Input{
			EventID:        seq,
			Kind:           "task.notification",
			TaskID:         taskID,
			NotificationID: notificationID,
			Status:         status,
			CreatedAt:      createdAt,
			ParentRunID:    parentRunID,
			SourceRefs:     refs,
			SourceOverflow: overflow,
		}
		b, _ := json.Marshal(in)
		tag, insertErr := conn.Exec(txCtx, `INSERT INTO runtime_inputs(agent_id,event_id,payload,notification_id) VALUES($1,$2,$3,$4) ON CONFLICT DO NOTHING`, agentID, seq, b, notificationID)
		if insertErr != nil {
			return insertErr
		}
		inserted = tag.RowsAffected() > 0
		return nil
	})
	if err != nil {
		return "", "", false, err
	}
	return seq, parentRunID, inserted, nil
}

// ListIncompleteAgentTaskIDs returns IDs of non-completed tasks for agentID.
func (r *Repository) ListIncompleteAgentTaskIDs(ctx context.Context, agentID string) ([]string, error) {
	rows, err := r.db.Conn(ctx).Query(ctx, `SELECT id FROM runtime_tasks WHERE agent_id=$1 AND snapshot->>'Status'<>'completed'`, agentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := []string{}
	for rows.Next() {
		var taskID string
		if rows.Scan(&taskID) == nil {
			ids = append(ids, taskID)
		}
	}
	return ids, nil
}

// ListIncompleteRunTaskIDs returns IDs of non-completed tasks for (agentID, runID).
func (r *Repository) ListIncompleteRunTaskIDs(ctx context.Context, agentID, runID string) ([]string, error) {
	rows, err := r.db.Conn(ctx).Query(ctx, `SELECT id FROM runtime_tasks WHERE agent_id=$1 AND run_id=$2 AND snapshot->>'Status'<>'completed'`, agentID, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := []string{}
	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil {
			ids = append(ids, id)
		}
	}
	return ids, nil
}
