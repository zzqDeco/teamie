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

// ReadObservation reads a saved tool observation snapshot by (runID, callID, key).
func (r *Repository) ReadObservation(ctx context.Context, runID, callID, key string) ([]byte, bool, error) {
	var out []byte
	err := r.db.Conn(ctx).QueryRow(ctx, `SELECT response FROM runtime_tool_observations WHERE run_id=$1 AND call_id=$2 AND observation_key=$3`, runID, callID, key).Scan(&out)
	if postgres.IsNoRows(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return out, true, nil
}

// SaveObservation persists a tool observation snapshot under the active worker lease fence.
func (r *Repository) SaveObservation(ctx context.Context, lease *domain.Lease, runID, callID, key string, payload []byte) error {
	return r.WithinTx(ctx, func(txCtx context.Context) error {
		if err := r.CheckLeaseForUpdate(txCtx, lease); err != nil {
			return err
		}
		_, err := r.db.Conn(txCtx).Exec(txCtx, `INSERT INTO runtime_tool_observations(run_id,call_id,observation_key,response) VALUES($1,$2,$3,$4) ON CONFLICT DO NOTHING`, runID, callID, key, payload)
		return err
	})
}
