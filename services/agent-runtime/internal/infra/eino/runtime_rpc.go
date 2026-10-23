package eino

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	bg "github.com/cloudwego/eino/adk/backgroundtask"
	"github.com/jackc/pgx/v5"
	wire "teamie/contracts/gen/agent/kitex_gen/agent"
	"teamie/services/agent-runtime/internal/domain"
)

type RuntimeRPC struct{ Engine *Engine }
type RPC = RuntimeRPC

func runtimeOK(v any) (*wire.RuntimeResponse, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return runtimeFail(503, "runtime_unavailable")
	}
	s := string(b)
	return &wire.RuntimeResponse{Ok: true, DataJson: &s}, nil
}

func runtimeFail(status int32, code string) (*wire.RuntimeResponse, error) {
	message := code
	return &wire.RuntimeResponse{StatusCode: &status, ErrorCode: &code, ErrorMessage: &message}, nil
}
func (r *RuntimeRPC) authorize(ctx context.Context, q *wire.RuntimeRequest, write bool) (*wire.AgentDefinition, *wire.RuntimeResponse) {
	if q == nil || !domain.ValidServiceKey(r.Engine.Config.ControlToken, q.ServiceToken) {
		v, _ := runtimeFail(403, "forbidden")
		return nil, v
	}
	if !validUUID(q.AgentId) || !validUUID(q.OwnerId) {
		v, _ := runtimeFail(404, "not_found")
		return nil, v
	}
	pc, c := context.WithTimeout(ctx, 4*time.Second)
	defer c()
	result, err := r.Engine.Control.GetAgent(pc, &wire.AgentRequest{ServiceToken: r.Engine.Config.ControlToken, AgentId: q.AgentId})
	if err != nil {
		v, _ := runtimeFail(503, "control_unavailable")
		return nil, v
	}
	if result.Agent == nil || result.Agent.OwnerId != q.OwnerId {
		v, _ := runtimeFail(404, "not_found")
		return nil, v
	}
	if write && (!result.Agent.Enabled || result.Agent.DeletedAt != nil) {
		v, _ := runtimeFail(409, "agent_disabled")
		return nil, v
	}
	return result.Agent, nil
}
func (r *RuntimeRPC) State(ctx context.Context, q *wire.RuntimeRequest) (*wire.RuntimeResponse, error) {
	def, fail := r.authorize(ctx, q, false)
	if fail != nil {
		return fail, nil
	}
	out := map[string]any{"state": "idle", "sessionId": "agent:" + q.AgentId, "pendingInputs": 0, "runningTasks": 0}
	if !def.Enabled || def.DeletedAt != nil {
		out["state"] = "disabled"
	}
	var pending, tasks int
	err := r.Engine.Store.DB.QueryRow(ctx, `SELECT (SELECT count(*) FROM runtime_inputs WHERE agent_id=$1 AND state='pending'),(SELECT count(*) FROM runtime_tasks WHERE agent_id=$1 AND snapshot->>'Status' IN ('pending','running'))`, q.AgentId).Scan(&pending, &tasks)
	if err != nil {
		return runtimeFail(503, "runtime_unavailable")
	}
	out["pendingInputs"] = pending
	out["runningTasks"] = tasks
	var id, status string
	err = r.Engine.Store.DB.QueryRow(ctx, `SELECT id::text,state FROM runtime_runs WHERE agent_id=$1 AND state IN ('running','waiting_input','retry','waiting_configuration','queued') ORDER BY started_at DESC LIMIT 1`, q.AgentId).Scan(&id, &status)
	if err == nil {
		out["activeRunId"] = id
		if def.Enabled && def.DeletedAt == nil {
			out["state"] = status
		}
	}
	var currentState, lastError string
	var lastActive *time.Time
	if r.Engine.Store.DB.QueryRow(ctx, `SELECT state,last_error,last_active_at FROM runtime_agents WHERE agent_id=$1`, q.AgentId).Scan(&currentState, &lastError, &lastActive) == nil {
		if def.Enabled && def.DeletedAt == nil {
			if _, active := out["activeRunId"]; !active {
				out["state"] = currentState
			}
		}
		if lastActive != nil {
			out["lastActiveAt"] = lastActive.UTC()
		}
		if lastError != "" {
			out["lastError"] = publicErrorCode(lastError)
		}
	}
	return runtimeOK(out)
}

type publicRun struct {
	ID         string     `json:"id"`
	Status     string     `json:"status"`
	InputCount int        `json:"inputCount"`
	CreatedAt  time.Time  `json:"createdAt"`
	UpdatedAt  time.Time  `json:"updatedAt"`
	StartedAt  time.Time  `json:"startedAt"`
	FinishedAt *time.Time `json:"finishedAt,omitempty"`
	ErrorCode  string     `json:"errorCode,omitempty"`
}
type publicTask struct {
	ID          string     `json:"id"`
	RunID       *string    `json:"runId,omitempty"`
	Kind        string     `json:"kind"`
	Description string     `json:"description"`
	Status      bg.Status  `json:"status"`
	CreatedAt   time.Time  `json:"createdAt"`
	UpdatedAt   time.Time  `json:"updatedAt"`
	FinishedAt  *time.Time `json:"finishedAt,omitempty"`
	ExitCode    *int32     `json:"exitCode,omitempty"`
}

func publicErrorCode(code string) string {
	return domain.PublicErrorCode(code)
}
func requestLimit(q *wire.RuntimeRequest) int {
	n := int(q.GetLimit())
	if n <= 0 {
		return 50
	}
	if n > 100 {
		return 100
	}
	return n
}
func (r *RuntimeRPC) runs(ctx context.Context, q *wire.RuntimeRequest, detail bool) (*wire.RuntimeResponse, error) {
	if _, f := r.authorize(ctx, q, false); f != nil {
		return f, nil
	}
	if detail && !validUUID(q.GetResourceId()) {
		return runtimeFail(404, "not_found")
	}
	cursor := q.GetCursor()
	if cursor != "" && !validUUID(cursor) {
		return runtimeFail(400, "invalid_cursor")
	}
	rows, err := r.Engine.Store.DB.Query(ctx, `SELECT id::text,state,jsonb_array_length(input_json),started_at,updated_at,finished_at,safe_error FROM runtime_runs WHERE agent_id=$1 AND ($2='' OR id::text=$2) AND ($3='' OR id::text>$3) ORDER BY id LIMIT $4`, q.AgentId, map[bool]string{true: q.GetResourceId(), false: ""}[detail], cursor, requestLimit(q)+1)
	if err != nil {
		return runtimeFail(503, "runtime_unavailable")
	}
	defer rows.Close()
	out := []publicRun{}
	for rows.Next() {
		var v publicRun
		if err = rows.Scan(&v.ID, &v.Status, &v.InputCount, &v.CreatedAt, &v.UpdatedAt, &v.FinishedAt, &v.ErrorCode); err != nil {
			return runtimeFail(503, "runtime_unavailable")
		}
		v.CreatedAt = v.CreatedAt.UTC()
		v.UpdatedAt = v.UpdatedAt.UTC()
		if v.FinishedAt != nil {
			x := v.FinishedAt.UTC()
			v.FinishedAt = &x
		}
		v.StartedAt = v.CreatedAt
		if v.ErrorCode != "" {
			v.ErrorCode = publicErrorCode(v.ErrorCode)
		}
		out = append(out, v)
	}
	if rows.Err() != nil {
		return runtimeFail(503, "runtime_unavailable")
	}
	if detail {
		if len(out) == 0 {
			return runtimeFail(404, "not_found")
		}
		return runtimeOK(map[string]any{"run": out[0]})
	}
	response := map[string]any{"runs": out}
	if len(out) > requestLimit(q) {
		response["runs"] = out[:requestLimit(q)]
		response["cursor"] = out[requestLimit(q)-1].ID
	}
	return runtimeOK(response)
}
func (r *RuntimeRPC) Runs(ctx context.Context, q *wire.RuntimeRequest) (*wire.RuntimeResponse, error) {
	return r.runs(ctx, q, false)
}
func (r *RuntimeRPC) Run(ctx context.Context, q *wire.RuntimeRequest) (*wire.RuntimeResponse, error) {
	return r.runs(ctx, q, true)
}
func (r *RuntimeRPC) tasks(ctx context.Context, q *wire.RuntimeRequest, detail bool) (*wire.RuntimeResponse, error) {
	if _, f := r.authorize(ctx, q, false); f != nil {
		return f, nil
	}
	if detail && q.GetResourceId() == "" {
		return runtimeFail(404, "not_found")
	}
	if len(q.GetResourceId()) > 200 || len(q.GetCursor()) > 200 {
		return runtimeFail(400, "invalid_cursor")
	}
	rows, err := r.Engine.Store.DB.Query(ctx, `SELECT id,run_id::text,snapshot FROM runtime_tasks WHERE agent_id=$1 AND ($2='' OR id=$2) AND id>$3 ORDER BY id LIMIT $4`, q.AgentId, map[bool]string{true: q.GetResourceId(), false: ""}[detail], q.GetCursor(), requestLimit(q)+1)
	if err != nil {
		return runtimeFail(503, "runtime_unavailable")
	}
	defer rows.Close()
	out := []publicTask{}
	for rows.Next() {
		var id string
		var run *string
		var b []byte
		if err = rows.Scan(&id, &run, &b); err != nil {
			return runtimeFail(503, "runtime_unavailable")
		}
		var task bg.Task
		if json.Unmarshal(b, &task) != nil {
			return runtimeFail(503, "runtime_unavailable")
		}
		v := publicTask{ID: id, RunID: run, Kind: "shell", Description: "Workspace command", Status: task.Status, CreatedAt: task.CreatedAt.UTC(), UpdatedAt: task.UpdatedAt.UTC(), FinishedAt: utcPointer(task.DoneAt)}
		if task.Status == bg.StatusCompleted {
			var result struct {
				ExitCode *int32 `json:"exitCode"`
			}
			if json.Unmarshal(task.ResultData, &result) == nil {
				v.ExitCode = result.ExitCode
			}
		}
		out = append(out, v)
	}
	if rows.Err() != nil {
		return runtimeFail(503, "runtime_unavailable")
	}
	if detail {
		if len(out) == 0 {
			return runtimeFail(404, "not_found")
		}
		return runtimeOK(map[string]any{"task": out[0]})
	}
	response := map[string]any{"tasks": out}
	if len(out) > requestLimit(q) {
		response["tasks"] = out[:requestLimit(q)]
		response["cursor"] = out[requestLimit(q)-1].ID
	}
	return runtimeOK(response)
}
func (r *RuntimeRPC) Tasks(ctx context.Context, q *wire.RuntimeRequest) (*wire.RuntimeResponse, error) {
	return r.tasks(ctx, q, false)
}
func (r *RuntimeRPC) Task(ctx context.Context, q *wire.RuntimeRequest) (*wire.RuntimeResponse, error) {
	return r.tasks(ctx, q, true)
}

type publicEvent struct {
	ID        string    `json:"id"`
	Type      string    `json:"type"`
	RunID     *string   `json:"runId,omitempty"`
	TaskID    *string   `json:"taskId,omitempty"`
	ToolName  *string   `json:"toolName,omitempty"`
	Status    *string   `json:"status,omitempty"`
	CreatedAt time.Time `json:"createdAt"`
	ErrorCode *string   `json:"errorCode,omitempty"`
}

func (r *RuntimeRPC) Events(ctx context.Context, q *wire.RuntimeRequest) (*wire.RuntimeResponse, error) {
	if _, f := r.authorize(ctx, q, false); f != nil {
		return f, nil
	}
	after := int64(0)
	if q.GetCursor() != "" {
		var err error
		after, err = strconv.ParseInt(q.GetCursor(), 10, 64)
		if err != nil || after < 0 {
			return runtimeFail(400, "invalid_cursor")
		}
	}
	rows, err := r.Engine.Store.DB.Query(ctx, `SELECT id::text,type,run_id::text,task_id,tool_name,status,created_at,error_code FROM runtime_public_events WHERE agent_id=$1 AND id>$2 ORDER BY id LIMIT $3`, q.AgentId, after, requestLimit(q)+1)
	if err != nil {
		return runtimeFail(503, "runtime_unavailable")
	}
	defer rows.Close()
	out := []publicEvent{}
	for rows.Next() {
		var v publicEvent
		if err = rows.Scan(&v.ID, &v.Type, &v.RunID, &v.TaskID, &v.ToolName, &v.Status, &v.CreatedAt, &v.ErrorCode); err != nil {
			return runtimeFail(503, "runtime_unavailable")
		}
		v.CreatedAt = v.CreatedAt.UTC()
		if v.ErrorCode != nil {
			code := publicErrorCode(*v.ErrorCode)
			v.ErrorCode = &code
		}
		out = append(out, v)
	}
	if rows.Err() != nil {
		return runtimeFail(503, "runtime_unavailable")
	}
	more := len(out) > requestLimit(q)
	if more {
		out = out[:requestLimit(q)]
	}
	cursor := strconv.FormatInt(after, 10)
	if len(out) > 0 {
		cursor = out[len(out)-1].ID
	}
	return runtimeOK(map[string]any{"events": out, "cursor": cursor, "hasMore": more})
}
func (r *RuntimeRPC) CancelRun(ctx context.Context, q *wire.RuntimeRequest) (*wire.RuntimeResponse, error) {
	if _, f := r.authorize(ctx, q, true); f != nil {
		return f, nil
	}
	if !validUUID(q.GetResourceId()) {
		return runtimeFail(404, "not_found")
	}
	tx, err := r.Engine.Store.DB.Begin(ctx)
	if err != nil {
		return runtimeFail(503, "runtime_unavailable")
	}
	defer tx.Rollback(ctx)
	var state string
	err = tx.QueryRow(ctx, `SELECT state FROM runtime_runs WHERE agent_id=$1 AND id=$2 FOR UPDATE`, q.AgentId, q.GetResourceId()).Scan(&state)
	if errors.Is(err, pgx.ErrNoRows) {
		return runtimeFail(404, "not_found")
	}
	if err != nil {
		return runtimeFail(503, "runtime_unavailable")
	}
	if !domain.CanCancelRun(state) {
		return runtimeFail(409, "run_not_cancelable")
	}
	_, err = tx.Exec(ctx, `UPDATE runtime_runs SET cancel_requested=true,state=CASE WHEN state='running' THEN state ELSE 'canceled' END,updated_at=now(),finished_at=CASE WHEN state='running' THEN finished_at ELSE now() END WHERE id=$1`, q.GetResourceId())
	if err == nil && state != "running" {
		_, err = tx.Exec(ctx, `UPDATE runtime_inputs SET state='failed' WHERE agent_id=$1 AND run_id=$2`, q.AgentId, q.GetResourceId())
	}
	if err == nil {
		_, err = tx.Exec(ctx, `INSERT INTO runtime_public_events(agent_id,type,run_id,status) VALUES($1,'run.cancel_requested',$2,'canceled')`, q.AgentId, q.GetResourceId())
	}
	if err == nil {
		err = tx.Commit(ctx)
	}
	if err != nil {
		return runtimeFail(503, "runtime_unavailable")
	}
	r.Engine.cancelRunTasks(ctx, q.AgentId, q.GetResourceId())
	return r.Run(ctx, q)
}
func (r *RuntimeRPC) RetryRun(ctx context.Context, q *wire.RuntimeRequest) (*wire.RuntimeResponse, error) {
	if _, f := r.authorize(ctx, q, true); f != nil {
		return f, nil
	}
	if !runtimeFlag(r.Engine.Config.FlagsFile, "agents.runtime.v3") {
		return runtimeFail(409, "runtime_disabled")
	}
	if !validUUID(q.GetResourceId()) {
		return runtimeFail(404, "not_found")
	}
	tx, err := r.Engine.Store.DB.Begin(ctx)
	if err != nil {
		return runtimeFail(503, "runtime_unavailable")
	}
	defer tx.Rollback(ctx)
	var state string
	err = tx.QueryRow(ctx, `SELECT state FROM runtime_runs WHERE agent_id=$1 AND id=$2 FOR UPDATE`, q.AgentId, q.GetResourceId()).Scan(&state)
	if errors.Is(err, pgx.ErrNoRows) {
		return runtimeFail(404, "not_found")
	}
	if err != nil {
		return runtimeFail(503, "runtime_unavailable")
	}
	if !domain.CanRetryRun(state) {
		return runtimeFail(409, "run_not_retryable")
	}
	_, err = tx.Exec(ctx, `UPDATE runtime_runs SET state='retry',cancel_requested=false,next_attempt=now(),finished_at=NULL,updated_at=now(),safe_error='' WHERE id=$1`, q.GetResourceId())
	if err == nil {
		tag, inputErr := tx.Exec(ctx, `UPDATE runtime_inputs SET state='processing' WHERE agent_id=$1 AND run_id=$2`, q.AgentId, q.GetResourceId())
		if inputErr != nil {
			return runtimeFail(503, "runtime_unavailable")
		}
		// InvalidateRun deliberately detached unavailable sources. Returning
		// before Commit also rolls back the Run update above; never resurrect
		// its old input batch or publish a retry event with no discoverable work.
		if tag.RowsAffected() == 0 {
			return runtimeFail(409, "run_not_retryable")
		}
	}
	if err == nil {
		_, err = tx.Exec(ctx, `INSERT INTO runtime_public_events(agent_id,type,run_id,status) VALUES($1,'run.retry_requested',$2,'retry')`, q.AgentId, q.GetResourceId())
	}
	if err == nil {
		err = tx.Commit(ctx)
	}
	if err != nil {
		return runtimeFail(503, "runtime_unavailable")
	}
	return r.Run(ctx, q)
}
func (r *RuntimeRPC) CancelTask(ctx context.Context, q *wire.RuntimeRequest) (*wire.RuntimeResponse, error) {
	if _, f := r.authorize(ctx, q, true); f != nil {
		return f, nil
	}
	store := &TaskStore{Store: r.Engine.Store, AgentID: q.AgentId}
	for i := 0; i < 4; i++ {
		task, err := store.Get(ctx, q.GetResourceId())
		if errors.Is(err, bg.ErrNotFound) {
			return runtimeFail(404, "not_found")
		}
		if err != nil {
			return runtimeFail(503, "runtime_unavailable")
		}
		if terminalTask(task.Status) {
			return runtimeFail(409, "task_not_cancelable")
		}
		_, err = store.RequestCancel(ctx, &bg.RequestCancelRequest{TaskID: task.Spec.ID, ExpectedVersion: task.Version, Reason: "owner_cancelled"})
		if errors.Is(err, bg.ErrVersionConflict) {
			continue
		}
		if err != nil {
			return runtimeFail(503, "runtime_unavailable")
		}
		r.Engine.confirmTaskStopped(ctx, store, task.Spec.ID)
		return r.Task(ctx, q)
	}
	return runtimeFail(409, "task_version_conflict")
}

var _ wire.AgentRuntimeService = (*RuntimeRPC)(nil)
var _ domain.RuntimeEngine = (*Engine)(nil)

func toWireRequest(q domain.RuntimeQuery) *wire.RuntimeRequest {
	req := &wire.RuntimeRequest{
		ServiceToken: q.ServiceToken,
		AgentId:      q.AgentID,
		OwnerId:      q.OwnerID,
	}
	if q.ResourceID != "" {
		res := q.ResourceID
		req.ResourceId = &res
	}
	if q.Cursor != "" {
		cur := q.Cursor
		req.Cursor = &cur
	}
	if q.Limit > 0 {
		lim := q.Limit
		req.Limit = &lim
	}
	return req
}

func toDomainResult(resp *wire.RuntimeResponse, err error) (domain.RuntimeResult, error) {
	if err != nil {
		return domain.RuntimeResult{}, err
	}
	if resp == nil {
		return domain.RuntimeResult{StatusCode: 503, ErrorCode: "runtime_unavailable", ErrorMessage: "runtime_unavailable"}, nil
	}
	return domain.RuntimeResult{
		OK:           resp.Ok,
		DataJSON:     resp.GetDataJson(),
		StatusCode:   resp.GetStatusCode(),
		ErrorCode:    resp.GetErrorCode(),
		ErrorMessage: resp.GetErrorMessage(),
	}, nil
}

func (e *Engine) State(ctx context.Context, q domain.RuntimeQuery) (domain.RuntimeResult, error) {
	return toDomainResult((&RuntimeRPC{Engine: e}).State(ctx, toWireRequest(q)))
}

func (e *Engine) Runs(ctx context.Context, q domain.RuntimeQuery) (domain.RuntimeResult, error) {
	return toDomainResult((&RuntimeRPC{Engine: e}).Runs(ctx, toWireRequest(q)))
}

func (e *Engine) RunDetail(ctx context.Context, q domain.RuntimeQuery) (domain.RuntimeResult, error) {
	return toDomainResult((&RuntimeRPC{Engine: e}).Run(ctx, toWireRequest(q)))
}

func (e *Engine) Tasks(ctx context.Context, q domain.RuntimeQuery) (domain.RuntimeResult, error) {
	return toDomainResult((&RuntimeRPC{Engine: e}).Tasks(ctx, toWireRequest(q)))
}

func (e *Engine) TaskDetail(ctx context.Context, q domain.RuntimeQuery) (domain.RuntimeResult, error) {
	return toDomainResult((&RuntimeRPC{Engine: e}).Task(ctx, toWireRequest(q)))
}

func (e *Engine) Events(ctx context.Context, q domain.RuntimeQuery) (domain.RuntimeResult, error) {
	return toDomainResult((&RuntimeRPC{Engine: e}).Events(ctx, toWireRequest(q)))
}

func (e *Engine) CancelRun(ctx context.Context, q domain.RuntimeQuery) (domain.RuntimeResult, error) {
	return toDomainResult((&RuntimeRPC{Engine: e}).CancelRun(ctx, toWireRequest(q)))
}

func (e *Engine) RetryRun(ctx context.Context, q domain.RuntimeQuery) (domain.RuntimeResult, error) {
	return toDomainResult((&RuntimeRPC{Engine: e}).RetryRun(ctx, toWireRequest(q)))
}

func (e *Engine) CancelTask(ctx context.Context, q domain.RuntimeQuery) (domain.RuntimeResult, error) {
	return toDomainResult((&RuntimeRPC{Engine: e}).CancelTask(ctx, toWireRequest(q)))
}

func publicErrorCodeOptional(v string) string {
	return domain.PublicErrorCodeOptional(v)
}

func utcPointer(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	v := t.UTC()
	return &v
}
