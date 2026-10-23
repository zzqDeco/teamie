package eino

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"teamie/pkg/postgres"
)

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
	old, profile, code, mode, version, raw, err := s.DB.GetContextRecovery(ctx, run.ID, step)
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
	return s.DB.SaveContextRecovery(
		ctx,
		lease,
		run.ID,
		step,
		contextProfile(run),
		hash,
		streaming,
		code,
		decision,
		func(oldProfile, oldHash string, oldMode bool, oldCode string, oldVersion int, oldDecision []byte) error {
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
			return nil
		},
	)
}

func (m *GatewayModel) cachedRecovery(ctx context.Context, run *Run, step int, hash string, streaming bool) error {
	if contextProfile(run) != contextProfileV1 {
		return nil
	}
	failure, err := m.Store.CachedContextRecovery(ctx, run, step, hash, streaming)
	if postgres.IsNoRows(err) {
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
