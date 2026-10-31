package eino

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"slices"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/adk/middlewares/summarization"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
	"go.uber.org/zap"
	"teamie/pkg/observability"
)

func (w *agentWorker) makeAgent(ctx context.Context) (adk.TypedAgent[*schema.AgenticMessage], error) {
	run, err := CurrentRun(ctx)
	if err != nil {
		return nil, err
	}
	imToolBuilder := &Tools{
		Store:            w.engine.Store,
		Lease:            w.lease,
		IM:               w.imc,
		Sandbox:          w.engine.Sandbox,
		SandboxToken:     w.engine.Config.SandboxToken,
		GatewayStreamURL: w.engine.Config.GatewayStreamURL,
		GatewayToken:     w.engine.Config.GatewayToken,
		OwnerID:          w.def.OwnerId,
		AgentID:          w.def.Id,
		FlagsFile:        w.engine.Config.FlagsFile,
		Authorize:        w.authorize,
	}
	if w.engine.presenceEnabled() || run.resume != nil && run.resume.Presence != nil && run.resume.Presence.Enabled {
		imToolBuilder.Presence = w.presence
	}
	presenceActive, err := imToolBuilder.initRunPresence(ctx, run)
	if err != nil {
		return nil, err
	}
	if presenceActive {
		imToolBuilder.Presence = w.presence
	} else {
		imToolBuilder.Presence = nil
	}

	availableTools := w.tools
	if presenceActive {
		if mentionsForRun(run) {
			availableTools, err = imToolBuilder.BuildIMWithMentions(fileDeliveryForRun(run))
		} else if fileDeliveryForRun(run) {
			availableTools, err = imToolBuilder.BuildIMWithFiles()
		} else {
			availableTools, err = imToolBuilder.BuildIM()
		}
		if err != nil {
			return nil, err
		}
	} else {
		if fileDeliveryForRun(run) {
			availableTools, err = imToolBuilder.BuildIMWithFiles()
			if err != nil {
				return nil, err
			}
		}
		if mentionsForRun(run) {
			availableTools, err = imToolBuilder.BuildIMWithMentions(fileDeliveryForRun(run))
			if err != nil {
				return nil, err
			}
		}
	}
	activeTools, err := toolsForRun(ctx, availableTools, run)
	if err != nil {
		return nil, err
	}
	var (
		mcpTools       []tool.BaseTool
		presenceTools  []tool.BaseTool
		unknownHandler func(context.Context, string, string) (string, error)
	)
	if presenceActive {
		presenceTools, err = imToolBuilder.BuildPresenceTools()
		if err != nil {
			return nil, err
		}
		unknownHandler = imToolBuilder.HandleUnknownMCPTool
	} else {
		mcpTools, err = imToolBuilder.BuildMCPTools(ctx, run)
		if err != nil {
			return nil, err
		}
	}
	if contextProfile(run) == contextProfileV1 {
		budget, err := ParseContextBudgetSnapshot(run.ContextBudget)
		if err != nil {
			return nil, err
		}
		archive := &ContextArchive{Store: w.engine.Store, Lease: w.lease, IM: w.imc, Authorize: w.authorize}
		contextTools, err := archive.Tools(ctx)
		if err != nil {
			return nil, err
		}
		pageTool, err := w.tasks.backend.ReadPageTool()
		if err != nil {
			return nil, err
		}
		activeTools = append(activeTools, contextTools...)
		activeTools = append(activeTools, pageTool)
		if budget.WebSearch {
			webTools, err := imToolBuilder.BuildWebTools()
			if err != nil {
				return nil, err
			}
			activeTools = append(activeTools, webTools...)
		}
		if budget.ScheduledWakeups {
			scheduleTools, err := imToolBuilder.BuildScheduleTools(budget.DirectMessages)
			if err != nil {
				return nil, err
			}
			activeTools = append(activeTools, scheduleTools...)
		}
		if budget.DirectMessages {
			dmTools, err := imToolBuilder.BuildDirectMessageTools()
			if err != nil {
				return nil, err
			}
			activeTools = append(activeTools, dmTools...)
		}
		if presenceActive {
			activeTools = append(activeTools, presenceTools...)
		} else {
			activeTools = append(activeTools, mcpTools...)
		}
		pipeline, err := NewContextPipeline(ctx, ContextPipelineConfig{Model: w.gatewayModel, Budget: budget, Backend: archive, ArchivePath: archive.Path, ArchiveToolPath: archive.ArchiveToolPath, ArchiveTranscript: archive.ArchiveTranscript})
		if err != nil {
			return nil, err
		}
		handlers := []adk.TypedChatModelAgentMiddleware[*schema.AgenticMessage]{}
		if presenceActive {
			handlers = append(handlers, imToolBuilder.NewMCPDynamicToolMiddleware())
		}
		handlers = append(handlers, pipeline)
		handlers = append(handlers, w.tasks.handlers...)
		instruction := contextInstruction(w.def, budget)
		if budget.FileDelivery {
			instruction += fileDeliveryGuidance
		}
		if budget.StructuredMentions {
			instruction += mentionGuidance
		}
		if budget.WebSearch {
			instruction += webSearchGuidance
		}
		if budget.ScheduledWakeups {
			if budget.DirectMessages {
				instruction += autonomousScheduleGuidance
			} else {
				instruction += scheduleWakeupGuidance
			}
		}
		if budget.DirectMessages {
			instruction += directMessageGuidance
		}
		if presenceActive {
			instruction += presenceAndMarketplaceGuidance
		} else if len(mcpTools) > 0 {
			instruction += marketplaceAppGuidance
		}
		return adk.NewTypedChatModelAgent(ctx, &adk.TypedChatModelAgentConfig[*schema.AgenticMessage]{Name: "teamie_agent", Description: "A user-owned Teamie participant", Instruction: instruction, Model: w.gatewayModel, ToolsConfig: adk.ToolsConfig{ToolsNodeConfig: compose.ToolsNodeConfig{Tools: activeTools, ExecuteSequentially: true, UnknownToolsHandler: unknownHandler}}, MaxIterations: math.MaxInt, Handlers: handlers, ModelRetryConfig: pipeline.RetryConfig()})
	}
	if presenceActive {
		activeTools = append(activeTools, presenceTools...)
	} else {
		activeTools = append(activeTools, mcpTools...)
	}
	legacyInstruction := instruction(w.def)
	if presenceActive {
		legacyInstruction += presenceAndMarketplaceGuidance
	} else if len(mcpTools) > 0 {
		legacyInstruction += marketplaceAppGuidance
	}
	trigger := summaryTrigger(int(w.contextWindow.Load()))
	finalize := finalizeSummary
	if run.toolProfile == v034ToolProfile {
		finalize = finalizeV034Summary
	}
	summaryFailovers := 1
	summary, err := summarization.NewTyped(ctx, &summarization.TypedConfig[*schema.AgenticMessage]{
		Model:           w.gatewayModel,
		ModelOptions:    []model.Option{model.WithAgenticToolChoice(&schema.AgenticToolChoice{Type: schema.ToolChoiceForbidden})},
		Trigger:         &summarization.TriggerCondition{ContextTokens: trigger, ContextMessages: 32},
		UserInstruction: summaryInstruction,
		Finalize:        finalize,
		Failover: &summarization.TypedFailoverConfig[*schema.AgenticMessage]{
			MaxRetries:     &summaryFailovers,
			ShouldFailover: retryEmptyLengthSummary,
		},
	})
	if err != nil {
		return nil, err
	}
	handlers := []adk.TypedChatModelAgentMiddleware[*schema.AgenticMessage]{}
	if presenceActive {
		handlers = append(handlers, imToolBuilder.NewMCPDynamicToolMiddleware())
	}
	handlers = append(handlers, summary)
	handlers = append(handlers, w.tasks.handlers...)
	return adk.NewTypedChatModelAgent(ctx, &adk.TypedChatModelAgentConfig[*schema.AgenticMessage]{Name: "teamie_agent", Description: "A user-owned Teamie participant", Instruction: legacyInstruction, Model: w.gatewayModel, ToolsConfig: adk.ToolsConfig{ToolsNodeConfig: compose.ToolsNodeConfig{Tools: activeTools, ExecuteSequentially: true, UnknownToolsHandler: unknownHandler}}, MaxIterations: math.MaxInt, Handlers: handlers})
}

func (w *agentWorker) validate(ctx context.Context, inputs []Input) ([]Input, error) {
	valid := []Input{}
	for _, in := range inputs {
		if in.Kind == "task.notification" {
			valid = append(valid, in)
			continue
		}
		if in.Kind == "schedule.wakeup" {
			if err := w.engine.authorizeScheduleInput(ctx, w.imc, w.lease, in); err != nil {
				if errors.Is(err, errContextUnavailable) {
					if in.ScheduleID != "" {
						_ = w.engine.Store.DB.CancelActiveSchedule(ctx, w.lease.AgentID, in.ScheduleID)
					}
					if err = w.engine.Store.Skip(ctx, w.lease, in.EventID); err != nil {
						return nil, err
					}
					continue
				}
				return nil, err
			}
			valid = append(valid, in)
			continue
		}
		if in.Kind == "app.notification" {
			if err := w.engine.authorizeAppNotificationInput(ctx, w.imc, w.lease, in); err != nil {
				if errors.Is(err, errContextUnavailable) {
					if err = w.engine.Store.Skip(ctx, w.lease, in.EventID); err != nil {
						return nil, err
					}
					continue
				}
				return nil, err
			}
			valid = append(valid, in)
			continue
		}
		if in.Kind == "im.unread_digest" {
			if err := w.engine.authorizeUnreadDigestInput(ctx, w.imc, w.lease, in); err != nil {
				if errors.Is(err, errContextUnavailable) {
					if err = w.engine.Store.Skip(ctx, w.lease, in.EventID); err != nil {
						return nil, err
					}
					continue
				}
				return nil, err
			}
			valid = append(valid, in)
			continue
		}
		message, err := w.imc.Message(ctx, in.ConversationID, in.MessageID)
		if err != nil {
			var api *APIError
			if errors.As(err, &api) && (api.Status == 403 || api.Status == 404) {
				if err = w.engine.Store.Skip(ctx, w.lease, in.EventID); err != nil {
					return nil, err
				}
				continue
			}
			return nil, err
		}
		if message.RetractedAt != nil || message.Body == "" && len(message.Attachments) == 0 {
			if err = w.engine.Store.Skip(ctx, w.lease, in.EventID); err != nil {
				return nil, err
			}
			continue
		}
		in.Body = message.Body
		in.Attachments = message.Attachments
		in.Mentions = message.Mentions
		in.ReplyToID = message.ReplyToID
		valid = append(valid, in)
	}
	return valid, nil
}

func (w *agentWorker) prepare(ctx context.Context, items []Input, resume *checkpointMetadata) (*Run, context.Context, []Input, error) {
	if err := w.authorize(ctx); err != nil {
		return nil, nil, nil, err
	}
	if !runtimeFlag(w.engine.Config.FlagsFile, "agents.runtime.v3") {
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
		pending, err := w.engine.Store.Pending(ctx, w.lease)
		if err != nil {
			return nil, nil, nil, err
		}
		unique = pending
	}
	eligible, err := w.engine.Store.eligibleInputs(ctx, w.lease, unique)
	if err != nil {
		return nil, nil, nil, err
	}
	batch, extra := boundedInputBatch(eligible)
	valid, err := w.validate(ctx, batch)
	if err != nil {
		return nil, nil, nil, err
	}
	if len(valid) == 0 {
		return nil, nil, nil, errors.New("no_pending_inputs")
	}
	profile := contextProfileLegacy
	budget := json.RawMessage(`{}`)
	// An explicit enabled flag admits the new policy. Existing Runs restore
	// their persisted selection even when the flag or gateway changes.
	if w.engine.Config.FlagsFile != "" && runtimeFlag(w.engine.Config.FlagsFile, "agents.context.v1") {
		profile = contextProfileV1
		budget, err = newRunContextBudget(int(w.contextWindow.Load()), runtimeFlag(w.engine.Config.FlagsFile, "agents.files.deliver.v1"), runtimeFlag(w.engine.Config.FlagsFile, "agents.mentions.v1"), runtimeFlag(w.engine.Config.FlagsFile, "agents.web.v1"), runtimeFlag(w.engine.Config.FlagsFile, "agents.schedule.v1"), runtimeFlag(w.engine.Config.FlagsFile, "im.dm.create"), runtimeFlagDefault(w.engine.Config.FlagsFile, "agents.style.conversational.v1", true))
		if err != nil {
			return nil, nil, nil, err
		}
	}
	run, err := w.engine.Store.StartRunWithContextPolicy(ctx, w.lease, valid, profile, budget)
	if err != nil {
		return nil, nil, nil, err
	}
	invalid := []string{}
	for _, in := range run.Inputs {
		if in.Kind == "task.notification" {
			continue
		}
		if in.Kind == "schedule.wakeup" {
			if readErr := w.engine.authorizeScheduleInput(ctx, w.imc, w.lease, in); readErr != nil {
				if errors.Is(readErr, errContextUnavailable) {
					if in.ScheduleID != "" {
						_ = w.engine.Store.DB.CancelActiveSchedule(ctx, w.lease.AgentID, in.ScheduleID)
					}
					invalid = append(invalid, in.EventID)
					continue
				}
				_ = w.engine.Store.FinishRun(ctx, w.lease, run, "retry", "im_unavailable", true)
				return nil, nil, nil, &ModelError{Code: "im_unavailable", Retry: true}
			}
			continue
		}
		if in.Kind == "app.notification" {
			if readErr := w.engine.authorizeAppNotificationInput(ctx, w.imc, w.lease, in); readErr != nil {
				if errors.Is(readErr, errContextUnavailable) {
					invalid = append(invalid, in.EventID)
					continue
				}
				_ = w.engine.Store.FinishRun(ctx, w.lease, run, "retry", "im_unavailable", true)
				return nil, nil, nil, &ModelError{Code: "im_unavailable", Retry: true}
			}
			continue
		}
		if in.Kind == "im.unread_digest" {
			if readErr := w.engine.authorizeUnreadDigestInput(ctx, w.imc, w.lease, in); readErr != nil {
				if errors.Is(readErr, errContextUnavailable) {
					invalid = append(invalid, in.EventID)
					continue
				}
				_ = w.engine.Store.FinishRun(ctx, w.lease, run, "retry", "im_unavailable", true)
				return nil, nil, nil, &ModelError{Code: "im_unavailable", Retry: true}
			}
			continue
		}
		m, readErr := w.imc.Message(ctx, in.ConversationID, in.MessageID)
		if readErr != nil {
			var api *APIError
			if errors.As(readErr, &api) && (api.Status == 403 || api.Status == 404) {
				invalid = append(invalid, in.EventID)
				continue
			}
			_ = w.engine.Store.FinishRun(ctx, w.lease, run, "retry", "im_unavailable", true)
			return nil, nil, nil, &ModelError{Code: "im_unavailable", Retry: true}
		}
		if m.RetractedAt != nil || m.Body != in.Body || !slices.Equal(m.Attachments, in.Attachments) {
			invalid = append(invalid, in.EventID)
		}
	}
	if len(invalid) > 0 {
		if err = w.engine.Store.InvalidateRun(ctx, w.lease, run, invalid); err != nil {
			return nil, nil, nil, err
		}
		return nil, nil, nil, errors.New("source_unavailable")
	}
	run.toolProfile, err = w.engine.Store.runToolProfile(ctx, run.ID)
	if err != nil {
		return nil, nil, nil, err
	}
	resume, err = w.engine.Store.selectRunResume(ctx, resume, w.def.Revision, run)
	if err != nil {
		return nil, nil, nil, err
	}
	if err = w.engine.Store.configureRun(ctx, w.lease, run, w.def.Revision, w.generation, resume); err != nil {
		return nil, nil, nil, err
	}
	run.Front = &frontSlot{slots: w.engine.slots}
	runctx, diagnostic := observability.Begin(WithRun(ctx, run), "agent-run", zap.Int("custom.runtime.input_count", len(run.Inputs)), zap.Int64("custom.runtime.generation", w.generation))
	run.diagnostic = diagnostic
	inputIDs := []string{}
	parentRuns := []string{}
	for _, in := range run.Inputs {
		if len(inputIDs) < 32 {
			inputIDs = append(inputIDs, in.EventID)
		}
		if in.ParentRunID != "" && len(parentRuns) < 32 {
			parentRuns = append(parentRuns, in.ParentRunID)
		}
	}
	observability.L(runctx).Info("Agent run started", zap.String("event.action", "agent-run-started"), zap.Strings("custom.input.event_ids", inputIDs), zap.Strings("custom.runtime.parent_run_ids", parentRuns))
	runctx, cancel := context.WithCancel(runctx)
	w.guard.set(run, cancel, run.releaseFront)
	w.guard.onFinish(run, func() { diagnostic.End(runctx.Err(), zap.String("custom.runtime.state", "scope-closed")) })
	w.qmu.Lock()
	for _, in := range run.Inputs {
		w.queued[in.EventID] = w.generation
	}
	w.qmu.Unlock()
	w.session.bind(run, nil)
	if err = run.Front.acquire(runctx); err != nil {
		w.guard.finish()
		return nil, nil, nil, err
	}
	if w.engine.Config.IMRealtimeURL != "" {
		w.guard.onFinish(run, beginTyping(runctx, run.Inputs, w.imc.Typing, func(ctx context.Context) error {
			if !runtimeFlag(w.engine.Config.FlagsFile, "agents.runtime.v3") {
				return &ModelError{Code: "runtime_disabled", Retry: true}
			}
			if err := w.authorize(ctx); err != nil {
				return err
			}
			return w.engine.Store.CheckLease(ctx, w.lease)
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
	w.engine.report(runctx, w.def.Id, "running", "", true)
	return run, runctx, remaining, nil
}

func (w *agentWorker) makeInput(ctx context.Context, _ *adk.TurnLoop[Input, *schema.AgenticMessage], items []Input) (*adk.GenInputResult[Input, *schema.AgenticMessage], error) {
	run, runctx, remaining, err := w.prepare(ctx, items, nil)
	if err != nil {
		return nil, err
	}
	return &adk.GenInputResult[Input, *schema.AgenticMessage]{RunCtx: runctx, Input: &adk.TypedAgentInput[*schema.AgenticMessage]{Messages: inputMessages(run.Inputs), EnableStreaming: w.engine.Config.GatewayStreamURL != ""}, Consumed: run.Inputs, Remaining: remaining}, nil
}

func (w *agentWorker) onAgentEvents(ctx context.Context, tc *adk.TurnContext[Input, *schema.AgenticMessage], events *adk.AsyncIterator[*adk.TypedAgentEvent[*schema.AgenticMessage]]) (eventFailure error) {
	e, def, lease := w.engine, w.def, w.lease
	defer w.guard.finish()
	var failure error
	diagnosticState, preempted := "aborted", false
	currentRun, _ := CurrentRun(ctx)
	defer func() {
		if currentRun != nil && currentRun.diagnostic != nil {
			logFailure := failure
			if eventFailure != nil {
				logFailure = eventFailure
			}
			var cancelError *adk.CancelError
			if preempted && errors.As(logFailure, &cancelError) {
				logFailure = nil
			}
			currentRun.diagnostic.End(logFailure, zap.String("custom.runtime.state", diagnosticState), zap.Bool("custom.runtime.preempted", preempted), zap.String("custom.runtime.failure_code", failureCode(failure)))
		}
	}()
	var businessTargets []string
	for {
		event, ok := events.Next()
		if !ok {
			break
		}
		if event == nil {
			continue
		}
		if event.Err != nil && failure == nil && !modelRetryObservation(event.Err) {
			failure = event.Err
		}
		if event.Output != nil && event.Output.MessageOutput != nil && event.Output.MessageOutput.IsStreaming {
			_, err := event.Output.MessageOutput.GetMessage()
			if err != nil && failure == nil && !modelRetryObservation(err) {
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
	w.session.bind(run, targets)
	select {
	case <-tc.Preempted:
		preempted, diagnosticState = true, "interrupted"
		if err = e.Store.preemptRun(commit, lease, run); err != nil {
			return err
		}
		w.qmu.Lock()
		for _, in := range run.Inputs {
			delete(w.queued, in.EventID)
		}
		w.qmu.Unlock()
		return nil
	default:
	}
	if len(businessTargets) > 0 {
		diagnosticState = "waiting_input"
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
		diagnosticState = state
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
	diagnosticState = "completed"
	if err = e.Store.FinishRun(commit, lease, run, "completed", "", false); err != nil {
		return err
	}
	w.qmu.Lock()
	for _, in := range run.Inputs {
		delete(w.queued, in.EventID)
	}
	w.qmu.Unlock()
	e.report(commit, def.Id, "idle", "", true)
	return nil
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
