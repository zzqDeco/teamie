package engine

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"math"
	"net/http"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/adk/middlewares/summarization"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
	"github.com/cloudwego/kitex/client"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	wire "teamie/agent-protocol/kitex_gen/agent"
	"teamie/agent-protocol/kitex_gen/agent/agentcontrolservice"
	"teamie/agent-protocol/kitex_gen/agent/modelgatewayservice"
	"teamie/agent-protocol/kitex_gen/agent/sandboxservice"
	"teamie/backend/kitex_gen/im/identityservice"
)

type Config struct{ ControlAddr, GatewayAddr, SandboxAddr, IdentityAddr, IMURL, IMRealtimeURL, ControlToken, GatewayToken, SandboxToken, IMToken, FlagsFile, GatewayStreamURL string }
type Engine struct {
	Store    *Store
	Config   Config
	Control  agentcontrolservice.Client
	Gateway  modelgatewayservice.Client
	Sandbox  sandboxservice.Client
	Identity identityservice.Client
	slots    chan struct{}
	owner    string
}

func New(store *Store, c Config) (*Engine, error) {
	if c.GatewayStreamURL == "" {
		c.GatewayStreamURL = "http://llm-gateway:8086/internal/v1/chat/stream"
	}
	opts := func(addr string, timeout time.Duration) []client.Option {
		return []client.Option{client.WithHostPorts(addr), client.WithRPCTimeout(timeout), client.WithConnectTimeout(time.Second)}
	}
	cp, e := agentcontrolservice.NewClient("teamie.agent-control", opts(c.ControlAddr, 4*time.Second)...)
	if e != nil {
		return nil, e
	}
	// RPC now carries only bounded Ping/Cancel operations. Native model
	// generation follows HTTP disconnect/cancellation without an RPC timer.
	gwRPC, e := modelgatewayservice.NewClient("teamie.model-gateway", opts(c.GatewayAddr, 4*time.Second)...)
	if e != nil {
		return nil, e
	}
	gw, e := newGatewayHTTPClient(gwRPC, c.GatewayStreamURL)
	if e != nil {
		return nil, e
	}
	sb, e := sandboxservice.NewClient("teamie.sandbox", opts(c.SandboxAddr, 70*time.Second)...)
	if e != nil {
		return nil, e
	}
	id, e := identityservice.NewClient("teamie.identity", opts(c.IdentityAddr, 4*time.Second)...)
	if e != nil {
		return nil, e
	}
	return &Engine{Store: store, Config: c, Control: cp, Gateway: gw, Sandbox: sb, Identity: id, slots: make(chan struct{}, 1), owner: uuid.NewString()}, nil
}
func (e *Engine) Ready(ctx context.Context) error {
	if err := e.Store.DB.Ping(ctx); err != nil {
		return err
	}
	r, err := e.Control.Ping(ctx, &wire.PingRequest{})
	if err != nil {
		return err
	}
	if !r.Ready {
		return errors.New("control_unavailable")
	}
	return nil
}
func (e *Engine) report(ctx context.Context, id, state, code string, active bool) {
	code = publicErrorCodeOptional(code)
	_, _ = e.Store.DB.Exec(ctx, `UPDATE runtime_agents SET state=$3,last_error=$4,last_active_at=CASE WHEN $5 THEN now() ELSE last_active_at END WHERE agent_id=$1 AND lease_owner=$2 AND lease_until>now()`, id, e.owner, state, publicErrorCodeOptional(code), active)
	r := &wire.RuntimeReport{ServiceToken: e.Config.ControlToken, AgentId: id, RuntimeState: state, SessionId: "agent:" + id}
	if code != "" {
		r.LastError = &code
	}
	if active {
		v := time.Now().UTC().Format(time.RFC3339Nano)
		r.LastActiveAt = &v
	}
	_, _ = e.Control.ReportRuntime(ctx, r)
}
func (e *Engine) cancelRun(id string, run *Run) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, _ = e.Gateway.Cancel(ctx, &wire.CancelRequest{ServiceToken: e.Config.GatewayToken, AgentId: id, RunId: executionID(run)})
	}()
	go func() {
		defer wg.Done()
		_, _ = e.Sandbox.Cancel(ctx, &wire.CancelRequest{ServiceToken: e.Config.SandboxToken, AgentId: id, RunId: executionID(run)})
	}()
	wg.Wait()
}

type workerHandle struct {
	revision int64
	cancel   context.CancelFunc
	done     chan struct{}
}

func (e *Engine) Run(ctx context.Context) {
	workers := map[string]*workerHandle{}
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	defer func() {
		for _, w := range workers {
			w.cancel()
		}
		for _, w := range workers {
			<-w.done
		}
	}()
	refresh := func() {
		r, err := e.Control.ListAgents(ctx, &wire.AgentListRequest{ServiceToken: e.Config.ControlToken})
		if err != nil {
			return
		}
		defs := map[string]*wire.AgentDefinition{}
		for _, d := range r.Agents {
			defs[d.Id] = d
		}
		for id, w := range workers {
			d := defs[id]
			select {
			case <-w.done:
				delete(workers, id)
				continue
			default:
			}
			if d == nil || !d.Enabled || d.DeletedAt != nil || d.Revision != w.revision {
				w.cancel()
			}
		}
		for _, d := range r.Agents {
			if !d.Enabled || d.DeletedAt != nil {
				pc, c := context.WithTimeout(ctx, 3*time.Second)
				e.cancelAgentTasks(pc, d.Id)
				c()
				continue
			}
			if _, exists := workers[d.Id]; exists {
				continue
			}
			lease, err := e.Store.Acquire(ctx, d.Id, e.owner)
			if errors.Is(err, pgx.ErrNoRows) {
				continue
			}
			if err != nil {
				continue
			}
			wc, cancel := context.WithCancel(ctx)
			w := &workerHandle{revision: d.Revision, cancel: cancel, done: make(chan struct{})}
			workers[d.Id] = w
			go func(def *wire.AgentDefinition, l *Lease, handle *workerHandle) {
				defer close(handle.done)
				defer func() {
					release, done := context.WithTimeout(context.Background(), 5*time.Second)
					defer done()
					e.Store.Release(release, l)
				}()
				e.work(wc, def, l)
			}(d, lease, w)
		}
	}
	refresh()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			refresh()
		}
	}
}

type turnGuard struct {
	mu      sync.Mutex
	run     *Run
	cancel  context.CancelFunc
	release func()
}

func (g *turnGuard) set(r *Run, c context.CancelFunc, release func()) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.run = r
	g.cancel = c
	g.release = release
}
func (g *turnGuard) finish() {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.cancel != nil {
		g.cancel()
		g.cancel = nil
	}
	if g.release != nil {
		g.release()
		g.release = nil
	}
	g.run = nil
}
func (g *turnGuard) onFinish(run *Run, cleanup func()) {
	g.mu.Lock()
	if g.run != run || g.cancel == nil {
		g.mu.Unlock()
		cleanup()
		return
	}
	release := g.release
	g.release = func() {
		if release != nil {
			release()
		}
		cleanup()
	}
	g.mu.Unlock()
}
func (g *turnGuard) current() *Run { g.mu.Lock(); defer g.mu.Unlock(); return g.run }
func (e *Engine) work(ctx context.Context, def *wire.AgentDefinition, lease *Lease) {
	ctx, cancelWorker := context.WithCancel(ctx)
	defer cancelWorker()
	go func() {
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				pc, c := context.WithTimeout(ctx, 4*time.Second)
				err := e.Store.Renew(pc, lease)
				c()
				if err != nil {
					cancelWorker()
					return
				}
			}
		}
	}()
	authorize := func(ctx context.Context) error {
		pc, c := context.WithTimeout(ctx, 4*time.Second)
		defer c()
		r, err := e.Control.GetAgent(pc, &wire.AgentRequest{ServiceToken: e.Config.ControlToken, AgentId: def.Id})
		if err != nil {
			return &ModelError{Code: "control_unavailable", Retry: true}
		}
		if r.Agent == nil || !r.Agent.Enabled || r.Agent.DeletedAt != nil || r.Agent.Revision != def.Revision {
			return &ModelError{Code: "principal_disabled", Retry: true}
		}
		if run, runErr := CurrentRun(ctx); runErr == nil && e.Store.runCanceled(ctx, run.ID) {
			return &ModelError{Code: "run_cancelled", Retry: false}
		}
		return nil
	}
	var contextWindow atomic.Int64
	contextWindow.Store(16384)
	// Typing and normal IM calls share credentials: issuance rotates the
	// principal's single token and must never create competing token caches.
	imc := &IM{BaseURL: e.Config.IMURL, RealtimeURL: e.Config.IMRealtimeURL, ServiceKey: e.Config.IMToken, AgentID: def.Id, Identity: e.Identity}
	gatewayModel := &GatewayModel{Client: e.Gateway, Token: e.Config.GatewayToken, OwnerID: def.OwnerId, AgentID: def.Id, Store: e.Store, Lease: lease, Authorize: authorize, StreamURL: e.Config.GatewayStreamURL, ContextWindow: func() int { return int(contextWindow.Load()) }}
	tools, err := (&Tools{Store: e.Store, Lease: lease, IM: imc, Sandbox: e.Sandbox, SandboxToken: e.Config.SandboxToken, Authorize: authorize}).BuildIM()
	if err != nil {
		e.report(ctx, def.Id, "error", "tool_failed", false)
		return
	}
	tasks, err := e.tasks(ctx, def, lease, authorize)
	if err != nil {
		e.report(ctx, def.Id, "error", "tool_failed", false)
		return
	}
	go tasks.dispatch(ctx)
	defer func() {
		pc, c := context.WithTimeout(context.Background(), 12*time.Second)
		defer c()
		if authorize(pc) != nil {
			e.cancelAgentTasks(pc, def.Id)
		}
		_ = tasks.manager.Close(pc)
	}()
	session := &SessionStore[*schema.AgenticMessage]{Store: e.Store, Lease: lease, Revision: def.Revision}
	guard := &turnGuard{}
	var loop *adk.TurnLoop[Input, *schema.AgenticMessage]
	var loopDone chan *adk.TurnLoopExitState[Input, *schema.AgenticMessage]
	var loopCancel context.CancelFunc
	generation := int64(0)
	queued := map[string]int64{}
	var qmu sync.Mutex
	clearQueue := func() { qmu.Lock(); queued = map[string]int64{}; qmu.Unlock() }
	stop := func() {
		if loop == nil {
			return
		}
		loop.Stop(adk.WithImmediate())
		if run := guard.current(); run != nil {
			e.cancelRun(def.Id, run)
		}
		<-loopDone
		loopCancel()
		guard.finish()
		loop = nil
		clearQueue()
	}
	defer stop()
	makeAgent := func(ctx context.Context) (adk.TypedAgent[*schema.AgenticMessage], error) {
		run, err := CurrentRun(ctx)
		if err != nil {
			return nil, err
		}
		activeTools, err := toolsForRun(ctx, tools, run)
		if err != nil {
			return nil, err
		}
		trigger := summaryTrigger(int(contextWindow.Load()))
		finalize := finalizeSummary
		if run.toolProfile == v034ToolProfile {
			finalize = finalizeV034Summary
		}
		summary, err := summarization.NewTyped(ctx, &summarization.TypedConfig[*schema.AgenticMessage]{
			Model:           gatewayModel,
			ModelOptions:    []model.Option{model.WithAgenticToolChoice(&schema.AgenticToolChoice{Type: schema.ToolChoiceForbidden})},
			Trigger:         &summarization.TriggerCondition{ContextTokens: trigger, ContextMessages: 32},
			UserInstruction: summaryInstruction,
			Finalize:        finalize,
		})
		if err != nil {
			return nil, err
		}
		handlers := append([]adk.TypedChatModelAgentMiddleware[*schema.AgenticMessage]{summary}, tasks.handlers...)
		return adk.NewTypedChatModelAgent(ctx, &adk.TypedChatModelAgentConfig[*schema.AgenticMessage]{Name: "teamie_agent", Description: "A user-owned Teamie participant", Instruction: instruction(def), Model: gatewayModel, ToolsConfig: adk.ToolsConfig{ToolsNodeConfig: compose.ToolsNodeConfig{Tools: activeTools, ExecuteSequentially: true}}, MaxIterations: math.MaxInt, Handlers: handlers})
	}
	validate := func(ctx context.Context, inputs []Input) ([]Input, error) {
		valid := []Input{}
		for _, in := range inputs {
			if in.Kind == "task.notification" {
				valid = append(valid, in)
				continue
			}
			message, err := imc.Message(ctx, in.ConversationID, in.MessageID)
			if err != nil {
				var api *APIError
				if errors.As(err, &api) && (api.Status == 403 || api.Status == 404) {
					if err = e.Store.Skip(ctx, lease, in.EventID); err != nil {
						return nil, err
					}
					continue
				}
				return nil, err
			}
			if message.RetractedAt != nil || message.Body == "" && len(message.Attachments) == 0 {
				if err = e.Store.Skip(ctx, lease, in.EventID); err != nil {
					return nil, err
				}
				continue
			}
			in.Body = message.Body
			in.Attachments = message.Attachments
			in.ReplyToID = message.ReplyToID
			valid = append(valid, in)
		}
		return valid, nil
	}
	prepare := func(ctx context.Context, items []Input, resume *checkpointMetadata) (*Run, context.Context, []Input, error) {
		if err := authorize(ctx); err != nil {
			return nil, nil, nil, err
		}
		if !runtimeFlag(e.Config.FlagsFile, "agents.runtime.v3") {
			return nil, nil, nil, &ModelError{Code: "runtime_disabled", Retry: true}
		}
		// Deduplicate checkpoint and fresh delivery copies without changing order.
		unique := []Input{}
		seen := map[string]bool{}
		for _, in := range items {
			if !seen[in.EventID] {
				seen[in.EventID] = true
				unique = append(unique, in)
			}
		}
		if len(unique) == 0 {
			pending, err := e.Store.Pending(ctx, lease)
			if err != nil {
				return nil, nil, nil, err
			}
			unique = pending
		}
		eligible, err := e.Store.eligibleInputs(ctx, lease, unique)
		if err != nil {
			return nil, nil, nil, err
		}
		batch, extra := boundedInputBatch(eligible)
		valid, err := validate(ctx, batch)
		if err != nil {
			return nil, nil, nil, err
		}
		if len(valid) == 0 {
			return nil, nil, nil, errors.New("no_pending_inputs")
		}
		run, err := e.Store.StartRun(ctx, lease, valid)
		if err != nil {
			return nil, nil, nil, err
		}
		invalid := []string{}
		for _, in := range run.Inputs {
			if in.Kind == "task.notification" {
				continue
			}
			m, readErr := imc.Message(ctx, in.ConversationID, in.MessageID)
			if readErr != nil {
				var api *APIError
				if errors.As(readErr, &api) && (api.Status == 403 || api.Status == 404) {
					invalid = append(invalid, in.EventID)
					continue
				}
				_ = e.Store.FinishRun(ctx, lease, run, "retry", "im_unavailable", true)
				return nil, nil, nil, &ModelError{Code: "im_unavailable", Retry: true}
			}
			if m.RetractedAt != nil || m.Body != in.Body || !slices.Equal(m.Attachments, in.Attachments) {
				invalid = append(invalid, in.EventID)
			}
		}
		if len(invalid) > 0 {
			if err = e.Store.InvalidateRun(ctx, lease, run, invalid); err != nil {
				return nil, nil, nil, err
			}
			return nil, nil, nil, errors.New("source_unavailable")
		}
		run.toolProfile, err = e.Store.runToolProfile(ctx, run.ID)
		if err != nil {
			return nil, nil, nil, err
		}
		if resume != nil && !resumeCompatibleForRun(resume, def.Revision, run) {
			resume = nil
		}
		if err = e.Store.configureRun(ctx, lease, run, def.Revision, generation, resume); err != nil {
			return nil, nil, nil, err
		}
		run.Front = &frontSlot{slots: e.slots}
		runctx, cancel := context.WithCancel(WithRun(ctx, run))
		guard.set(run, cancel, run.releaseFront)
		qmu.Lock()
		for _, in := range run.Inputs {
			queued[in.EventID] = generation
		}
		qmu.Unlock()
		session.bind(run, nil)
		if err = run.Front.acquire(runctx); err != nil {
			guard.finish()
			return nil, nil, nil, err
		}
		if e.Config.IMRealtimeURL != "" {
			guard.onFinish(run, beginTyping(runctx, run.Inputs, imc.Typing, func(ctx context.Context) error {
				if !runtimeFlag(e.Config.FlagsFile, "agents.runtime.v3") {
					return &ModelError{Code: "runtime_disabled", Retry: true}
				}
				if err := authorize(ctx); err != nil {
					return err
				}
				return e.Store.CheckLease(ctx, lease)
			}, nil))
		}
		consumed := map[string]bool{}
		for _, in := range run.Inputs {
			consumed[in.EventID] = true
		}
		remaining := append([]Input{}, extra...)
		for _, in := range batch {
			if !consumed[in.EventID] {
				remaining = append(remaining, in)
			}
		}
		e.report(runctx, def.Id, "running", "", true)
		return run, runctx, remaining, nil
	}
	makeInput := func(ctx context.Context, _ *adk.TurnLoop[Input, *schema.AgenticMessage], items []Input) (*adk.GenInputResult[Input, *schema.AgenticMessage], error) {
		run, runctx, remaining, err := prepare(ctx, items, nil)
		if err != nil {
			return nil, err
		}
		return &adk.GenInputResult[Input, *schema.AgenticMessage]{RunCtx: runctx, Input: &adk.TypedAgentInput[*schema.AgenticMessage]{Messages: inputMessages(run.Inputs), EnableStreaming: e.Config.GatewayStreamURL != ""}, Consumed: run.Inputs, Remaining: remaining}, nil
	}
	start := func() {
		generation++
		lc, cancel := context.WithCancel(ctx)
		loopCancel = cancel
		loopDone = make(chan *adk.TurnLoopExitState[Input, *schema.AgenticMessage], 1)
		loop = adk.NewTurnLoop(adk.TurnLoopConfig[Input, *schema.AgenticMessage]{GenInput: makeInput, GenResume: func(ctx context.Context, active *adk.TurnLoop[Input, *schema.AgenticMessage], interrupted, unhandled, newItems []Input) (*adk.GenResumeResult[Input, *schema.AgenticMessage], error) {
			metadata, _ := e.Store.ResumeMetadata(ctx, "turnloop:"+def.Id)
			all := append(append(append([]Input{}, interrupted...), unhandled...), newItems...)
			run, runctx, remaining, err := prepare(ctx, all, metadata)
			if err != nil {
				return nil, err
			}
			if resumeCompatibleForRun(metadata, def.Revision, run) {
				targets := map[string]any{}
				for _, id := range metadata.Targets {
					targets[id] = nil
				}
				return &adk.GenResumeResult[Input, *schema.AgenticMessage]{RunCtx: runctx, Decision: adk.TurnLoopResumeDecisionResume, ResumeParams: &adk.ResumeParams{Targets: targets}, Consumed: run.Inputs, Remaining: remaining}, nil
			}
			return &adk.GenResumeResult[Input, *schema.AgenticMessage]{RunCtx: runctx, Decision: adk.TurnLoopResumeDecisionStartNewTurn, Input: &adk.TypedAgentInput[*schema.AgenticMessage]{Messages: inputMessages(run.Inputs), EnableStreaming: e.Config.GatewayStreamURL != ""}, Consumed: run.Inputs, Remaining: remaining}, nil
		}, PrepareAgent: func(ctx context.Context, _ *adk.TurnLoop[Input, *schema.AgenticMessage], _ []Input) (adk.TypedAgent[*schema.AgenticMessage], error) {
			return makeAgent(ctx)
		}, OnAgentEvents: func(ctx context.Context, tc *adk.TurnContext[Input, *schema.AgenticMessage], events *adk.AsyncIterator[*adk.TypedAgentEvent[*schema.AgenticMessage]]) error {
			defer guard.finish()
			var failure error
			var businessTargets []string
			for {
				event, ok := events.Next()
				if !ok {
					break
				}
				if event == nil {
					continue
				}
				if event.Err != nil && failure == nil {
					failure = event.Err
				}
				if event.Output != nil && event.Output.MessageOutput != nil && event.Output.MessageOutput.IsStreaming {
					_, err := event.Output.MessageOutput.GetMessage()
					if err != nil && failure == nil {
						failure = err
					}
				}
				if event.Action != nil && event.Action.Interrupted != nil {
					for _, v := range event.Action.Interrupted.InterruptContexts {
						if v != nil {
							businessTargets = append(businessTargets, v.ID)
						}
					}
				}
			}
			run, err := CurrentRun(ctx)
			if err != nil {
				return err
			}
			if failure == nil && ctx.Err() != nil {
				failure = ctx.Err()
			}
			commit, done := detachedPersistence(ctx)
			defer done()
			targets := targetsFromError(failure)
			if len(businessTargets) > 0 {
				targets = businessTargets
			}
			session.bind(run, targets)
			select {
			case <-tc.Preempted:
				if err = e.Store.preemptRun(commit, lease, run); err != nil {
					return err
				}
				qmu.Lock()
				for _, in := range run.Inputs {
					delete(queued, in.EventID)
				}
				qmu.Unlock()
				return nil
			default:
			}
			if len(businessTargets) > 0 {
				if err = e.Store.FinishRun(commit, lease, run, "waiting_input", "", true); err != nil {
					return err
				}
				e.report(commit, def.Id, "idle", "", true)
				return nil
			}
			if failure != nil {
				e.cancelRun(def.Id, run)
				state := "failed"
				retry := retryError(failure)
				if retry {
					state = "retry"
				}
				if e.Store.runCanceled(commit, run.ID) {
					state = "canceled"
					retry = false
				}
				if err = e.Store.FinishRun(commit, lease, run, state, SafeError(failure), retry); err != nil {
					return err
				}
				e.report(commit, def.Id, "error", SafeError(failure), true)
				var canceled *adk.CancelError
				if errors.As(failure, &canceled) {
					return nil
				}
				return failure
			}
			if err = e.Store.FinishRun(commit, lease, run, "completed", "", false); err != nil {
				return err
			}
			qmu.Lock()
			for _, in := range run.Inputs {
				delete(queued, in.EventID)
			}
			qmu.Unlock()
			e.report(commit, def.Id, "idle", "", true)
			return nil
		}, Store: session, CheckpointID: "turnloop:" + def.Id, SessionID: "agent:" + def.Id, SessionStore: session})
		// Push before Run so restored checkpoints can see newly accepted resume intent.
		pending, _ := e.Store.Pending(ctx, lease)
		qmu.Lock()
		for _, in := range pending {
			if ok, _ := loop.Push(in); ok {
				queued[in.EventID] = generation
			}
		}
		qmu.Unlock()
		loop.Run(lc)
		go func(active *adk.TurnLoop[Input, *schema.AgenticMessage], done chan *adk.TurnLoopExitState[Input, *schema.AgenticMessage]) {
			done <- active.Wait()
		}(loop, loopDone)
	}
	poll := time.NewTicker(time.Second)
	defer poll.Stop()
	lastState := ""
	lastReport := time.Time{}
	tick := func() bool {
		if err := e.ingest(ctx, imc, lease); err != nil {
			if errors.Is(err, ErrLeaseLost) {
				return false
			}
			e.report(ctx, def.Id, "error", "im_unavailable", false)
			return true
		}
		if err := tasks.notifications(ctx, lease); errors.Is(err, ErrLeaseLost) {
			return false
		}
		if loop != nil {
			select {
			case exit := <-loopDone:
				guard.finish()
				loopCancel()
				loop = nil
				clearQueue()
				if exit.ExitReason != nil {
					slog.Info("agent turn stopped", "agent_id", def.Id, "reason", SafeError(exit.ExitReason))
				}
			default:
			}
		}
		if run := guard.current(); run != nil && e.Store.runCanceled(ctx, run.ID) {
			pc, c := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
			e.cancelRunTasks(pc, def.Id, run.ID)
			c()
			stop()
		}
		if !runtimeFlag(e.Config.FlagsFile, "agents.runtime.v3") {
			stop()
			e.report(ctx, def.Id, "idle", "runtime_disabled", false)
			return true
		}
		pc, c := context.WithTimeout(ctx, 3*time.Second)
		ping, err := e.Gateway.Ping(pc, &wire.PingRequest{ServiceToken: &e.Config.GatewayToken})
		c()
		ready := err == nil && ping.Ready && ping.GetConfigured() && ping.GetEnabled()
		if ready && ping.ContextWindowTokens != nil && ping.GetContextWindowTokens() >= 4096 {
			contextWindow.Store(ping.GetContextWindowTokens())
		}
		if !ready {
			stop()
			state, code := "waiting_configuration", "model_unconfigured"
			if err != nil {
				state, code = "error", "gateway_unavailable"
			}
			if state != lastState || time.Since(lastReport) > 10*time.Second {
				e.report(ctx, def.Id, state, code, false)
				lastState = state
				lastReport = time.Now()
			}
			return true
		}
		if lastState != "idle" {
			e.report(ctx, def.Id, "idle", "", false)
			lastState = "idle"
		}
		pending, err := e.Store.FreshPending(ctx, lease)
		if err != nil {
			return true
		}
		if loop == nil {
			recoverable, err := e.Store.Pending(ctx, lease)
			if err != nil {
				return true
			}
			if len(recoverable) > 0 || len(pending) > 0 {
				start()
			}
		}

		if loop != nil {
			qmu.Lock()
			for _, in := range pending {
				if queued[in.EventID] == generation {
					continue
				}
				ok, _ := loop.Push(in, adk.WithPushStrategy(func(_ context.Context, tc *adk.TurnContext[Input, *schema.AgenticMessage]) []adk.PushOption[Input, *schema.AgenticMessage] {
					if relevantInput(in, tc) {
						return []adk.PushOption[Input, *schema.AgenticMessage]{adk.WithPreempt[Input, *schema.AgenticMessage](adk.AnySafePoint)}
					}
					return nil
				}))
				if ok {
					queued[in.EventID] = generation
				}
			}
			qmu.Unlock()
		}
		if loop != nil {
			// A live loop may be idle while an older run's backoff expires or an
			// owner requests retry. Processing inputs need their own delivery path.
			due, dueErr := e.Store.Pending(ctx, lease)
			if dueErr == nil {
				qmu.Lock()
				for _, in := range due {
					if queued[in.EventID] == generation {
						continue
					}
					if ok, _ := loop.Push(in); ok {
						queued[in.EventID] = generation
					}
				}
				qmu.Unlock()
			}
		}
		return true
	}
	if !tick() {
		return
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-poll.C:
			if !tick() {
				return
			}
		}
	}
}
func (e *Engine) ingest(ctx context.Context, imc *IM, l *Lease) error {
	cursor, err := e.Store.Cursor(ctx, l)
	if err != nil {
		return err
	}
	var page struct {
		Events []struct {
			ID             string   `json:"id"`
			Type           string   `json:"type"`
			WorkspaceID    string   `json:"workspaceId"`
			ConversationID string   `json:"conversationId"`
			Message        *Message `json:"message"`
		} `json:"events"`
		Cursor  string `json:"cursor"`
		HasMore bool   `json:"hasMore"`
	}
	if err = imc.Call(ctx, http.MethodGet, "/api/v1/sync?limit=32&cursor="+cursor, nil, &page); err != nil {
		return err
	}
	inputs := []Input{}
	var bootstrap *Bootstrap
	for _, event := range page.Events {
		m := event.Message
		if event.Type != "message.created" || m == nil || m.SenderID == l.AgentID || m.RetractedAt != nil || m.Body == "" && len(m.Attachments) == 0 {
			continue
		}
		ws := event.WorkspaceID
		if ws == "" {
			if bootstrap == nil {
				bootstrap, err = imc.Bootstrap(ctx)
				if err != nil {
					return err
				}
			}
			for _, c := range bootstrap.Conversations {
				if c.ID == event.ConversationID {
					ws = c.WorkspaceID
					break
				}
			}
		}
		inputs = append(inputs, Input{EventID: event.ID, WorkspaceID: ws, ConversationID: event.ConversationID, MessageID: m.ID, SenderID: m.SenderID, SenderName: m.SenderName, SenderKind: m.SenderKind, Body: m.Body, Attachments: m.Attachments, ReplyToID: m.ReplyToID, Seq: m.Seq, CreatedAt: m.CreatedAt})
	}
	return e.Store.Ingest(ctx, l, page.Cursor, inputs)
}
func finalizeSummary(ctx context.Context, original []*schema.AgenticMessage, summary *schema.AgenticMessage) ([]*schema.AgenticMessage, error) {
	return finalizeSummaryWithSourceFormat(ctx, original, summary, false)
}

// Only runs pinned to the v0.3.4 tool profile use the pre-attachment source
// representation. Native-only runs must keep their v0.3.5 request bytes.
func finalizeV034Summary(ctx context.Context, original []*schema.AgenticMessage, summary *schema.AgenticMessage) ([]*schema.AgenticMessage, error) {
	return finalizeSummaryWithSourceFormat(ctx, original, summary, true)
}

func finalizeSummaryWithSourceFormat(_ context.Context, original []*schema.AgenticMessage, summary *schema.AgenticMessage, omitEmptyAttachmentIDs bool) ([]*schema.AgenticMessage, error) {
	if summary == nil || agenticText(summary) == "" || len(agenticText(summary)) > 32768 || hasToolCalls(summary) {
		return nil, errors.New("invalid_context_summary")
	}
	sources := []map[string]any{}
	seen := map[string]bool{}
	appendSource := func(source map[string]any) {
		id, _ := source["eventId"].(string)
		if id == "" || seen[id] {
			return
		}
		seen[id] = true
		clean := map[string]any{}
		for _, key := range []string{"eventId", "workspaceId", "conversationId", "messageId", "replyToId", "senderId", "senderKind"} {
			if value, ok := source[key].(string); ok {
				clean[key] = value
			}
		}
		// Retain bounded attachment references through repeated compaction;
		// metadata/content can be retrieved explicitly from the source message.
		retained := []string{}
		switch ids := source["attachmentIds"].(type) {
		case []string:
			if !omitEmptyAttachmentIDs {
				// Preserve native-only v0.3.5 receipts, including their empty
				// arrays and original fresh-source representation.
				if len(ids) <= 8 {
					clean["attachmentIds"] = ids
				}
				break
			}
			for _, id := range ids {
				if validUUID(id) && len(retained) < 8 {
					retained = append(retained, id)
				}
			}
		case []any:
			for _, value := range ids {
				if id, ok := value.(string); ok && validUUID(id) && len(retained) < 8 {
					retained = append(retained, id)
				}
			}
		}
		// The v0.3.4 format and parsed previous summaries omit empty references.
		if len(retained) > 0 {
			clean["attachmentIds"] = retained
		}
		sources = append(sources, clean)
	}
	for _, message := range original {
		if message == nil {
			continue
		}
		if strings.HasPrefix(agenticText(message), "TEAMIE_SESSION_SUMMARY ") {
			if index := strings.LastIndex(agenticText(message), "\nTEAMIE_SOURCE_REFERENCES "); index >= 0 {
				var previous []map[string]any
				if json.Unmarshal([]byte(agenticText(message)[index+len("\nTEAMIE_SOURCE_REFERENCES "):]), &previous) == nil {
					for _, source := range previous {
						appendSource(source)
					}
				}
			}
		}
		if !strings.HasPrefix(agenticText(message), "TEAMIE_NEW_MESSAGE ") {
			continue
		}
		var in Input
		if json.Unmarshal([]byte(strings.TrimPrefix(agenticText(message), "TEAMIE_NEW_MESSAGE ")), &in) == nil {
			source := map[string]any{"eventId": in.EventID, "workspaceId": in.WorkspaceID, "conversationId": in.ConversationID, "messageId": in.MessageID, "replyToId": in.ReplyToID, "senderId": in.SenderID, "senderKind": in.SenderKind}
			attachmentIDs := []string{}
			for _, a := range in.Attachments {
				attachmentIDs = append(attachmentIDs, a.ID)
			}
			if !omitEmptyAttachmentIDs || len(attachmentIDs) > 0 {
				source["attachmentIds"] = attachmentIDs
			}
			appendSource(source)
		}
	}
	// Full provenance remains in durable inbox/session audit; the working
	// context keeps a bounded recent reference index through repeated compaction.
	if len(sources) > 32 {
		sources = sources[len(sources)-32:]
	}
	provenance, _ := json.Marshal(sources)
	// Native summarization replaces the complete in-flight model state. Retain
	// the actual leading platform policy; only transcript data is compacted.
	// Summary content remains a user message regardless of the model's role.
	out := make([]*schema.AgenticMessage, 0, len(original)+1)
	for _, message := range original {
		if message == nil || message.Role != schema.AgenticRoleTypeSystem {
			break
		}
		out = append(out, message)
	}
	out = append(out, schema.UserAgenticMessage("TEAMIE_SESSION_SUMMARY (source-scoped memory; verify current access using tools):\n"+agenticText(summary)+"\nTEAMIE_SOURCE_REFERENCES "+string(provenance)))
	start := len(original)
	for i := len(original) - 1; i >= 0; i-- {
		if original[i] != nil && original[i].Role == schema.AgenticRoleTypeUser && !hasToolResults(original[i]) && len(original)-i <= 12 {
			start = i
			break
		}
	}
	if start < len(original) {
		out = append(out, original[start:]...)
	}
	return out, nil
}

// The transport byte ceiling and configured model window are independent.
// Large-context providers retain a conservative platform trigger so a single
// summary request still fits the gateway's bounded transport.
func summaryTrigger(window int) int {
	if window < 4096 {
		window = 16384
	}
	trigger := window * 3 / 4
	if trigger > 12000 {
		trigger = 12000
	}
	return trigger
}
func boundedInputBatch(inputs []Input) ([]Input, []Input) {
	n, total := 0, 0
	for _, in := range inputs {
		b, _ := json.Marshal(in)
		if n > 0 && (total+len(b) > 48<<10 || n >= 32) {
			break
		}
		total += len(b)
		n++
	}
	return inputs[:n], inputs[n:]
}
