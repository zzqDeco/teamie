package pg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"teamie/services/agent-runtime/internal/domain"
)

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

// ReportAgentState updates the persisted state and last_error for an Agent if lease_owner matches.
func (r *Repository) ReportAgentState(ctx context.Context, agentID, owner, state, code string, active bool) error {
	_, err := r.db.Conn(ctx).Exec(ctx, `UPDATE runtime_agents SET state=$3,last_error=$4,last_active_at=CASE WHEN $5 THEN now() ELSE last_active_at END WHERE agent_id=$1 AND lease_owner=$2 AND lease_until>now()`, agentID, owner, state, code, active)
	return err
}

// ListPendingInputs returns raw JSON payloads of pending or due-retry inputs ordered by delivery_seq.
func (r *Repository) ListPendingInputs(ctx context.Context, agentID string) ([][]byte, error) {
	rows, err := r.db.Conn(ctx).Query(ctx, `SELECT i.payload FROM runtime_inputs i LEFT JOIN runtime_runs r ON i.run_id=r.id WHERE i.agent_id=$1 AND i.state IN ('pending','processing') AND (r.id IS NULL OR (r.next_attempt<=now() AND r.state<>'waiting_input')) ORDER BY i.delivery_seq LIMIT 32`, agentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out [][]byte
	for rows.Next() {
		var b []byte
		if err = rows.Scan(&b); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// ListFreshPendingInputs returns raw JSON payloads of strictly pending inputs ordered by delivery_seq.
func (r *Repository) ListFreshPendingInputs(ctx context.Context, agentID string) ([][]byte, error) {
	rows, err := r.db.Conn(ctx).Query(ctx, `SELECT payload FROM runtime_inputs WHERE agent_id=$1 AND state='pending' ORDER BY delivery_seq LIMIT 256`, agentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out [][]byte
	for rows.Next() {
		var b []byte
		if err = rows.Scan(&b); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// FilterEligibleInputIDs returns the set of eventIDs in ids that are currently pending or processing.
func (r *Repository) FilterEligibleInputIDs(ctx context.Context, agentID string, ids []string) (map[string]bool, error) {
	rows, err := r.db.Conn(ctx).Query(ctx, `SELECT event_id::text FROM runtime_inputs WHERE agent_id=$1 AND event_id::text=ANY($2::text[]) AND state IN ('pending','processing')`, agentID, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	eligible := map[string]bool{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			return nil, err
		}
		eligible[id] = true
	}
	return eligible, rows.Err()
}

// StartOrResumeRun atomically starts a new Run or resumes an interrupted/retryable Run for firstEventID.
func (r *Repository) StartOrResumeRun(
	ctx context.Context,
	l *domain.Lease,
	firstEventID, newRunID, attemptID string,
	resolveNew func() (key string, inputsJSON []byte, profile string, budget json.RawMessage, eventIDs []string, err error),
) (runID string, inputsJSON []byte, contextHead, acceptanceHead int64, contextProfile string, contextBudget json.RawMessage, resumed bool, err error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return "", nil, 0, 0, "", nil, false, err
	}
	defer tx.Rollback(ctx)
	if err = CheckLeaseTx(ctx, tx, l); err != nil {
		return "", nil, 0, 0, "", nil, false, err
	}
	var existing string
	var eventIDs []string
	err = tx.QueryRow(ctx, `SELECT run_id::text FROM runtime_inputs WHERE agent_id=$1 AND event_id=$2 AND run_id IS NOT NULL`, l.AgentID, firstEventID).Scan(&existing)
	if err == nil {
		resumed = true
		runID = existing
		err = tx.QueryRow(ctx, `UPDATE runtime_runs SET state='running',attempt_id=$2,attempts=attempts+1 WHERE id=$1 AND state IN ('running','interrupted','waiting_configuration','retry','waiting_input') RETURNING input_json,context_head,acceptance_head,context_profile,context_budget`, runID, attemptID).Scan(&inputsJSON, &contextHead, &acceptanceHead, &contextProfile, &contextBudget)
		if err != nil {
			return "", nil, 0, 0, "", nil, false, err
		}
		var parsed []struct {
			EventID string `json:"eventId"`
		}
		if err = json.Unmarshal(inputsJSON, &parsed); err != nil {
			return "", nil, 0, 0, "", nil, false, err
		}
		for _, item := range parsed {
			eventIDs = append(eventIDs, item.EventID)
		}
	} else if errors.Is(err, pgx.ErrNoRows) {
		runID = newRunID
		var key string
		key, inputsJSON, contextProfile, contextBudget, eventIDs, err = resolveNew()
		if err != nil {
			return "", nil, 0, 0, "", nil, false, err
		}
		err = tx.QueryRow(ctx, `SELECT (SELECT COALESCE(max(seq),0) FROM runtime_session_events WHERE session_id=$1),(SELECT COALESCE(max(seq),0) FROM runtime_session_acceptances WHERE agent_id=$2)`, "agent:"+l.AgentID, l.AgentID).Scan(&contextHead, &acceptanceHead)
		if err != nil {
			return "", nil, 0, 0, "", nil, false, err
		}
		_, err = tx.Exec(ctx, `INSERT INTO runtime_runs(id,agent_id,input_key,input_json,attempt_id,context_head,acceptance_head,context_profile,context_budget) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)`, runID, l.AgentID, key, inputsJSON, attemptID, contextHead, acceptanceHead, contextProfile, contextBudget)
		if err != nil {
			return "", nil, 0, 0, "", nil, false, err
		}
	} else {
		return "", nil, 0, 0, "", nil, false, err
	}
	for _, evID := range eventIDs {
		if _, err = tx.Exec(ctx, `UPDATE runtime_inputs SET state='processing',run_id=$3 WHERE agent_id=$1 AND event_id=$2 AND state IN ('pending','processing')`, l.AgentID, evID, runID); err != nil {
			return "", nil, 0, 0, "", nil, false, err
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return "", nil, 0, 0, "", nil, false, err
	}
	return runID, inputsJSON, contextHead, acceptanceHead, contextProfile, contextBudget, resumed, nil
}

// FinishRun transitions a Run to completed, retry, waiting_input, failed, or canceled under the worker lease fence.
func (r *Repository) FinishRun(ctx context.Context, l *domain.Lease, runID, state, safeError string, retry bool) error {
	tx, e := r.pool.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	if e = CheckLeaseTx(ctx, tx, l); e != nil {
		return e
	}
	if state == "completed" {
		if e = AcceptSessionAttemptTx(ctx, tx, l.AgentID, runID); e != nil {
			return e
		}
	}
	if retry {
		_, e = tx.Exec(ctx, `UPDATE runtime_runs SET state=$2,safe_error=$3,updated_at=now(),next_attempt=now()+interval '15 seconds' WHERE id=$1`, runID, state, safeError)
	} else {
		_, e = tx.Exec(ctx, `UPDATE runtime_runs SET state=$2,safe_error=$3,updated_at=now(),committed_attempt=CASE WHEN $2='completed' THEN attempt_id ELSE NULL END,finished_at=now() WHERE id=$1`, runID, state, safeError)
		if e == nil {
			_, e = tx.Exec(ctx, `UPDATE runtime_inputs SET state=$2 WHERE agent_id=$1 AND run_id=$3`, l.AgentID, map[bool]string{true: "done", false: "failed"}[state == "completed"], runID)
		}
	}
	if e != nil {
		return e
	}
	_, e = tx.Exec(ctx, `INSERT INTO runtime_public_events(agent_id,type,run_id,status,error_code) VALUES($1,'run.status',$2,$3,NULLIF($4,''))`, l.AgentID, runID, state, safeError)
	if e != nil {
		return e
	}
	return tx.Commit(ctx)
}

// InvalidateRun abandons a replay whose source was revoked or changed.
func (r *Repository) InvalidateRun(ctx context.Context, l *domain.Lease, runID string, invalid []string) error {
	tx, e := r.pool.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	if e = CheckLeaseTx(ctx, tx, l); e != nil {
		return e
	}
	if _, e = tx.Exec(ctx, `UPDATE runtime_runs SET state='failed',safe_error='source_unavailable',finished_at=now() WHERE id=$1`, runID); e != nil {
		return e
	}
	if _, e = tx.Exec(ctx, `UPDATE runtime_inputs SET state=CASE WHEN event_id::text=ANY($3::text[]) THEN 'skipped' ELSE 'pending' END,run_id=NULL WHERE agent_id=$1 AND run_id=$2`, l.AgentID, runID, invalid); e != nil {
		return e
	}
	return tx.Commit(ctx)
}

// ConfigureRun updates a Run's revision, generation, and context_attempts and records run.status=running.
func (r *Repository) ConfigureRun(ctx context.Context, l *domain.Lease, runID string, revision, generation int64, resumeAttempt string, hasResume bool) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err = CheckLeaseTx(ctx, tx, l); err != nil {
		return err
	}
	if hasResume {
		_, err = tx.Exec(ctx, `UPDATE runtime_runs SET revision=$2,generation=$3,context_attempts=array_append(context_attempts,$4::uuid),updated_at=now() WHERE id=$1`, runID, revision, generation, resumeAttempt)
	} else {
		_, err = tx.Exec(ctx, `UPDATE runtime_runs SET revision=$2,generation=$3,context_attempts='{}',updated_at=now() WHERE id=$1`, runID, revision, generation)
	}
	if err == nil {
		_, err = tx.Exec(ctx, `INSERT INTO runtime_public_events(agent_id,type,run_id,status) VALUES($1,'run.status',$2,'running')`, l.AgentID, runID)
	}
	if err == nil {
		err = tx.Commit(ctx)
	}
	return err
}

// PreemptRun interrupts an active Run for new input after accepting its session attempt.
func (r *Repository) PreemptRun(ctx context.Context, l *domain.Lease, runID string) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err = CheckLeaseTx(ctx, tx, l); err != nil {
		return err
	}
	if err = AcceptSessionAttemptTx(ctx, tx, l.AgentID, runID); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE runtime_runs SET state='interrupted',safe_error='new_input',committed_attempt=attempt_id,finished_at=now(),updated_at=now() WHERE id=$1`, runID)
	if err == nil {
		_, err = tx.Exec(ctx, `UPDATE runtime_inputs SET state='done' WHERE agent_id=$1 AND run_id=$2`, l.AgentID, runID)
	}
	if err == nil {
		_, err = tx.Exec(ctx, `INSERT INTO runtime_public_events(agent_id,type,run_id,status,error_code) VALUES($1,'run.status',$2,'interrupted','new_input')`, l.AgentID, runID)
	}
	if err == nil {
		err = tx.Commit(ctx)
	}
	return err
}

// IsRunCanceled reports whether cancel_requested is set on runID.
func (r *Repository) IsRunCanceled(ctx context.Context, runID string) bool {
	var v bool
	_ = r.db.Conn(ctx).QueryRow(ctx, `SELECT cancel_requested FROM runtime_runs WHERE id=$1`, runID).Scan(&v)
	return v
}

// CheckpointExists reports whether a checkpoint row exists for id.
func (r *Repository) CheckpointExists(ctx context.Context, id string) bool {
	var v bool
	_ = r.db.Conn(ctx).QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM runtime_checkpoints WHERE checkpoint_id=$1)`, id).Scan(&v)
	return v
}

// GetCheckpointMetadata returns the raw JSON metadata column for checkpoint_id.
func (r *Repository) GetCheckpointMetadata(ctx context.Context, id string) ([]byte, error) {
	var b []byte
	err := r.db.Conn(ctx).QueryRow(ctx, `SELECT metadata FROM runtime_checkpoints WHERE checkpoint_id=$1`, id).Scan(&b)
	return b, err
}

// GetCheckpoint returns the payload and metadata for checkpoint_id.
func (r *Repository) GetCheckpoint(ctx context.Context, id string) ([]byte, []byte, bool, error) {
	var b, metadata []byte
	err := r.db.Conn(ctx).QueryRow(ctx, `SELECT payload,metadata FROM runtime_checkpoints WHERE checkpoint_id=$1`, id).Scan(&b, &metadata)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil, false, nil
	}
	return b, metadata, err == nil, err
}

// SetCheckpoint upserts a checkpoint row under the active worker lease fence.
func (r *Repository) SetCheckpoint(ctx context.Context, l *domain.Lease, id string, payload, metadata []byte) error {
	tx, e := r.pool.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	if e = CheckLeaseTx(ctx, tx, l); e != nil {
		return e
	}
	_, e = tx.Exec(ctx, `INSERT INTO runtime_checkpoints(checkpoint_id,payload,metadata) VALUES($1,$2,$3) ON CONFLICT(checkpoint_id) DO UPDATE SET payload=EXCLUDED.payload,metadata=EXCLUDED.metadata,updated_at=now()`, id, payload, metadata)
	if e != nil {
		return e
	}
	return tx.Commit(ctx)
}

// DeleteCheckpoint deletes a checkpoint row under the active worker lease fence.
func (r *Repository) DeleteCheckpoint(ctx context.Context, l *domain.Lease, id string) error {
	tx, e := r.pool.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	if e = CheckLeaseTx(ctx, tx, l); e != nil {
		return e
	}
	_, e = tx.Exec(ctx, `DELETE FROM runtime_checkpoints WHERE checkpoint_id=$1`, id)
	if e != nil {
		return e
	}
	return tx.Commit(ctx)
}

// SessionEventWrite holds a serialized session event ready for persistence.
type SessionEventWrite struct {
	EventID string
	Kind    string
	Payload []byte
	Hash    string
}

// AppendSessionEvents persists a batch of session events under the active worker lease fence.
// If a conflicting payload hash already exists for the same (sessionID, eventID), duplicate is returned as true.
func (r *Repository) AppendSessionEvents(ctx context.Context, l *domain.Lease, sessionID, runID, attemptID string, items []SessionEventWrite) (bool, error) {
	tx, e := r.pool.Begin(ctx)
	if e != nil {
		return false, e
	}
	defer tx.Rollback(ctx)
	if e = CheckLeaseTx(ctx, tx, l); e != nil {
		return false, e
	}
	for _, item := range items {
		tag, err := tx.Exec(ctx, `INSERT INTO runtime_session_events(session_id,event_id,kind,run_id,attempt_id,payload,payload_hash) VALUES($1,$2,$3,$4,$5,$6,$7) ON CONFLICT DO NOTHING`, sessionID, item.EventID, item.Kind, runID, attemptID, item.Payload, item.Hash)
		if err != nil {
			return false, err
		}
		if tag.RowsAffected() == 0 {
			var old string
			if err = tx.QueryRow(ctx, `SELECT payload_hash FROM runtime_session_events WHERE session_id=$1 AND event_id=$2`, sessionID, item.EventID).Scan(&old); err != nil {
				return false, err
			}
			if old != item.Hash {
				return true, nil
			}
		}
	}
	return false, tx.Commit(ctx)
}

// LoadSessionEventPayloads loads visible session event payloads for a Run.
func (r *Repository) LoadSessionEventPayloads(
	ctx context.Context,
	sessionID, runID, attemptID string,
	contextHead, acceptanceHead int64,
	afterEventID string,
	kinds []string,
	limit int,
	reverse bool,
) ([][]byte, bool, error) {
	var after int64
	if reverse {
		after = 1<<63 - 1
	}
	visible := `(e.attempt_id=$2 OR (e.run_id=RUN AND e.attempt_id=ANY(r.context_attempts)) OR EXISTS(SELECT 1 FROM runtime_session_acceptances acceptance WHERE acceptance.attempt_id=e.attempt_id AND acceptance.seq<=ACCEPT))`
	afterVisible := strings.NewReplacer("RUN", "$5", "ACCEPT", "$6").Replace(visible)
	pageVisible := strings.NewReplacer("RUN", "$7", "ACCEPT", "$8").Replace(visible)
	conn := r.db.Conn(ctx)
	if afterEventID != "" {
		err := conn.QueryRow(ctx, `SELECT e.seq FROM runtime_session_events e JOIN runtime_runs r ON r.id=e.run_id WHERE e.session_id=$1 AND `+afterVisible+` AND e.event_id=$3 AND (e.run_id=$5 OR e.seq<=$4)`, sessionID, attemptID, afterEventID, contextHead, runID, acceptanceHead).Scan(&after)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, true, nil
		}
		if err != nil {
			return nil, false, err
		}
	}
	if limit <= 0 {
		limit = 1<<31 - 1
	}
	order, comparison := "ASC", ">"
	if reverse {
		order, comparison = "DESC", "<"
	}
	rows, err := conn.Query(ctx, `SELECT e.payload FROM runtime_session_events e JOIN runtime_runs r ON r.id=e.run_id WHERE e.session_id=$1 AND `+pageVisible+` AND (e.run_id=$7 OR e.seq<=$6) AND e.seq `+comparison+` $3 AND (cardinality($4::text[])=0 OR e.kind=ANY($4)) ORDER BY e.seq `+order+` LIMIT $5`, sessionID, attemptID, after, kinds, limit, contextHead, runID, acceptanceHead)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	var out [][]byte
	for rows.Next() {
		var b []byte
		if err = rows.Scan(&b); err != nil {
			return nil, false, err
		}
		out = append(out, b)
	}
	return out, false, rows.Err()
}

// SessionEventExists reports whether a session event with eventID exists in sessionID.
func (r *Repository) SessionEventExists(ctx context.Context, sessionID, eventID string) (bool, error) {
	var exists bool
	err := r.db.Conn(ctx).QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM runtime_session_events WHERE session_id=$1 AND event_id=$2)`, sessionID, eventID).Scan(&exists)
	return exists, err
}

// GetCachedModelCall looks up a cached model call receipt for (runID, step) and verifies request_hash.
func (r *Repository) GetCachedModelCall(ctx context.Context, runID string, step int, hash string) ([]byte, error) {
	var old string
	var b []byte
	err := r.db.Conn(ctx).QueryRow(ctx, `SELECT request_hash,response FROM runtime_model_calls WHERE run_id=$1 AND step=$2`, runID, step).Scan(&old, &b)
	if err == nil && old != hash {
		return nil, domain.ErrReplayConflict
	}
	return b, err
}

// GetModelStepAndRecovery fetches any recorded model call response and context recovery decision for (runID, step).
func (r *Repository) GetModelStepAndRecovery(ctx context.Context, runID string, step int) ([]byte, []byte, error) {
	var raw, decisionRaw []byte
	err := r.db.Conn(ctx).QueryRow(ctx, `SELECT (SELECT response FROM runtime_model_calls WHERE run_id=$1 AND step=$2), (SELECT decision_json FROM runtime_context_recoveries WHERE run_id=$1 AND model_step=$2)`, runID, step).Scan(&raw, &decisionRaw)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil, nil
	}
	return raw, decisionRaw, err
}

// SaveModelCall persists a model call receipt under the active worker lease fence.
func (r *Repository) SaveModelCall(ctx context.Context, l *domain.Lease, runID string, step int, hash string, b []byte) error {
	tx, e := r.pool.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	if e = CheckLeaseTx(ctx, tx, l); e != nil {
		return e
	}
	var rejected bool
	if e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM runtime_context_recoveries WHERE run_id=$1 AND model_step=$2)`, runID, step).Scan(&rejected); e != nil {
		return e
	}
	if rejected {
		return domain.ErrReplayConflict
	}
	tag, e := tx.Exec(ctx, `INSERT INTO runtime_model_calls(run_id,step,request_hash,response) VALUES($1,$2,$3,$4) ON CONFLICT DO NOTHING`, runID, step, hash, b)
	if e != nil {
		return e
	}
	if tag.RowsAffected() == 0 {
		var old string
		e = tx.QueryRow(ctx, `SELECT request_hash FROM runtime_model_calls WHERE run_id=$1 AND step=$2`, runID, step).Scan(&old)
		if e != nil {
			return e
		}
		if old != hash {
			return domain.ErrReplayConflict
		}
	}
	return tx.Commit(ctx)
}

// RecordToolIntent records a tool call intent and returns any previously committed result.
func (r *Repository) RecordToolIntent(ctx context.Context, l *domain.Lease, runID, id, name, hash string) ([]byte, error) {
	tx, e := r.pool.Begin(ctx)
	if e != nil {
		return nil, e
	}
	defer tx.Rollback(ctx)
	if e = CheckLeaseTx(ctx, tx, l); e != nil {
		return nil, e
	}
	tag, e := tx.Exec(ctx, `INSERT INTO runtime_tool_calls(run_id,call_id,name,request_hash) VALUES($1,$2,$3,$4) ON CONFLICT DO NOTHING`, runID, id, name, hash)
	if e != nil {
		return nil, e
	}
	if tag.RowsAffected() == 1 {
		_, e = tx.Exec(ctx, `INSERT INTO runtime_public_events(agent_id,type,run_id,tool_name,status) VALUES($1,'tool.status',$2,$3,'running')`, l.AgentID, runID, strings.TrimPrefix(name, "native:"))
		if e != nil {
			return nil, e
		}
	}
	var old, oldName string
	var b []byte
	e = tx.QueryRow(ctx, `SELECT request_hash,name,result FROM runtime_tool_calls WHERE run_id=$1 AND call_id=$2`, runID, id).Scan(&old, &oldName, &b)
	if e != nil {
		return nil, e
	}
	if old != hash || name != oldName {
		return nil, fmt.Errorf("%w: tool intent changed", domain.ErrReplayConflict)
	}
	if e = tx.Commit(ctx); e != nil {
		return nil, e
	}
	return b, nil
}

// RecordToolResult commits the result of a tool call under the active worker lease fence.
func (r *Repository) RecordToolResult(ctx context.Context, l *domain.Lease, runID, id string, b []byte) error {
	tx, e := r.pool.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	if e = CheckLeaseTx(ctx, tx, l); e != nil {
		return e
	}
	tag, e := tx.Exec(ctx, `UPDATE runtime_tool_calls SET result=$3 WHERE run_id=$1 AND call_id=$2 AND result IS NULL`, runID, id, b)
	if e != nil {
		return e
	}
	if tag.RowsAffected() == 1 {
		_, e = tx.Exec(ctx, `INSERT INTO runtime_public_events(agent_id,type,run_id,tool_name,status) SELECT $1,'tool.status',run_id,regexp_replace(name,'^native:',''),'completed' FROM runtime_tool_calls WHERE run_id=$2 AND call_id=$3`, l.AgentID, runID, id)
		if e != nil {
			return e
		}
	}
	return tx.Commit(ctx)
}

// ListModelCallResponses returns all recorded model call responses for runID ordered by step.
func (r *Repository) ListModelCallResponses(ctx context.Context, runID string) ([][]byte, error) {
	rows, err := r.db.Conn(ctx).Query(ctx, `SELECT response FROM runtime_model_calls WHERE run_id=$1 ORDER BY step`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out [][]byte
	for rows.Next() {
		var raw []byte
		if err = rows.Scan(&raw); err != nil {
			return nil, err
		}
		out = append(out, raw)
	}
	return out, rows.Err()
}

// ToolCallResultRecord holds a persisted tool call receipt row.
type ToolCallResultRecord struct {
	CallID string
	Name   string
	Result []byte
}

// ListIMToolCallResults returns committed IM tool call results for runID.
func (r *Repository) ListIMToolCallResults(ctx context.Context, runID string) ([]ToolCallResultRecord, error) {
	rows, err := r.db.Conn(ctx).Query(ctx, `SELECT call_id,name,result FROM runtime_tool_calls WHERE run_id=$1 AND result IS NOT NULL AND name IN ('im_list_conversations','im_list_messages','im_get_message','im_send_message','im_get_attachment')`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ToolCallResultRecord
	for rows.Next() {
		var item ToolCallResultRecord
		if err = rows.Scan(&item.CallID, &item.Name, &item.Result); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

// AttemptEventPayload holds an attempt_id and serialized session event payload.
type AttemptEventPayload struct {
	AttemptID string
	Payload   []byte
}

// ListOtherAttemptMessageEvents returns message session events for runID from attempts other than excludeAttemptID.
func (r *Repository) ListOtherAttemptMessageEvents(ctx context.Context, runID, excludeAttemptID string) ([]AttemptEventPayload, error) {
	rows, err := r.db.Conn(ctx).Query(ctx, `SELECT attempt_id::text,payload FROM runtime_session_events WHERE run_id=$1 AND attempt_id<>$2 AND kind='message' ORDER BY seq`, runID, excludeAttemptID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AttemptEventPayload
	for rows.Next() {
		var item AttemptEventPayload
		if err = rows.Scan(&item.AttemptID, &item.Payload); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

// GetContextRecovery queries a recorded context recovery decision for (runID, step).
func (r *Repository) GetContextRecovery(ctx context.Context, runID string, step int) (hash, profile, code string, streaming bool, version int, decisionJSON []byte, err error) {
	err = r.db.Conn(ctx).QueryRow(ctx, `SELECT request_hash,context_profile,streaming,error_code,decision_version,decision_json FROM runtime_context_recoveries WHERE run_id=$1 AND model_step=$2`, runID, step).Scan(&hash, &profile, &streaming, &code, &version, &decisionJSON)
	return
}

// SaveContextRecovery persists a context recovery decision under the active worker lease fence.
func (r *Repository) SaveContextRecovery(
	ctx context.Context,
	lease *domain.Lease,
	runID string,
	step int,
	profile, hash string,
	streaming bool,
	code string,
	decision []byte,
	checkConflict func(oldProfile, oldHash string, oldMode bool, oldCode string, oldVersion int, oldDecision []byte) error,
) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err = CheckLeaseTx(ctx, tx, lease); err != nil {
		return err
	}
	var success bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM runtime_model_calls WHERE run_id=$1 AND step=$2)`, runID, step).Scan(&success); err != nil {
		return err
	}
	if success {
		return domain.ErrReplayConflict
	}
	tag, err := tx.Exec(ctx, `INSERT INTO runtime_context_recoveries(run_id,model_step,context_profile,request_hash,streaming,error_code,decision_version,decision_json) VALUES($1,$2,$3,$4,$5,$6,1,$7) ON CONFLICT DO NOTHING`, runID, step, profile, hash, streaming, code, decision)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		var oldProfile, oldHash, oldCode string
		var oldMode bool
		var oldVersion int
		var oldDecision []byte
		err = tx.QueryRow(ctx, `SELECT context_profile,request_hash,streaming,error_code,decision_version,decision_json FROM runtime_context_recoveries WHERE run_id=$1 AND model_step=$2`, runID, step).Scan(&oldProfile, &oldHash, &oldMode, &oldCode, &oldVersion, &oldDecision)
		if err != nil {
			return err
		}
		if err = checkConflict(oldProfile, oldHash, oldMode, oldCode, oldVersion, oldDecision); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}
