package pg

import (
	"context"
	"encoding/json"
	"time"

	"teamie/pkg/postgres"
	"teamie/services/agent-runtime/internal/domain"
)

func utcTimePtr(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	v := t.UTC()
	return &v
}

// GetAgentRuntimeSummary queries the persisted runtime counters, active run, and agent state row.
func (r *Repository) GetAgentRuntimeSummary(ctx context.Context, agentID string) (domain.AgentRuntimeSummary, error) {
	var out domain.AgentRuntimeSummary
	conn := r.db.Conn(ctx)
	err := conn.QueryRow(ctx, `SELECT (SELECT count(*) FROM runtime_inputs WHERE agent_id=$1 AND state='pending'),(SELECT count(*) FROM runtime_tasks WHERE agent_id=$1 AND snapshot->>'Status' IN ('pending','running'))`, agentID).Scan(&out.PendingInputs, &out.RunningTasks)
	if err != nil {
		return out, err
	}
	var id, status string
	if conn.QueryRow(ctx, `SELECT id::text,state FROM runtime_runs WHERE agent_id=$1 AND state IN ('running','waiting_input','retry','waiting_configuration','queued') ORDER BY started_at DESC LIMIT 1`, agentID).Scan(&id, &status) == nil {
		out.ActiveRunID = id
		out.ActiveStatus = status
	}
	var currentState, lastError string
	var lastActive *time.Time
	if conn.QueryRow(ctx, `SELECT state,last_error,last_active_at FROM runtime_agents WHERE agent_id=$1`, agentID).Scan(&currentState, &lastError, &lastActive) == nil {
		out.HasAgentRow = true
		out.CurrentState = currentState
		out.LastError = lastError
		out.LastActiveAt = utcTimePtr(lastActive)
	}
	return out, nil
}

// ListRuns queries runtime_runs for an Agent with optional resourceID or pagination cursor.
func (r *Repository) ListRuns(ctx context.Context, agentID, resourceID, cursor string, limit int) ([]domain.PublicRun, error) {
	rows, err := r.db.Conn(ctx).Query(ctx, `SELECT id::text,state,jsonb_array_length(input_json),started_at,updated_at,finished_at,safe_error FROM runtime_runs WHERE agent_id=$1 AND ($2='' OR id::text=$2) AND ($3='' OR id::text>$3) ORDER BY id LIMIT $4`, agentID, resourceID, cursor, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.PublicRun{}
	for rows.Next() {
		var v domain.PublicRun
		if err = rows.Scan(&v.ID, &v.Status, &v.InputCount, &v.CreatedAt, &v.UpdatedAt, &v.FinishedAt, &v.ErrorCode); err != nil {
			return nil, err
		}
		v.CreatedAt = v.CreatedAt.UTC()
		v.UpdatedAt = v.UpdatedAt.UTC()
		v.FinishedAt = utcTimePtr(v.FinishedAt)
		v.StartedAt = v.CreatedAt
		if v.ErrorCode != "" {
			v.ErrorCode = domain.PublicErrorCode(v.ErrorCode)
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

type taskSnapshotProjection struct {
	Status     string     `json:"Status"`
	CreatedAt  time.Time  `json:"CreatedAt"`
	UpdatedAt  time.Time  `json:"UpdatedAt"`
	DoneAt     *time.Time `json:"DoneAt"`
	ResultData []byte     `json:"ResultData"`
}

// ListTasks queries runtime_tasks for an Agent with optional resourceID or pagination cursor.
func (r *Repository) ListTasks(ctx context.Context, agentID, resourceID, cursor string, limit int) ([]domain.PublicTask, error) {
	rows, err := r.db.Conn(ctx).Query(ctx, `SELECT id,run_id::text,snapshot FROM runtime_tasks WHERE agent_id=$1 AND ($2='' OR id=$2) AND id>$3 ORDER BY id LIMIT $4`, agentID, resourceID, cursor, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.PublicTask{}
	for rows.Next() {
		var id string
		var run *string
		var b []byte
		if err = rows.Scan(&id, &run, &b); err != nil {
			return nil, err
		}
		var task taskSnapshotProjection
		if err = json.Unmarshal(b, &task); err != nil {
			return nil, err
		}
		v := domain.PublicTask{
			ID:          id,
			RunID:       run,
			Kind:        "shell",
			Description: "Workspace command",
			Status:      task.Status,
			CreatedAt:   task.CreatedAt.UTC(),
			UpdatedAt:   task.UpdatedAt.UTC(),
			FinishedAt:  utcTimePtr(task.DoneAt),
		}
		if task.Status == "completed" {
			var result struct {
				ExitCode *int32 `json:"exitCode"`
			}
			if json.Unmarshal(task.ResultData, &result) == nil {
				v.ExitCode = result.ExitCode
			}
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// ListPublicEvents queries runtime_public_events for an Agent after afterID.
func (r *Repository) ListPublicEvents(ctx context.Context, agentID string, afterID int64, limit int) ([]domain.PublicEvent, error) {
	rows, err := r.db.Conn(ctx).Query(ctx, `SELECT id::text,type,run_id::text,task_id,tool_name,status,created_at,error_code FROM runtime_public_events WHERE agent_id=$1 AND id>$2 ORDER BY id LIMIT $3`, agentID, afterID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.PublicEvent{}
	for rows.Next() {
		var v domain.PublicEvent
		if err = rows.Scan(&v.ID, &v.Type, &v.RunID, &v.TaskID, &v.ToolName, &v.Status, &v.CreatedAt, &v.ErrorCode); err != nil {
			return nil, err
		}
		v.CreatedAt = v.CreatedAt.UTC()
		if v.ErrorCode != nil {
			code := domain.PublicErrorCode(*v.ErrorCode)
			v.ErrorCode = &code
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// RequestRunCancel atomically marks a cancelable run as cancel_requested/canceled in PostgreSQL.
func (r *Repository) RequestRunCancel(ctx context.Context, agentID, runID string) (int32, string, error) {
	var (
		statusCode int32
		errorCode  string
	)
	err := r.WithinTx(ctx, func(txCtx context.Context) error {
		conn := r.db.Conn(txCtx)
		var state string
		err := conn.QueryRow(txCtx, `SELECT state FROM runtime_runs WHERE agent_id=$1 AND id=$2 FOR UPDATE`, agentID, runID).Scan(&state)
		if postgres.IsNoRows(err) {
			statusCode, errorCode = 404, "not_found"
			return nil
		}
		if err != nil {
			return err
		}
		if !domain.CanCancelRun(state) {
			statusCode, errorCode = 409, "run_not_cancelable"
			return nil
		}
		if _, err = conn.Exec(txCtx, `UPDATE runtime_runs SET cancel_requested=true,state=CASE WHEN state='running' THEN state ELSE 'canceled' END,updated_at=now(),finished_at=CASE WHEN state='running' THEN finished_at ELSE now() END WHERE id=$1`, runID); err != nil {
			return err
		}
		if state != "running" {
			if _, err = conn.Exec(txCtx, `UPDATE runtime_inputs SET state='failed' WHERE agent_id=$1 AND run_id=$2`, agentID, runID); err != nil {
				return err
			}
		}
		_, err = conn.Exec(txCtx, `INSERT INTO runtime_public_events(agent_id,type,run_id,status) VALUES($1,'run.cancel_requested',$2,'canceled')`, agentID, runID)
		return err
	})
	return statusCode, errorCode, err
}

// RequestRunRetry atomically transitions a retryable run back to 'retry' in PostgreSQL.
func (r *Repository) RequestRunRetry(ctx context.Context, agentID, runID string) (int32, string, error) {
	var (
		statusCode int32
		errorCode  string
	)
	err := r.WithinTx(ctx, func(txCtx context.Context) error {
		conn := r.db.Conn(txCtx)
		var state string
		err := conn.QueryRow(txCtx, `SELECT state FROM runtime_runs WHERE agent_id=$1 AND id=$2 FOR UPDATE`, agentID, runID).Scan(&state)
		if postgres.IsNoRows(err) {
			statusCode, errorCode = 404, "not_found"
			return nil
		}
		if err != nil {
			return err
		}
		if !domain.CanRetryRun(state) {
			statusCode, errorCode = 409, "run_not_retryable"
			return nil
		}
		if _, err = conn.Exec(txCtx, `UPDATE runtime_runs SET state='retry',cancel_requested=false,next_attempt=now(),finished_at=NULL,updated_at=now(),safe_error='' WHERE id=$1`, runID); err != nil {
			return err
		}
		tag, err := conn.Exec(txCtx, `UPDATE runtime_inputs SET state='processing' WHERE agent_id=$1 AND run_id=$2`, agentID, runID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			statusCode, errorCode = 409, "run_not_retryable"
			return domain.ErrEmptyRun
		}
		_, err = conn.Exec(txCtx, `INSERT INTO runtime_public_events(agent_id,type,run_id,status) VALUES($1,'run.retry_requested',$2,'retry')`, agentID, runID)
		return err
	})
	if err == domain.ErrEmptyRun {
		return statusCode, errorCode, nil
	}
	return statusCode, errorCode, err
}
