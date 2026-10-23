package pg

import (
	"context"
	"encoding/json"
	"errors"

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
