package eino

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	bg "github.com/cloudwego/eino/adk/backgroundtask"
	"github.com/cloudwego/kitex/client/callopt"
	"github.com/google/uuid"
	wire "teamie/contracts/gen/agent/kitex_gen/agent"
	"teamie/contracts/gen/agent/kitex_gen/agent/sandboxservice"
)

type cleanupSandboxFixture struct {
	sandboxservice.Client
	alive map[string]bool
	calls map[string]int
}

func (f *cleanupSandboxFixture) CancelTask(_ context.Context, request *wire.SandboxTaskRequest, _ ...callopt.Option) (*wire.SandboxTaskResponse, error) {
	key := request.AgentId + "/" + request.TaskId
	f.calls[key]++
	f.alive[key] = false
	return &wire.SandboxTaskResponse{Ok: true, TaskId: request.TaskId, State: "canceled"}, nil
}

func TestTaskCleanupStopsSandboxAfterTerminalStatusRace(t *testing.T) {
	for _, scope := range []string{"agent", "run"} {
		for _, status := range []bg.Status{bg.StatusFailed, bg.StatusCanceled, bg.StatusCompleted} {
			t.Run(scope+"/"+string(status), func(t *testing.T) {
				s, lease, run := nativeModelRun(t)
				ctx := WithRun(context.Background(), run)
				create := func(store *TaskStore, taskCtx context.Context, status bg.Status) *bg.Task {
					t.Helper()
					task, err := store.Create(taskCtx, &bg.CreateTaskRequest{Spec: bg.Spec{ID: uuid.NewString(), ExecutorKey: "cleanup-probe"}, LeaseExpiryPolicy: bg.LeaseExpiryRetry})
					if err == nil {
						task, err = store.Start(taskCtx, &bg.StartTaskRequest{TaskID: task.Spec.ID, ExpectedVersion: task.Version})
					}
					if err == nil {
						switch status {
						case bg.StatusFailed:
							task, err = store.Fail(taskCtx, &bg.FailTaskRequest{TaskID: task.Spec.ID, ExpectedVersion: task.Version, Error: "model gateway: principal_disabled"})
						case bg.StatusCanceled:
							task, err = store.RequestCancel(taskCtx, &bg.RequestCancelRequest{TaskID: task.Spec.ID, ExpectedVersion: task.Version, Reason: "owner_disabled"})
							if err == nil {
								task, err = store.AckCancel(taskCtx, &bg.AckCancelRequest{TaskID: task.Spec.ID, ExpectedVersion: task.Version})
							}
						case bg.StatusCompleted:
							task, err = store.Complete(taskCtx, &bg.CompleteTaskRequest{TaskID: task.Spec.ID, ExpectedVersion: task.Version})
						}
					}
					if err != nil {
						t.Fatal(err)
					}
					return task
				}
				store := &TaskStore{Store: s, AgentID: lease.AgentID, Lease: lease}
				task := create(store, ctx, status)
				before, _ := json.Marshal(task)
				foreignAgent := uuid.NewString()
				foreign := create(&TaskStore{Store: s, AgentID: foreignAgent}, context.Background(), bg.StatusFailed)
				key, foreignKey := lease.AgentID+"/"+task.Spec.ID, foreignAgent+"/"+foreign.Spec.ID
				sandbox := &cleanupSandboxFixture{alive: map[string]bool{key: status != bg.StatusCompleted, foreignKey: true}, calls: map[string]int{}}
				engine := &Engine{Store: s, Sandbox: sandbox, Config: Config{SandboxToken: "cleanup-fixture-token"}}
				if scope == "agent" {
					engine.cancelAgentTasks(context.Background(), lease.AgentID)
				} else {
					engine.cancelRunTasks(context.Background(), lease.AgentID, run.ID)
				}
				if sandbox.alive[key] {
					t.Fatal("terminal Runtime status hid a live Sandbox execution from cleanup")
				}
				if sandbox.calls[foreignKey] != 0 || !sandbox.alive[foreignKey] {
					t.Fatal("cleanup crossed Agent ownership")
				}
				if status == bg.StatusCompleted && sandbox.calls[key] != 0 {
					t.Fatal("cleanup unnecessarily canceled a confirmed completed execution")
				}
				unchanged, err := store.Get(ctx, task.Spec.ID)
				after, _ := json.Marshal(unchanged)
				if err != nil || !bytes.Equal(before, after) {
					t.Fatal("physical cleanup rewrote an immutable terminal task result", err)
				}
			})
		}
	}
}
