package engine

import (
	"context"
	"encoding/json"
	"time"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"
	"github.com/jackc/pgx/v5"
	"teamie/agent-runtime/internal/engine/domain"
	"teamie/agent-runtime/internal/engine/infra/eino"
)

const nativeRuntimeVersion = domain.NativeRuntimeVersion

type checkpointMetadata = domain.CheckpointMetadata

func (s *Store) ResumeMetadata(ctx context.Context, id string) (*checkpointMetadata, error) {
	var b []byte
	err := s.DB.QueryRow(ctx, `SELECT metadata FROM runtime_checkpoints WHERE checkpoint_id=$1`, id).Scan(&b)
	if err != nil {
		return nil, err
	}
	v := new(checkpointMetadata)
	if len(b) == 0 {
		return v, nil
	}
	err = json.Unmarshal(b, v)
	return v, err
}
func (s *SessionStore[M]) bind(run *Run, targets []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.run = run
	s.targets = append([]string{}, targets...)
}
func (s *SessionStore[M]) metadata() []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	m := checkpointMetadata{Version: nativeRuntimeVersion, Revision: s.Revision, Targets: append([]string{}, s.targets...)}
	if s.run != nil {
		m.ToolProfile = s.run.toolProfile
		if contextProfile(s.run) == contextProfileV1 {
			m.ContextProfile = s.run.ContextProfile
			_, budget, err := canonicalContextPolicy(s.run.ContextProfile, s.run.ContextBudget)
			if err == nil {
				m.ContextBudgetHash = digest(budget)
			}
		}
		m.RunID = s.run.ID
		m.Attempt = s.run.Attempt
		m.ModelStep = s.run.Step.Load()
	}
	b, _ := json.Marshal(m)
	return b
}
func (s *Store) configureRun(ctx context.Context, l *Lease, r *Run, revision, generation int64, resume *checkpointMetadata) error {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err = checkLease(ctx, tx, l); err != nil {
		return err
	}
	r.Revision = revision
	r.Generation = generation
	if resume != nil {
		r.Step.Store(resume.ModelStep)
		_, err = tx.Exec(ctx, `UPDATE runtime_runs SET revision=$2,generation=$3,context_attempts=array_append(context_attempts,$4::uuid),updated_at=now() WHERE id=$1`, r.ID, revision, generation, resume.Attempt)
	} else {
		_, err = tx.Exec(ctx, `UPDATE runtime_runs SET revision=$2,generation=$3,context_attempts='{}',updated_at=now() WHERE id=$1`, r.ID, revision, generation)
	}
	if err == nil {
		_, err = tx.Exec(ctx, `INSERT INTO runtime_public_events(agent_id,type,run_id,status) VALUES($1,'run.status',$2,'running')`, l.AgentID, r.ID)
	}
	if err == nil {
		err = tx.Commit(ctx)
	}
	return err
}

// FreshPending never lets processing rows occupy the delivery page. It is the
// push path while a native turn is active; recovery still uses Pending.
func (s *Store) FreshPending(ctx context.Context, l *Lease) ([]Input, error) {
	rows, err := s.DB.Query(ctx, `SELECT payload FROM runtime_inputs WHERE agent_id=$1 AND state='pending' ORDER BY delivery_seq LIMIT 256`, l.AgentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Input{}
	for rows.Next() {
		var b []byte
		var in Input
		if err = rows.Scan(&b); err == nil {
			err = json.Unmarshal(b, &in)
		}
		if err != nil {
			return nil, err
		}
		out = append(out, in)
	}
	return out, rows.Err()
}
func (s *Store) preemptRun(ctx context.Context, l *Lease, r *Run) error {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err = checkLease(ctx, tx, l); err != nil {
		return err
	}
	if err = acceptSessionAttempt(ctx, tx, l.AgentID, r.ID); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE runtime_runs SET state='interrupted',safe_error='new_input',committed_attempt=attempt_id,finished_at=now(),updated_at=now() WHERE id=$1`, r.ID)
	if err == nil {
		_, err = tx.Exec(ctx, `UPDATE runtime_inputs SET state='done' WHERE agent_id=$1 AND run_id=$2`, l.AgentID, r.ID)
	}
	if err == nil {
		_, err = tx.Exec(ctx, `INSERT INTO runtime_public_events(agent_id,type,run_id,status,error_code) VALUES($1,'run.status',$2,'interrupted','new_input')`, l.AgentID, r.ID)
	}
	if err == nil {
		err = tx.Commit(ctx)
	}
	return err
}
func (s *Store) runCanceled(ctx context.Context, id string) bool {
	var v bool
	_ = s.DB.QueryRow(ctx, `SELECT cancel_requested FROM runtime_runs WHERE id=$1`, id).Scan(&v)
	return v
}
func (s *Store) checkpointExists(ctx context.Context, id string) bool {
	var v bool
	_ = s.DB.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM runtime_checkpoints WHERE checkpoint_id=$1)`, id).Scan(&v)
	return v
}
func targetsFromError(err error) []string {
	return eino.TargetsFromCancelError(err)
}
func inputMessages(inputs []Input) []*schema.AgenticMessage {
	return eino.BuildInputMessages(inputs, func(in Input) string { return in.Kind })
}
func relevantInput(in Input, tc *adk.TurnContext[Input, *schema.AgenticMessage]) bool {
	if tc == nil {
		return false
	}
	return domain.RelevantPreemptInput(toDomainInput(in), toDomainInputs(tc.Consumed))
}
func resumeCompatible(m *checkpointMetadata, revision int64, runID string) bool {
	return domain.ResumeCompatible(m, revision, runID)
}
func detachedPersistence(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
}

func acceptSessionAttempt(ctx context.Context, tx pgx.Tx, agent, runID string) error {
	_, err := tx.Exec(ctx, `INSERT INTO runtime_session_acceptances(agent_id,attempt_id,run_id) SELECT $1,unnest(array_append(context_attempts,attempt_id)),id FROM runtime_runs WHERE id=$2 ON CONFLICT DO NOTHING`, agent, runID)
	return err
}

func (s *Store) readObservation(ctx context.Context, run *Run, call, key string) ([]byte, error) {
	var out []byte
	err := s.DB.QueryRow(ctx, `SELECT response FROM runtime_tool_observations WHERE run_id=$1 AND call_id=$2 AND observation_key=$3`, run.ID, call, key).Scan(&out)
	return out, err
}
func (s *Store) saveObservation(ctx context.Context, l *Lease, run *Run, call, key string, b []byte) error {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err = checkLease(ctx, tx, l); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO runtime_tool_observations(run_id,call_id,observation_key,response) VALUES($1,$2,$3,$4) ON CONFLICT DO NOTHING`, run.ID, call, key, b)
	if err == nil {
		err = tx.Commit(ctx)
	}
	return err
}

func (s *Store) eligibleInputs(ctx context.Context, l *Lease, inputs []Input) ([]Input, error) {
	ids := make([]string, 0, len(inputs))
	for _, in := range inputs {
		ids = append(ids, in.EventID)
	}
	rows, err := s.DB.Query(ctx, `SELECT event_id::text FROM runtime_inputs WHERE agent_id=$1 AND event_id::text=ANY($2::text[]) AND state IN ('pending','processing')`, l.AgentID, ids)
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
	if err = rows.Err(); err != nil {
		return nil, err
	}
	out := []Input{}
	for _, in := range inputs {
		if eligible[in.EventID] {
			out = append(out, in)
		}
	}
	return out, nil
}
