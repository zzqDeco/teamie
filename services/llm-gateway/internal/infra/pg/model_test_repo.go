package pg

import (
	"context"
	"fmt"

	"teamie/pkg/postgres"
	"teamie/services/llm-gateway/internal/domain"
)

var _ domain.ModelTestStore = (*PGStore)(nil)

const loadModelTestsSQL = `
SELECT
	model_id,
	ok,
	tested_at,
	latency_ms,
	error_code,
	model_updated_at,
	provider_updated_at
FROM gateway_model_tests`

const upsertModelTestSQL = `
INSERT INTO gateway_model_tests (
	model_id,
	ok,
	tested_at,
	latency_ms,
	error_code,
	model_updated_at,
	provider_updated_at
) VALUES ($1, $2, $3, $4, $5, $6, $7)
ON CONFLICT (model_id) DO UPDATE SET
	ok = EXCLUDED.ok,
	tested_at = EXCLUDED.tested_at,
	latency_ms = EXCLUDED.latency_ms,
	error_code = EXCLUDED.error_code,
	model_updated_at = EXCLUDED.model_updated_at,
	provider_updated_at = EXCLUDED.provider_updated_at
WHERE EXCLUDED.tested_at >= gateway_model_tests.tested_at`

// LoadModelTests loads all persisted per-model administrator test diagnostics.
// It is never called by Load, LoadForPing, UpdateCatalog, UpdateRouting, or UpdateWebSearch.
func (s *PGStore) LoadModelTests(ctx context.Context) (map[string]domain.ModelTestRecord, error) {
	rows, err := s.DB.Conn(ctx).Query(ctx, loadModelTestsSQL)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string]domain.ModelTestRecord)
	for rows.Next() {
		var (
			rec       domain.ModelTestRecord
			latencyMS *int64
			errorCode *string
		)
		if err := rows.Scan(
			&rec.ModelID,
			&rec.OK,
			&rec.TestedAt,
			&latencyMS,
			&errorCode,
			&rec.ModelUpdatedAt,
			&rec.ProviderUpdatedAt,
		); err != nil {
			return nil, err
		}
		rec.LatencyMS = latencyMS
		if errorCode != nil {
			rec.ErrorCode = *errorCode
		}
		norm := rec.Normalized()
		out[norm.ModelID] = norm
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// RecordModelTest persists or updates the latest test diagnostic for a model profile.
// Under concurrent tests on the same model, only the newest tested_at wins.
func (s *PGStore) RecordModelTest(ctx context.Context, rec domain.ModelTestRecord) error {
	norm := rec.Normalized()
	if !domain.ValidModelID(norm.ModelID) {
		return domain.Err(400, "invalid_request", "Invalid model ID")
	}
	var errorCode *string
	if !norm.OK {
		code := norm.ErrorCode
		errorCode = &code
	}
	_, err := s.DB.Conn(ctx).Exec(
		ctx,
		upsertModelTestSQL,
		norm.ModelID,
		norm.OK,
		norm.TestedAt,
		norm.LatencyMS,
		errorCode,
		norm.ModelUpdatedAt,
		norm.ProviderUpdatedAt,
	)
	if err != nil {
		if postgres.IsForeignKeyViolation(err) {
			return fmt.Errorf("%w: %v", domain.ErrModelTestTargetMissing, err)
		}
		return err
	}
	return nil
}
