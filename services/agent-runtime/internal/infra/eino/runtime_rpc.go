package eino

import (
	"context"
	"errors"
	"time"

	bg "github.com/cloudwego/eino/adk/backgroundtask"
	"teamie/services/agent-runtime/internal/domain"
)

type publicRun = domain.PublicRun
type publicTask = domain.PublicTask
type publicEvent = domain.PublicEvent

var _ domain.RuntimeEngine = (*Engine)(nil)

func (e *Engine) IsFeatureEnabled(flag string) bool {
	return runtimeFlag(e.Config.FlagsFile, flag)
}

func (e *Engine) CancelRunTasks(ctx context.Context, agentID, runID string) {
	e.cancelRunTasks(ctx, agentID, runID)
}

func (e *Engine) CancelTaskByID(ctx context.Context, agentID, taskID string) (statusCode int32, errorCode string, err error) {
	store := &TaskStore{Store: e.Store, AgentID: agentID}
	for i := 0; i < 4; i++ {
		task, getErr := store.Get(ctx, taskID)
		if errors.Is(getErr, bg.ErrNotFound) {
			return 404, "not_found", nil
		}
		if getErr != nil {
			return 503, "runtime_unavailable", getErr
		}
		if terminalTask(task.Status) {
			return 409, "task_not_cancelable", nil
		}
		_, reqErr := store.RequestCancel(ctx, &bg.RequestCancelRequest{TaskID: task.Spec.ID, ExpectedVersion: task.Version, Reason: "owner_cancelled"})
		if errors.Is(reqErr, bg.ErrVersionConflict) {
			continue
		}
		if reqErr != nil {
			return 503, "runtime_unavailable", reqErr
		}
		e.confirmTaskStopped(ctx, store, task.Spec.ID)
		return 0, "", nil
	}
	return 409, "task_version_conflict", nil
}

func publicErrorCode(code string) string {
	return domain.PublicErrorCode(code)
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
