package pg

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"teamie/pkg/postgres"
	"teamie/services/agent-runtime/internal/domain"
)

// StartOrResumeRun atomically starts a new Run or resumes an interrupted/retryable Run for firstEventID.
func (r *Repository) StartOrResumeRun(
	ctx context.Context,
	l *domain.Lease,
	firstEventID, newRunID, attemptID string,
	resolveNew func() (key string, inputsJSON []byte, profile string, budget json.RawMessage, eventIDs []string, err error),
) (runID string, inputsJSON []byte, contextHead, acceptanceHead int64, contextProfile string, contextBudget json.RawMessage, resumed bool, err error) {
	err = r.WithinTx(ctx, func(txCtx context.Context) error {
		if err := r.CheckLeaseForUpdate(txCtx, l); err != nil {
			return err
		}
		conn := r.db.Conn(txCtx)
		var existing string
		var eventIDs []string
		qErr := conn.QueryRow(txCtx, `SELECT run_id::text FROM runtime_inputs WHERE agent_id=$1 AND event_id=$2 AND run_id IS NOT NULL`, l.AgentID, firstEventID).Scan(&existing)
		if qErr == nil {
			resumed = true
			runID = existing
			if err := conn.QueryRow(txCtx, `UPDATE runtime_runs SET state='running',attempt_id=$2,attempts=attempts+1 WHERE id=$1 AND state IN ('running','interrupted','waiting_configuration','retry','waiting_input') RETURNING input_json,context_head,acceptance_head,context_profile,context_budget`, runID, attemptID).Scan(&inputsJSON, &contextHead, &acceptanceHead, &contextProfile, &contextBudget); err != nil {
				return err
			}
			var parsed []struct {
				EventID string `json:"eventId"`
			}
			if err := json.Unmarshal(inputsJSON, &parsed); err != nil {
				return err
			}
			for _, item := range parsed {
				eventIDs = append(eventIDs, item.EventID)
			}
		} else if postgres.IsNoRows(qErr) {
			runID = newRunID
			var key string
			var rErr error
			key, inputsJSON, contextProfile, contextBudget, eventIDs, rErr = resolveNew()
			if rErr != nil {
				return rErr
			}
			if err := conn.QueryRow(txCtx, `SELECT (SELECT COALESCE(max(seq),0) FROM runtime_session_events WHERE session_id=$1),(SELECT COALESCE(max(seq),0) FROM runtime_session_acceptances WHERE agent_id=$2)`, "agent:"+l.AgentID, l.AgentID).Scan(&contextHead, &acceptanceHead); err != nil {
				return err
			}
			if _, err := conn.Exec(txCtx, `INSERT INTO runtime_runs(id,agent_id,input_key,input_json,attempt_id,context_head,acceptance_head,context_profile,context_budget) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)`, runID, l.AgentID, key, inputsJSON, attemptID, contextHead, acceptanceHead, contextProfile, contextBudget); err != nil {
				return err
			}
		} else {
			return qErr
		}
		for _, evID := range eventIDs {
			if _, err := conn.Exec(txCtx, `UPDATE runtime_inputs SET state='processing',run_id=$3 WHERE agent_id=$1 AND event_id=$2 AND state IN ('pending','processing')`, l.AgentID, evID, runID); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return "", nil, 0, 0, "", nil, false, err
	}
	return runID, inputsJSON, contextHead, acceptanceHead, contextProfile, contextBudget, resumed, nil
}

// FinishRun transitions a Run to completed, retry, waiting_input, failed, or canceled under the worker lease fence.
func (r *Repository) FinishRun(ctx context.Context, l *domain.Lease, runID, state, safeError string, retry bool) error {
	return r.WithinTx(ctx, func(txCtx context.Context) error {
		if e := r.CheckLeaseForUpdate(txCtx, l); e != nil {
			return e
		}
		conn := r.db.Conn(txCtx)
		if state == "completed" {
			if e := r.acceptSessionAttempt(txCtx, l.AgentID, runID); e != nil {
				return e
			}
		}
		var e error
		if retry {
			_, e = conn.Exec(txCtx, `UPDATE runtime_runs SET state=$2,safe_error=$3,updated_at=now(),next_attempt=now()+interval '15 seconds' WHERE id=$1`, runID, state, safeError)
		} else {
			_, e = conn.Exec(txCtx, `UPDATE runtime_runs SET state=$2,safe_error=$3,updated_at=now(),committed_attempt=CASE WHEN $2='completed' THEN attempt_id ELSE NULL END,finished_at=now() WHERE id=$1`, runID, state, safeError)
			if e == nil {
				_, e = conn.Exec(txCtx, `UPDATE runtime_inputs SET state=$2 WHERE agent_id=$1 AND run_id=$3`, l.AgentID, map[bool]string{true: "done", false: "failed"}[state == "completed"], runID)
			}
		}
		if e != nil {
			return e
		}
		_, e = conn.Exec(txCtx, `INSERT INTO runtime_public_events(agent_id,type,run_id,status,error_code) VALUES($1,'run.status',$2,$3,NULLIF($4,''))`, l.AgentID, runID, state, safeError)
		return e
	})
}

// InvalidateRun abandons a replay whose source was revoked or changed.
func (r *Repository) InvalidateRun(ctx context.Context, l *domain.Lease, runID string, invalid []string) error {
	return r.WithinTx(ctx, func(txCtx context.Context) error {
		if e := r.CheckLeaseForUpdate(txCtx, l); e != nil {
			return e
		}
		conn := r.db.Conn(txCtx)
		if _, e := conn.Exec(txCtx, `UPDATE runtime_runs SET state='failed',safe_error='source_unavailable',finished_at=now() WHERE id=$1`, runID); e != nil {
			return e
		}
		_, e := conn.Exec(txCtx, `UPDATE runtime_inputs SET state=CASE WHEN event_id::text=ANY($3::text[]) THEN 'skipped' ELSE 'pending' END,run_id=NULL WHERE agent_id=$1 AND run_id=$2`, l.AgentID, runID, invalid)
		return e
	})
}

// ConfigureRun updates a Run's revision, generation, and context_attempts and records run.status=running.
func (r *Repository) ConfigureRun(ctx context.Context, l *domain.Lease, runID string, revision, generation int64, resumeAttempt string, hasResume bool) error {
	return r.WithinTx(ctx, func(txCtx context.Context) error {
		if err := r.CheckLeaseForUpdate(txCtx, l); err != nil {
			return err
		}
		conn := r.db.Conn(txCtx)
		var err error
		if hasResume {
			_, err = conn.Exec(txCtx, `UPDATE runtime_runs SET revision=$2,generation=$3,context_attempts=array_append(context_attempts,$4::uuid),updated_at=now() WHERE id=$1`, runID, revision, generation, resumeAttempt)
		} else {
			_, err = conn.Exec(txCtx, `UPDATE runtime_runs SET revision=$2,generation=$3,context_attempts='{}',updated_at=now() WHERE id=$1`, runID, revision, generation)
		}
		if err == nil {
			_, err = conn.Exec(txCtx, `INSERT INTO runtime_public_events(agent_id,type,run_id,status) VALUES($1,'run.status',$2,'running')`, l.AgentID, runID)
		}
		return err
	})
}

// PreemptRun interrupts an active Run for new input after accepting its session attempt.
func (r *Repository) PreemptRun(ctx context.Context, l *domain.Lease, runID string) error {
	return r.WithinTx(ctx, func(txCtx context.Context) error {
		if err := r.CheckLeaseForUpdate(txCtx, l); err != nil {
			return err
		}
		if err := r.acceptSessionAttempt(txCtx, l.AgentID, runID); err != nil {
			return err
		}
		conn := r.db.Conn(txCtx)
		_, err := conn.Exec(txCtx, `UPDATE runtime_runs SET state='interrupted',safe_error='new_input',committed_attempt=attempt_id,finished_at=now(),updated_at=now() WHERE id=$1`, runID)
		if err == nil {
			_, err = conn.Exec(txCtx, `UPDATE runtime_inputs SET state='done' WHERE agent_id=$1 AND run_id=$2`, l.AgentID, runID)
		}
		if err == nil {
			_, err = conn.Exec(txCtx, `INSERT INTO runtime_public_events(agent_id,type,run_id,status,error_code) VALUES($1,'run.status',$2,'interrupted','new_input')`, l.AgentID, runID)
		}
		return err
	})
}

// IsRunCanceled reports whether cancel_requested is set on runID.
func (r *Repository) IsRunCanceled(ctx context.Context, runID string) bool {
	var v bool
	_ = r.db.Conn(ctx).QueryRow(ctx, `SELECT cancel_requested FROM runtime_runs WHERE id=$1`, runID).Scan(&v)
	return v
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
	if postgres.IsNoRows(err) {
		return nil, nil, nil
	}
	return raw, decisionRaw, err
}

// SaveModelCall persists a model call receipt under the active worker lease fence.
func (r *Repository) SaveModelCall(ctx context.Context, l *domain.Lease, runID string, step int, hash string, b []byte) error {
	return r.WithinTx(ctx, func(txCtx context.Context) error {
		if e := r.CheckLeaseForUpdate(txCtx, l); e != nil {
			return e
		}
		conn := r.db.Conn(txCtx)
		var rejected bool
		if e := conn.QueryRow(txCtx, `SELECT EXISTS(SELECT 1 FROM runtime_context_recoveries WHERE run_id=$1 AND model_step=$2)`, runID, step).Scan(&rejected); e != nil {
			return e
		}
		if rejected {
			return domain.ErrReplayConflict
		}
		tag, e := conn.Exec(txCtx, `INSERT INTO runtime_model_calls(run_id,step,request_hash,response) VALUES($1,$2,$3,$4) ON CONFLICT DO NOTHING`, runID, step, hash, b)
		if e != nil {
			return e
		}
		if tag.RowsAffected() == 0 {
			var old string
			if e = conn.QueryRow(txCtx, `SELECT request_hash FROM runtime_model_calls WHERE run_id=$1 AND step=$2`, runID, step).Scan(&old); e != nil {
				return e
			}
			if old != hash {
				return domain.ErrReplayConflict
			}
		}
		return nil
	})
}

// RecordToolIntent records a tool call intent and returns any previously committed result.
func (r *Repository) RecordToolIntent(ctx context.Context, l *domain.Lease, runID, id, name, hash string) ([]byte, error) {
	b, _, err := r.RecordExternalToolIntent(ctx, l, runID, id, name, hash)
	return b, err
}

// RecordExternalToolIntent atomically distinguishes a fresh claim from an
// unfinished external operation. The claim survives attempts and worker leases.
func (r *Repository) RecordExternalToolIntent(ctx context.Context, l *domain.Lease, runID, id, name, hash string) ([]byte, bool, error) {
	var b []byte
	var created bool
	err := r.WithinTx(ctx, func(txCtx context.Context) error {
		if e := r.CheckLeaseForUpdate(txCtx, l); e != nil {
			return e
		}
		conn := r.db.Conn(txCtx)
		tag, e := conn.Exec(txCtx, `INSERT INTO runtime_tool_calls(run_id,call_id,name,request_hash) VALUES($1,$2,$3,$4) ON CONFLICT DO NOTHING`, runID, id, name, hash)
		if e != nil {
			return e
		}
		created = tag.RowsAffected() == 1
		if created {
			if _, e = conn.Exec(txCtx, `INSERT INTO runtime_public_events(agent_id,type,run_id,tool_name,status) VALUES($1,'tool.status',$2,$3,'running')`, l.AgentID, runID, strings.TrimPrefix(name, "native:")); e != nil {
				return e
			}
		}
		var old, oldName string
		if e = conn.QueryRow(txCtx, `SELECT request_hash,name,result FROM runtime_tool_calls WHERE run_id=$1 AND call_id=$2`, runID, id).Scan(&old, &oldName, &b); e != nil {
			return e
		}
		if old != hash || name != oldName {
			return fmt.Errorf("%w: tool intent changed", domain.ErrReplayConflict)
		}
		return nil
	})
	if err != nil {
		return nil, false, err
	}
	return b, created, nil
}

// RecordToolResult commits the result of a tool call under the active worker lease fence.
func (r *Repository) RecordToolResult(ctx context.Context, l *domain.Lease, runID, id string, b []byte) error {
	return r.WithinTx(ctx, func(txCtx context.Context) error {
		if e := r.CheckLeaseForUpdate(txCtx, l); e != nil {
			return e
		}
		conn := r.db.Conn(txCtx)
		tag, e := conn.Exec(txCtx, `UPDATE runtime_tool_calls SET result=$3 WHERE run_id=$1 AND call_id=$2 AND result IS NULL`, runID, id, b)
		if e != nil {
			return e
		}
		if tag.RowsAffected() == 1 {
			if _, e = conn.Exec(txCtx, `INSERT INTO runtime_public_events(agent_id,type,run_id,tool_name,status) SELECT $1,'tool.status',run_id,regexp_replace(name,'^native:',''),'completed' FROM runtime_tool_calls WHERE run_id=$2 AND call_id=$3`, l.AgentID, runID, id); e != nil {
				return e
			}
		}
		return nil
	})
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
type ToolCallResultRecord = domain.ToolCallResultRecord

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
type AttemptEventPayload = domain.AttemptEventPayload

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
	return r.WithinTx(ctx, func(txCtx context.Context) error {
		if err := r.CheckLeaseForUpdate(txCtx, lease); err != nil {
			return err
		}
		conn := r.db.Conn(txCtx)
		var success bool
		if err := conn.QueryRow(txCtx, `SELECT EXISTS(SELECT 1 FROM runtime_model_calls WHERE run_id=$1 AND step=$2)`, runID, step).Scan(&success); err != nil {
			return err
		}
		if success {
			return domain.ErrReplayConflict
		}
		tag, err := conn.Exec(txCtx, `INSERT INTO runtime_context_recoveries(run_id,model_step,context_profile,request_hash,streaming,error_code,decision_version,decision_json) VALUES($1,$2,$3,$4,$5,$6,1,$7) ON CONFLICT DO NOTHING`, runID, step, profile, hash, streaming, code, decision)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			var oldProfile, oldHash, oldCode string
			var oldMode bool
			var oldVersion int
			var oldDecision []byte
			if err = conn.QueryRow(txCtx, `SELECT context_profile,request_hash,streaming,error_code,decision_version,decision_json FROM runtime_context_recoveries WHERE run_id=$1 AND model_step=$2`, runID, step).Scan(&oldProfile, &oldHash, &oldMode, &oldCode, &oldVersion, &oldDecision); err != nil {
				return err
			}
			if err = checkConflict(oldProfile, oldHash, oldMode, oldCode, oldVersion, oldDecision); err != nil {
				return err
			}
		}
		return nil
	})
}
