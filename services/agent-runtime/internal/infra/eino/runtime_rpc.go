package eino

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	bg "github.com/cloudwego/eino/adk/backgroundtask"
	wire "teamie/contracts/gen/agent/kitex_gen/agent"
	"teamie/services/agent-runtime/internal/domain"
)

type RuntimeRPC struct{ Engine *Engine }
type RPC = RuntimeRPC

type publicRun = domain.PublicRun
type publicTask = domain.PublicTask
type publicEvent = domain.PublicEvent

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
	summary, err := r.Engine.Store.Repo().GetAgentRuntimeSummary(ctx, q.AgentId)
	if err != nil {
		return runtimeFail(503, "runtime_unavailable")
	}
	out["pendingInputs"] = summary.PendingInputs
	out["runningTasks"] = summary.RunningTasks
	if summary.ActiveRunID != "" {
		out["activeRunId"] = summary.ActiveRunID
		if def.Enabled && def.DeletedAt == nil {
			out["state"] = summary.ActiveStatus
		}
	}
	if summary.HasAgentRow {
		if def.Enabled && def.DeletedAt == nil {
			if _, active := out["activeRunId"]; !active {
				out["state"] = summary.CurrentState
			}
		}
		if summary.LastActiveAt != nil {
			out["lastActiveAt"] = summary.LastActiveAt.UTC()
		}
		if summary.LastError != "" {
			out["lastError"] = publicErrorCode(summary.LastError)
		}
	}
	return runtimeOK(out)
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
	resID := ""
	if detail {
		resID = q.GetResourceId()
	}
	out, err := r.Engine.Store.Repo().ListRuns(ctx, q.AgentId, resID, cursor, requestLimit(q)+1)
	if err != nil {
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
	resID := ""
	if detail {
		resID = q.GetResourceId()
	}
	out, err := r.Engine.Store.Repo().ListTasks(ctx, q.AgentId, resID, q.GetCursor(), requestLimit(q)+1)
	if err != nil {
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
	out, err := r.Engine.Store.Repo().ListPublicEvents(ctx, q.AgentId, after, requestLimit(q)+1)
	if err != nil {
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
	statusCode, errorCode, err := r.Engine.Store.Repo().RequestRunCancel(ctx, q.AgentId, q.GetResourceId())
	if err != nil {
		return runtimeFail(503, "runtime_unavailable")
	}
	if statusCode != 0 {
		return runtimeFail(statusCode, errorCode)
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
	statusCode, errorCode, err := r.Engine.Store.Repo().RequestRunRetry(ctx, q.AgentId, q.GetResourceId())
	if err != nil {
		return runtimeFail(503, "runtime_unavailable")
	}
	if statusCode != 0 {
		return runtimeFail(statusCode, errorCode)
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

func (e *Engine) RunTasks(ctx context.Context, q domain.RuntimeQuery) (domain.RuntimeResult, error) {
	return e.Tasks(ctx, q)
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

func (e *Engine) CancelRunTasks(ctx context.Context, agentID, runID string) {
	e.cancelRunTasks(ctx, agentID, runID)
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
