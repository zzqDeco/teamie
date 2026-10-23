package app

import (
	"context"
	"encoding/json"
	"strconv"
	"time"

	"teamie/services/agent-runtime/internal/domain"
)

func unavailableResult() domain.RuntimeResult {
	return domain.RuntimeResult{
		StatusCode:   503,
		ErrorCode:    "runtime_unavailable",
		ErrorMessage: "runtime_unavailable",
	}
}

func failResult(status int32, code string) (domain.RuntimeResult, error) {
	return domain.RuntimeResult{
		StatusCode:   status,
		ErrorCode:    code,
		ErrorMessage: code,
	}, nil
}

func okResult(v any) (domain.RuntimeResult, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return unavailableResult(), nil
	}
	return domain.RuntimeResult{
		OK:       true,
		DataJSON: string(b),
	}, nil
}

func (s *Service) authorizeInspect(ctx context.Context, q domain.RuntimeQuery, write bool) (*domain.AgentProfile, *domain.RuntimeResult) {
	if !domain.ValidServiceKey(s.ControlToken, q.ServiceToken) {
		res, _ := failResult(403, "forbidden")
		return nil, &res
	}
	if !domain.ValidUUID(q.AgentID) || !domain.ValidUUID(q.OwnerID) {
		res, _ := failResult(404, "not_found")
		return nil, &res
	}
	if s.Control == nil {
		res, _ := failResult(503, "control_unavailable")
		return nil, &res
	}
	pc, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	profile, err := s.Control.GetAgentProfile(pc, q.AgentID)
	if err != nil {
		res, _ := failResult(503, "control_unavailable")
		return nil, &res
	}
	if profile == nil || profile.OwnerID != q.OwnerID {
		res, _ := failResult(404, "not_found")
		return nil, &res
	}
	if write && (!profile.Enabled || profile.Deleted) {
		res, _ := failResult(409, "agent_disabled")
		return nil, &res
	}
	return profile, nil
}

// State returns the current runtime state for an Agent.
func (s *Service) State(ctx context.Context, q domain.RuntimeQuery) (domain.RuntimeResult, error) {
	if s == nil {
		return unavailableResult(), nil
	}
	if s.Inspect != nil && s.Control != nil && s.ControlToken != "" {
		profile, fail := s.authorizeInspect(ctx, q, false)
		if fail != nil {
			return *fail, nil
		}
		out := map[string]any{"state": "idle", "sessionId": "agent:" + q.AgentID, "pendingInputs": 0, "runningTasks": 0}
		if !profile.Enabled || profile.Deleted {
			out["state"] = "disabled"
		}
		summary, err := s.Inspect.GetAgentRuntimeSummary(ctx, q.AgentID)
		if err != nil {
			return failResult(503, "runtime_unavailable")
		}
		out["pendingInputs"] = summary.PendingInputs
		out["runningTasks"] = summary.RunningTasks
		if summary.ActiveRunID != "" {
			out["activeRunId"] = summary.ActiveRunID
			if profile.Enabled && !profile.Deleted {
				out["state"] = summary.ActiveStatus
			}
		}
		if summary.HasAgentRow {
			if profile.Enabled && !profile.Deleted {
				if _, active := out["activeRunId"]; !active {
					out["state"] = summary.CurrentState
				}
			}
			if summary.LastActiveAt != nil {
				out["lastActiveAt"] = summary.LastActiveAt.UTC()
			}
			if summary.LastError != "" {
				out["lastError"] = domain.PublicErrorCode(summary.LastError)
			}
		}
		return okResult(out)
	}
	if s.Engine == nil {
		return unavailableResult(), nil
	}
	return s.Engine.State(ctx, q)
}

func (s *Service) queryRuns(ctx context.Context, q domain.RuntimeQuery, detail bool) (domain.RuntimeResult, error) {
	if _, fail := s.authorizeInspect(ctx, q, false); fail != nil {
		return *fail, nil
	}
	if detail && !domain.ValidUUID(q.ResourceID) {
		return failResult(404, "not_found")
	}
	if q.Cursor != "" && !domain.ValidUUID(q.Cursor) {
		return failResult(400, "invalid_cursor")
	}
	limit := q.NormalizeLimit()
	resID := ""
	if detail {
		resID = q.ResourceID
	}
	out, err := s.Inspect.ListRuns(ctx, q.AgentID, resID, q.Cursor, limit+1)
	if err != nil {
		return failResult(503, "runtime_unavailable")
	}
	if detail {
		if len(out) == 0 {
			return failResult(404, "not_found")
		}
		return okResult(map[string]any{"run": out[0]})
	}
	response := map[string]any{"runs": out}
	if len(out) > limit {
		response["runs"] = out[:limit]
		response["cursor"] = out[limit-1].ID
	}
	return okResult(response)
}

// Runs lists paginated runtime runs for an Agent.
func (s *Service) Runs(ctx context.Context, q domain.RuntimeQuery) (domain.RuntimeResult, error) {
	if s == nil {
		return unavailableResult(), nil
	}
	if s.Inspect != nil && s.Control != nil && s.ControlToken != "" {
		return s.queryRuns(ctx, q, false)
	}
	if s.Engine == nil {
		return unavailableResult(), nil
	}
	return s.Engine.Runs(ctx, q)
}

// RunDetail returns a single runtime run by resource ID.
func (s *Service) RunDetail(ctx context.Context, q domain.RuntimeQuery) (domain.RuntimeResult, error) {
	if s == nil {
		return unavailableResult(), nil
	}
	if s.Inspect != nil && s.Control != nil && s.ControlToken != "" {
		return s.queryRuns(ctx, q, true)
	}
	if s.Engine == nil {
		return unavailableResult(), nil
	}
	return s.Engine.RunDetail(ctx, q)
}

func (s *Service) queryTasks(ctx context.Context, q domain.RuntimeQuery, detail bool) (domain.RuntimeResult, error) {
	if _, fail := s.authorizeInspect(ctx, q, false); fail != nil {
		return *fail, nil
	}
	if detail && q.ResourceID == "" {
		return failResult(404, "not_found")
	}
	if len(q.ResourceID) > 200 || len(q.Cursor) > 200 {
		return failResult(400, "invalid_cursor")
	}
	limit := q.NormalizeLimit()
	resID := ""
	if detail {
		resID = q.ResourceID
	}
	out, err := s.Inspect.ListTasks(ctx, q.AgentID, resID, q.Cursor, limit+1)
	if err != nil {
		return failResult(503, "runtime_unavailable")
	}
	if detail {
		if len(out) == 0 {
			return failResult(404, "not_found")
		}
		return okResult(map[string]any{"task": out[0]})
	}
	response := map[string]any{"tasks": out}
	if len(out) > limit {
		response["tasks"] = out[:limit]
		response["cursor"] = out[limit-1].ID
	}
	return okResult(response)
}

// Tasks lists paginated background tasks for an Agent.
func (s *Service) Tasks(ctx context.Context, q domain.RuntimeQuery) (domain.RuntimeResult, error) {
	if s == nil {
		return unavailableResult(), nil
	}
	if s.Inspect != nil && s.Control != nil && s.ControlToken != "" {
		return s.queryTasks(ctx, q, false)
	}
	if s.Engine == nil {
		return unavailableResult(), nil
	}
	return s.Engine.Tasks(ctx, q)
}

// TaskDetail returns a single background task by resource ID.
func (s *Service) TaskDetail(ctx context.Context, q domain.RuntimeQuery) (domain.RuntimeResult, error) {
	if s == nil {
		return unavailableResult(), nil
	}
	if s.Inspect != nil && s.Control != nil && s.ControlToken != "" {
		return s.queryTasks(ctx, q, true)
	}
	if s.Engine == nil {
		return unavailableResult(), nil
	}
	return s.Engine.TaskDetail(ctx, q)
}

// Events lists paginated public runtime events for an Agent.
func (s *Service) Events(ctx context.Context, q domain.RuntimeQuery) (domain.RuntimeResult, error) {
	if s == nil {
		return unavailableResult(), nil
	}
	if s.Inspect != nil && s.Control != nil && s.ControlToken != "" {
		if _, fail := s.authorizeInspect(ctx, q, false); fail != nil {
			return *fail, nil
		}
		after := int64(0)
		if q.Cursor != "" {
			var err error
			after, err = strconv.ParseInt(q.Cursor, 10, 64)
			if err != nil || after < 0 {
				return failResult(400, "invalid_cursor")
			}
		}
		limit := q.NormalizeLimit()
		out, err := s.Inspect.ListPublicEvents(ctx, q.AgentID, after, limit+1)
		if err != nil {
			return failResult(503, "runtime_unavailable")
		}
		more := len(out) > limit
		if more {
			out = out[:limit]
		}
		cursor := strconv.FormatInt(after, 10)
		if len(out) > 0 {
			cursor = out[len(out)-1].ID
		}
		return okResult(map[string]any{"events": out, "cursor": cursor, "hasMore": more})
	}
	if s.Engine == nil {
		return unavailableResult(), nil
	}
	return s.Engine.Events(ctx, q)
}

// CancelRun requests cancellation of an active or pending run.
func (s *Service) CancelRun(ctx context.Context, q domain.RuntimeQuery) (domain.RuntimeResult, error) {
	if s == nil {
		return unavailableResult(), nil
	}
	if s.Inspect != nil && s.Control != nil && s.ControlToken != "" {
		if _, fail := s.authorizeInspect(ctx, q, true); fail != nil {
			return *fail, nil
		}
		if !domain.ValidUUID(q.ResourceID) {
			return failResult(404, "not_found")
		}
		statusCode, errorCode, err := s.Inspect.RequestRunCancel(ctx, q.AgentID, q.ResourceID)
		if err != nil {
			return failResult(503, "runtime_unavailable")
		}
		if statusCode != 0 {
			return failResult(statusCode, errorCode)
		}
		if s.Engine != nil {
			s.Engine.CancelRunTasks(ctx, q.AgentID, q.ResourceID)
		}
		return s.RunDetail(ctx, q)
	}
	if s.Engine == nil {
		return unavailableResult(), nil
	}
	return s.Engine.CancelRun(ctx, q)
}

// RetryRun schedules a retry for a failed or interrupted run.
func (s *Service) RetryRun(ctx context.Context, q domain.RuntimeQuery) (domain.RuntimeResult, error) {
	if s == nil || s.Engine == nil {
		return unavailableResult(), nil
	}
	return s.Engine.RetryRun(ctx, q)
}

// CancelTask requests cancellation of an active background task.
func (s *Service) CancelTask(ctx context.Context, q domain.RuntimeQuery) (domain.RuntimeResult, error) {
	if s == nil || s.Engine == nil {
		return unavailableResult(), nil
	}
	return s.Engine.CancelTask(ctx, q)
}
