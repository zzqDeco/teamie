package engine

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"
)

const contextRecoverySchemaSQL = `
CREATE TABLE IF NOT EXISTS runtime_context_recoveries (
 run_id uuid NOT NULL REFERENCES runtime_runs(id),
 model_step integer NOT NULL CHECK(model_step > 0),
 context_profile text NOT NULL CHECK(context_profile = 'teamie-context-v038'),
 request_hash text NOT NULL,
 streaming boolean NOT NULL,
 error_code text NOT NULL CHECK(error_code IN ('context_overflow','request_too_large')),
 decision_version integer NOT NULL CHECK(decision_version = 1),
 decision_json jsonb NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(run_id,model_step)
);`

type contextRecoveryDecision struct {
	Version           int                   `json:"version"`
	Action            string                `json:"action"`
	Budget            ContextBudgetSnapshot `json:"budget"`
	ConfigID          string                `json:"configId,omitempty"`
	AdmissionConfigID string                `json:"admissionConfigId,omitempty"`
	CounterMode       string                `json:"counterMode,omitempty"`
}

func recoveryDecision(run *Run, meta ...any) ([]byte, error) {
	if contextProfile(run) != contextProfileV1 {
		return nil, ErrReplayConflict
	}
	budget, err := ParseContextBudgetSnapshot(run.ContextBudget)
	if err != nil {
		return nil, err
	}
	cfgID, admID, mode := "", "", ""
	if budget.Version >= 7 {
		hasAdm := false
		for _, item := range meta {
			switch v := item.(type) {
			case string:
				cfgID = strings.TrimSpace(v)
			case requestAdmissionState:
				admID = strings.TrimSpace(v.ConfigID)
				mode = strings.TrimSpace(v.CounterMode)
				hasAdm = true
			}
		}
		if !hasAdm {
			admID = cfgID
		}
	}
	return json.Marshal(contextRecoveryDecision{
		Version:           1,
		Action:            "compact_prefix",
		Budget:            budget,
		ConfigID:          cfgID,
		AdmissionConfigID: admID,
		CounterMode:       mode,
	})
}

func isContextOverflow(code string) bool {
	return code == "context_overflow" || code == "request_too_large"
}

// Failure receipts are an immutable part of the model-step tape. Replaying a
// rejected request must reproduce the recovery branch without another call.
func (s *Store) CachedContextRecovery(ctx context.Context, run *Run, step int, hash string, streaming bool) (*ModelError, error) {
	var old, profile, code string
	var mode bool
	var version int
	var raw []byte
	err := s.DB.QueryRow(ctx, `SELECT request_hash,context_profile,streaming,error_code,decision_version,decision_json FROM runtime_context_recoveries WHERE run_id=$1 AND model_step=$2`, run.ID, step).Scan(&old, &profile, &mode, &code, &version, &raw)
	if err != nil {
		return nil, err
	}
	expected, err := recoveryDecision(run)
	if err != nil {
		return nil, err
	}
	var a, b contextRecoveryDecision
	if json.Unmarshal(raw, &a) != nil || json.Unmarshal(expected, &b) != nil {
		return nil, ErrReplayConflict
	}
	a.ConfigID, a.AdmissionConfigID, a.CounterMode = "", "", ""
	b.ConfigID, b.AdmissionConfigID, b.CounterMode = "", "", ""
	if old != hash || profile != contextProfile(run) || mode != streaming || version != 1 || !isContextOverflow(code) || a != b {
		return nil, ErrReplayConflict
	}
	return &ModelError{Code: code}, nil
}

func (s *Store) SaveContextRecovery(ctx context.Context, lease *Lease, run *Run, step int, hash string, streaming bool, code string, meta ...any) error {
	if step < 1 || !isContextOverflow(code) {
		return errors.New("invalid context recovery")
	}
	decision, err := recoveryDecision(run, meta...)
	if err != nil {
		return err
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	// Same Agent row lock as SaveModel serializes mutually exclusive decisions.
	if err = checkLease(ctx, tx, lease); err != nil {
		return err
	}
	var success bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM runtime_model_calls WHERE run_id=$1 AND step=$2)`, run.ID, step).Scan(&success); err != nil {
		return err
	}
	if success {
		return ErrReplayConflict
	}
	tag, err := tx.Exec(ctx, `INSERT INTO runtime_context_recoveries(run_id,model_step,context_profile,request_hash,streaming,error_code,decision_version,decision_json) VALUES($1,$2,$3,$4,$5,$6,1,$7) ON CONFLICT DO NOTHING`, run.ID, step, contextProfile(run), hash, streaming, code, decision)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		var oldProfile, oldHash, oldCode string
		var oldMode bool
		var oldVersion int
		var oldDecision []byte
		err = tx.QueryRow(ctx, `SELECT context_profile,request_hash,streaming,error_code,decision_version,decision_json FROM runtime_context_recoveries WHERE run_id=$1 AND model_step=$2`, run.ID, step).Scan(&oldProfile, &oldHash, &oldMode, &oldCode, &oldVersion, &oldDecision)
		if err != nil {
			return err
		}
		var a, b contextRecoveryDecision
		if json.Unmarshal(oldDecision, &a) != nil || json.Unmarshal(decision, &b) != nil {
			return ErrReplayConflict
		}
		if a.ConfigID != "" && b.ConfigID != "" && a.ConfigID != b.ConfigID {
			return ErrReplayConflict
		}
		a.ConfigID, a.AdmissionConfigID, a.CounterMode = "", "", ""
		b.ConfigID, b.AdmissionConfigID, b.CounterMode = "", "", ""
		if oldProfile != contextProfile(run) || oldHash != hash || oldMode != streaming || oldCode != code || oldVersion != 1 || a != b {
			return ErrReplayConflict
		}
	}
	return tx.Commit(ctx)
}

func (m *GatewayModel) cachedRecovery(ctx context.Context, run *Run, step int, hash string, streaming bool) error {
	if contextProfile(run) != contextProfileV1 {
		return nil
	}
	failure, err := m.Store.CachedContextRecovery(ctx, run, step, hash, streaming)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	return failure
}

func (m *GatewayModel) gatewayFailure(ctx context.Context, run *Run, step int, hash string, streaming bool, code string, meta ...any) error {
	code = safeGatewayCode(code)
	if contextProfile(run) == contextProfileV1 && isContextOverflow(code) {
		if err := m.Store.SaveContextRecovery(ctx, m.Lease, run, step, hash, streaming, code, meta...); err != nil {
			return err
		}
		return &ModelError{Code: code}
	}
	return &ModelError{Code: code, Retry: code != "invalid_request" && code != "request_too_large" && code != "context_overflow"}
}
