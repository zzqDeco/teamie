package domain_test

import (
	"testing"
	"time"

	"teamie/services/llm-gateway/internal/domain"
)

func TestSanitizeModelTestErrorCodeAllowlist(t *testing.T) {
	allowed := map[string]string{
		"provider_auth_failed":      "provider_auth_failed",
		"provider_invalid_response": "provider_invalid_response",
		"invalid_upstream_response": "provider_invalid_response",
		"upstream_rate_limited":     "upstream_rate_limited",
		"upstream_timeout":          "upstream_timeout",
		"upstream_unavailable":      "upstream_unavailable",
		"upstream_http_error":       "upstream_http_error",
		"upstream_error":            "upstream_error",
	}
	for in, want := range allowed {
		if got := domain.SanitizeModelTestErrorCode(in); got != want {
			t.Fatalf("SanitizeModelTestErrorCode(%q) = %q, want %q", in, got, want)
		}
		if got := domain.SanitizeModelTestErrorCode("  " + in + "\n"); got != want {
			t.Fatalf("SanitizeModelTestErrorCode(padded %q) = %q, want %q", in, got, want)
		}
	}

	for _, other := range []string{
		"",
		"provider_failed",
		"context_overflow",
		"request_too_large",
		"upstream_in_stream_error",
		"upstream_interrupted",
		"canceled",
		"run_cancelled",
		"model_disabled",
		"model_not_configured",
		"model_unconfigured",
		"https://secret.example/v1/chat/completions Bearer sk-secret",
	} {
		if got := domain.SanitizeModelTestErrorCode(other); got != "provider_failed" {
			t.Fatalf("SanitizeModelTestErrorCode(%q) = %q, want provider_failed", other, got)
		}
	}
}

func TestModelTestRecordNormalizedAndFreshness(t *testing.T) {
	t0 := time.Date(2026, 11, 7, 12, 0, 0, 123456000, time.UTC)
	neg := int64(-9)
	normOK := (domain.ModelTestRecord{
		ModelID:           " m1 ",
		OK:                true,
		TestedAt:          t0,
		LatencyMS:         &neg,
		ErrorCode:         "upstream_error",
		ModelUpdatedAt:    t0,
		ProviderUpdatedAt: t0,
	}).Normalized()
	if normOK.ModelID != "m1" || !normOK.OK || normOK.ErrorCode != "" || normOK.LatencyMS == nil || *normOK.LatencyMS != 0 {
		t.Fatalf("unexpected Normalized OK record: %+v", normOK)
	}

	normFail := (domain.ModelTestRecord{
		ModelID:           "m1",
		OK:                false,
		TestedAt:          t0,
		ErrorCode:         "raw leak sk-secret",
		ModelUpdatedAt:    t0,
		ProviderUpdatedAt: t0,
	}).Normalized()
	if normFail.OK || normFail.ErrorCode != "provider_failed" || normFail.LatencyMS != nil {
		t.Fatalf("unexpected Normalized failure record: %+v", normFail)
	}

	cfg := domain.Config{
		Providers: []domain.ProviderProfile{{ID: "p1", UpdatedAt: t0}},
		Models:    []domain.ModelProfile{{ID: "m1", ProviderID: "p1", UpdatedAt: t0}},
	}
	records := map[string]domain.ModelTestRecord{
		"m1": {
			ModelID:           "m1",
			OK:                true,
			TestedAt:          t0.Add(time.Second),
			ModelUpdatedAt:    t0,
			ProviderUpdatedAt: t0,
		},
	}
	if _, ok := cfg.FreshModelTest(cfg.Models[0], records); !ok {
		t.Fatal("expected exact timestamp match to be fresh")
	}

	// Model updatedAt mismatch omits lastTest.
	staleModel := cfg.Models[0]
	staleModel.UpdatedAt = t0.Add(time.Microsecond)
	if _, ok := cfg.FreshModelTest(staleModel, records); ok {
		t.Fatal("expected model updatedAt change to omit stale lastTest")
	}

	// Provider updatedAt mismatch omits lastTest.
	cfgProvChanged := domain.Config{
		Providers: []domain.ProviderProfile{{ID: "p1", UpdatedAt: t0.Add(time.Microsecond)}},
		Models:    []domain.ModelProfile{{ID: "m1", ProviderID: "p1", UpdatedAt: t0}},
	}
	if _, ok := cfgProvChanged.FreshModelTest(cfgProvChanged.Models[0], records); ok {
		t.Fatal("expected provider updatedAt change to omit stale lastTest")
	}

	// Zero timestamps fail closed.
	cfgZero := domain.Config{
		Providers: []domain.ProviderProfile{{ID: "p1"}},
		Models:    []domain.ModelProfile{{ID: "m1", ProviderID: "p1"}},
	}
	if _, ok := cfgZero.FreshModelTest(cfgZero.Models[0], map[string]domain.ModelTestRecord{"m1": {ModelID: "m1"}}); ok {
		t.Fatal("expected zero timestamps to fail closed")
	}
}
