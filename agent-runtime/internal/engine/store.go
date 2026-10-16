package engine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var ErrLeaseLost = errors.New("runtime lease lost")
var ErrReplayConflict = errors.New("durable execution replay conflict")

type Store struct{ DB *pgxpool.Pool }
type Lease struct {
	AgentID, Owner string
	Fence          int64
}
type Input struct {
	EventID        string `json:"eventId"`
	WorkspaceID    string `json:"workspaceId"`
	ConversationID string `json:"conversationId"`
	MessageID      string `json:"messageId"`
	SenderID       string `json:"senderId"`
	SenderName     string `json:"senderName"`
	SenderKind     string `json:"senderKind"`
	Body           string `json:"body"`
	Seq            int64  `json:"seq"`
	CreatedAt      string `json:"createdAt"`
}
type Run struct {
	ID, Attempt, AgentID string
	Inputs               []Input
	Step                 atomic.Int64
}

func executionID(run *Run) string { return run.ID + ":" + run.Attempt }

type runKey struct{}

func WithRun(ctx context.Context, run *Run) context.Context {
	return context.WithValue(ctx, runKey{}, run)
}
func CurrentRun(ctx context.Context) (*Run, error) {
	r, ok := ctx.Value(runKey{}).(*Run)
	if !ok {
		return nil, errors.New("missing run context")
	}
	return r, nil
}
func digest(b []byte) string { v := sha256.Sum256(b); return hex.EncodeToString(v[:]) }

const schemaSQL = `
CREATE TABLE IF NOT EXISTS agent_runtime_schema_migrations(version integer PRIMARY KEY, applied_at timestamptz NOT NULL DEFAULT now());
CREATE TABLE IF NOT EXISTS runtime_agents(agent_id uuid PRIMARY KEY, cursor numeric(30) NOT NULL DEFAULT 0, lease_owner text, lease_until timestamptz, fence bigint NOT NULL DEFAULT 0, updated_at timestamptz NOT NULL DEFAULT now());
CREATE TABLE IF NOT EXISTS runtime_inputs(agent_id uuid NOT NULL, event_id numeric(30) NOT NULL, payload jsonb NOT NULL, state text NOT NULL DEFAULT 'pending', run_id uuid, created_at timestamptz NOT NULL DEFAULT now(), PRIMARY KEY(agent_id,event_id));
CREATE TABLE IF NOT EXISTS runtime_runs(id uuid PRIMARY KEY, agent_id uuid NOT NULL, input_key text NOT NULL, input_json jsonb NOT NULL, state text NOT NULL DEFAULT 'running', attempt_id uuid NOT NULL, committed_attempt uuid, attempts integer NOT NULL DEFAULT 1, next_attempt timestamptz NOT NULL DEFAULT now(), safe_error text NOT NULL DEFAULT '', started_at timestamptz NOT NULL DEFAULT now(), finished_at timestamptz, UNIQUE(agent_id,input_key));
CREATE INDEX IF NOT EXISTS runtime_inputs_pending ON runtime_inputs(agent_id,event_id) WHERE state IN ('pending','processing');
CREATE TABLE IF NOT EXISTS runtime_session_events(seq bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY, session_id text NOT NULL, event_id text NOT NULL, kind text NOT NULL, run_id uuid NOT NULL REFERENCES runtime_runs(id), attempt_id uuid NOT NULL, payload bytea NOT NULL, payload_hash text NOT NULL, UNIQUE(session_id,event_id));
CREATE INDEX IF NOT EXISTS runtime_session_order ON runtime_session_events(session_id,seq);
CREATE TABLE IF NOT EXISTS runtime_checkpoints(checkpoint_id text PRIMARY KEY, payload bytea NOT NULL, updated_at timestamptz NOT NULL DEFAULT now());
CREATE TABLE IF NOT EXISTS runtime_model_calls(run_id uuid NOT NULL REFERENCES runtime_runs(id), step integer NOT NULL, request_hash text NOT NULL, response bytea NOT NULL, PRIMARY KEY(run_id,step));
CREATE TABLE IF NOT EXISTS runtime_tool_calls(run_id uuid NOT NULL REFERENCES runtime_runs(id), call_id text NOT NULL, name text NOT NULL, request_hash text NOT NULL, result bytea, created_at timestamptz NOT NULL DEFAULT now(), PRIMARY KEY(run_id,call_id));
INSERT INTO agent_runtime_schema_migrations(version) VALUES(1) ON CONFLICT DO NOTHING;`

func Open(ctx context.Context, url string) (*Store, error) {
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, err
	}
	cfg.MaxConns = 4
	cfg.MinConns = 0
	cfg.ConnConfig.ConnectTimeout = 5 * time.Second
	cfg.ConnConfig.RuntimeParams["timezone"] = "UTC"
	db, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, err
	}
	tx, err := db.Begin(ctx)
	if err != nil {
		db.Close()
		return nil, err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(704205)"); err == nil {
		_, err = tx.Exec(ctx, schemaSQL)
	}
	if err == nil {
		err = tx.Commit(ctx)
	}
	if err != nil {
		db.Close()
		return nil, err
	}
	return &Store{DB: db}, nil
}
func (s *Store) Acquire(ctx context.Context, agentID, owner string) (*Lease, error) {
	if _, err := uuid.Parse(agentID); err != nil {
		return nil, err
	}
	_, err := s.DB.Exec(ctx, "INSERT INTO runtime_agents(agent_id) VALUES($1) ON CONFLICT DO NOTHING", agentID)
	if err != nil {
		return nil, err
	}
	l := &Lease{AgentID: agentID, Owner: owner}
	err = s.DB.QueryRow(ctx, `UPDATE runtime_agents SET lease_owner=$2,lease_until=now()+interval '45 seconds',fence=fence+1 WHERE agent_id=$1 AND (lease_until IS NULL OR lease_until<now()) RETURNING fence`, agentID, owner).Scan(&l.Fence)
	return l, err
}
func (s *Store) Renew(ctx context.Context, l *Lease) error {
	tag, err := s.DB.Exec(ctx, `UPDATE runtime_agents SET lease_until=now()+interval '45 seconds' WHERE agent_id=$1 AND lease_owner=$2 AND fence=$3 AND lease_until>now()`, l.AgentID, l.Owner, l.Fence)
	if err == nil && tag.RowsAffected() != 1 {
		return ErrLeaseLost
	}
	return err
}
func (s *Store) Release(ctx context.Context, l *Lease) {
	_, _ = s.DB.Exec(ctx, `UPDATE runtime_agents SET lease_until=NULL,lease_owner=NULL WHERE agent_id=$1 AND lease_owner=$2 AND fence=$3`, l.AgentID, l.Owner, l.Fence)
}
func checkLease(ctx context.Context, tx pgx.Tx, l *Lease) error {
	var ok bool
	err := tx.QueryRow(ctx, `SELECT lease_owner=$2 AND fence=$3 AND lease_until>now() FROM runtime_agents WHERE agent_id=$1 FOR UPDATE`, l.AgentID, l.Owner, l.Fence).Scan(&ok)
	if err != nil {
		return err
	}
	if !ok {
		return ErrLeaseLost
	}
	return nil
}
func (s *Store) CheckLease(ctx context.Context, l *Lease) error {
	var ok bool
	err := s.DB.QueryRow(ctx, `SELECT lease_owner=$2 AND fence=$3 AND lease_until>now() FROM runtime_agents WHERE agent_id=$1`, l.AgentID, l.Owner, l.Fence).Scan(&ok)
	if err != nil {
		return err
	}
	if !ok {
		return ErrLeaseLost
	}
	return nil
}
func (s *Store) Cursor(ctx context.Context, l *Lease) (string, error) {
	var c string
	err := s.DB.QueryRow(ctx, `SELECT cursor::text FROM runtime_agents WHERE agent_id=$1`, l.AgentID).Scan(&c)
	return c, err
}
func (s *Store) Ingest(ctx context.Context, l *Lease, cursor string, inputs []Input) error {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err = checkLease(ctx, tx, l); err != nil {
		return err
	}
	for _, in := range inputs {
		b, e := json.Marshal(in)
		if e != nil {
			return e
		}
		if _, err = tx.Exec(ctx, `INSERT INTO runtime_inputs(agent_id,event_id,payload) VALUES($1,$2,$3) ON CONFLICT DO NOTHING`, l.AgentID, in.EventID, b); err != nil {
			return err
		}
	}
	if _, err = tx.Exec(ctx, `UPDATE runtime_agents SET cursor=GREATEST(cursor,$2::numeric),updated_at=now() WHERE agent_id=$1`, l.AgentID, cursor); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
func (s *Store) Pending(ctx context.Context, l *Lease) ([]Input, error) {
	rows, err := s.DB.Query(ctx, `SELECT i.payload FROM runtime_inputs i LEFT JOIN runtime_runs r ON i.run_id=r.id WHERE i.agent_id=$1 AND i.state IN ('pending','processing') AND (r.id IS NULL OR r.next_attempt<=now()) ORDER BY i.event_id LIMIT 4`, l.AgentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Input{}
	for rows.Next() {
		var b []byte
		var in Input
		if err = rows.Scan(&b); err != nil {
			return nil, err
		}
		if err = json.Unmarshal(b, &in); err != nil {
			return nil, err
		}
		out = append(out, in)
	}
	return out, rows.Err()
}
func (s *Store) Skip(ctx context.Context, l *Lease, id string) error {
	tx, e := s.DB.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	if e = checkLease(ctx, tx, l); e != nil {
		return e
	}
	_, e = tx.Exec(ctx, `UPDATE runtime_inputs SET state='skipped' WHERE agent_id=$1 AND event_id=$2`, l.AgentID, id)
	if e != nil {
		return e
	}
	return tx.Commit(ctx)
}
func (s *Store) StartRun(ctx context.Context, l *Lease, inputs []Input) (*Run, error) {
	if len(inputs) == 0 {
		return nil, errors.New("empty run")
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	if err = checkLease(ctx, tx, l); err != nil {
		return nil, err
	}
	// A crashed run is replayed with its original immutable input batch.
	var existing string
	err = tx.QueryRow(ctx, `SELECT run_id::text FROM runtime_inputs WHERE agent_id=$1 AND event_id=$2 AND run_id IS NOT NULL`, l.AgentID, inputs[0].EventID).Scan(&existing)
	r := &Run{ID: uuid.NewString(), Attempt: uuid.NewString(), AgentID: l.AgentID, Inputs: inputs}
	if err == nil {
		r.ID = existing
		var b []byte
		err = tx.QueryRow(ctx, `UPDATE runtime_runs SET state='running',attempt_id=$2,attempts=attempts+1 WHERE id=$1 AND state IN ('running','interrupted','waiting_configuration','retry') RETURNING input_json`, r.ID, r.Attempt).Scan(&b)
		if err != nil {
			return nil, err
		}
		if err = json.Unmarshal(b, &r.Inputs); err != nil {
			return nil, err
		}
	} else if errors.Is(err, pgx.ErrNoRows) {
		b, e := json.Marshal(inputs)
		if e != nil {
			return nil, e
		}
		ids := make([]string, len(inputs))
		for i, in := range inputs {
			ids[i] = in.EventID
		}
		key := strings.Join(ids, ",")
		_, err = tx.Exec(ctx, `INSERT INTO runtime_runs(id,agent_id,input_key,input_json,attempt_id) VALUES($1,$2,$3,$4,$5)`, r.ID, l.AgentID, key, b, r.Attempt)
		if err != nil {
			return nil, err
		}
	} else {
		return nil, err
	}
	for _, in := range r.Inputs {
		if _, err = tx.Exec(ctx, `UPDATE runtime_inputs SET state='processing',run_id=$3 WHERE agent_id=$1 AND event_id=$2 AND state IN ('pending','processing')`, l.AgentID, in.EventID, r.ID); err != nil {
			return nil, err
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return r, nil
}
func (s *Store) FinishRun(ctx context.Context, l *Lease, r *Run, state, safeError string, retry bool) error {
	tx, e := s.DB.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	if e = checkLease(ctx, tx, l); e != nil {
		return e
	}
	if retry {
		_, e = tx.Exec(ctx, `UPDATE runtime_runs SET state=$2,safe_error=$3,next_attempt=now()+interval '15 seconds' WHERE id=$1`, r.ID, state, safeError)
	} else {
		_, e = tx.Exec(ctx, `UPDATE runtime_runs SET state=$2,safe_error=$3,committed_attempt=CASE WHEN $2='completed' THEN attempt_id ELSE NULL END,finished_at=now() WHERE id=$1`, r.ID, state, safeError)
		if e == nil {
			_, e = tx.Exec(ctx, `UPDATE runtime_inputs SET state=$2 WHERE agent_id=$1 AND run_id=$3`, l.AgentID, map[bool]string{true: "done", false: "failed"}[state == "completed"], r.ID)
		}
	}
	if e != nil {
		return e
	}
	return tx.Commit(ctx)
}

type SessionStore struct {
	Store *Store
	Lease *Lease
}

func (s *SessionStore) AppendEvents(ctx context.Context, sessionID string, events []*adk.SessionEvent[*schema.Message]) error {
	// Native Eino flushes terminal events/checkpoints after cancellation. Keep
	// values and lease fencing while bounding the independent persistence work.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()

	r, e := CurrentRun(ctx)
	if e != nil {
		return e
	}
	if sessionID != "agent:"+s.Lease.AgentID {
		return errors.New("session scope mismatch")
	}
	tx, e := s.Store.DB.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	if e = checkLease(ctx, tx, s.Lease); e != nil {
		return e
	}
	ser := &schema.HumanReadableSerializer{}
	for _, event := range events {
		if event == nil || event.EventID == "" {
			return adk.ErrInvalidEventID
		}
		if e = adk.NormalizeSessionEventKind(event); e != nil {
			return e
		}
		b, err := ser.Marshal(event)
		if err != nil {
			return err
		}
		hash := digest(b)
		tag, err := tx.Exec(ctx, `INSERT INTO runtime_session_events(session_id,event_id,kind,run_id,attempt_id,payload,payload_hash) VALUES($1,$2,$3,$4,$5,$6,$7) ON CONFLICT DO NOTHING`, sessionID, event.EventID, string(event.Kind), r.ID, r.Attempt, b, hash)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			var old string
			if err = tx.QueryRow(ctx, `SELECT payload_hash FROM runtime_session_events WHERE session_id=$1 AND event_id=$2`, sessionID, event.EventID).Scan(&old); err != nil {
				return err
			}
			if old != hash {
				return adk.ErrDuplicateEventID
			}
		}
	}
	return tx.Commit(ctx)
}
func (s *SessionStore) LoadEvents(ctx context.Context, sessionID string, req *adk.LoadSessionEventsRequest) (*adk.LoadSessionEventsResult[*schema.Message], error) {
	r, e := CurrentRun(ctx)
	if e != nil {
		return nil, e
	}
	if sessionID != "agent:"+s.Lease.AgentID {
		return nil, errors.New("session scope mismatch")
	}
	if req == nil {
		req = &adk.LoadSessionEventsRequest{}
	}
	var after int64
	if req.Reverse {
		after = 1<<63 - 1
	}
	visible := `(e.attempt_id=$2 OR (r.state='completed' AND e.attempt_id=r.committed_attempt))`
	if req.After != "" {
		e = s.Store.DB.QueryRow(ctx, `SELECT e.seq FROM runtime_session_events e JOIN runtime_runs r ON r.id=e.run_id WHERE e.session_id=$1 AND `+visible+` AND e.event_id=$3`, sessionID, r.Attempt, req.After).Scan(&after)
		if errors.Is(e, pgx.ErrNoRows) {
			return nil, adk.ErrEventIDOutOfRange
		}
		if e != nil {
			return nil, e
		}
	}
	kinds := make([]string, 0, len(req.Kinds))
	for _, k := range req.Kinds {
		kinds = append(kinds, string(k))
	}
	limit := req.Limit
	if limit <= 0 {
		limit = 1<<31 - 1
	}
	order, comparison := "ASC", ">"
	if req.Reverse {
		order, comparison = "DESC", "<"
	}
	rows, e := s.Store.DB.Query(ctx, `SELECT e.payload FROM runtime_session_events e JOIN runtime_runs r ON r.id=e.run_id WHERE e.session_id=$1 AND `+visible+` AND e.seq `+comparison+` $3 AND (cardinality($4::text[])=0 OR e.kind=ANY($4)) ORDER BY e.seq `+order+` LIMIT $5`, sessionID, r.Attempt, after, kinds, limit)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := &adk.LoadSessionEventsResult[*schema.Message]{Events: []*adk.SessionEvent[*schema.Message]{}}
	ser := &schema.HumanReadableSerializer{}
	for rows.Next() {
		var b []byte
		if e = rows.Scan(&b); e != nil {
			return nil, e
		}
		event := new(adk.SessionEvent[*schema.Message])
		if e = ser.Unmarshal(b, event); e != nil {
			return nil, e
		}
		out.Events = append(out.Events, event)
		out.Next = event.EventID
	}
	return out, rows.Err()
}
func (s *SessionStore) Get(ctx context.Context, id string) ([]byte, bool, error) {
	if !strings.Contains(id, s.Lease.AgentID) {
		return nil, false, errors.New("checkpoint scope mismatch")
	}
	var b []byte
	e := s.Store.DB.QueryRow(ctx, `SELECT payload FROM runtime_checkpoints WHERE checkpoint_id=$1`, id).Scan(&b)
	if errors.Is(e, pgx.ErrNoRows) {
		return nil, false, nil
	}
	return b, e == nil, e
}
func (s *SessionStore) Set(ctx context.Context, id string, b []byte) error {
	// Native Eino flushes terminal events/checkpoints after cancellation. Keep
	// values and lease fencing while bounding the independent persistence work.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()

	if !strings.Contains(id, s.Lease.AgentID) {
		return errors.New("checkpoint scope mismatch")
	}
	tx, e := s.Store.DB.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	if e = checkLease(ctx, tx, s.Lease); e != nil {
		return e
	}
	_, e = tx.Exec(ctx, `INSERT INTO runtime_checkpoints(checkpoint_id,payload) VALUES($1,$2) ON CONFLICT(checkpoint_id) DO UPDATE SET payload=EXCLUDED.payload,updated_at=now()`, id, b)
	if e != nil {
		return e
	}
	return tx.Commit(ctx)
}
func (s *SessionStore) Delete(ctx context.Context, id string) error {
	// Native Eino flushes terminal events/checkpoints after cancellation. Keep
	// values and lease fencing while bounding the independent persistence work.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()

	if !strings.Contains(id, s.Lease.AgentID) {
		return errors.New("checkpoint scope mismatch")
	}
	tx, e := s.Store.DB.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	if e = checkLease(ctx, tx, s.Lease); e != nil {
		return e
	}
	_, e = tx.Exec(ctx, `DELETE FROM runtime_checkpoints WHERE checkpoint_id=$1`, id)
	if e != nil {
		return e
	}
	return tx.Commit(ctx)
}

func (s *Store) CachedModel(ctx context.Context, r *Run, step int, hash string) ([]byte, error) {
	var old string
	var b []byte
	e := s.DB.QueryRow(ctx, `SELECT request_hash,response FROM runtime_model_calls WHERE run_id=$1 AND step=$2`, r.ID, step).Scan(&old, &b)
	if e == nil && old != hash {
		return nil, ErrReplayConflict
	}
	return b, e
}
func (s *Store) SaveModel(ctx context.Context, l *Lease, r *Run, step int, hash string, b []byte) error {
	tx, e := s.DB.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	if e = checkLease(ctx, tx, l); e != nil {
		return e
	}
	tag, e := tx.Exec(ctx, `INSERT INTO runtime_model_calls(run_id,step,request_hash,response) VALUES($1,$2,$3,$4) ON CONFLICT DO NOTHING`, r.ID, step, hash, b)
	if e != nil {
		return e
	}
	if tag.RowsAffected() == 0 {
		var old string
		e = tx.QueryRow(ctx, `SELECT request_hash FROM runtime_model_calls WHERE run_id=$1 AND step=$2`, r.ID, step).Scan(&old)
		if e != nil {
			return e
		}
		if old != hash {
			return ErrReplayConflict
		}
	}
	return tx.Commit(ctx)
}
func (s *Store) ToolIntent(ctx context.Context, l *Lease, r *Run, id, name string, payload []byte) ([]byte, error) {
	if id == "" {
		return nil, errors.New("tool call has no identity")
	}
	hash := digest(payload)
	tx, e := s.DB.Begin(ctx)
	if e != nil {
		return nil, e
	}
	defer tx.Rollback(ctx)
	if e = checkLease(ctx, tx, l); e != nil {
		return nil, e
	}
	_, e = tx.Exec(ctx, `INSERT INTO runtime_tool_calls(run_id,call_id,name,request_hash) VALUES($1,$2,$3,$4) ON CONFLICT DO NOTHING`, r.ID, id, name, hash)
	if e != nil {
		return nil, e
	}
	var old, oldName string
	var b []byte
	e = tx.QueryRow(ctx, `SELECT request_hash,name,result FROM runtime_tool_calls WHERE run_id=$1 AND call_id=$2`, r.ID, id).Scan(&old, &oldName, &b)
	if e != nil {
		return nil, e
	}
	if old != hash || name != oldName {
		return nil, fmt.Errorf("%w: tool intent changed", ErrReplayConflict)
	}
	if e = tx.Commit(ctx); e != nil {
		return nil, e
	}
	return b, nil
}
func (s *Store) ToolResult(ctx context.Context, l *Lease, r *Run, id string, b []byte) error {
	tx, e := s.DB.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	if e = checkLease(ctx, tx, l); e != nil {
		return e
	}
	_, e = tx.Exec(ctx, `UPDATE runtime_tool_calls SET result=$3 WHERE run_id=$1 AND call_id=$2 AND result IS NULL`, r.ID, id, b)
	if e != nil {
		return e
	}
	return tx.Commit(ctx)
}

// InvalidateRun abandons a replay whose source was revoked or changed. Unrelated
// inputs get a fresh run; they must not inherit the abandoned model/tool tape.
func (s *Store) InvalidateRun(ctx context.Context, l *Lease, r *Run, invalid []string) error {
	tx, e := s.DB.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	if e = checkLease(ctx, tx, l); e != nil {
		return e
	}
	if _, e = tx.Exec(ctx, `UPDATE runtime_runs SET state='failed',safe_error='source_unavailable',finished_at=now() WHERE id=$1`, r.ID); e != nil {
		return e
	}
	if _, e = tx.Exec(ctx, `UPDATE runtime_inputs SET state=CASE WHEN event_id::text=ANY($3::text[]) THEN 'skipped' ELSE 'pending' END,run_id=NULL WHERE agent_id=$1 AND run_id=$2`, l.AgentID, r.ID, invalid); e != nil {
		return e
	}
	return tx.Commit(ctx)
}
