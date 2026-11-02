package pg

import (
	"context"
	"encoding/json"
	"time"

	"teamie/pkg/postgres"
	"teamie/services/agent-runtime/internal/domain"
)

// AuditOrphanRuns inspects non-terminal Runs that no longer own any runtime_inputs rows
// and classifies whether all of their original inputs have already reached a terminal state
// under a successor Run.
func (r *Repository) AuditOrphanRuns(ctx context.Context, agentID string) (*domain.OrphanRunReport, error) {
	return r.processOrphanRuns(ctx, agentID, false)
}

// ReconcileOrphanRuns audits orphan Runs and atomically transitions verified superseded_completed
// orphan retry/waiting_configuration Runs to canceled (safe_error='superseded_orphan_run')
// while preserving all historical model/tool call receipts.
func (r *Repository) ReconcileOrphanRuns(ctx context.Context, agentID string) (*domain.OrphanRunReport, error) {
	return r.processOrphanRuns(ctx, agentID, true)
}

func (r *Repository) processOrphanRuns(ctx context.Context, agentID string, reconcile bool) (*domain.OrphanRunReport, error) {
	mode := "audit"
	if reconcile {
		mode = "reconcile"
	}
	conn := r.db.Conn(ctx)
	rows, err := conn.Query(ctx, `
		SELECT r.id::text,
		       r.agent_id::text,
		       r.state,
		       r.safe_error,
		       r.started_at,
		       r.updated_at,
		       r.input_json,
		       (SELECT count(*) FROM runtime_model_calls m WHERE m.run_id = r.id),
		       (SELECT count(*) FROM runtime_tool_calls t WHERE t.run_id = r.id AND t.result IS NOT NULL),
		       (SELECT count(*) FROM runtime_tool_calls t WHERE t.run_id = r.id AND t.result IS NULL),
		       (SELECT count(*) FROM runtime_tasks k WHERE k.run_id = r.id AND k.snapshot->>'Status' IN ('pending','running'))
		FROM runtime_runs r
		WHERE r.state IN ('retry','waiting_configuration','running','waiting_input')
		  AND ($1 = '' OR r.agent_id::text = $1)
		  AND NOT EXISTS (SELECT 1 FROM runtime_inputs i WHERE i.run_id = r.id)
		ORDER BY r.started_at ASC, r.id ASC`, agentID)
	if err != nil {
		return nil, err
	}

	type rawOrphan struct {
		runID              string
		agentID            string
		state              string
		safeError          string
		startedAt          time.Time
		updatedAt          time.Time
		inputJSON          []byte
		modelCallCount     int
		completedToolCalls int
		unconfirmedIntents int
		activeTasks        int
	}
	var candidates []rawOrphan
	for rows.Next() {
		var c rawOrphan
		if err = rows.Scan(
			&c.runID,
			&c.agentID,
			&c.state,
			&c.safeError,
			&c.startedAt,
			&c.updatedAt,
			&c.inputJSON,
			&c.modelCallCount,
			&c.completedToolCalls,
			&c.unconfirmedIntents,
			&c.activeTasks,
		); err != nil {
			rows.Close()
			return nil, err
		}
		candidates = append(candidates, c)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()

	report := &domain.OrphanRunReport{
		Mode: mode,
		Runs: make([]domain.OrphanRunAuditItem, 0, len(candidates)),
	}

	for _, c := range candidates {
		var parsedInputs []struct {
			EventID string `json:"eventId"`
		}
		decodeOK := json.Unmarshal(c.inputJSON, &parsedInputs) == nil && len(parsedInputs) > 0
		summaries := make([]domain.OrphanRunInputSummary, 0, len(parsedInputs))
		allSuperseded := decodeOK

		for _, in := range parsedInputs {
			summary := domain.OrphanRunInputSummary{EventID: in.EventID}
			var curState, curRunID, curRunState string
			qErr := conn.QueryRow(ctx, `
				SELECT i.state, COALESCE(i.run_id::text,''), COALESCE(sr.state,'')
				FROM runtime_inputs i
				LEFT JOIN runtime_runs sr ON i.run_id = sr.id
				WHERE i.agent_id = $1 AND i.event_id = $2`, c.agentID, in.EventID).Scan(&curState, &curRunID, &curRunState)
			if postgres.IsNoRows(qErr) {
				summary.CurrentState = "missing"
				allSuperseded = false
			} else if qErr != nil {
				return nil, qErr
			} else {
				summary.CurrentState = curState
				summary.CurrentRunID = curRunID
				summary.CurrentRunState = curRunState
				switch curState {
				case "skipped":
					// Explicitly skipped source (retracted/unauthorized).
				case "done":
					if curRunID == "" || curRunID == c.runID || (curRunState != "completed" && curRunState != "interrupted") {
						allSuperseded = false
					}
				default:
					allSuperseded = false
				}
			}
			summaries = append(summaries, summary)
		}

		reconcilableState := c.state == "retry" || c.state == "waiting_configuration"
		classification := domain.OrphanRunManualReviewRequired
		if reconcilableState && allSuperseded && c.activeTasks == 0 && c.unconfirmedIntents == 0 {
			classification = domain.OrphanRunSupersededCompleted
		}

		item := domain.OrphanRunAuditItem{
			RunID:              c.runID,
			AgentID:            c.agentID,
			State:              c.state,
			SafeError:          c.safeError,
			StartedAt:          c.startedAt.UTC(),
			UpdatedAt:          c.updatedAt.UTC(),
			InputCount:         len(parsedInputs),
			ModelCallCount:     c.modelCallCount,
			CompletedToolCalls: c.completedToolCalls,
			UnconfirmedIntents: c.unconfirmedIntents,
			ActiveTasks:        c.activeTasks,
			Classification:     classification,
			Inputs:             summaries,
		}

		if reconcile && classification == domain.OrphanRunSupersededCompleted {
			reconciled := false
			txErr := r.WithinTx(ctx, func(txCtx context.Context) error {
				txConn := r.db.Conn(txCtx)
				var lockedState string
				sErr := txConn.QueryRow(txCtx, `
					SELECT state
					FROM runtime_runs
					WHERE id = $1
					  AND state IN ('retry','waiting_configuration')
					  AND NOT EXISTS (SELECT 1 FROM runtime_inputs i WHERE i.run_id = runtime_runs.id)
					FOR UPDATE`, c.runID).Scan(&lockedState)
				if postgres.IsNoRows(sErr) {
					return nil
				}
				if sErr != nil {
					return sErr
				}
				if _, uErr := txConn.Exec(txCtx, `
					UPDATE runtime_runs
					SET state = 'canceled',
					    safe_error = 'superseded_orphan_run',
					    finished_at = COALESCE(finished_at, now()),
					    updated_at = now()
					WHERE id = $1`, c.runID); uErr != nil {
					return uErr
				}
				if _, eErr := txConn.Exec(txCtx, `
					INSERT INTO runtime_public_events(agent_id, type, run_id, status, error_code)
					VALUES($1, 'run.status', $2, 'canceled', 'superseded_orphan_run')`, c.agentID, c.runID); eErr != nil {
					return eErr
				}
				reconciled = true
				return nil
			})
			if txErr != nil {
				return nil, txErr
			}
			if reconciled {
				item.Reconciled = true
				item.State = "canceled"
				item.SafeError = "superseded_orphan_run"
				report.ReconciledCount++
			}
		}

		report.TotalOrphans++
		if classification == domain.OrphanRunSupersededCompleted {
			report.SupersededCompleted++
		} else {
			report.ManualReview++
		}
		report.Runs = append(report.Runs, item)
	}
	return report, nil
}
