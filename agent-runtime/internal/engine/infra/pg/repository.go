// Package pg implements the PostgreSQL outbound repository adapters for the
// Agent Runtime engine using the shared teamie/backend/pkg/postgres mechanism.
package pg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"teamie/agent-runtime/internal/engine/domain"
	"teamie/backend/pkg/postgres"
)

// OpenPool opens the Agent Runtime's dedicated PostgreSQL connection pool with
// UTC normalization, 5s connect timeout, MaxConns=4, MinConns=0, and the writer schema guard setting.
func OpenPool(ctx context.Context, url, writerSchemaSetting string, schemaVersion int) (*pgxpool.Pool, error) {
	opts := []postgres.Option{
		postgres.WithMaxConns(4),
		postgres.WithMinConns(0),
	}
	if writerSchemaSetting != "" {
		opts = append(opts, postgres.WithRuntimeParam(writerSchemaSetting, fmt.Sprint(schemaVersion)))
	}
	return postgres.Open(ctx, url, opts...)
}

// Repository implements the domain persistence ports against PostgreSQL.
type Repository struct {
	db *postgres.DB
}

// New constructs a Repository backed by a context-aware postgres.DB handle.
func New(db *postgres.DB) *Repository {
	return &Repository{db: db}
}

// NewFromPool constructs a Repository backed by a pgxpool.Pool.
func NewFromPool(pool *pgxpool.Pool) *Repository {
	return &Repository{db: postgres.New(pool)}
}

var (
	_ domain.TxManager             = (*Repository)(nil)
	_ domain.SessionRepository     = (*Repository)(nil)
	_ domain.ScheduleRepository    = (*Repository)(nil)
	_ domain.ToolReceiptRepository = (*Repository)(nil)
	_ domain.ArchiveRepository     = (*Repository)(nil)
)

// WithinTx executes fn inside an atomic database transaction.
func (r *Repository) WithinTx(ctx context.Context, fn func(ctx context.Context) error) error {
	return r.db.WithinTx(ctx, fn)
}

// Ping verifies database connectivity.
func (r *Repository) Ping(ctx context.Context) error {
	return r.db.Ping(ctx)
}

// AcquireLease acquires or steals an expired worker lease for agentID, incrementing the fence token.
func (r *Repository) AcquireLease(ctx context.Context, agentID, owner string) (*domain.Lease, error) {
	if !domain.ValidUUID(agentID) {
		return nil, errors.New("invalid agent UUID")
	}
	if _, err := r.db.Conn(ctx).Exec(ctx, "INSERT INTO runtime_agents(agent_id) VALUES($1) ON CONFLICT DO NOTHING", agentID); err != nil {
		return nil, err
	}
	l := &domain.Lease{AgentID: agentID, Owner: owner}
	err := r.db.Conn(ctx).QueryRow(ctx, `UPDATE runtime_agents SET lease_owner=$2,lease_until=now()+interval '45 seconds',fence=fence+1 WHERE agent_id=$1 AND (lease_until IS NULL OR lease_until<now()) RETURNING fence`, agentID, owner).Scan(&l.Fence)
	return l, err
}

// RenewLease extends an active worker lease if the owner and fence still match.
func (r *Repository) RenewLease(ctx context.Context, l *domain.Lease) error {
	tag, err := r.db.Conn(ctx).Exec(ctx, `UPDATE runtime_agents SET lease_until=now()+interval '45 seconds' WHERE agent_id=$1 AND lease_owner=$2 AND fence=$3 AND lease_until>now()`, l.AgentID, l.Owner, l.Fence)
	if err == nil && tag.RowsAffected() != 1 {
		return domain.ErrLeaseLost
	}
	return err
}

// ReleaseLease releases a worker lease if owned by the given fence.
func (r *Repository) ReleaseLease(ctx context.Context, l *domain.Lease) {
	_, _ = r.db.Conn(ctx).Exec(ctx, `UPDATE runtime_agents SET lease_until=NULL,lease_owner=NULL WHERE agent_id=$1 AND lease_owner=$2 AND fence=$3`, l.AgentID, l.Owner, l.Fence)
}

// CheckLease verifies that the lease is currently active without locking the row.
func (r *Repository) CheckLease(ctx context.Context, l *domain.Lease) error {
	var ok bool
	err := r.db.Conn(ctx).QueryRow(ctx, `SELECT lease_owner=$2 AND fence=$3 AND lease_until>now() FROM runtime_agents WHERE agent_id=$1`, l.AgentID, l.Owner, l.Fence).Scan(&ok)
	if err != nil {
		return err
	}
	if !ok {
		return domain.ErrLeaseLost
	}
	return nil
}

// CheckLeaseForUpdate locks the agent row FOR UPDATE and verifies the active lease fence.
func (r *Repository) CheckLeaseForUpdate(ctx context.Context, l *domain.Lease) error {
	var ok bool
	err := r.db.Conn(ctx).QueryRow(ctx, `SELECT lease_owner=$2 AND fence=$3 AND lease_until>now() FROM runtime_agents WHERE agent_id=$1 FOR UPDATE`, l.AgentID, l.Owner, l.Fence).Scan(&ok)
	if err != nil {
		return err
	}
	if !ok {
		return domain.ErrLeaseLost
	}
	return nil
}

// GetCursor returns the current IM sync cursor for the leased Agent.
func (r *Repository) GetCursor(ctx context.Context, l *domain.Lease) (string, error) {
	var c string
	err := r.db.Conn(ctx).QueryRow(ctx, `SELECT cursor::text FROM runtime_agents WHERE agent_id=$1`, l.AgentID).Scan(&c)
	return c, err
}

// IngestInputs atomically persists inbox inputs before advancing the Agent's IM sync cursor.
func (r *Repository) IngestInputs(ctx context.Context, l *domain.Lease, cursor string, inputs []domain.Input) error {
	return r.WithinTx(ctx, func(txCtx context.Context) error {
		if err := r.CheckLeaseForUpdate(txCtx, l); err != nil {
			return err
		}
		conn := r.db.Conn(txCtx)
		for _, in := range inputs {
			b, err := json.Marshal(in)
			if err != nil {
				return err
			}
			if _, err = conn.Exec(txCtx, `INSERT INTO runtime_inputs(agent_id,event_id,payload) VALUES($1,$2,$3) ON CONFLICT DO NOTHING`, l.AgentID, in.EventID, b); err != nil {
				return err
			}
		}
		_, err := conn.Exec(txCtx, `UPDATE runtime_agents SET cursor=GREATEST(cursor,$2::numeric),updated_at=now() WHERE agent_id=$1`, l.AgentID, cursor)
		return err
	})
}

// SkipInput marks an inbox event as skipped under the active worker lease fence.
func (r *Repository) SkipInput(ctx context.Context, l *domain.Lease, eventID string) error {
	return r.WithinTx(ctx, func(txCtx context.Context) error {
		if err := r.CheckLeaseForUpdate(txCtx, l); err != nil {
			return err
		}
		_, err := r.db.Conn(txCtx).Exec(txCtx, `UPDATE runtime_inputs SET state='skipped' WHERE agent_id=$1 AND event_id=$2`, l.AgentID, eventID)
		return err
	})
}

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

// ResolveRunSources recursively walks durable run/task/schedule parent provenance to compute
// the canonical InputSource closure for runID.
func (r *Repository) ResolveRunSources(ctx context.Context, agentID, runID string) ([]domain.InputSource, error) {
	visited := map[string]bool{}
	active := map[string]bool{}
	refs := []domain.InputSource{}
	conn := r.db.Conn(ctx)
	var visit func(string) error
	visit = func(id string) error {
		if active[id] {
			return domain.ErrContextUnavailable
		}
		if visited[id] {
			return nil
		}
		active[id] = true
		defer delete(active, id)
		visited[id] = true
		var raw []byte
		if err := conn.QueryRow(ctx, `SELECT input_json FROM runtime_runs WHERE id=$1 AND agent_id=$2`, id, agentID).Scan(&raw); err != nil {
			return err
		}
		var inputs []domain.Input
		if json.Unmarshal(raw, &inputs) != nil {
			return domain.ErrContextUnavailable
		}
		for _, in := range inputs {
			if in.Kind != "task.notification" && in.Kind != "schedule.wakeup" && in.ConversationID != "" && in.MessageID != "" {
				ref := domain.InputSource{
					EventID:        in.EventID,
					WorkspaceID:    in.WorkspaceID,
					ConversationID: in.ConversationID,
					MessageID:      in.MessageID,
					ReplyToID:      in.ReplyToID,
					SenderID:       in.SenderID,
					SenderKind:     in.SenderKind,
				}
				for _, attachment := range in.Attachments {
					ref.AttachmentIDs = append(ref.AttachmentIDs, attachment.ID)
				}
				refs = append(refs, ref)
			}
			refs = append(refs, in.SourceRefs...)
			parent := in.ParentRunID
			if parent == "" && in.Kind == "task.notification" && len(in.SourceRefs) == 0 {
				if in.TaskID == "" {
					return domain.ErrContextUnavailable
				}
				var storedParent *string
				if err := conn.QueryRow(ctx, `SELECT run_id::text FROM runtime_tasks WHERE id=$1 AND agent_id=$2`, in.TaskID, agentID).Scan(&storedParent); err != nil || storedParent == nil {
					return domain.ErrContextUnavailable
				}
				parent = *storedParent
			}
			if parent == "" && in.Kind == "schedule.wakeup" && len(in.SourceRefs) == 0 {
				if in.ScheduleID == "" {
					return domain.ErrContextUnavailable
				}
				var storedParent *string
				if err := conn.QueryRow(ctx, `SELECT created_run_id::text FROM runtime_schedules WHERE id=$1 AND agent_id=$2`, in.ScheduleID, agentID).Scan(&storedParent); err != nil || storedParent == nil {
					return domain.ErrContextUnavailable
				}
				parent = *storedParent
			}
			if parent != "" {
				if !domain.ValidUUID(parent) {
					return domain.ErrContextUnavailable
				}
				if err := visit(parent); err != nil {
					return err
				}
			} else if in.SourceOverflow {
				return domain.ErrContextUnavailable
			}
		}
		return nil
	}
	if err := visit(runID); err != nil {
		return nil, err
	}
	refs, err := domain.CanonicalContextSources(refs)
	if err == nil && len(refs) == 0 {
		err = domain.ErrContextUnavailable
	}
	return refs, err
}
