package app

import (
	"context"
	"errors"
	"time"

	"teamie/services/llm-gateway/internal/domain"
)

type singleAttemptTestKey struct{}

// WithSingleAttemptTest marks ctx as an administrator connection test so upstream
// HTTP 429 failures report immediately after a single provider call.
func WithSingleAttemptTest(ctx context.Context) context.Context {
	return context.WithValue(ctx, singleAttemptTestKey{}, true)
}

// SingleAttemptTest reports whether ctx executes a single-attempt administrator test.
func SingleAttemptTest(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	v, _ := ctx.Value(singleAttemptTestKey{}).(bool)
	return v
}

func advanceUpdatedAt(prev time.Time) time.Time {
	now := time.Now().UTC().Truncate(time.Microsecond)
	prev = prev.UTC().Truncate(time.Microsecond)
	if !prev.IsZero() && !now.After(prev) {
		return prev.Add(time.Microsecond)
	}
	return now
}

// LoadModelTests reads persisted administrator model test diagnostics.
// It is never called by configuration commands, routing, Ping, or inference fallback.
func (s *Service) LoadModelTests(ctx context.Context) (map[string]domain.ModelTestRecord, error) {
	s.ensureInit()
	ts := s.modelTestStore()
	if ts == nil {
		return nil, nil
	}
	return ts.LoadModelTests(ctx)
}

// RecordModelTestOutcome persists a model test diagnostic iff an upstream provider
// call was actually attempted on a catalog model. Precondition rejections without
// a provider call never write or overwrite gateway_model_tests.
func (s *Service) RecordModelTestOutcome(outcome CallOutcome) error {
	s.ensureInit()
	if !outcome.ProviderAttempted || !outcome.HasCatalogTarget {
		return nil
	}
	ts := s.modelTestStore()
	if ts == nil {
		return nil
	}
	testedAt := outcome.TestedAt
	if testedAt.IsZero() {
		testedAt = time.Now().UTC()
	}
	rec := domain.ModelTestRecord{
		ModelID:           outcome.ExercisedModelID,
		OK:                outcome.ProviderOK,
		TestedAt:          testedAt.UTC().Truncate(time.Microsecond),
		LatencyMS:         outcome.LatencyMS,
		ErrorCode:         outcome.ProviderErrorCode,
		ModelUpdatedAt:    outcome.ModelUpdatedAt,
		ProviderUpdatedAt: outcome.ProviderUpdatedAt,
	}.Normalized()

	persistCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := ts.RecordModelTest(persistCtx, rec); err != nil {
		if errors.Is(err, domain.ErrModelTestTargetMissing) {
			return nil
		}
		return err
	}
	return nil
}
