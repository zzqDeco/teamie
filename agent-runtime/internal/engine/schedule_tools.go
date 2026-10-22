package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"
	"github.com/cloudwego/eino/compose"
	"github.com/jackc/pgx/v5"
	"go.uber.org/zap"
	"teamie/backend/pkg/observability"
)

const (
	maxActiveSchedulesPerAgent = 32
	minScheduleDelaySeconds    = 1
	minScheduleIntervalSeconds = 60
	maxScheduleIntervalSeconds = 30 * 24 * 3600
	maxScheduleHorizon         = 365 * 24 * time.Hour
	maxScheduleRuns            = 10000
	maxSchedulePromptRunes     = 4000
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
	return "sched-" + digest([]byte(run.AgentID+"/"+run.ID+"/"+call))[:40]
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

type scheduleView struct {
	ScheduleID      string  `json:"scheduleId"`
	Kind            string  `json:"kind"`
	IntervalSeconds *int64  `json:"intervalSeconds,omitempty"`
	CronExpr        string  `json:"cronExpr,omitempty"`
	Timezone        string  `json:"timezone"`
	Prompt          string  `json:"prompt"`
	Status          string  `json:"status"`
	MaxRuns         *int    `json:"maxRuns,omitempty"`
	RunCount        int     `json:"runCount"`
	NextRunAt       string  `json:"nextRunAt"`
	LastRunAt       *string `json:"lastRunAt,omitempty"`
	CreatedAt       string  `json:"createdAt"`
}

func resolveScheduleTimezone(raw string) (*time.Location, string, error) {
	name := strings.TrimSpace(raw)
	if name == "" || strings.EqualFold(name, "UTC") {
		return time.UTC, "UTC", nil
	}
	if len(name) > 128 || strings.ContainsAny(name, "\r\n\t\x00") {
		return nil, "", errors.New("invalid_timezone")
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		return nil, "", errors.New("invalid_timezone")
	}
	return loc, loc.String(), nil
}

type cronSpec struct {
	minute      [60]bool
	hour        [24]bool
	dom         [32]bool
	month       [13]bool
	dow         [7]bool
	domWildcard bool
	dowWildcard bool
}

func parseCronField(field string, minVal, maxVal int, mapSevenToZero bool) ([]bool, bool, error) {
	bits := make([]bool, maxVal+1)
	if field == "*" {
		for v := minVal; v <= maxVal; v++ {
			if mapSevenToZero && v == 7 {
				bits[0] = true
			} else {
				bits[v] = true
			}
		}
		return bits, true, nil
	}
	parts := strings.Split(field, ",")
	if len(parts) == 0 || len(parts) > 32 {
		return nil, false, errors.New("invalid_cron_field")
	}
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			return nil, false, errors.New("invalid_cron_field")
		}
		step := 1
		rangePart := part
		if idx := strings.IndexByte(part, '/'); idx >= 0 {
			rangePart = part[:idx]
			stepStr := part[idx+1:]
			parsedStep, err := strconv.Atoi(stepStr)
			if err != nil || parsedStep <= 0 || parsedStep > maxVal {
				return nil, false, errors.New("invalid_cron_field")
			}
			step = parsedStep
		}
		var start, end int
		if rangePart == "*" {
			start, end = minVal, maxVal
			if mapSevenToZero && end == 7 {
				end = 6
			}
		} else if dash := strings.IndexByte(rangePart, '-'); dash >= 0 {
			var err1, err2 error
			start, err1 = strconv.Atoi(rangePart[:dash])
			end, err2 = strconv.Atoi(rangePart[dash+1:])
			if err1 != nil || err2 != nil || start < minVal || end > maxVal || start > end {
				return nil, false, errors.New("invalid_cron_field")
			}
		} else {
			val, err := strconv.Atoi(rangePart)
			if err != nil || val < minVal || val > maxVal {
				return nil, false, errors.New("invalid_cron_field")
			}
			if idx := strings.IndexByte(part, '/'); idx >= 0 {
				return nil, false, errors.New("invalid_cron_field")
			}
			start, end = val, val
		}
		for v := start; v <= end; v += step {
			if mapSevenToZero && v == 7 {
				bits[0] = true
			} else {
				bits[v] = true
			}
		}
	}
	return bits, false, nil
}

func parseCronSpec(expr string) (*cronSpec, error) {
	trimmed := strings.TrimSpace(expr)
	if trimmed == "" || len(trimmed) > 128 {
		return nil, errors.New("invalid_cron_expression")
	}
	fields := strings.Fields(trimmed)
	if len(fields) != 5 {
		return nil, errors.New("invalid_cron_expression")
	}
	minutes, _, err := parseCronField(fields[0], 0, 59, false)
	if err != nil {
		return nil, err
	}
	hours, _, err := parseCronField(fields[1], 0, 23, false)
	if err != nil {
		return nil, err
	}
	doms, domWild, err := parseCronField(fields[2], 1, 31, false)
	if err != nil {
		return nil, err
	}
	months, _, err := parseCronField(fields[3], 1, 12, false)
	if err != nil {
		return nil, err
	}
	dows, dowWild, err := parseCronField(fields[4], 0, 7, true)
	if err != nil {
		return nil, err
	}
	spec := &cronSpec{domWildcard: domWild, dowWildcard: dowWild}
	copy(spec.minute[:], minutes[:60])
	copy(spec.hour[:], hours[:24])
	copy(spec.dom[:], doms[:32])
	copy(spec.month[:], months[:13])
	copy(spec.dow[:], dows[:7])
	return spec, nil
}

func matchCronDay(spec *cronSpec, dom, dow int) bool {
	domMatch := spec.dom[dom]
	dowMatch := spec.dow[dow]
	if spec.domWildcard && spec.dowWildcard {
		return true
	}
	if spec.domWildcard {
		return dowMatch
	}
	if spec.dowWildcard {
		return domMatch
	}
	return domMatch || dowMatch
}

func nextCronTime(expr, tzName string, after time.Time) (time.Time, error) {
	loc, _, err := resolveScheduleTimezone(tzName)
	if err != nil {
		return time.Time{}, err
	}
	spec, err := parseCronSpec(expr)
	if err != nil {
		return time.Time{}, err
	}
	t := after.UTC().Truncate(time.Minute).Add(time.Minute).In(loc)
	limit := t.AddDate(5, 0, 0)
	for !t.After(limit) {
		if !spec.month[int(t.Month())] {
			next := time.Date(t.Year(), t.Month()+1, 1, 0, 0, 0, 0, loc)
			if !next.After(t) {
				next = t.Add(time.Duration(60-t.Minute()) * time.Minute)
			}
			t = next
			continue
		}
		if !matchCronDay(spec, t.Day(), int(t.Weekday())) {
			next := time.Date(t.Year(), t.Month(), t.Day()+1, 0, 0, 0, 0, loc)
			if !next.After(t) {
				next = t.Add(time.Duration(60-t.Minute()) * time.Minute)
			}
			t = next
			continue
		}
		if !spec.hour[t.Hour()] {
			t = t.Add(time.Duration(60-t.Minute()) * time.Minute)
			continue
		}
		if !spec.minute[t.Minute()] {
			t = t.Add(time.Minute)
			continue
		}
		return t.UTC(), nil
	}
	return time.Time{}, errors.New("invalid_cron_expression")
}

func scanScheduleView(row pgx.Row) (scheduleView, error) {
	var (
		out             scheduleView
		intervalSeconds *int64
		cronExpr        string
		maxRuns         *int
		nextRunAt       time.Time
		lastRunAt       *time.Time
		createdAt       time.Time
	)
	err := row.Scan(
		&out.ScheduleID,
		&out.Kind,
		&intervalSeconds,
		&cronExpr,
		&out.Timezone,
		&out.Prompt,
		&out.Status,
		&maxRuns,
		&out.RunCount,
		&nextRunAt,
		&lastRunAt,
		&createdAt,
	)
	if err != nil {
		return scheduleView{}, err
	}
	out.IntervalSeconds = intervalSeconds
	out.CronExpr = cronExpr
	out.MaxRuns = maxRuns
	out.NextRunAt = nextRunAt.UTC().Format(time.RFC3339)
	if lastRunAt != nil {
		formatted := lastRunAt.UTC().Format(time.RFC3339)
		out.LastRunAt = &formatted
	}
	out.CreatedAt = createdAt.UTC().Format(time.RFC3339)
	return out, nil
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
			var now time.Time
			if err = t.Store.DB.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&now); err != nil {
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
			prompt := strings.TrimSpace(args.Prompt)
			if pLen := len([]rune(prompt)); pLen < 1 || pLen > maxSchedulePromptRunes || strings.ContainsAny(prompt, "\x00") {
				return &Observation{Error: "invalid_arguments"}, nil
			}
			_, tzName, err := resolveScheduleTimezone(args.Timezone)
			if err != nil {
				return &Observation{Error: "invalid_timezone"}, nil
			}
			tx, err := t.Store.DB.Begin(ctx)
			if err != nil {
				return nil, err
			}
			defer tx.Rollback(ctx)
			if err = checkLease(ctx, tx, t.Lease); err != nil {
				return nil, err
			}
			callID := compose.GetToolCallID(ctx)
			schedID := stableScheduleID(run, callID)
			var savedSnapshot []byte
			if obsErr := tx.QueryRow(ctx, `SELECT response FROM runtime_tool_observations WHERE run_id=$1 AND call_id=$2 AND observation_key='schedule-create-v1'`, run.ID, callID).Scan(&savedSnapshot); obsErr == nil {
				var recovered scheduleView
				if json.Unmarshal(savedSnapshot, &recovered) == nil && recovered.ScheduleID == schedID {
					if err = tx.Commit(ctx); err != nil {
						return nil, err
					}
					return &Observation{OK: true, Data: map[string]any{"schedule": recovered}}, nil
				}
			}
			existing, getErr := scanScheduleView(tx.QueryRow(ctx, `SELECT id,kind,interval_seconds,cron_expr,timezone,prompt,status,max_runs,run_count,next_run_at,last_run_at,created_at FROM runtime_schedules WHERE id=$1 AND agent_id=$2`, schedID, run.AgentID))
			if getErr == nil {
				if err = tx.Commit(ctx); err != nil {
					return nil, err
				}
				return &Observation{OK: true, Data: map[string]any{"schedule": existing}}, nil
			}
			if !errors.Is(getErr, pgx.ErrNoRows) {
				return nil, getErr
			}

			var now time.Time
			if err = tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&now); err != nil {
				return nil, err
			}
			kind := strings.TrimSpace(args.Kind)
			runAtRaw := strings.TrimSpace(args.RunAt)
			cronExpr := strings.TrimSpace(args.CronExpr)
			var (
				nextRunAt       time.Time
				intervalSeconds *int64
				maxRuns         *int
			)
			switch kind {
			case "once":
				if (args.DelaySeconds > 0 && runAtRaw != "") || (args.DelaySeconds <= 0 && runAtRaw == "") || args.IntervalSeconds != 0 || cronExpr != "" || args.MaxRuns < 0 || args.MaxRuns > 1 {
					return &Observation{Error: "invalid_arguments"}, nil
				}
				if args.DelaySeconds > 0 {
					if args.DelaySeconds < minScheduleDelaySeconds || args.DelaySeconds > maxScheduleIntervalSeconds {
						return &Observation{Error: "invalid_arguments"}, nil
					}
					nextRunAt = now.UTC().Add(time.Duration(args.DelaySeconds) * time.Second)
				} else {
					parsed, parseErr := time.Parse(time.RFC3339Nano, runAtRaw)
					if parseErr != nil || !parsed.After(now) || parsed.After(now.Add(maxScheduleHorizon)) {
						return &Observation{Error: "invalid_arguments"}, nil
					}
					nextRunAt = parsed.UTC()
				}
				one := 1
				maxRuns = &one
			case "interval":
				if args.IntervalSeconds < minScheduleIntervalSeconds || args.IntervalSeconds > maxScheduleIntervalSeconds || args.DelaySeconds != 0 || runAtRaw != "" || cronExpr != "" || args.MaxRuns < 0 || args.MaxRuns > maxScheduleRuns {
					return &Observation{Error: "invalid_arguments"}, nil
				}
				iv := args.IntervalSeconds
				intervalSeconds = &iv
				if args.MaxRuns > 0 {
					mr := args.MaxRuns
					maxRuns = &mr
				}
				nextRunAt = now.UTC().Add(time.Duration(args.IntervalSeconds) * time.Second)
			case "cron":
				if cronExpr == "" || args.DelaySeconds != 0 || runAtRaw != "" || args.IntervalSeconds != 0 || args.MaxRuns < 0 || args.MaxRuns > maxScheduleRuns {
					return &Observation{Error: "invalid_arguments"}, nil
				}
				next, cronErr := nextCronTime(cronExpr, tzName, now)
				if cronErr != nil {
					return &Observation{Error: "invalid_cron_expression"}, nil
				}
				cronExpr = strings.Join(strings.Fields(cronExpr), " ")
				if args.MaxRuns > 0 {
					mr := args.MaxRuns
					maxRuns = &mr
				}
				nextRunAt = next.UTC()
			default:
				return &Observation{Error: "invalid_arguments"}, nil
			}
			var activeCount int
			if err = tx.QueryRow(ctx, `SELECT count(*) FROM runtime_schedules WHERE agent_id=$1 AND status='active'`, run.AgentID).Scan(&activeCount); err != nil {
				return nil, err
			}
			if activeCount >= maxActiveSchedulesPerAgent {
				return &Observation{Error: "schedule_limit_exceeded"}, nil
			}
			refs, overflow := inputSources(run.Inputs)
			if overflow || len(refs) == 0 {
				resolved, srcErr := (&ContextArchive{Store: t.Store, Lease: t.Lease, IM: t.IM, Authorize: t.Authorize}).runSources(ctx, run.ID)
				if srcErr == nil && len(resolved) > 0 {
					refs, overflow = resolved, false
				}
			}
			refsJSON, _ := json.Marshal(refs)
			created, err := scanScheduleView(tx.QueryRow(ctx, `INSERT INTO runtime_schedules(id,agent_id,created_run_id,kind,interval_seconds,cron_expr,timezone,prompt,status,max_runs,run_count,next_run_at,source_refs,source_overflow,created_at,updated_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,'active',$9,0,$10,$11,$12,$13,$13) RETURNING id,kind,interval_seconds,cron_expr,timezone,prompt,status,max_runs,run_count,next_run_at,last_run_at,created_at`,
				schedID, run.AgentID, run.ID, kind, intervalSeconds, cronExpr, tzName, prompt, maxRuns, nextRunAt, refsJSON, overflow, now.UTC(),
			))
			if err != nil {
				return nil, err
			}
			snapJSON, _ := json.Marshal(created)
			if _, err = tx.Exec(ctx, `INSERT INTO runtime_tool_observations(run_id,call_id,observation_key,response) VALUES($1,$2,'schedule-create-v1',$3) ON CONFLICT DO NOTHING`, run.ID, callID, snapJSON); err != nil {
				return nil, err
			}
			if err = tx.Commit(ctx); err != nil {
				return nil, err
			}
			return &Observation{OK: true, Data: map[string]any{"schedule": created}}, nil
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
			rows, err := t.Store.DB.Query(ctx, `SELECT id,COALESCE(created_run_id::text,''),kind,interval_seconds,cron_expr,timezone,prompt,status,max_runs,run_count,next_run_at,last_run_at,source_refs,source_overflow,created_at FROM runtime_schedules WHERE agent_id=$1 AND status='active' ORDER BY next_run_at ASC, id ASC LIMIT $2`, run.AgentID, maxActiveSchedulesPerAgent)
			if err != nil {
				return nil, err
			}
			type scheduleCandidate struct {
				view           scheduleView
				createdRunID   string
				sourceRefsJSON []byte
				sourceOverflow bool
			}
			candidates := []scheduleCandidate{}
			for rows.Next() {
				var (
					cand            scheduleCandidate
					intervalSeconds *int64
					cronExpr        string
					maxRuns         *int
					nextRunAt       time.Time
					lastRunAt       *time.Time
					createdAt       time.Time
				)
				if err = rows.Scan(&cand.view.ScheduleID, &cand.createdRunID, &cand.view.Kind, &intervalSeconds, &cronExpr, &cand.view.Timezone, &cand.view.Prompt, &cand.view.Status, &maxRuns, &cand.view.RunCount, &nextRunAt, &lastRunAt, &cand.sourceRefsJSON, &cand.sourceOverflow, &createdAt); err != nil {
					rows.Close()
					return nil, err
				}
				cand.view.IntervalSeconds = intervalSeconds
				cand.view.CronExpr = cronExpr
				cand.view.MaxRuns = maxRuns
				cand.view.NextRunAt = nextRunAt.UTC().Format(time.RFC3339)
				if lastRunAt != nil {
					formatted := lastRunAt.UTC().Format(time.RFC3339)
					cand.view.LastRunAt = &formatted
				}
				cand.view.CreatedAt = createdAt.UTC().Format(time.RFC3339)
				candidates = append(candidates, cand)
			}
			err = rows.Err()
			rows.Close()
			if err != nil {
				return nil, err
			}
			archive := &ContextArchive{Store: t.Store, Lease: t.Lease, IM: t.IM, Authorize: t.Authorize}
			schedules := []scheduleView{}
			for _, cand := range candidates {
				refs, srcErr := archive.scheduleRowSources(ctx, cand.createdRunID, cand.sourceRefsJSON, cand.sourceOverflow)
				if srcErr == nil {
					srcErr = archive.authorizeSources(ctx, refs)
				}
				if srcErr != nil {
					if errors.Is(srcErr, errContextUnavailable) {
						_, _ = t.Store.DB.Exec(ctx, `UPDATE runtime_schedules SET status='canceled',updated_at=now() WHERE id=$1 AND agent_id=$2 AND status='active'`, cand.view.ScheduleID, run.AgentID)
						continue
					}
					return nil, srcErr
				}
				schedules = append(schedules, cand.view)
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
			id := strings.TrimSpace(args.ScheduleID)
			if id == "" || len(id) > 128 || strings.ContainsAny(id, "\r\n\t\x00") {
				return &Observation{Error: "invalid_arguments"}, nil
			}
			tx, err := t.Store.DB.Begin(ctx)
			if err != nil {
				return nil, err
			}
			defer tx.Rollback(ctx)
			if err = checkLease(ctx, tx, t.Lease); err != nil {
				return nil, err
			}
			var status string
			err = tx.QueryRow(ctx, `SELECT status FROM runtime_schedules WHERE id=$1 AND agent_id=$2 FOR UPDATE`, id, run.AgentID).Scan(&status)
			if errors.Is(err, pgx.ErrNoRows) {
				return &Observation{Error: "not_found"}, nil
			}
			if err != nil {
				return nil, err
			}
			if status == "active" {
				if _, err = tx.Exec(ctx, `UPDATE runtime_schedules SET status='canceled',updated_at=now() WHERE id=$1 AND agent_id=$2`, id, run.AgentID); err != nil {
					return nil, err
				}
				status = "canceled"
			}
			if err = tx.Commit(ctx); err != nil {
				return nil, err
			}
			return &Observation{OK: true, Data: map[string]any{"scheduleId": id, "status": status}}, nil
		})
	}, utils.WithMarshalOutput(marshalObservation))
	if err != nil {
		return nil, err
	}

	return []tool.BaseTool{clockTool, createTool, listTool, cancelTool}, nil
}

type dueScheduleRecord struct {
	id              string
	createdRunID    string
	kind            string
	intervalSeconds *int64
	cronExpr        string
	timezone        string
	prompt          string
	maxRuns         *int
	runCount        int
	nextRunAt       time.Time
	sourceRefsJSON  []byte
	sourceOverflow  bool
}

type drainedScheduleLog struct {
	scheduleID     string
	notificationID string
	eventID        string
	parentRunID    string
	inserted       bool
}

func (e *Engine) drainSchedules(ctx context.Context, l *Lease) (failure error) {
	if !runtimeFlag(e.Config.FlagsFile, "agents.schedule.v1") {
		return nil
	}
	ctx, operation := observability.Begin(ctx, "schedule-wakeup-drain", zap.String("custom.agent.id", l.AgentID))
	defer func() { operation.EndQuiet(failure) }()
	tx, err := e.Store.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err = checkLease(ctx, tx, l); err != nil {
		return err
	}
	var now time.Time
	if err = tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&now); err != nil {
		return err
	}
	rows, err := tx.Query(ctx, `SELECT id,created_run_id::text,kind,interval_seconds,cron_expr,timezone,prompt,max_runs,run_count,next_run_at,source_refs,source_overflow FROM runtime_schedules WHERE agent_id=$1 AND status='active' AND next_run_at<=$2 ORDER BY next_run_at ASC, id ASC FOR UPDATE SKIP LOCKED LIMIT 16`, l.AgentID, now)
	if err != nil {
		return err
	}
	due := []dueScheduleRecord{}
	for rows.Next() {
		var r dueScheduleRecord
		if err = rows.Scan(&r.id, &r.createdRunID, &r.kind, &r.intervalSeconds, &r.cronExpr, &r.timezone, &r.prompt, &r.maxRuns, &r.runCount, &r.nextRunAt, &r.sourceRefsJSON, &r.sourceOverflow); err != nil {
			rows.Close()
			return err
		}
		due = append(due, r)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if len(due) == 0 {
		return tx.Commit(ctx)
	}
	logs := make([]drainedScheduleLog, 0, len(due))
	for _, r := range due {
		nextCount := r.runCount + 1
		var seq string
		if err = tx.QueryRow(ctx, `SELECT (90000000000000000000::numeric+nextval('runtime_notification_input_seq'))::text`).Scan(&seq); err != nil {
			return err
		}
		notificationID := fmt.Sprintf("schedule:%s:%d", r.id, nextCount)
		var refs []InputSource
		_ = json.Unmarshal(r.sourceRefsJSON, &refs)
		in := Input{
			EventID:        seq,
			Kind:           "schedule.wakeup",
			ScheduleID:     r.id,
			NotificationID: notificationID,
			Body:           r.prompt,
			CreatedAt:      now.UTC().Format(time.RFC3339Nano),
			ParentRunID:    r.createdRunID,
			SourceRefs:     refs,
			SourceOverflow: r.sourceOverflow,
		}
		payload, _ := json.Marshal(in)
		tag, err := tx.Exec(ctx, `INSERT INTO runtime_inputs(agent_id,event_id,payload,notification_id) VALUES($1,$2,$3,$4) ON CONFLICT DO NOTHING`, l.AgentID, seq, payload, notificationID)
		if err != nil {
			return err
		}
		inserted := tag.RowsAffected() > 0
		if !inserted {
			seq = ""
		}
		nextStatus := "active"
		nextRunAt := r.nextRunAt.UTC()
		if r.kind == "once" || (r.maxRuns != nil && nextCount >= *r.maxRuns) {
			nextStatus = "completed"
		} else if r.kind == "interval" && r.intervalSeconds != nil && *r.intervalSeconds > 0 {
			step := time.Duration(*r.intervalSeconds) * time.Second
			next := r.nextRunAt.Add(step)
			if !next.After(now) {
				missed := now.Sub(r.nextRunAt) / step
				next = r.nextRunAt.Add((missed + 1) * step)
			}
			nextRunAt = next.UTC()
		} else if r.kind == "cron" {
			next, cronErr := nextCronTime(r.cronExpr, r.timezone, now)
			if cronErr != nil {
				nextStatus = "completed"
			} else {
				nextRunAt = next.UTC()
			}
		} else {
			nextStatus = "completed"
		}
		if _, err = tx.Exec(ctx, `UPDATE runtime_schedules SET status=$3,run_count=$4,last_run_at=$5,next_run_at=$6,updated_at=$5 WHERE id=$1 AND agent_id=$2`, r.id, l.AgentID, nextStatus, nextCount, now.UTC(), nextRunAt); err != nil {
			return err
		}
		logs = append(logs, drainedScheduleLog{
			scheduleID:     r.id,
			notificationID: notificationID,
			eventID:        seq,
			parentRunID:    r.createdRunID,
			inserted:       inserted,
		})
	}
	if err = tx.Commit(ctx); err != nil {
		return err
	}
	for _, item := range logs {
		observability.L(ctx).Info("Schedule wakeup persisted",
			zap.Bool("custom.input.inserted", item.inserted),
			zap.String("custom.schedule.notification_id", observability.ID(item.notificationID)),
			zap.String("event.action", "schedule-wakeup-persisted"),
			zap.String("custom.agent.id", l.AgentID),
			zap.String("custom.schedule.id", observability.ID(item.scheduleID)),
			zap.String("custom.input.event_id", item.eventID),
			zap.String("custom.runtime.parent_run_id", item.parentRunID),
		)
	}
	return nil
}

func (e *Engine) authorizeScheduleInput(ctx context.Context, imc *IM, lease *Lease, in Input) error {
	if strings.TrimSpace(in.Body) == "" {
		return errContextUnavailable
	}
	if in.ScheduleID != "" {
		var status string
		err := e.Store.DB.QueryRow(ctx, `SELECT status FROM runtime_schedules WHERE id=$1 AND agent_id=$2`, in.ScheduleID, lease.AgentID).Scan(&status)
		if err == nil && status == "canceled" {
			return errContextUnavailable
		}
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
	}
	archive := &ContextArchive{Store: e.Store, Lease: lease, IM: imc}
	var (
		refs []InputSource
		err  error
	)
	if !in.SourceOverflow && len(in.SourceRefs) > 0 {
		refs, err = canonicalContextSources(in.SourceRefs)
	} else {
		parent := in.ParentRunID
		if parent == "" && in.ScheduleID != "" {
			var storedParent *string
			if qErr := e.Store.DB.QueryRow(ctx, `SELECT created_run_id::text FROM runtime_schedules WHERE id=$1 AND agent_id=$2`, in.ScheduleID, lease.AgentID).Scan(&storedParent); qErr == nil && storedParent != nil {
				parent = *storedParent
			}
		}
		if parent == "" {
			return errContextUnavailable
		}
		refs, err = archive.runSources(ctx, parent)
	}
	if err != nil {
		return err
	}
	if len(refs) == 0 {
		return errContextUnavailable
	}
	return archive.authorizeSources(ctx, refs)
}

func (a *ContextArchive) scheduleRowSources(ctx context.Context, createdRunID string, sourceRefsJSON []byte, sourceOverflow bool) ([]InputSource, error) {
	var refs []InputSource
	if len(sourceRefsJSON) > 0 {
		if json.Unmarshal(sourceRefsJSON, &refs) != nil {
			return nil, ErrReplayConflict
		}
	}
	if sourceOverflow || len(refs) == 0 {
		if !validUUID(createdRunID) {
			return nil, errContextUnavailable
		}
		return a.runSources(ctx, createdRunID)
	}
	canonical, err := canonicalContextSources(refs)
	if err != nil {
		return nil, err
	}
	if len(canonical) == 0 {
		if !validUUID(createdRunID) {
			return nil, errContextUnavailable
		}
		return a.runSources(ctx, createdRunID)
	}
	return canonical, nil
}

func (a *ContextArchive) scheduleSourcesByID(ctx context.Context, agentID, scheduleID string) ([]InputSource, error) {
	if scheduleID == "" || !validUUID(agentID) {
		return nil, errContextUnavailable
	}
	var (
		createdRunID   string
		sourceRefsJSON []byte
		sourceOverflow bool
	)
	err := a.Store.DB.QueryRow(ctx, `SELECT COALESCE(created_run_id::text,''),source_refs,source_overflow FROM runtime_schedules WHERE id=$1 AND agent_id=$2`, scheduleID, agentID).Scan(&createdRunID, &sourceRefsJSON, &sourceOverflow)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, errContextUnavailable
	}
	if err != nil {
		return nil, err
	}
	return a.scheduleRowSources(ctx, createdRunID, sourceRefsJSON, sourceOverflow)
}

func (a *ContextArchive) authorizeScheduleListReceipt(ctx context.Context, agentID string, cached []byte) error {
	var receipt struct {
		OK   bool `json:"ok"`
		Data struct {
			Schedules []scheduleView `json:"schedules"`
		} `json:"data"`
	}
	if json.Unmarshal(cached, &receipt) != nil {
		return ErrReplayConflict
	}
	if !receipt.OK {
		return nil
	}
	if receipt.Data.Schedules == nil {
		return errContextUnavailable
	}
	for _, item := range receipt.Data.Schedules {
		refs, err := a.scheduleSourcesByID(ctx, agentID, item.ScheduleID)
		if err == nil {
			err = a.authorizeSources(ctx, refs)
		}
		if err != nil {
			if errors.Is(err, errContextUnavailable) {
				_, _ = a.Store.DB.Exec(ctx, `UPDATE runtime_schedules SET status='canceled',updated_at=now() WHERE id=$1 AND agent_id=$2 AND status='active'`, item.ScheduleID, agentID)
			}
			return err
		}
	}
	return nil
}
