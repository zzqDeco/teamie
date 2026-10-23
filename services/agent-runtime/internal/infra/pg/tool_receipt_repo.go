package pg

import (
	"context"

	"teamie/pkg/postgres"
	"teamie/services/agent-runtime/internal/domain"
)

// GetObservation reads a saved tool observation snapshot by (runID, callID, key),
// returning pgx.ErrNoRows if not found.
func (r *Repository) GetObservation(ctx context.Context, runID, callID, key string) ([]byte, error) {
	var out []byte
	err := r.db.Conn(ctx).QueryRow(ctx, `SELECT response FROM runtime_tool_observations WHERE run_id=$1 AND call_id=$2 AND observation_key=$3`, runID, callID, key).Scan(&out)
	return out, err
}

// ReadObservation reads a saved tool observation snapshot by (runID, callID, key).
func (r *Repository) ReadObservation(ctx context.Context, runID, callID, key string) ([]byte, bool, error) {
	out, err := r.GetObservation(ctx, runID, callID, key)
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
