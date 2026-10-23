package pg

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5"
	"teamie/pkg/postgres"
	"teamie/services/agent-runtime/internal/domain"
)

// ClockNow returns the database server's current wall-clock timestamp.
func (r *Repository) ClockNow(ctx context.Context) (time.Time, error) {
	var now time.Time
	err := r.db.Conn(ctx).QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&now)
	return now, err
}

// CountActiveSchedules returns the number of active schedules for agentID.
func (r *Repository) CountActiveSchedules(ctx context.Context, agentID string) (int, error) {
	var count int
	err := r.db.Conn(ctx).QueryRow(ctx, `SELECT count(*) FROM runtime_schedules WHERE agent_id=$1 AND status='active'`, agentID).Scan(&count)
	return count, err
}

// ScanScheduleItem scans a single schedule row into a domain.ScheduleItem.
func ScanScheduleItem(row pgx.Row) (domain.ScheduleItem, error) {
	var (
		out             domain.ScheduleItem
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
		return domain.ScheduleItem{}, err
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

// FindScheduleByID looks up a schedule row by scheduleID and agentID.
func (r *Repository) FindScheduleByID(ctx context.Context, agentID, scheduleID string) (*domain.ScheduleItem, bool, error) {
	item, err := ScanScheduleItem(r.db.Conn(ctx).QueryRow(ctx, `SELECT id,kind,interval_seconds,cron_expr,timezone,prompt,status,max_runs,run_count,next_run_at,last_run_at,created_at FROM runtime_schedules WHERE id=$1 AND agent_id=$2`, scheduleID, agentID))
	if postgres.IsNoRows(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return &item, true, nil
}

// CreateSchedule inserts a new active schedule row and returns its ScheduleItem view.
func (r *Repository) CreateSchedule(ctx context.Context, agentID, runID, scheduleID string, spec domain.ValidatedScheduleSpec, sources []domain.InputSource, overflow bool, now time.Time) (*domain.ScheduleItem, error) {
	refsJSON, _ := json.Marshal(sources)
	item, err := ScanScheduleItem(r.db.Conn(ctx).QueryRow(ctx,
		`INSERT INTO runtime_schedules(id,agent_id,created_run_id,kind,interval_seconds,cron_expr,timezone,prompt,status,max_runs,run_count,next_run_at,source_refs,source_overflow,created_at,updated_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,'active',$9,0,$10,$11,$12,$13,$13) RETURNING id,kind,interval_seconds,cron_expr,timezone,prompt,status,max_runs,run_count,next_run_at,last_run_at,created_at`,
		scheduleID, agentID, runID, spec.Kind, spec.IntervalSeconds, spec.CronExpr, spec.Timezone, spec.Prompt, spec.MaxRuns, spec.NextRunAt, refsJSON, overflow, now.UTC(),
	))
	if err != nil {
		return nil, err
	}
	return &item, nil
}

// ListActiveScheduleCandidates returns active schedules for agentID ordered by next_run_at ASC, id ASC.
func (r *Repository) ListActiveScheduleCandidates(ctx context.Context, agentID string, limit int) ([]domain.ScheduleCandidate, error) {
	rows, err := r.db.Conn(ctx).Query(ctx, `SELECT id,COALESCE(created_run_id::text,''),kind,interval_seconds,cron_expr,timezone,prompt,status,max_runs,run_count,next_run_at,last_run_at,source_refs,source_overflow,created_at FROM runtime_schedules WHERE agent_id=$1 AND status='active' ORDER BY next_run_at ASC, id ASC LIMIT $2`, agentID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	candidates := []domain.ScheduleCandidate{}
	for rows.Next() {
		var (
			cand            domain.ScheduleCandidate
			intervalSeconds *int64
			cronExpr        string
			maxRuns         *int
			nextRunAt       time.Time
			lastRunAt       *time.Time
			createdAt       time.Time
		)
		if err = rows.Scan(&cand.View.ScheduleID, &cand.CreatedRunID, &cand.View.Kind, &intervalSeconds, &cronExpr, &cand.View.Timezone, &cand.View.Prompt, &cand.View.Status, &maxRuns, &cand.View.RunCount, &nextRunAt, &lastRunAt, &cand.SourceRefsJSON, &cand.SourceOverflow, &createdAt); err != nil {
			return nil, err
		}
		cand.View.IntervalSeconds = intervalSeconds
		cand.View.CronExpr = cronExpr
		cand.View.MaxRuns = maxRuns
		cand.View.NextRunAt = nextRunAt.UTC().Format(time.RFC3339)
		if lastRunAt != nil {
			formatted := lastRunAt.UTC().Format(time.RFC3339)
			cand.View.LastRunAt = &formatted
		}
		cand.View.CreatedAt = createdAt.UTC().Format(time.RFC3339)
		candidates = append(candidates, cand)
	}
	return candidates, rows.Err()
}

// CancelActiveSchedule marks an active schedule as canceled.
func (r *Repository) CancelActiveSchedule(ctx context.Context, agentID, scheduleID string) error {
	_, err := r.db.Conn(ctx).Exec(ctx, `UPDATE runtime_schedules SET status='canceled',updated_at=now() WHERE id=$1 AND agent_id=$2 AND status='active'`, scheduleID, agentID)
	return err
}

// CancelScheduleWithLease cancels a schedule owned by lease.AgentID inside a fenced transaction.
func (r *Repository) CancelScheduleWithLease(ctx context.Context, lease *domain.Lease, scheduleID string) (string, bool, error) {
	var (
		status string
		found  bool
	)
	err := r.WithinTx(ctx, func(txCtx context.Context) error {
		if err := r.CheckLeaseForUpdate(txCtx, lease); err != nil {
			return err
		}
		conn := r.db.Conn(txCtx)
		err := conn.QueryRow(txCtx, `SELECT status FROM runtime_schedules WHERE id=$1 AND agent_id=$2 FOR UPDATE`, scheduleID, lease.AgentID).Scan(&status)
		if postgres.IsNoRows(err) {
			return nil
		}
		if err != nil {
			return err
		}
		found = true
		if status == "active" {
			if _, err = conn.Exec(txCtx, `UPDATE runtime_schedules SET status='canceled',updated_at=now() WHERE id=$1 AND agent_id=$2`, scheduleID, lease.AgentID); err != nil {
				return err
			}
			status = "canceled"
		}
		return nil
	})
	return status, found, err
}

// GetScheduleStatusAndParent retrieves a schedule's status and created_run_id.
func (r *Repository) GetScheduleStatusAndParent(ctx context.Context, agentID, scheduleID string) (string, string, bool, error) {
	var (
		status       string
		createdRunID *string
	)
	err := r.db.Conn(ctx).QueryRow(ctx, `SELECT status,created_run_id::text FROM runtime_schedules WHERE id=$1 AND agent_id=$2`, scheduleID, agentID).Scan(&status, &createdRunID)
	if postgres.IsNoRows(err) {
		return "", "", false, nil
	}
	if err != nil {
		return "", "", false, err
	}
	parent := ""
	if createdRunID != nil {
		parent = *createdRunID
	}
	return status, parent, true, nil
}

// GetScheduleSourcesMeta retrieves a schedule's provenance metadata columns.
func (r *Repository) GetScheduleSourcesMeta(ctx context.Context, agentID, scheduleID string) (string, []byte, bool, bool, error) {
	var (
		createdRunID   string
		sourceRefsJSON []byte
		sourceOverflow bool
	)
	err := r.db.Conn(ctx).QueryRow(ctx, `SELECT COALESCE(created_run_id::text,''),source_refs,source_overflow FROM runtime_schedules WHERE id=$1 AND agent_id=$2`, scheduleID, agentID).Scan(&createdRunID, &sourceRefsJSON, &sourceOverflow)
	if postgres.IsNoRows(err) {
		return "", nil, false, false, nil
	}
	if err != nil {
		return "", nil, false, false, err
	}
	return createdRunID, sourceRefsJSON, sourceOverflow, true, nil
}

// CancelAllActiveSchedules cancels all active schedules for agentID.
func (r *Repository) CancelAllActiveSchedules(ctx context.Context, agentID string) error {
	_, err := r.db.Conn(ctx).Exec(ctx, `UPDATE runtime_schedules SET status='canceled',updated_at=now() WHERE agent_id=$1 AND status='active'`, agentID)
	return err
}

// DrainedScheduleLog records the outcome of draining a due schedule wakeup into runtime_inputs.
type DrainedScheduleLog struct {
	ScheduleID     string
	NotificationID string
	EventID        string
	ParentRunID    string
	Inserted       bool
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

// DrainDueSchedules atomically claims up to 16 due active schedules under the worker lease fence,
// inserts wakeup inputs, advances schedule next_run_at/status, and returns log records.
func (r *Repository) DrainDueSchedules(ctx context.Context, l *domain.Lease) ([]DrainedScheduleLog, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	if err = CheckLeaseTx(ctx, tx, l); err != nil {
		return nil, err
	}
	var now time.Time
	if err = tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&now); err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, `SELECT id,created_run_id::text,kind,interval_seconds,cron_expr,timezone,prompt,max_runs,run_count,next_run_at,source_refs,source_overflow FROM runtime_schedules WHERE agent_id=$1 AND status='active' AND next_run_at<=$2 ORDER BY next_run_at ASC, id ASC FOR UPDATE SKIP LOCKED LIMIT 16`, l.AgentID, now)
	if err != nil {
		return nil, err
	}
	due := []dueScheduleRecord{}
	for rows.Next() {
		var rec dueScheduleRecord
		if err = rows.Scan(&rec.id, &rec.createdRunID, &rec.kind, &rec.intervalSeconds, &rec.cronExpr, &rec.timezone, &rec.prompt, &rec.maxRuns, &rec.runCount, &rec.nextRunAt, &rec.sourceRefsJSON, &rec.sourceOverflow); err != nil {
			rows.Close()
			return nil, err
		}
		due = append(due, rec)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	if len(due) == 0 {
		return nil, tx.Commit(ctx)
	}
	logs := make([]DrainedScheduleLog, 0, len(due))
	for _, rec := range due {
		nextStatus, nextCount, nextRunAt := domain.AdvanceDueSchedule(rec.kind, rec.intervalSeconds, rec.cronExpr, rec.timezone, rec.maxRuns, rec.runCount, rec.nextRunAt, now)
		var seq string
		if err = tx.QueryRow(ctx, `SELECT (90000000000000000000::numeric+nextval('runtime_notification_input_seq'))::text`).Scan(&seq); err != nil {
			return nil, err
		}
		notificationID := domain.ScheduleNotificationID(rec.id, nextCount)
		var refs []domain.InputSource
		_ = json.Unmarshal(rec.sourceRefsJSON, &refs)
		in := domain.Input{
			EventID:        seq,
			Kind:           "schedule.wakeup",
			ScheduleID:     rec.id,
			NotificationID: notificationID,
			Body:           rec.prompt,
			CreatedAt:      now.UTC().Format(time.RFC3339Nano),
			ParentRunID:    rec.createdRunID,
			SourceRefs:     refs,
			SourceOverflow: rec.sourceOverflow,
		}
		payload, _ := json.Marshal(in)
		tag, err := tx.Exec(ctx, `INSERT INTO runtime_inputs(agent_id,event_id,payload,notification_id) VALUES($1,$2,$3,$4) ON CONFLICT DO NOTHING`, l.AgentID, seq, payload, notificationID)
		if err != nil {
			return nil, err
		}
		inserted := tag.RowsAffected() > 0
		if !inserted {
			seq = ""
		}
		if _, err = tx.Exec(ctx, `UPDATE runtime_schedules SET status=$3,run_count=$4,last_run_at=$5,next_run_at=$6,updated_at=$5 WHERE id=$1 AND agent_id=$2`, rec.id, l.AgentID, nextStatus, nextCount, now.UTC(), nextRunAt); err != nil {
			return nil, err
		}
		logs = append(logs, DrainedScheduleLog{
			ScheduleID:     rec.id,
			NotificationID: notificationID,
			EventID:        seq,
			ParentRunID:    rec.createdRunID,
			Inserted:       inserted,
		})
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return logs, nil
}
