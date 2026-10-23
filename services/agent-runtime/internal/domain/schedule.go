package domain

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Schedule policy constants and quota ceilings.
const (
	MaxActiveSchedulesPerAgent   = 32
	MinScheduleDelaySeconds      = 1
	MinScheduleIntervalSeconds   = 60
	MaxScheduleIntervalSeconds   = 30 * 24 * 3600
	MaxScheduleHorizon           = 365 * 24 * time.Hour
	MaxScheduleRuns              = 10000
	MaxSchedulePromptRunes       = 4000
	ScheduleCreateObservationKey = "schedule-create-v1"
)

// ScheduleSpec captures the caller-supplied arguments for schedule_create.
type ScheduleSpec struct {
	Kind            string `json:"kind"`
	DelaySeconds    int64  `json:"delaySeconds,omitempty"`
	RunAt           string `json:"runAt,omitempty"`
	IntervalSeconds int64  `json:"intervalSeconds,omitempty"`
	CronExpr        string `json:"cronExpr,omitempty"`
	Timezone        string `json:"timezone,omitempty"`
	Prompt          string `json:"prompt"`
	MaxRuns         int    `json:"maxRuns,omitempty"`
}

// ValidatedScheduleSpec holds a normalized, validated schedule ready for persistence.
type ValidatedScheduleSpec struct {
	Kind            string
	IntervalSeconds *int64
	CronExpr        string
	Timezone        string
	Prompt          string
	MaxRuns         *int
	NextRunAt       time.Time
}

// ScheduleItem represents the public tool observation view of a runtime schedule.
type ScheduleItem struct {
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

// ScheduleCandidate pairs a ScheduleItem with its provenance metadata for ACL filtering.
type ScheduleCandidate struct {
	View           ScheduleItem
	CreatedRunID   string
	SourceRefsJSON []byte
	SourceOverflow bool
}

// DueScheduleRecord represents an active schedule row due for wakeup delivery.
type DueScheduleRecord struct {
	ID              string
	CreatedRunID    string
	Kind            string
	IntervalSeconds *int64
	CronExpr        string
	Timezone        string
	Prompt          string
	MaxRuns         *int
	RunCount        int
	NextRunAt       time.Time
	SourceRefsJSON  []byte
	SourceOverflow  bool
}

// ScheduleInputPayload represents the durable inbox payload for a TEAMIE_SCHEDULE_WAKEUP event.
type ScheduleInputPayload struct {
	EventID        string        `json:"eventId"`
	Kind           string        `json:"kind"`
	ScheduleID     string        `json:"scheduleId"`
	NotificationID string        `json:"notificationId"`
	Body           string        `json:"body"`
	CreatedAt      string        `json:"createdAt"`
	ParentRunID    string        `json:"parentRunId,omitempty"`
	SourceRefs     []InputSource `json:"sourceRefs,omitempty"`
	SourceOverflow bool          `json:"sourceOverflow,omitempty"`
}

// StableScheduleID computes the deterministic schedule ID for a tool call within a Run.
func StableScheduleID(agentID, runID, callID string) string {
	return "sched-" + Digest([]byte(agentID+"/"+runID+"/"+callID))[:40]
}

// ScheduleNotificationID returns the deterministic notification ID for a firing schedule step.
func ScheduleNotificationID(scheduleID string, nextCount int) string {
	return fmt.Sprintf("schedule:%s:%d", scheduleID, nextCount)
}

// ResolveScheduleTimezone validates and normalizes an IANA timezone name (defaulting to UTC).
func ResolveScheduleTimezone(raw string) (*time.Location, string, error) {
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

// NextCronTime computes the next UTC firing time strictly after `after` for a 5-field cron
// expression in `tzName`, protecting against DST fall-back backward jumps.
func NextCronTime(expr, tzName string, after time.Time) (time.Time, error) {
	loc, _, err := ResolveScheduleTimezone(tzName)
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

// ValidateSchedulePromptAndTimezone validates the prompt and timezone prior to opening a transaction.
func ValidateSchedulePromptAndTimezone(promptRaw, timezoneRaw string) (prompt, tzName, errCode string) {
	prompt = strings.TrimSpace(promptRaw)
	if pLen := len([]rune(prompt)); pLen < 1 || pLen > MaxSchedulePromptRunes || strings.ContainsAny(prompt, "\x00") {
		return "", "", "invalid_arguments"
	}
	_, tzName, err := ResolveScheduleTimezone(timezoneRaw)
	if err != nil {
		return "", "", "invalid_timezone"
	}
	return prompt, tzName, ""
}

// ValidateScheduleSpec validates a ScheduleSpec against server wall-clock time `now`.
// If invalid, it returns a non-empty error code ("invalid_arguments", "invalid_timezone", or "invalid_cron_expression").
func ValidateScheduleSpec(spec ScheduleSpec, now time.Time) (ValidatedScheduleSpec, string) {
	prompt, tzName, errCode := ValidateSchedulePromptAndTimezone(spec.Prompt, spec.Timezone)
	if errCode != "" {
		return ValidatedScheduleSpec{}, errCode
	}
	kind := strings.TrimSpace(spec.Kind)
	runAtRaw := strings.TrimSpace(spec.RunAt)
	cronExpr := strings.TrimSpace(spec.CronExpr)
	var (
		nextRunAt       time.Time
		intervalSeconds *int64
		maxRuns         *int
	)
	switch kind {
	case "once":
		if (spec.DelaySeconds > 0 && runAtRaw != "") || (spec.DelaySeconds <= 0 && runAtRaw == "") || spec.IntervalSeconds != 0 || cronExpr != "" || spec.MaxRuns < 0 || spec.MaxRuns > 1 {
			return ValidatedScheduleSpec{}, "invalid_arguments"
		}
		if spec.DelaySeconds > 0 {
			if spec.DelaySeconds < MinScheduleDelaySeconds || spec.DelaySeconds > MaxScheduleIntervalSeconds {
				return ValidatedScheduleSpec{}, "invalid_arguments"
			}
			nextRunAt = now.UTC().Add(time.Duration(spec.DelaySeconds) * time.Second)
		} else {
			parsed, parseErr := time.Parse(time.RFC3339Nano, runAtRaw)
			if parseErr != nil || !parsed.After(now) || parsed.After(now.Add(MaxScheduleHorizon)) {
				return ValidatedScheduleSpec{}, "invalid_arguments"
			}
			nextRunAt = parsed.UTC()
		}
		one := 1
		maxRuns = &one
	case "interval":
		if spec.IntervalSeconds < MinScheduleIntervalSeconds || spec.IntervalSeconds > MaxScheduleIntervalSeconds || spec.DelaySeconds != 0 || runAtRaw != "" || cronExpr != "" || spec.MaxRuns < 0 || spec.MaxRuns > MaxScheduleRuns {
			return ValidatedScheduleSpec{}, "invalid_arguments"
		}
		iv := spec.IntervalSeconds
		intervalSeconds = &iv
		if spec.MaxRuns > 0 {
			mr := spec.MaxRuns
			maxRuns = &mr
		}
		nextRunAt = now.UTC().Add(time.Duration(spec.IntervalSeconds) * time.Second)
	case "cron":
		if cronExpr == "" || spec.DelaySeconds != 0 || runAtRaw != "" || spec.IntervalSeconds != 0 || spec.MaxRuns < 0 || spec.MaxRuns > MaxScheduleRuns {
			return ValidatedScheduleSpec{}, "invalid_arguments"
		}
		next, cronErr := NextCronTime(cronExpr, tzName, now)
		if cronErr != nil {
			return ValidatedScheduleSpec{}, "invalid_cron_expression"
		}
		cronExpr = strings.Join(strings.Fields(cronExpr), " ")
		if spec.MaxRuns > 0 {
			mr := spec.MaxRuns
			maxRuns = &mr
		}
		nextRunAt = next.UTC()
	default:
		return ValidatedScheduleSpec{}, "invalid_arguments"
	}
	return ValidatedScheduleSpec{
		Kind:            kind,
		IntervalSeconds: intervalSeconds,
		CronExpr:        cronExpr,
		Timezone:        tzName,
		Prompt:          prompt,
		MaxRuns:         maxRuns,
		NextRunAt:       nextRunAt,
	}, ""
}

// CheckScheduleQuota returns "schedule_limit_exceeded" if activeCount has reached MaxActiveSchedulesPerAgent.
func CheckScheduleQuota(activeCount int) string {
	if activeCount >= MaxActiveSchedulesPerAgent {
		return "schedule_limit_exceeded"
	}
	return ""
}

// ValidateScheduleCancelID trims and validates a schedule ID for cancellation.
func ValidateScheduleCancelID(raw string) (string, bool) {
	id := strings.TrimSpace(raw)
	if id == "" || len(id) > 128 || strings.ContainsAny(id, "\r\n\t\x00") {
		return "", false
	}
	return id, true
}

// AdvanceDueSchedule computes the next status, run count, and next_run_at timestamp after a due schedule fires.
func AdvanceDueSchedule(kind string, intervalSeconds *int64, cronExpr, timezone string, maxRuns *int, currentRunCount int, scheduledRunAt, now time.Time) (nextStatus string, nextCount int, nextRunAt time.Time) {
	nextCount = currentRunCount + 1
	nextStatus = "active"
	nextRunAt = scheduledRunAt.UTC()
	if kind == "once" || (maxRuns != nil && nextCount >= *maxRuns) {
		nextStatus = "completed"
		return nextStatus, nextCount, nextRunAt
	}
	if kind == "interval" && intervalSeconds != nil && *intervalSeconds > 0 {
		step := time.Duration(*intervalSeconds) * time.Second
		next := scheduledRunAt.Add(step)
		if !next.After(now) {
			missed := now.Sub(scheduledRunAt) / step
			next = scheduledRunAt.Add((missed + 1) * step)
		}
		return nextStatus, nextCount, next.UTC()
	}
	if kind == "cron" {
		next, cronErr := NextCronTime(cronExpr, timezone, now)
		if cronErr != nil {
			return "completed", nextCount, nextRunAt
		}
		return nextStatus, nextCount, next.UTC()
	}
	return "completed", nextCount, nextRunAt
}

// ToolObservationReceipt represents a durable tool observation snapshot persisted
// under a worker lease fence so replayed turns preserve stable tool results.
type ToolObservationReceipt struct {
	RunID          string
	CallID         string
	ObservationKey string
	Response       []byte
}

// Valid reports whether the tool observation receipt has non-empty identifiers.
func (r ToolObservationReceipt) Valid() bool {
	return ValidUUID(r.RunID) && r.CallID != "" && r.ObservationKey != ""
}

// ToolRequestHash computes the deterministic SHA-256 request hash for a tool call.
func ToolRequestHash(name, rawArgs string) string {
	return Digest([]byte(name + "\x00" + rawArgs))
}
