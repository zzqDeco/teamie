package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"teamie/backend/pkg/observability"
	"time"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"teamie/agent-protocol/modeltransport"
	"teamie/agent-runtime/internal/engine/app"
	"teamie/agent-runtime/internal/engine/domain"
	"teamie/agent-runtime/internal/engine/infra/pg"
	"teamie/backend/pkg/imtext"
)

var ErrLeaseLost = domain.ErrLeaseLost
var ErrReplayConflict = domain.ErrReplayConflict

type Store struct{ DB *pgxpool.Pool }
type Lease = domain.Lease
type Input struct {
	EventID        string           `json:"eventId"`
	WorkspaceID    string           `json:"workspaceId"`
	ConversationID string           `json:"conversationId"`
	MessageID      string           `json:"messageId"`
	SenderID       string           `json:"senderId"`
	SenderName     string           `json:"senderName"`
	SenderKind     string           `json:"senderKind"`
	Body           string           `json:"body"`
	Attachments    []Attachment     `json:"attachments,omitempty"`
	Mentions       []imtext.Mention `json:"mentions,omitempty"`
	ReplyToID      string           `json:"replyToId,omitempty"`
	Seq            int64            `json:"seq"`
	CreatedAt      string           `json:"createdAt"`
	Kind           string           `json:"kind,omitempty"`
	TaskID         string           `json:"taskId,omitempty"`
	ScheduleID     string           `json:"scheduleId,omitempty"`
	NotificationID string           `json:"notificationId,omitempty"`
	Status         string           `json:"status,omitempty"`
	ParentRunID    string           `json:"parentRunId,omitempty"`
	SourceRefs     []InputSource    `json:"sourceRefs,omitempty"`
	SourceOverflow bool             `json:"sourceOverflow,omitempty"`
}
type Run struct {
	diagnostic                  *observability.Operation
	toolProfile                 string
	ContextProfile              string
	ContextBudget               json.RawMessage
	ID, Attempt, AgentID        string
	Inputs                      []Input
	Step                        atomic.Int64
	Revision, Generation        int64
	ContextHead, AcceptanceHead int64
	Front                       *frontSlot
	usageState                  usageAnchorState
}

func (r *Run) getUsageAnchor() *contextUsageAnchor {
	if r == nil {
		return nil
	}
	return r.usageState.get()
}

func (r *Run) getUsageAnchorForConfig(configID string) *contextUsageAnchor {
	if r == nil {
		return nil
	}
	return r.usageState.getForConfig(configID)
}

func (r *Run) setUsageAnchor(anchor *contextUsageAnchor, onlyIfValid bool) {
	if r == nil {
		return
	}
	r.usageState.set(anchor, onlyIfValid)
}

func (r *Run) clearUsageAnchor() {
	if r == nil {
		return
	}
	r.usageState.clear()
}

func (r *Run) setAdmissionConfig(configID, counterMode string) {
	if r == nil {
		return
	}
	r.usageState.setAdmissionConfig(configID, counterMode)
}

func (r *Run) clearAdmissionConfig() {
	if r == nil {
		return
	}
	r.usageState.clearAdmissionConfig()
}

func (r *Run) consumeAdmissionConfig(preserve bool) (string, string, bool) {
	if r == nil {
		return "", "", false
	}
	return r.usageState.consumeAdmissionConfig(preserve)
}

func (r *Run) invalidateUsageAnchor(messages []*schema.AgenticMessage) {
	if r == nil {
		return
	}
	r.usageState.invalidate(messages)
}

func (r *Run) computeRequestUsageAnchor(step int, request []byte, result *schema.AgenticMessage, configID string) *contextUsageAnchor {
	if r == nil || contextProfile(r) != contextProfileV1 || result == nil || result.ResponseMeta == nil || result.ResponseMeta.TokenUsage == nil || result.ResponseMeta.TokenUsage.PromptTokens <= 0 {
		return nil
	}
	budget, err := ParseContextBudgetSnapshot(r.ContextBudget)
	if err != nil || budget.Version < 7 {
		return nil
	}
	configID = strings.TrimSpace(configID)
	if configID == "" {
		if saved, ok := extractUsageAnchor(result); ok {
			configID = saved.ConfigID
		}
	}
	if configID == "" {
		return nil
	}
	var envelope modeltransport.Request
	var messages []*schema.AgenticMessage
	var opts model.Options
	if json.Unmarshal(request, &envelope) != nil || (&schema.HumanReadableSerializer{}).Unmarshal(envelope.Messages, &messages) != nil || json.Unmarshal(envelope.Options, &opts) != nil {
		return nil
	}
	if isSummaryModelOptions(&opts) || len(messages) == 0 {
		return nil
	}
	visibleBytes, prefixHash, err := visibleContextBytes(messages, opts.Tools, opts.DeferredTools, opts.Stop, opts.AgenticToolChoice)
	if err != nil {
		return nil
	}
	localTokens := estimateVisibleTokensV7(visibleBytes)
	promptTokens := result.ResponseMeta.TokenUsage.PromptTokens
	if !validCalibratedTokenBounds(promptTokens, localTokens) {
		return nil
	}
	return &contextUsageAnchor{
		Step:         step,
		ConfigID:     configID,
		MessageCount: len(messages),
		PrefixHash:   prefixHash,
		LocalTokens:  localTokens,
		PromptTokens: promptTokens,
	}
}

func (r *Run) recordModelUsageAnchor(step int, request []byte, result *schema.AgenticMessage, configID string, admission ...requestAdmissionState) {
	if r == nil || result == nil {
		return
	}
	if configID == "" {
		if savedID, ok := extractRecordedModelConfigID(result); ok {
			configID = savedID
		}
	}
	var adm requestAdmissionState
	if len(admission) > 0 {
		adm = admission[0]
	} else if savedAdm, ok := extractRecordedAdmissionMetadata(result); ok {
		adm = savedAdm
	} else {
		adm = requestAdmissionState{ConfigID: strings.TrimSpace(configID), Known: strings.TrimSpace(configID) != ""}
	}
	anchor := r.computeRequestUsageAnchor(step, request, result, configID)
	if anchor == nil {
		if contextProfile(r) == contextProfileV1 {
			if budget, err := ParseContextBudgetSnapshot(r.ContextBudget); err == nil && budget.Version >= 7 {
				attachRecordedStepMetadata(result, step, configID, adm)
			}
		}
		return
	}
	anchor.AdmissionConfigID = strings.TrimSpace(adm.ConfigID)
	anchor.CounterMode = strings.TrimSpace(adm.CounterMode)
	attachUsageAnchor(result, anchor)
	r.setUsageAnchor(anchor, false)
}

func executionID(run *Run) string { return run.ID + ":" + run.Attempt }

type runKey struct{}

func WithRun(ctx context.Context, run *Run) context.Context {
	return context.WithValue(observability.WithFields(ctx, runFields(run)...), runKey{}, run)
}
func CurrentRun(ctx context.Context) (*Run, error) {
	r, ok := ctx.Value(runKey{}).(*Run)
	if !ok {
		return nil, errors.New("missing run context")
	}
	return r, nil
}
func digest(b []byte) string { return domain.Digest(b) }

func toDomainInput(in Input) domain.Input {
	var mentions []domain.MentionSpan
	if len(in.Mentions) > 0 {
		mentions = make([]domain.MentionSpan, len(in.Mentions))
		for i, m := range in.Mentions {
			mentions[i] = domain.MentionSpan(m)
		}
	}
	return domain.Input{
		EventID:        in.EventID,
		WorkspaceID:    in.WorkspaceID,
		ConversationID: in.ConversationID,
		MessageID:      in.MessageID,
		SenderID:       in.SenderID,
		SenderName:     in.SenderName,
		SenderKind:     in.SenderKind,
		Body:           in.Body,
		Attachments:    in.Attachments,
		Mentions:       mentions,
		ReplyToID:      in.ReplyToID,
		Seq:            in.Seq,
		CreatedAt:      in.CreatedAt,
		Kind:           in.Kind,
		TaskID:         in.TaskID,
		ScheduleID:     in.ScheduleID,
		NotificationID: in.NotificationID,
		Status:         in.Status,
		ParentRunID:    in.ParentRunID,
		SourceRefs:     in.SourceRefs,
		SourceOverflow: in.SourceOverflow,
	}
}

func toDomainInputs(inputs []Input) []domain.Input {
	out := make([]domain.Input, len(inputs))
	for i, in := range inputs {
		out[i] = toDomainInput(in)
	}
	return out
}

func (s *Store) Repo() *pg.Repository {
	if s == nil {
		return nil
	}
	return pg.NewFromPool(s.DB)
}

func (s *Store) App(im domain.IMGateway) *app.Service {
	repo := s.Repo()
	return &app.Service{
		Tx:           repo,
		Sessions:     repo,
		Schedules:    repo,
		Archive:      repo,
		ToolReceipts: repo,
		IM:           im,
	}
}

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
	db, err := pg.OpenPool(ctx, url, runtimeWriterSchemaSetting, runtimeSchemaVersion)
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
		err = validateRuntimeSchema(ctx, tx)
	}
	if err == nil {
		_, err = tx.Exec(ctx, schemaSQL+schemaV2SQL+schemaV3SQL+contextArchiveSchemaSQL+contextRecoverySchemaSQL+schemaV4SQL+schemaV5SQL+schemaV6SQL+schemaV7SQL+schemaV8SQL+schemaV9SQL+schemaV10SQL+schemaV11SQL+schemaV12SQL)
	}
	if err == nil {
		err = tx.Commit(ctx)
	}
	if err != nil {
		_ = tx.Rollback(ctx)
		db.Close()
		return nil, err
	}
	return &Store{DB: db}, nil
}
func (s *Store) Acquire(ctx context.Context, agentID, owner string) (*Lease, error) {
	return s.Repo().AcquireLease(ctx, agentID, owner)
}
func (s *Store) Renew(ctx context.Context, l *Lease) error {
	return s.Repo().RenewLease(ctx, l)
}
func (s *Store) Release(ctx context.Context, l *Lease) {
	s.Repo().ReleaseLease(ctx, l)
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
	return s.Repo().CheckLease(ctx, l)
}
func (s *Store) Cursor(ctx context.Context, l *Lease) (string, error) {
	return s.Repo().GetCursor(ctx, l)
}
func (s *Store) Ingest(ctx context.Context, l *Lease, cursor string, inputs []Input) error {
	return s.Repo().IngestInputs(ctx, l, cursor, toDomainInputs(inputs))
}
func (s *Store) Pending(ctx context.Context, l *Lease) ([]Input, error) {
	rows, err := s.DB.Query(ctx, `SELECT i.payload FROM runtime_inputs i LEFT JOIN runtime_runs r ON i.run_id=r.id WHERE i.agent_id=$1 AND i.state IN ('pending','processing') AND (r.id IS NULL OR (r.next_attempt<=now() AND r.state<>'waiting_input')) ORDER BY i.delivery_seq LIMIT 32`, l.AgentID)
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
	return s.Repo().SkipInput(ctx, l, id)
}
func (s *Store) StartRun(ctx context.Context, l *Lease, inputs []Input) (*Run, error) {
	return s.StartRunWithContextPolicy(ctx, l, inputs, contextProfileLegacy, json.RawMessage(`{}`))
}

// Admission chooses a context policy only for a new logical Run. Recovery
// always restores the persisted policy and budget, regardless of live flags.
func (s *Store) StartRunWithContextPolicy(ctx context.Context, l *Lease, inputs []Input, profile string, budget json.RawMessage) (*Run, error) {
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
		err = tx.QueryRow(ctx, `UPDATE runtime_runs SET state='running',attempt_id=$2,attempts=attempts+1 WHERE id=$1 AND state IN ('running','interrupted','waiting_configuration','retry','waiting_input') RETURNING input_json,context_head,acceptance_head,context_profile,context_budget`, r.ID, r.Attempt).Scan(&b, &r.ContextHead, &r.AcceptanceHead, &r.ContextProfile, &r.ContextBudget)
		if err != nil {
			return nil, err
		}
		if err = json.Unmarshal(b, &r.Inputs); err != nil {
			return nil, err
		}
	} else if errors.Is(err, pgx.ErrNoRows) {
		r.ContextProfile, r.ContextBudget, err = canonicalContextPolicy(profile, budget)
		if err != nil {
			return nil, err
		}
		b, e := json.Marshal(inputs)
		if e != nil {
			return nil, e
		}
		ids := make([]string, len(inputs))
		for i, in := range inputs {
			ids[i] = in.EventID
		}
		key := strings.Join(ids, ",")
		err = tx.QueryRow(ctx, `SELECT (SELECT COALESCE(max(seq),0) FROM runtime_session_events WHERE session_id=$1),(SELECT COALESCE(max(seq),0) FROM runtime_session_acceptances WHERE agent_id=$2)`, "agent:"+l.AgentID, l.AgentID).Scan(&r.ContextHead, &r.AcceptanceHead)
		if err != nil {
			return nil, err
		}
		_, err = tx.Exec(ctx, `INSERT INTO runtime_runs(id,agent_id,input_key,input_json,attempt_id,context_head,acceptance_head,context_profile,context_budget) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)`, r.ID, l.AgentID, key, b, r.Attempt, r.ContextHead, r.AcceptanceHead, r.ContextProfile, r.ContextBudget)
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
	if state == "completed" {
		if e = acceptSessionAttempt(ctx, tx, l.AgentID, r.ID); e != nil {
			return e
		}
	}
	if retry {
		_, e = tx.Exec(ctx, `UPDATE runtime_runs SET state=$2,safe_error=$3,updated_at=now(),next_attempt=now()+interval '15 seconds' WHERE id=$1`, r.ID, state, safeError)
	} else {
		_, e = tx.Exec(ctx, `UPDATE runtime_runs SET state=$2,safe_error=$3,updated_at=now(),committed_attempt=CASE WHEN $2='completed' THEN attempt_id ELSE NULL END,finished_at=now() WHERE id=$1`, r.ID, state, safeError)
		if e == nil {
			_, e = tx.Exec(ctx, `UPDATE runtime_inputs SET state=$2 WHERE agent_id=$1 AND run_id=$3`, l.AgentID, map[bool]string{true: "done", false: "failed"}[state == "completed"], r.ID)
		}
	}
	if e != nil {
		return e
	}
	_, e = tx.Exec(ctx, `INSERT INTO runtime_public_events(agent_id,type,run_id,status,error_code) VALUES($1,'run.status',$2,$3,NULLIF($4,''))`, l.AgentID, r.ID, state, safeError)
	if e != nil {
		return e
	}
	return tx.Commit(ctx)
}

type SessionStore[M adk.MessageType] struct {
	Store    *Store
	Lease    *Lease
	mu       sync.Mutex
	run      *Run
	targets  []string
	Revision int64
}

func (s *SessionStore[M]) AppendEvents(ctx context.Context, sessionID string, events []*adk.SessionEvent[M]) error {
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
func (s *SessionStore[M]) LoadEvents(ctx context.Context, sessionID string, req *adk.LoadSessionEventsRequest) (*adk.LoadSessionEventsResult[M], error) {
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
	visible := `(e.attempt_id=$2 OR (e.run_id=RUN AND e.attempt_id=ANY(r.context_attempts)) OR EXISTS(SELECT 1 FROM runtime_session_acceptances acceptance WHERE acceptance.attempt_id=e.attempt_id AND acceptance.seq<=ACCEPT))`
	afterVisible := strings.NewReplacer("RUN", "$5", "ACCEPT", "$6").Replace(visible)
	pageVisible := strings.NewReplacer("RUN", "$7", "ACCEPT", "$8").Replace(visible)
	if req.After != "" {
		e = s.Store.DB.QueryRow(ctx, `SELECT e.seq FROM runtime_session_events e JOIN runtime_runs r ON r.id=e.run_id WHERE e.session_id=$1 AND `+afterVisible+` AND e.event_id=$3 AND (e.run_id=$5 OR e.seq<=$4)`, sessionID, r.Attempt, req.After, r.ContextHead, r.ID, r.AcceptanceHead).Scan(&after)
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
	rows, e := s.Store.DB.Query(ctx, `SELECT e.payload FROM runtime_session_events e JOIN runtime_runs r ON r.id=e.run_id WHERE e.session_id=$1 AND `+pageVisible+` AND (e.run_id=$7 OR e.seq<=$6) AND e.seq `+comparison+` $3 AND (cardinality($4::text[])=0 OR e.kind=ANY($4)) ORDER BY e.seq `+order+` LIMIT $5`, sessionID, r.Attempt, after, kinds, limit, r.ContextHead, r.ID, r.AcceptanceHead)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := &adk.LoadSessionEventsResult[M]{Events: []*adk.SessionEvent[M]{}}
	for rows.Next() {
		var b []byte
		if e = rows.Scan(&b); e != nil {
			return nil, e
		}
		event := new(adk.SessionEvent[M])
		if e = decodeSessionPayload(b, event); e != nil {
			return nil, e
		}
		out.Events = append(out.Events, event)
		out.Next = event.EventID
	}
	return out, rows.Err()
}
func (s *SessionStore[M]) Get(ctx context.Context, id string) ([]byte, bool, error) {
	if !strings.Contains(id, s.Lease.AgentID) {
		return nil, false, errors.New("checkpoint scope mismatch")
	}
	var b, metadata []byte
	e := s.Store.DB.QueryRow(ctx, `SELECT payload,metadata FROM runtime_checkpoints WHERE checkpoint_id=$1`, id).Scan(&b, &metadata)
	if errors.Is(e, pgx.ErrNoRows) {
		return nil, false, nil
	}
	if e == nil && strings.HasPrefix(id, "turnloop:") {
		var m checkpointMetadata
		if json.Unmarshal(metadata, &m) != nil || m.Version != nativeRuntimeVersion {
			return nil, false, nil
		}
	}
	return b, e == nil, e
}
func (s *SessionStore[M]) Set(ctx context.Context, id string, b []byte) error {
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
	_, e = tx.Exec(ctx, `INSERT INTO runtime_checkpoints(checkpoint_id,payload,metadata) VALUES($1,$2,$3) ON CONFLICT(checkpoint_id) DO UPDATE SET payload=EXCLUDED.payload,metadata=EXCLUDED.metadata,updated_at=now()`, id, b, s.metadata())
	if e != nil {
		return e
	}
	return tx.Commit(ctx)
}
func (s *SessionStore[M]) Delete(ctx context.Context, id string) error {
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

func (s *Store) RecordedModelStepAdmission(ctx context.Context, r *Run, step int) (string, string, bool, error) {
	if s == nil || s.DB == nil || r == nil || r.ID == "" || step < 1 {
		return "", "", false, nil
	}
	var raw, decisionRaw []byte
	err := s.DB.QueryRow(ctx, `SELECT (SELECT response FROM runtime_model_calls WHERE run_id=$1 AND step=$2), (SELECT decision_json FROM runtime_context_recoveries WHERE run_id=$1 AND model_step=$2)`, r.ID, step).Scan(&raw, &decisionRaw)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", "", false, nil
		}
		return "", "", false, err
	}
	if len(raw) == 0 && len(decisionRaw) == 0 {
		return "", "", false, nil
	}
	if len(raw) > 0 {
		msg, decodeErr := decodeAgenticMessage(raw)
		if decodeErr != nil {
			return "", "", false, decodeErr
		}
		if adm, ok := extractRecordedAdmissionMetadata(msg); ok {
			return adm.ConfigID, adm.CounterMode, true, nil
		}
	}
	if len(decisionRaw) > 0 {
		var decision contextRecoveryDecision
		if unmarshalErr := json.Unmarshal(decisionRaw, &decision); unmarshalErr != nil {
			return "", "", false, unmarshalErr
		}
		if decision.CounterMode != "" || decision.AdmissionConfigID != "" {
			return strings.TrimSpace(decision.AdmissionConfigID), strings.TrimSpace(decision.CounterMode), true, nil
		}
		if id := strings.TrimSpace(decision.ConfigID); id != "" {
			return id, "", true, nil
		}
	}
	return "", "", true, nil
}

func (s *Store) RecordedModelStepConfigID(ctx context.Context, r *Run, step int) (string, bool) {
	if s == nil || s.DB == nil || r == nil || r.ID == "" || step < 1 {
		return "", false
	}
	var raw, decisionRaw []byte
	err := s.DB.QueryRow(ctx, `SELECT (SELECT response FROM runtime_model_calls WHERE run_id=$1 AND step=$2), (SELECT decision_json FROM runtime_context_recoveries WHERE run_id=$1 AND model_step=$2)`, r.ID, step).Scan(&raw, &decisionRaw)
	if err != nil || (len(raw) == 0 && len(decisionRaw) == 0) {
		return "", false
	}
	if len(raw) > 0 {
		if msg, decodeErr := decodeAgenticMessage(raw); decodeErr == nil {
			if id, ok := extractRecordedModelConfigID(msg); ok {
				return id, true
			}
			if adm, ok := extractRecordedAdmissionMetadata(msg); ok && adm.ConfigID != "" {
				return adm.ConfigID, true
			}
		}
	}
	if len(decisionRaw) > 0 {
		var decision contextRecoveryDecision
		if json.Unmarshal(decisionRaw, &decision) == nil {
			if id := strings.TrimSpace(decision.ConfigID); id != "" {
				return id, true
			}
			if id := strings.TrimSpace(decision.AdmissionConfigID); id != "" {
				return id, true
			}
		}
	}
	return "", true
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
	var rejected bool
	if e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM runtime_context_recoveries WHERE run_id=$1 AND model_step=$2)`, r.ID, step).Scan(&rejected); e != nil {
		return e
	}
	if rejected {
		return ErrReplayConflict
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
	tag, e := tx.Exec(ctx, `INSERT INTO runtime_tool_calls(run_id,call_id,name,request_hash) VALUES($1,$2,$3,$4) ON CONFLICT DO NOTHING`, r.ID, id, name, hash)
	if e != nil {
		return nil, e
	}
	if tag.RowsAffected() == 1 {
		_, e = tx.Exec(ctx, `INSERT INTO runtime_public_events(agent_id,type,run_id,tool_name,status) VALUES($1,'tool.status',$2,$3,'running')`, l.AgentID, r.ID, strings.TrimPrefix(name, "native:"))
		if e != nil {
			return nil, e
		}
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
	tag, e := tx.Exec(ctx, `UPDATE runtime_tool_calls SET result=$3 WHERE run_id=$1 AND call_id=$2 AND result IS NULL`, r.ID, id, b)
	if e != nil {
		return e
	}
	if tag.RowsAffected() == 1 {
		_, e = tx.Exec(ctx, `INSERT INTO runtime_public_events(agent_id,type,run_id,tool_name,status) SELECT $1,'tool.status',run_id,regexp_replace(name,'^native:',''),'completed' FROM runtime_tool_calls WHERE run_id=$2 AND call_id=$3`, l.AgentID, r.ID, id)
		if e != nil {
			return e
		}
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
