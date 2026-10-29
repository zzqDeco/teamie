package pg

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"

	"teamie/pkg/postgres"
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
	err := r.db.Conn(ctx).QueryRow(ctx, `SELECT COALESCE(lease_owner=$2 AND fence=$3 AND lease_until>now(),false) FROM runtime_agents WHERE agent_id=$1`, l.AgentID, l.Owner, l.Fence).Scan(&ok)
	if postgres.IsNoRows(err) {
		return domain.ErrLeaseLost
	}
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
	err := r.db.Conn(ctx).QueryRow(ctx, `SELECT COALESCE(lease_owner=$2 AND fence=$3 AND lease_until>now(),false) FROM runtime_agents WHERE agent_id=$1 FOR UPDATE`, l.AgentID, l.Owner, l.Fence).Scan(&ok)
	if postgres.IsNoRows(err) {
		return domain.ErrLeaseLost
	}
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

// ListReadReceiptInputs reads only source positions, using the existing
// (agent_id,event_id) primary key. No write lock is held while recovering history.
func (r *Repository) ListReadReceiptInputs(ctx context.Context, l *domain.Lease, after, through string, limit int) (domain.ReadReceiptInputPage, error) {
	out := domain.ReadReceiptInputPage{NextEventID: after}
	if l == nil || !l.Valid() {
		return out, domain.ErrLeaseLost
	}
	validCursor := func(value string) bool {
		if value == "" || len(value) > 30 {
			return false
		}
		for _, digit := range value {
			if digit < '0' || digit > '9' {
				return false
			}
		}
		return true
	}
	if !validCursor(after) || !validCursor(through) || limit < 1 || limit > 512 {
		return out, errors.New("invalid read receipt recovery page")
	}
	if err := r.CheckLease(ctx, l); err != nil {
		return out, err
	}
	rows, err := r.db.Conn(ctx).Query(ctx, `SELECT event_id::text,COALESCE(payload->>'conversationId',''),COALESCE(payload->>'seq','')
 FROM runtime_inputs
 WHERE agent_id=$1 AND event_id>$2::numeric AND event_id<=$3::numeric
   AND COALESCE(payload->>'kind','')=''
 ORDER BY runtime_inputs.event_id LIMIT $4`, l.AgentID, after, through, limit+1)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for scanned := 0; rows.Next(); scanned++ {
		if scanned == limit {
			out.HasMore = true
			break
		}
		var eventID, conversationID, rawSeq string
		if err := rows.Scan(&eventID, &conversationID, &rawSeq); err != nil {
			return out, err
		}
		// Progress follows scanned rows even when a legacy payload is invalid.
		out.NextEventID = eventID
		seq, parseErr := strconv.ParseInt(rawSeq, 10, 64)
		if parseErr == nil && seq > 0 && domain.ValidUUID(conversationID) {
			out.Items = append(out.Items, domain.ReceivedReadPosition{EventID: eventID, ConversationID: conversationID, Seq: seq})
		}
	}
	if err := rows.Err(); err != nil {
		return out, err
	}
	rows.Close()
	if err := r.CheckLease(ctx, l); err != nil {
		return domain.ReadReceiptInputPage{}, err
	}
	return out, nil
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
	if postgres.IsNoRows(err) {
		return nil, nil, false, nil
	}
	return b, metadata, err == nil, err
}

// SetCheckpoint upserts a checkpoint row under the active worker lease fence.
func (r *Repository) SetCheckpoint(ctx context.Context, l *domain.Lease, id string, payload, metadata []byte) error {
	return r.WithinTx(ctx, func(txCtx context.Context) error {
		if e := r.CheckLeaseForUpdate(txCtx, l); e != nil {
			return e
		}
		_, e := r.db.Conn(txCtx).Exec(txCtx, `INSERT INTO runtime_checkpoints(checkpoint_id,payload,metadata) VALUES($1,$2,$3) ON CONFLICT(checkpoint_id) DO UPDATE SET payload=EXCLUDED.payload,metadata=EXCLUDED.metadata,updated_at=now()`, id, payload, metadata)
		return e
	})
}

// DeleteCheckpoint deletes a checkpoint row under the active worker lease fence.
func (r *Repository) DeleteCheckpoint(ctx context.Context, l *domain.Lease, id string) error {
	return r.WithinTx(ctx, func(txCtx context.Context) error {
		if e := r.CheckLeaseForUpdate(txCtx, l); e != nil {
			return e
		}
		_, e := r.db.Conn(txCtx).Exec(txCtx, `DELETE FROM runtime_checkpoints WHERE checkpoint_id=$1`, id)
		return e
	})
}

// SessionEventWrite holds a serialized session event ready for persistence.
type SessionEventWrite = domain.SessionEventWrite

// AppendSessionEvents persists a batch of session events under the active worker lease fence.
// If a conflicting payload hash already exists for the same (sessionID, eventID), duplicate is returned as true.
func (r *Repository) AppendSessionEvents(ctx context.Context, l *domain.Lease, sessionID, runID, attemptID string, items []SessionEventWrite) (bool, error) {
	var conflict bool
	conflictSentinel := errors.New("session_event_conflict")
	err := r.WithinTx(ctx, func(txCtx context.Context) error {
		if e := r.CheckLeaseForUpdate(txCtx, l); e != nil {
			return e
		}
		conn := r.db.Conn(txCtx)
		for _, item := range items {
			tag, err := conn.Exec(txCtx, `INSERT INTO runtime_session_events(session_id,event_id,kind,run_id,attempt_id,payload,payload_hash) VALUES($1,$2,$3,$4,$5,$6,$7) ON CONFLICT DO NOTHING`, sessionID, item.EventID, item.Kind, runID, attemptID, item.Payload, item.Hash)
			if err != nil {
				return err
			}
			if tag.RowsAffected() == 0 {
				var old string
				if err = conn.QueryRow(txCtx, `SELECT payload_hash FROM runtime_session_events WHERE session_id=$1 AND event_id=$2`, sessionID, item.EventID).Scan(&old); err != nil {
					return err
				}
				if old != item.Hash {
					conflict = true
					return conflictSentinel
				}
			}
		}
		return nil
	})
	if errors.Is(err, conflictSentinel) {
		return true, nil
	}
	return conflict, err
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
		if postgres.IsNoRows(err) {
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
