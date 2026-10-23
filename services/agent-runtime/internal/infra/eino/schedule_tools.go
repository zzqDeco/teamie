package eino

import (
	"context"
	"errors"
	"time"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"
	"github.com/cloudwego/eino/compose"
	"go.uber.org/zap"
	"teamie/services/agent-runtime/internal/domain"
	"teamie/pkg/observability"
)

const (
	maxActiveSchedulesPerAgent = domain.MaxActiveSchedulesPerAgent
	minScheduleDelaySeconds    = domain.MinScheduleDelaySeconds
	minScheduleIntervalSeconds = domain.MinScheduleIntervalSeconds
	maxScheduleIntervalSeconds = domain.MaxScheduleIntervalSeconds
	maxScheduleHorizon         = domain.MaxScheduleHorizon
	maxScheduleRuns            = domain.MaxScheduleRuns
	maxSchedulePromptRunes     = domain.MaxSchedulePromptRunes
)

const scheduleWakeupGuidance = `
Scheduled wakeups and deterministic clock
Every input envelope (TEAMIE_NEW_MESSAGE, TEAMIE_TASK_NOTIFICATION, TEAMIE_SCHEDULE_WAKEUP) includes a server-authored UTC RFC3339 createdAt timestamp. When you need the current wall-clock time, local timezone conversion or weekday, call runtime_clock.
Use schedule_create to register a durable future or recurring self-wakeup (kind: "once", "interval", or "cron"), schedule_list to inspect your active schedules, and schedule_cancel to cancel a schedule you no longer need. Never use workspace_execute sleep loops or polling to wait for a future time.
When woken by a TEAMIE_SCHEDULE_WAKEUP envelope, its body contains the self-scheduled prompt and scheduleId identifies the firing schedule. Waking up does not automatically send any IM message; send an IM message only if the scheduled task requires notifying a conversation and current destination access permits it.
`

func scheduledWakeupsForRun(run *Run) bool {
	if run == nil || contextProfile(run) != contextProfileV1 || run.toolProfile == v034ToolProfile {
		return false
	}
	budget, err := ParseContextBudgetSnapshot(run.ContextBudget)
	return err == nil && budget.ScheduledWakeups
}

func stableScheduleID(run *Run, call string) string {
	return domain.StableScheduleID(run.AgentID, run.ID, call)
}

type runtimeClockArgs struct {
	Timezone string `json:"timezone,omitempty" jsonschema:"description=Optional IANA timezone name (for example UTC, Asia/Shanghai, America/New_York; default UTC)"`
}

type scheduleCreateArgs struct {
	Kind            string `json:"kind" jsonschema:"required,description=Schedule mode: once, interval, or cron"`
	DelaySeconds    int64  `json:"delaySeconds,omitempty" jsonschema:"description=For kind=once: relative delay in seconds from now (1 to 2592000)"`
	RunAt           string `json:"runAt,omitempty" jsonschema:"description=For kind=once: RFC3339 future timestamp (mutually exclusive with delaySeconds)"`
	IntervalSeconds int64  `json:"intervalSeconds,omitempty" jsonschema:"description=For kind=interval: recurring interval in seconds (60 to 2592000)"`
	CronExpr        string `json:"cronExpr,omitempty" jsonschema:"description=For kind=cron: 5-field cron expression (minute hour dom month dow)"`
	Timezone        string `json:"timezone,omitempty" jsonschema:"description=Optional IANA timezone for kind=cron (default UTC)"`
	Prompt          string `json:"prompt" jsonschema:"required,description=Self-wakeup instruction or heartbeat payload delivered in TEAMIE_SCHEDULE_WAKEUP (1 to 4000 characters)"`
	MaxRuns         int    `json:"maxRuns,omitempty" jsonschema:"description=Optional maximum wakeup count for interval or cron schedules (1 to 10000)"`
}

type scheduleListArgs struct{}

type scheduleCancelArgs struct {
	ScheduleID string `json:"scheduleId" jsonschema:"required,description=ID of the schedule to cancel"`
}

type scheduleView = domain.ScheduleItem

func resolveScheduleTimezone(raw string) (*time.Location, string, error) {
	return domain.ResolveScheduleTimezone(raw)
}

func nextCronTime(expr, tzName string, after time.Time) (time.Time, error) {
	return domain.NextCronTime(expr, tzName, after)
}

func (t *Tools) BuildScheduleTools() ([]tool.BaseTool, error) {
	clockTool, err := utils.InferTool("runtime_clock", "Return the current server UTC and local time, timezone offset, weekday and unix timestamp. Call this whenever you need current wall-clock time or local timezone alignment.", func(ctx context.Context, args *runtimeClockArgs) (*Observation, error) {
		run, err := CurrentRun(ctx)
		if err != nil {
			return nil, err
		}
		if !scheduledWakeupsForRun(run) {
			return nil, errors.New("schedule_not_admitted")
		}
		return t.execute(ctx, "runtime_clock", args, func(ctx context.Context, _ string) (*Observation, error) {
			if t.FlagsFile != "" && !runtimeFlag(t.FlagsFile, "agents.schedule.v1") {
				return &Observation{Error: "schedule_disabled"}, nil
			}
			tzInput := ""
			if args != nil {
				tzInput = args.Timezone
			}
			loc, tzName, err := resolveScheduleTimezone(tzInput)
			if err != nil {
				return &Observation{Error: "invalid_timezone"}, nil
			}
			now, err := t.Store.Repo().ClockNow(ctx)
			if err != nil {
				return nil, err
			}
			nowUTC := now.UTC()
			local := now.In(loc)
			_, offset := local.Zone()
			return &Observation{
				OK: true,
				Data: map[string]any{
					"utc":           nowUTC.Format(time.RFC3339),
					"local":         local.Format(time.RFC3339),
					"timezone":      tzName,
					"offsetSeconds": offset,
					"weekday":       local.Weekday().String(),
					"unixSeconds":   nowUTC.Unix(),
				},
			}, nil
		})
	}, utils.WithMarshalOutput(marshalObservation))
	if err != nil {
		return nil, err
	}

	createTool, err := utils.InferTool("schedule_create", "Create a durable self-scheduled wakeup or recurring automation (once, interval, or cron). Runtime will wake the Session with TEAMIE_SCHEDULE_WAKEUP when due.", func(ctx context.Context, args *scheduleCreateArgs) (*Observation, error) {
		run, err := CurrentRun(ctx)
		if err != nil {
			return nil, err
		}
		if !scheduledWakeupsForRun(run) {
			return nil, errors.New("schedule_not_admitted")
		}
		return t.execute(ctx, "schedule_create", args, func(ctx context.Context, _ string) (*Observation, error) {
			if t.FlagsFile != "" && !runtimeFlag(t.FlagsFile, "agents.schedule.v1") {
				return &Observation{Error: "schedule_disabled"}, nil
			}
			if args == nil {
				return &Observation{Error: "invalid_arguments"}, nil
			}
			archive := &ContextArchive{Store: t.Store, Lease: t.Lease, IM: t.IM, Authorize: t.Authorize}
			callID := compose.GetToolCallID(ctx)
			created, code, err := t.Store.ScheduleCoordinator(archive).EvaluateScheduleCreation(ctx, &domain.Lease{
				AgentID: run.AgentID,
				Owner:   t.Lease.Owner,
				Fence:   t.Lease.Fence,
			}, run.ID, callID, toDomainInputs(run.Inputs), domain.ScheduleSpec(*args))
			if err != nil {
				return nil, err
			}
			if code != "" {
				return &Observation{Error: code}, nil
			}
			return &Observation{OK: true, Data: map[string]any{"schedule": *created}}, nil
		})
	}, utils.WithMarshalOutput(marshalObservation))
	if err != nil {
		return nil, err
	}

	listTool, err := utils.InferTool("schedule_list", "List this Agent's active self-scheduled wakeups ordered by nextRunAt.", func(ctx context.Context, args *scheduleListArgs) (*Observation, error) {
		run, err := CurrentRun(ctx)
		if err != nil {
			return nil, err
		}
		if !scheduledWakeupsForRun(run) {
			return nil, errors.New("schedule_not_admitted")
		}
		return t.execute(ctx, "schedule_list", args, func(ctx context.Context, _ string) (*Observation, error) {
			if t.FlagsFile != "" && !runtimeFlag(t.FlagsFile, "agents.schedule.v1") {
				return &Observation{Error: "schedule_disabled"}, nil
			}
			candidates, err := t.Store.Repo().ListActiveScheduleCandidates(ctx, run.AgentID, maxActiveSchedulesPerAgent)
			if err != nil {
				return nil, err
			}
			archive := &ContextArchive{Store: t.Store, Lease: t.Lease, IM: t.IM, Authorize: t.Authorize}
			schedules, err := t.Store.ScheduleCoordinator(archive).FilterAuthorizedSchedules(ctx, run.AgentID, candidates)
			if err != nil {
				return nil, err
			}
			return &Observation{OK: true, Data: map[string]any{"schedules": schedules}}, nil
		})
	}, utils.WithMarshalOutput(marshalObservation))
	if err != nil {
		return nil, err
	}

	cancelTool, err := utils.InferTool("schedule_cancel", "Cancel an active schedule owned by this Agent.", func(ctx context.Context, args *scheduleCancelArgs) (*Observation, error) {
		run, err := CurrentRun(ctx)
		if err != nil {
			return nil, err
		}
		if !scheduledWakeupsForRun(run) {
			return nil, errors.New("schedule_not_admitted")
		}
		return t.execute(ctx, "schedule_cancel", args, func(ctx context.Context, _ string) (*Observation, error) {
			if t.FlagsFile != "" && !runtimeFlag(t.FlagsFile, "agents.schedule.v1") {
				return &Observation{Error: "schedule_disabled"}, nil
			}
			if args == nil {
				return &Observation{Error: "invalid_arguments"}, nil
			}
			id, ok := domain.ValidateScheduleCancelID(args.ScheduleID)
			if !ok {
				return &Observation{Error: "invalid_arguments"}, nil
			}
			status, found, err := t.Store.Repo().CancelScheduleWithLease(ctx, &domain.Lease{
				AgentID: run.AgentID,
				Owner:   t.Lease.Owner,
				Fence:   t.Lease.Fence,
			}, id)
			if err != nil {
				return nil, err
			}
			if !found {
				return &Observation{Error: "not_found"}, nil
			}
			return &Observation{OK: true, Data: map[string]any{"scheduleId": id, "status": status}}, nil
		})
	}, utils.WithMarshalOutput(marshalObservation))
	if err != nil {
		return nil, err
	}

	return []tool.BaseTool{clockTool, createTool, listTool, cancelTool}, nil
}

func (e *Engine) drainSchedules(ctx context.Context, l *Lease) (failure error) {
	if !runtimeFlag(e.Config.FlagsFile, "agents.schedule.v1") {
		return nil
	}
	ctx, operation := observability.Begin(ctx, "schedule-wakeup-drain", zap.String("custom.agent.id", l.AgentID))
	defer func() { operation.EndQuiet(failure) }()
	logs, err := e.Store.DB.DrainDueSchedules(ctx, l)
	if err != nil {
		return err
	}
	for _, item := range logs {
		observability.L(ctx).Info("Schedule wakeup persisted",
			zap.Bool("custom.input.inserted", item.Inserted),
			zap.String("custom.schedule.notification_id", observability.ID(item.NotificationID)),
			zap.String("event.action", "schedule-wakeup-persisted"),
			zap.String("custom.agent.id", l.AgentID),
			zap.String("custom.schedule.id", observability.ID(item.ScheduleID)),
			zap.String("custom.input.event_id", item.EventID),
			zap.String("custom.runtime.parent_run_id", item.ParentRunID),
		)
	}
	return nil
}

func (e *Engine) authorizeScheduleInput(ctx context.Context, imc *IM, lease *Lease, in Input) error {
	archive := &ContextArchive{Store: e.Store, Lease: lease, IM: imc}
	return e.Store.ScheduleCoordinator(archive).AuthorizeScheduleInput(ctx, lease, toDomainInput(in))
}

func (a *ContextArchive) scheduleRowSources(ctx context.Context, createdRunID string, sourceRefsJSON []byte, sourceOverflow bool) ([]InputSource, error) {
	return a.Store.ScheduleCoordinator(a).ResolveScheduleRowSources(ctx, a.Lease.AgentID, createdRunID, sourceRefsJSON, sourceOverflow)
}

func (a *ContextArchive) scheduleSourcesByID(ctx context.Context, agentID, scheduleID string) ([]InputSource, error) {
	if scheduleID == "" || !validUUID(agentID) {
		return nil, errContextUnavailable
	}
	createdRunID, sourceRefsJSON, sourceOverflow, found, err := a.Store.Repo().GetScheduleSourcesMeta(ctx, agentID, scheduleID)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, errContextUnavailable
	}
	return a.scheduleRowSources(ctx, createdRunID, sourceRefsJSON, sourceOverflow)
}

func (a *ContextArchive) authorizeScheduleListReceipt(ctx context.Context, agentID string, cached []byte) error {
	return a.Store.ScheduleCoordinator(a).AuthorizeScheduleListReceipt(ctx, agentID, cached)
}
