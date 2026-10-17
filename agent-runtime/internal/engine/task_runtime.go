package engine

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"time"

	"github.com/cloudwego/eino/adk"
	bg "github.com/cloudwego/eino/adk/backgroundtask"
	bgtool "github.com/cloudwego/eino/adk/backgroundtask/tool"
	bgmiddleware "github.com/cloudwego/eino/adk/middlewares/backgroundtask"
	fsmiddleware "github.com/cloudwego/eino/adk/middlewares/filesystem"
	"github.com/cloudwego/eino/adk/middlewares/plantask"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
	"github.com/google/uuid"
	wire "teamie/agent-protocol/kitex_gen/agent"
)

func runtimeFlag(path, key string) bool {
	if path == "" {
		return true
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	var v struct {
		Flags map[string]bool `json:"flags"`
	}
	return json.Unmarshal(b, &v) == nil && v.Flags[key]
}
func stableTaskID(run *Run, call string) string {
	return "shell-" + digest([]byte(run.AgentID + "/" + run.ID + "/" + call))[:40]
}

type taskContextSnapshot struct {
	AgentID, RunID, Attempt string
	Revision                int64
	ContextProfile          string          `json:",omitempty"`
	ContextBudget           json.RawMessage `json:",omitempty"`
	SourceRefs              []InputSource   `json:",omitempty"`
	SourceOverflow          bool            `json:",omitempty"`
}
type taskContexts struct {
	agentID  string
	revision int64
}

func (s taskContexts) CaptureContext(ctx context.Context) ([]byte, error) {
	r, err := CurrentRun(ctx)
	if err != nil {
		return nil, err
	}
	v := taskContextSnapshot{AgentID: r.AgentID, RunID: r.ID, Attempt: r.Attempt, Revision: r.Revision}
	if contextProfile(r) == contextProfileV1 {
		v.ContextProfile = r.ContextProfile
		v.ContextBudget = r.ContextBudget
		v.SourceRefs, v.SourceOverflow = inputSources(r.Inputs)
	}
	return json.Marshal(v)
}
func (s taskContexts) RestoreContext(ctx context.Context, b []byte) (context.Context, error) {
	var v taskContextSnapshot
	if json.Unmarshal(b, &v) != nil || v.AgentID != s.agentID || v.Revision != s.revision {
		return nil, errors.New("task_configuration_changed")
	}
	profile, budget, err := canonicalContextPolicy(v.ContextProfile, v.ContextBudget)
	if err != nil {
		return nil, err
	}
	refs := []Input{}
	if len(v.SourceRefs) > 0 || v.SourceOverflow {
		refs = append(refs, Input{Kind: "task.notification", ParentRunID: v.RunID, SourceRefs: v.SourceRefs, SourceOverflow: v.SourceOverflow})
	}
	return WithRun(ctx, &Run{ID: v.RunID, Attempt: v.Attempt, AgentID: v.AgentID, Revision: v.Revision, ContextProfile: profile, ContextBudget: budget, Inputs: refs}), nil
}

type workerTasks struct {
	store    *TaskStore
	manager  *bg.Manager
	backend  *SandboxBackend
	handlers []adk.TypedChatModelAgentMiddleware[*schema.AgenticMessage]
}

func (e *Engine) tasks(ctx context.Context, def *wire.AgentDefinition, l *Lease, authorize func(context.Context) error) (*workerTasks, error) {
	store := &TaskStore{Store: e.Store, AgentID: def.Id, Lease: l, AllowSubmit: func(ctx context.Context) error {
		if !runtimeFlag(e.Config.FlagsFile, "agents.tasks.execute") {
			return &ModelError{Code: "task_execution_disabled"}
		}
		return authorize(ctx)
	}}
	executors := bg.NewExecutorRegistry()
	registry := bgtool.NewRegistry()
	manager, err := bg.New(ctx, &bg.Config{Tasks: store, TaskEvents: store, Executors: executors, SendTaskCreatedEvent: bg.TaskCreatedSessionEventSender[*schema.AgenticMessage](), ContextSnapshotter: taskContexts{agentID: def.Id, revision: def.Revision}, IDGen: func(ctx context.Context, _ *bg.AllocateTaskIDRequest) (string, error) {
		r, err := CurrentRun(ctx)
		if err != nil {
			return "", err
		}
		id := compose.GetToolCallID(ctx)
		if id == "" {
			return "shell-" + uuid.NewString(), nil
		}
		return stableTaskID(r, id), nil
	}})
	if err != nil {
		return nil, err
	}
	backend := &SandboxBackend{Client: e.Sandbox, Token: e.Config.SandboxToken, AgentID: def.Id, Authorize: authorize, Store: e.Store, Lease: l}
	timeout := 1000
	filesystem, err := fsmiddleware.NewTyped[*schema.AgenticMessage](ctx, &fsmiddleware.MiddlewareConfig{Backend: backend, ExecuteToolConfig: &fsmiddleware.ExecuteToolConfig{ToolConfig: fsmiddleware.ToolConfig{Name: "workspace_execute"}}, Background: &fsmiddleware.BackgroundConfig{NotificationSessionID: func(context.Context) (string, error) { return "agent:" + def.Id, nil }, Recoverable: &fsmiddleware.RecoverableBackgroundConfig{Shell: backend, Manager: manager, Executors: executors, ToolRegistry: registry, ForegroundTimeoutMs: &timeout, ShouldAutoBackground: func(context.Context, *bg.ForegroundCandidate) bool { return true }}}})
	if err != nil {
		return nil, err
	}
	progress, err := bgtool.NewProgressReader(manager, 20)
	if err != nil {
		return nil, err
	}
	control, err := bgmiddleware.NewTyped(ctx, &bgmiddleware.TypedConfig[*schema.AgenticMessage]{Manager: manager, ProgressReadersByExecutorKey: map[string]bgmiddleware.TaskProgressReader{bgtool.RecoverableExecutorKey: progress}})
	if err != nil {
		return nil, err
	}
	plan, err := plantask.NewTyped[*schema.AgenticMessage](ctx, &plantask.Config{Backend: backend, BaseDir: "/workspace/.teamie/plan"})
	if err != nil {
		return nil, err
	}
	receipts := &nativeToolReceipts{Store: e.Store, Lease: l, Tasks: store, Manager: manager, Authorize: authorize}
	return &workerTasks{store: store, manager: manager, backend: backend, handlers: []adk.TypedChatModelAgentMiddleware[*schema.AgenticMessage]{receipts, filesystem, control, plan}}, nil
}

// nativeToolReceipts protects native filesystem/control effects using the same
// persisted receipt boundary as IM tools. All shell executions are submitted
// durably before StartCommand, even if the model asks to wait in foreground.
type nativeToolReceipts struct {
	*adk.TypedBaseChatModelAgentMiddleware[*schema.AgenticMessage]
	Store     *Store
	Lease     *Lease
	Tasks     *TaskStore
	Manager   *bg.Manager
	Authorize func(context.Context) error
}

func (m *nativeToolReceipts) before(ctx context.Context, tc *adk.ToolContext, args []byte) (*Run, []byte, error) {
	r, err := CurrentRun(ctx)
	if err != nil {
		return nil, nil, err
	}
	if m.Authorize != nil {
		if err = m.Authorize(ctx); err != nil {
			return nil, nil, err
		}
	}
	if tc.Name == "workspace_execute" || strings.HasPrefix(tc.Name, "task_") {
		r.releaseFront()
	}
	b, err := m.Store.ToolIntent(ctx, m.Lease, r, tc.CallID, "native:"+tc.Name, args)
	return r, b, err
}
func (m *nativeToolReceipts) WrapInvokableToolCall(_ context.Context, next adk.InvokableToolCallEndpoint, tc *adk.ToolContext) (adk.InvokableToolCallEndpoint, error) {
	if privateReadTool(tc.Name) {
		return next, nil
	}
	return func(ctx context.Context, args string, opts ...tool.Option) (string, error) {
		r, cached, err := m.before(ctx, tc, []byte(args))
		if err != nil {
			return "", err
		}
		if cached != nil {
			var out string
			err = json.Unmarshal(cached, &out)
			return out, err
		}
		out, err := next(ctx, args, opts...)
		if err != nil {
			return "", err
		}
		b, err := json.Marshal(out)
		if err == nil {
			err = m.Store.ToolResult(ctx, m.Lease, r, tc.CallID, b)
		}
		return out, err
	}, nil
}
func (m *nativeToolReceipts) WrapEnhancedInvokableToolCall(_ context.Context, next adk.EnhancedInvokableToolCallEndpoint, tc *adk.ToolContext) (adk.EnhancedInvokableToolCallEndpoint, error) {
	if privateReadTool(tc.Name) {
		return next, nil
	}
	return func(ctx context.Context, args *schema.ToolArgument, opts ...tool.Option) (*schema.ToolResult, error) {
		if args == nil {
			return nil, errors.New("missing tool arguments")
		}
		r, cached, err := m.before(ctx, tc, []byte(args.Text))
		if err != nil {
			return nil, err
		}
		if cached != nil {
			out := new(schema.ToolResult)
			err = json.Unmarshal(cached, out)
			return out, err
		}
		if tc.Name == "workspace_execute" {
			id := stableTaskID(r, tc.CallID)
			existing, getErr := m.Tasks.Get(ctx, id)
			if getErr == nil {
				if existing.Spec.SessionID != "agent:"+r.AgentID {
					return nil, errors.New("task session scope mismatch")
				}
				var exists bool
				if err = m.Store.DB.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM runtime_session_events WHERE session_id=$1 AND event_id=$2)`, "agent:"+r.AgentID, bg.TaskCreatedSessionEventID(existing.Spec.ID)).Scan(&exists); err != nil {
					return nil, err
				}
				if !exists {
					if err = bg.TaskCreatedSessionEventSender[*schema.AgenticMessage]()(ctx, existing); err != nil {
						return nil, err
					}
				}
				text, _ := json.Marshal(bgtool.ManagedToolResponseEvent{Type: bgtool.ManagedToolResponseEventLaunchResult, TaskID: id, Status: existing.Status, Description: "Workspace command"})
				out := &schema.ToolResult{Parts: []schema.ToolOutputPart{{Type: schema.ToolPartTypeText, Text: string(text)}}}
				b, _ := json.Marshal(out)
				if err = m.Store.ToolResult(ctx, m.Lease, r, tc.CallID, b); err != nil {
					return nil, err
				}
				return out, nil
			}
			if !errors.Is(getErr, bg.ErrNotFound) {
				return nil, getErr
			}
			var v map[string]any
			if json.Unmarshal([]byte(args.Text), &v) != nil {
				return nil, errors.New("invalid execution arguments")
			}
			v["run_in_background"] = true
			b, _ := json.Marshal(v)
			copy := *args
			copy.Text = string(b)
			args = &copy
		}
		out, err := next(ctx, args, opts...)
		if err != nil {
			return nil, err
		}
		b, err := json.Marshal(out)
		if err == nil {
			err = m.Store.ToolResult(ctx, m.Lease, r, tc.CallID, b)
		}
		return out, err
	}, nil
}

func (w *workerTasks) dispatch(ctx context.Context) {
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	active := map[string]bool{}
	finished := make(chan string, 8)
	for {
		select {
		case <-ctx.Done():
			return
		case id := <-finished:
			delete(active, id)
		case <-ticker.C:
			pending, err := w.manager.ListPending(ctx, &bg.ListPendingRequest{ExecutorKeys: []string{bgtool.RecoverableExecutorKey}, Limit: 32})
			if err != nil {
				continue
			}
			for _, task := range pending.Tasks {
				if active[task.Spec.ID] || len(active) >= 2 {
					continue
				}
				active[task.Spec.ID] = true
				go func(id string) {
					_ = w.manager.Execute(ctx, id)
					select {
					case finished <- id:
					case <-ctx.Done():
					}
				}(task.Spec.ID)
			}
		}
	}
}

// Notifications are durable inputs before an acknowledgement clears the outbox.
// A repeat delivery reuses notification_id, so completion can wake exactly once.
func (w *workerTasks) notifications(ctx context.Context, l *Lease) error {
	out, err := w.store.Receive(ctx, &bg.ReceiveNotificationsRequest{Limit: 100, LeaseDuration: 10 * time.Second})
	if err != nil {
		return err
	}
	for _, d := range out.Deliveries {
		n := d.Record
		if n.SessionID != "agent:"+l.AgentID {
			return errors.New("notification scope mismatch")
		}
		tx, err := w.store.begin(ctx)
		if err != nil {
			return err
		}
		var seq string
		err = tx.QueryRow(ctx, `SELECT (90000000000000000000::numeric+nextval('runtime_notification_input_seq'))::text`).Scan(&seq)
		in := Input{EventID: seq, Kind: "task.notification", TaskID: n.TaskID, NotificationID: n.ID, Status: string(n.Kind), CreatedAt: n.CreatedAt.UTC().Format(time.RFC3339Nano)}
		if err == nil {
			in.ParentRunID, in.SourceRefs, in.SourceOverflow, err = taskInputOrigin(ctx, tx, l.AgentID, n.TaskID)
		}
		b, _ := json.Marshal(in)
		if err == nil {
			_, err = tx.Exec(ctx, `INSERT INTO runtime_inputs(agent_id,event_id,payload,notification_id) VALUES($1,$2,$3,$4) ON CONFLICT DO NOTHING`, l.AgentID, seq, b, n.ID)
		}
		if err == nil {
			err = tx.Commit(ctx)
		} else {
			_ = tx.Rollback(ctx)
		}
		if err != nil {
			return err
		}
		if err = w.store.Ack(ctx, d.Receipt); err != nil {
			return err
		}
	}
	return nil
}
func (e *Engine) cancelAgentTasks(ctx context.Context, id string) {
	s := &TaskStore{Store: e.Store, AgentID: id}
	rows, err := e.Store.DB.Query(ctx, `SELECT id FROM runtime_tasks WHERE agent_id=$1 AND snapshot->>'Status' NOT IN ('completed','failed','canceled')`, id)
	if err != nil {
		return
	}
	ids := []string{}
	for rows.Next() {
		var taskID string
		if rows.Scan(&taskID) == nil {
			ids = append(ids, taskID)
		}
	}
	rows.Close()
	for _, taskID := range ids {
		for i := 0; i < 4; i++ {
			t, err := s.Get(ctx, taskID)
			if err != nil || terminalTask(t.Status) {
				break
			}
			_, err = s.RequestCancel(ctx, &bg.RequestCancelRequest{TaskID: taskID, ExpectedVersion: t.Version, Reason: "principal_disabled"})
			if !errors.Is(err, bg.ErrVersionConflict) {
				break
			}
		}
		e.confirmTaskStopped(ctx, s, taskID)
	}
}

// Preserve the same durable receipt boundary when native tools choose streaming.
func (m *nativeToolReceipts) WrapStreamableToolCall(ctx context.Context, next adk.StreamableToolCallEndpoint, tc *adk.ToolContext) (adk.StreamableToolCallEndpoint, error) {
	if privateReadTool(tc.Name) {
		return next, nil
	}
	wrapped, err := m.WrapInvokableToolCall(ctx, func(ctx context.Context, args string, opts ...tool.Option) (string, error) {
		stream, err := next(ctx, args, opts...)
		if err != nil {
			return "", err
		}
		defer stream.Close()
		var out strings.Builder
		for {
			v, e := stream.Recv()
			if e == io.EOF {
				break
			}
			if e != nil {
				return "", e
			}
			if out.Len()+len(v) > 1<<20 {
				return "", errors.New("tool output limit")
			}
			out.WriteString(v)
		}
		return out.String(), nil
	}, tc)
	if err != nil {
		return nil, err
	}
	return func(ctx context.Context, args string, opts ...tool.Option) (*schema.StreamReader[string], error) {
		out, e := wrapped(ctx, args, opts...)
		if e != nil {
			return nil, e
		}
		return schema.StreamReaderFromArray([]string{out}), nil
	}, nil
}
func (m *nativeToolReceipts) WrapEnhancedStreamableToolCall(ctx context.Context, next adk.EnhancedStreamableToolCallEndpoint, tc *adk.ToolContext) (adk.EnhancedStreamableToolCallEndpoint, error) {
	if privateReadTool(tc.Name) {
		return next, nil
	}
	wrapped, err := m.WrapEnhancedInvokableToolCall(ctx, func(ctx context.Context, args *schema.ToolArgument, opts ...tool.Option) (*schema.ToolResult, error) {
		stream, err := next(ctx, args, opts...)
		if err != nil {
			return nil, err
		}
		defer stream.Close()
		out := &schema.ToolResult{}
		total := 0
		for {
			v, e := stream.Recv()
			if e == io.EOF {
				break
			}
			if e != nil {
				return nil, e
			}
			if v == nil {
				continue
			}
			b, _ := json.Marshal(v)
			total += len(b)
			if total > 1<<20 {
				return nil, errors.New("tool output limit")
			}
			out.Parts = append(out.Parts, v.Parts...)
		}
		return out, nil
	}, tc)
	if err != nil {
		return nil, err
	}
	return func(ctx context.Context, args *schema.ToolArgument, opts ...tool.Option) (*schema.StreamReader[*schema.ToolResult], error) {
		out, e := wrapped(ctx, args, opts...)
		if e != nil {
			return nil, e
		}
		return schema.StreamReaderFromArray([]*schema.ToolResult{out}), nil
	}, nil
}

// A stop acknowledgement is terminal only after Sandbox confirms the logical
// operation ended (or never started). Expired retry tasks may need a new native
// claim solely to acknowledge cancellation, without starting external work.
func (e *Engine) confirmTaskStopped(ctx context.Context, store *TaskStore, id string) {
	response, err := e.Sandbox.CancelTask(ctx, &wire.SandboxTaskRequest{ServiceToken: e.Config.SandboxToken, AgentId: store.AgentID, TaskId: id})
	if err != nil || !response.Ok || (response.State != "canceled" && response.State != "not_started" && response.State != "completed" && response.State != "failed") {
		return
	}
	for i := 0; i < 4; i++ {
		task, err := store.Get(ctx, id)
		if err != nil || terminalTask(task.Status) || task.CancelRequestedAt == nil {
			return
		}
		if task.Status == bg.StatusPending {
			task, err = store.Start(ctx, &bg.StartTaskRequest{TaskID: id, ExpectedVersion: task.Version})
			if err != nil {
				continue
			}
		}
		if task.Status != bg.StatusRunning {
			return
		}
		_, err = store.AckCancel(ctx, &bg.AckCancelRequest{TaskID: id, ExpectedVersion: task.Version, Reason: task.CancelReason})
		if !errors.Is(err, bg.ErrVersionConflict) {
			return
		}
	}
}
func (e *Engine) cancelRunTasks(ctx context.Context, agent, runID string) {
	store := &TaskStore{Store: e.Store, AgentID: agent}
	rows, err := e.Store.DB.Query(ctx, `SELECT id FROM runtime_tasks WHERE agent_id=$1 AND run_id=$2 AND snapshot->>'Status' NOT IN ('completed','failed','canceled')`, agent, runID)
	if err != nil {
		return
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil {
			ids = append(ids, id)
		}
	}
	rows.Close()
	for _, id := range ids {
		for i := 0; i < 4; i++ {
			task, err := store.Get(ctx, id)
			if err != nil || terminalTask(task.Status) {
				break
			}
			_, err = store.RequestCancel(ctx, &bg.RequestCancelRequest{TaskID: id, ExpectedVersion: task.Version, Reason: "owner_cancelled"})
			if !errors.Is(err, bg.ErrVersionConflict) {
				break
			}
		}
		e.confirmTaskStopped(ctx, store, id)
	}
}
