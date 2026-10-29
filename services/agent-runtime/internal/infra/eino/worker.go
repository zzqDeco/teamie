package eino

import (
	"context"
	"errors"
	"go.uber.org/zap"
	"net/http"
	"sync"
	"sync/atomic"
	"teamie/pkg/observability"
	"time"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
	"github.com/google/uuid"
	wire "teamie/contracts/gen/agent/kitex_gen/agent"
	"teamie/contracts/gen/agent/kitex_gen/agent/agentcontrolservice"
	"teamie/contracts/gen/agent/kitex_gen/agent/modelgatewayservice"
	"teamie/contracts/gen/agent/kitex_gen/agent/sandboxservice"
	"teamie/contracts/gen/im/kitex_gen/im/identityservice"
	"teamie/pkg/rpcx"
	"teamie/services/agent-runtime/internal/domain"
)

type Config struct{ ControlAddr, GatewayAddr, SandboxAddr, IdentityAddr, IMURL, IMRealtimeURL, ControlToken, GatewayToken, SandboxToken, IMToken, FlagsFile, GatewayStreamURL string }
type Engine struct {
	Store                *Store
	Config               Config
	Control              agentcontrolservice.Client
	Gateway              modelgatewayservice.Client
	Sandbox              sandboxservice.Client
	Identity             identityservice.Client
	UnreadDigestInterval time.Duration
	slots                chan struct{}
	owner                string
}

func (e *Engine) unreadDigestInterval() time.Duration {
	if e != nil && e.UnreadDigestInterval > 0 {
		return e.UnreadDigestInterval
	}
	return defaultUnreadDigestInterval
}

func (e *Engine) presenceEnabled() bool {
	return e != nil && e.Config.FlagsFile != "" && runtimeFlag(e.Config.FlagsFile, "im.marketplace.enabled")
}

func New(store *Store, c Config) (*Engine, error) {
	if c.GatewayStreamURL == "" {
		c.GatewayStreamURL = "http://llm-gateway:8086/internal/v1/chat/stream"
	}
	cp, e := agentcontrolservice.NewClient("teamie.agent-control", rpcx.ClientOptions(c.ControlAddr, 4*time.Second, time.Second)...)
	if e != nil {
		return nil, e
	}
	// RPC now carries only bounded Ping/Cancel operations. Native model
	// generation follows HTTP disconnect/cancellation without an RPC timer.
	gwRPC, e := modelgatewayservice.NewClient("teamie.model-gateway", rpcx.ClientOptions(c.GatewayAddr, 4*time.Second, time.Second)...)
	if e != nil {
		return nil, e
	}
	gw, e := newGatewayHTTPClient(gwRPC, c.GatewayStreamURL)
	if e != nil {
		return nil, e
	}
	sb, e := sandboxservice.NewClient("teamie.sandbox", rpcx.ClientOptions(c.SandboxAddr, 70*time.Second, time.Second)...)
	if e != nil {
		return nil, e
	}
	id, e := identityservice.NewClient("teamie.identity", rpcx.ClientOptions(c.IdentityAddr, 4*time.Second, time.Second)...)
	if e != nil {
		return nil, e
	}
	return &Engine{Store: store, Config: c, Control: cp, Gateway: gw, Sandbox: sb, Identity: id, owner: uuid.NewString()}, nil
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
	_ = e.Store.DB.ReportAgentState(ctx, id, e.owner, state, code, active)
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

func (e *Engine) OwnerID() string {
	return e.owner
}

func (e *Engine) CancelAgentTasks(ctx context.Context, agentID string) {
	e.cancelAgentTasks(ctx, agentID)
}

func (e *Engine) WorkAgent(ctx context.Context, profile domain.AgentProfile, lease *domain.Lease) {
	e.work(ctx, agentDefinition(profile), lease)
}

func agentDefinition(profile domain.AgentProfile) *wire.AgentDefinition {
	def := &wire.AgentDefinition{
		Id:           profile.ID,
		OwnerId:      profile.OwnerID,
		Name:         profile.DisplayName,
		Description:  profile.RoleDescription,
		WorkspaceIds: append([]string(nil), profile.WorkspaceIDs...),
		Enabled:      profile.Enabled,
		Revision:     profile.Revision,
	}
	if profile.ModelID != "" {
		id := profile.ModelID
		def.ModelId = &id
	}
	if profile.Deleted {
		del := "deleted"
		def.DeletedAt = &del
	}
	return def
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

type agentWorker struct {
	presence         *conversationPresenceState
	mcpWatcher       *mcpNotificationWatcher
	engine           *Engine
	def              *wire.AgentDefinition
	lease            *Lease
	contextWindow    atomic.Int64
	imc              *IM
	reads            *readReceiptSender
	gatewayModel     *GatewayModel
	tools            []tool.BaseTool
	tasks            *workerTasks
	session          *SessionStore[*schema.AgenticMessage]
	guard            *turnGuard
	loop             *adk.TurnLoop[Input, *schema.AgenticMessage]
	loopDone         chan *adk.TurnLoopExitState[Input, *schema.AgenticMessage]
	loopCancel       context.CancelFunc
	generation       int64
	queued           map[string]int64
	qmu              sync.Mutex
	lastState        string
	lastReport       time.Time
	heartbeatEnsured bool
}

func (w *agentWorker) authorize(ctx context.Context) error {
	e, def := w.engine, w.def
	pc, c := context.WithTimeout(ctx, 4*time.Second)
	defer c()
	r, err := e.Control.GetAgent(pc, &wire.AgentRequest{ServiceToken: e.Config.ControlToken, AgentId: def.Id})
	if err != nil {
		return &ModelError{Code: "control_unavailable", Retry: true}
	}
	shouldPreempt := r.Agent == nil
	if !shouldPreempt {
		if coord := e.Store.ScheduleCoordinator(nil); coord != nil {
			shouldPreempt = coord.CheckRevisionPreemption(r.Agent.Enabled, r.Agent.DeletedAt != nil, r.Agent.Revision, def.Revision)
		} else {
			shouldPreempt = !r.Agent.Enabled || r.Agent.DeletedAt != nil || r.Agent.Revision != def.Revision
		}
	}
	if shouldPreempt {
		return &ModelError{Code: "principal_disabled", Retry: true}
	}
	if run, runErr := CurrentRun(ctx); runErr == nil && e.Store.runCanceled(ctx, run.ID) {
		return &ModelError{Code: "run_cancelled", Retry: false}
	}
	return nil
}

func (w *agentWorker) clearQueue() {
	w.qmu.Lock()
	w.queued = map[string]int64{}
	w.qmu.Unlock()
}

func (w *agentWorker) stop() {
	if w.loop == nil {
		return
	}
	w.loop.Stop(adk.WithImmediate())
	if run := w.guard.current(); run != nil {
		w.engine.cancelRun(w.def.Id, run)
	}
	<-w.loopDone
	w.loopCancel()
	w.guard.finish()
	w.loop = nil
	w.clearQueue()
}

func (w *agentWorker) start(ctx context.Context) {
	w.generation++
	lc, cancel := context.WithCancel(ctx)
	w.loopCancel = cancel
	w.loopDone = make(chan *adk.TurnLoopExitState[Input, *schema.AgenticMessage], 1)
	w.loop = adk.NewTurnLoop(adk.TurnLoopConfig[Input, *schema.AgenticMessage]{
		GenInput:  w.makeInput,
		GenResume: w.genResume,
		PrepareAgent: func(ctx context.Context, _ *adk.TurnLoop[Input, *schema.AgenticMessage], _ []Input) (adk.TypedAgent[*schema.AgenticMessage], error) {
			return w.makeAgent(ctx)
		},
		OnAgentEvents: w.onAgentEvents,
		Store:         w.session,
		CheckpointID:  "turnloop:" + w.def.Id,
		SessionID:     "agent:" + w.def.Id,
		SessionStore:  w.session,
	})
	// Push before Run so restored checkpoints can see newly accepted resume intent.
	pending, _ := w.engine.Store.Pending(ctx, w.lease)
	w.qmu.Lock()
	for _, in := range pending {
		if ok, _ := w.loop.Push(in); ok {
			w.queued[in.EventID] = w.generation
		}
	}
	w.qmu.Unlock()
	w.loop.Run(lc)
	go func(active *adk.TurnLoop[Input, *schema.AgenticMessage], done chan *adk.TurnLoopExitState[Input, *schema.AgenticMessage]) {
		done <- active.Wait()
	}(w.loop, w.loopDone)
}

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
	w := &agentWorker{
		engine: e,
		def:    def,
		lease:  lease,
		guard:  &turnGuard{},
		queued: map[string]int64{},
	}
	w.contextWindow.Store(16384)
	// Typing and normal IM calls share credentials: issuance rotates the
	// principal's single token and must never create competing token caches.
	w.imc = &IM{BaseURL: e.Config.IMURL, RealtimeURL: e.Config.IMRealtimeURL, ServiceKey: e.Config.IMToken, AgentID: def.Id, Identity: e.Identity}
	w.reads = newReadReceiptSender()
	w.gatewayModel = &GatewayModel{Client: e.Gateway, Token: e.Config.GatewayToken, OwnerID: def.OwnerId, AgentID: def.Id, ModelID: def.GetModelId(), Store: e.Store, Lease: lease, Authorize: w.authorize, StreamURL: e.Config.GatewayStreamURL, ContextWindow: func() int { return int(w.contextWindow.Load()) }}
	var err error
	w.tools, err = (&Tools{Store: e.Store, Lease: lease, IM: w.imc, Sandbox: e.Sandbox, SandboxToken: e.Config.SandboxToken, Authorize: w.authorize}).BuildIM()
	if err != nil {
		e.report(ctx, def.Id, "error", "tool_failed", false)
		return
	}
	w.tasks, err = e.tasks(ctx, def, lease, w.imc, w.authorize)
	if err != nil {
		e.report(ctx, def.Id, "error", "tool_failed", false)
		return
	}
	go w.tasks.dispatch(ctx)
	defer func() {
		pc, c := context.WithTimeout(context.Background(), 12*time.Second)
		defer c()
		if w.authorize(pc) != nil {
			e.cancelAgentTasks(pc, def.Id)
		}
		_ = w.tasks.manager.Close(pc)
	}()
	w.session = &SessionStore[*schema.AgenticMessage]{Store: e.Store, Lease: lease, Revision: def.Revision}
	defer w.stop()
	w.mcpWatcher = newMCPNotificationWatcher(e.Store, lease, w.imc, e.Config.FlagsFile)
	w.presence = newConversationPresenceState(e.unreadDigestInterval(), w.mcpWatcher.presenceChanged)
	_ = w.presence.Restore(ctx, e.Store, lease)
	w.mcpWatcher.setPresence(w.presence)
	stopReads := w.reads.start(ctx, e.Store, lease, func(rc context.Context) error {
		if !runtimeFlag(e.Config.FlagsFile, "agents.runtime.v3") {
			return &ModelError{Code: "runtime_disabled"}
		}
		return w.authorize(rc)
	}, w.imc.MarkRead)
	defer stopReads()
	stopMCPWatcher := w.mcpWatcher.start(ctx)
	defer stopMCPWatcher()
	pollInterval := time.Second
	if d := e.unreadDigestInterval(); d > 0 && d < pollInterval {
		pollInterval = d
	}
	poll := time.NewTicker(pollInterval)
	defer poll.Stop()
	if !w.tick(ctx) {
		return
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-w.mcpWatcher.wake():
			if !w.tick(ctx) {
				return
			}
		case <-poll.C:
			if !w.tick(ctx) {
				return
			}
		}
	}
}

func (w *agentWorker) tick(ctx context.Context) bool {
	var activePresence *conversationPresenceState
	if w.engine.presenceEnabled() {
		activePresence = w.presence
	}
	inputs, err := w.engine.ingestWithPresence(ctx, w.imc, w.lease, activePresence)
	if err != nil {
		if errors.Is(err, ErrLeaseLost) {
			return false
		}
		w.engine.report(ctx, w.def.Id, "error", "im_unavailable", false)
		return true
	}
	w.reads.recordInputs(inputs)
	if activePresence != nil {
		if _, digestErr := activePresence.FlushDueDigest(ctx, w.engine.Store, w.lease, time.Now().UTC()); errors.Is(digestErr, ErrLeaseLost) {
			return false
		}
	}
	if err := w.tasks.notifications(ctx, w.lease); errors.Is(err, ErrLeaseLost) {
		return false
	}
	if !w.heartbeatEnsured {
		ensured, hbErr := w.engine.ensureDefaultHeartbeatSchedule(ctx, w.def, w.imc, w.lease)
		if errors.Is(hbErr, ErrLeaseLost) {
			return false
		}
		if hbErr == nil && ensured {
			w.heartbeatEnsured = true
		}
	}
	if err := w.engine.drainSchedules(ctx, w.lease); errors.Is(err, ErrLeaseLost) {
		return false
	}
	w.mcpWatcher.requestSync()
	if w.loop != nil {
		select {
		case exit := <-w.loopDone:
			w.guard.finish()
			w.loopCancel()
			w.loop = nil
			w.clearQueue()
			if exit.ExitReason != nil {
				observability.L(ctx).Info("Agent turn stopped", zap.String("event.action", "agent-turn-stopped"), zap.String("custom.agent.id", w.def.Id), zap.String("custom.error.code", SafeError(exit.ExitReason)))
			}
		default:
		}
	}
	if run := w.guard.current(); run != nil && w.engine.Store.runCanceled(ctx, run.ID) {
		pc, c := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		w.engine.cancelRunTasks(pc, w.def.Id, run.ID)
		c()
		w.stop()
	}
	if !runtimeFlag(w.engine.Config.FlagsFile, "agents.runtime.v3") {
		w.stop()
		w.engine.report(ctx, w.def.Id, "idle", "runtime_disabled", false)
		return true
	}
	pc, c := context.WithTimeout(ctx, 3*time.Second)
	ping, err := w.engine.Gateway.Ping(pc, &wire.PingRequest{ServiceToken: &w.engine.Config.GatewayToken, ModelId: w.def.ModelId})
	c()
	ready := err == nil && ping.Ready && ping.GetConfigured() && ping.GetEnabled()
	if ready && ping.ContextWindowTokens != nil && ping.GetContextWindowTokens() >= 4096 {
		w.contextWindow.Store(ping.GetContextWindowTokens())
	}
	if !ready {
		w.stop()
		state, code := "waiting_configuration", "model_unconfigured"
		if err != nil {
			state, code = "error", "gateway_unavailable"
		}
		if state != w.lastState || time.Since(w.lastReport) > 10*time.Second {
			w.engine.report(ctx, w.def.Id, state, code, false)
			w.lastState = state
			w.lastReport = time.Now()
		}
		return true
	}
	if w.lastState != "idle" {
		w.engine.report(ctx, w.def.Id, "idle", "", false)
		w.lastState = "idle"
	}
	pending, err := w.engine.Store.FreshPending(ctx, w.lease)
	if err != nil {
		return true
	}
	if w.loop == nil {
		recoverable, err := w.engine.Store.Pending(ctx, w.lease)
		if err != nil {
			return true
		}
		if len(recoverable) > 0 || len(pending) > 0 {
			w.start(ctx)
		}
	}

	if w.loop != nil {
		w.qmu.Lock()
		for _, in := range pending {
			if w.queued[in.EventID] == w.generation {
				continue
			}
			ok, _ := w.loop.Push(in, adk.WithPushStrategy(func(_ context.Context, tc *adk.TurnContext[Input, *schema.AgenticMessage]) []adk.PushOption[Input, *schema.AgenticMessage] {
				if relevantInput(in, tc, activePresence) {
					// Complete the model's tool round and durable receipts before
					// handing new input to the next serial turn.
					return []adk.PushOption[Input, *schema.AgenticMessage]{adk.WithPreempt[Input, *schema.AgenticMessage](adk.AfterToolCalls)}
				}
				return nil
			}))
			if ok {
				w.queued[in.EventID] = w.generation
			}
		}
		w.qmu.Unlock()
	}
	if w.loop != nil {
		// A live loop may be idle while an older run's backoff expires or an
		// owner requests retry. Processing inputs need their own delivery path.
		due, dueErr := w.engine.Store.Pending(ctx, w.lease)
		if dueErr == nil {
			w.qmu.Lock()
			for _, in := range due {
				if w.queued[in.EventID] == w.generation {
					continue
				}
				if ok, _ := w.loop.Push(in); ok {
					w.queued[in.EventID] = w.generation
				}
			}
			w.qmu.Unlock()
		}
	}
	return true
}

func (e *Engine) ingest(ctx context.Context, imc *IM, l *Lease) (committed []Input, failure error) {
	return e.ingestWithPresence(ctx, imc, leaseOrNil(l), nil)
}

func leaseOrNil(l *Lease) *Lease { return l }

func (e *Engine) ingestWithPresence(ctx context.Context, imc *IM, l *Lease, presence *conversationPresenceState) (committed []Input, failure error) {
	ctx, operation := observability.Begin(ctx, "agent-input-ingest", zap.String("custom.agent.id", l.AgentID))
	defer func() { operation.EndQuiet(failure) }()
	cursor, err := e.Store.Cursor(ctx, l)
	if err != nil {
		return nil, err
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
		return nil, err
	}
	inputs := []Input{}
	var bootstrap *Bootstrap
	ensureBootstrap := func() (*Bootstrap, error) {
		if bootstrap != nil {
			return bootstrap, nil
		}
		b, bErr := imc.Bootstrap(ctx)
		if bErr != nil {
			return nil, bErr
		}
		bootstrap = b
		return bootstrap, nil
	}
	now := time.Now().UTC()
	unreadUpdated := false
	activeConvID := ""
	if presence != nil {
		b, bErr := ensureBootstrap()
		if bErr != nil {
			return nil, bErr
		}
		unreadUpdated = presence.ReconcileUnread(b.Conversations)
		activeConvID = presence.ActiveConversationID()
	}
	for _, event := range page.Events {
		m := event.Message
		if event.Type != "message.created" || m == nil || m.SenderID == l.AgentID || m.RetractedAt != nil || m.Body == "" && len(m.Attachments) == 0 {
			continue
		}
		if presence != nil && event.ConversationID != activeConvID {
			b, bErr := ensureBootstrap()
			if bErr != nil {
				return nil, bErr
			}
			var convMeta *Conversation
			for i := range b.Conversations {
				if b.Conversations[i].ID == event.ConversationID {
					convMeta = &b.Conversations[i]
					break
				}
			}
			ws := event.WorkspaceID
			if convMeta != nil && ws == "" {
				ws = convMeta.WorkspaceID
			}
			if convMeta != nil && ws != "" {
				presence.RecordUnread(ws, *convMeta, m, l.AgentID, now)
				unreadUpdated = true
			}
			continue
		}
		ws := event.WorkspaceID
		if ws == "" {
			b, bErr := ensureBootstrap()
			if bErr != nil {
				return nil, bErr
			}
			for _, c := range b.Conversations {
				if c.ID == event.ConversationID {
					ws = c.WorkspaceID
					break
				}
			}
		}
		inputs = append(inputs, Input{EventID: event.ID, WorkspaceID: ws, ConversationID: event.ConversationID, MessageID: m.ID, SenderID: m.SenderID, SenderName: m.SenderName, SenderKind: m.SenderKind, Body: m.Body, Attachments: m.Attachments, Mentions: m.Mentions, ReplyToID: m.ReplyToID, Seq: m.Seq, CreatedAt: m.CreatedAt})
	}
	if unreadUpdated {
		if err = presence.Persist(ctx, e.Store, l); err != nil {
			return nil, err
		}
	}
	if err = e.Store.Ingest(ctx, l, page.Cursor, inputs); err != nil {
		return nil, err
	}
	for _, input := range inputs {
		observability.L(ctx).Info("Agent input persisted", zap.String("event.action", "agent-input-persisted"), zap.String("custom.agent.id", l.AgentID), zap.String("custom.input.event_id", input.EventID), zap.String("custom.im.message_id", input.MessageID), zap.String("custom.im.conversation_id", input.ConversationID))
	}
	return inputs, nil
}
