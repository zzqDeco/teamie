package domain

import (
	"errors"
	"strings"
	"time"
)

// ErrModelTestTargetMissing indicates that the target model row no longer exists.
var ErrModelTestTargetMissing = errors.New("model test target does not exist")

// ModelTestRecord stores the persisted outcome of an administrator connection test
// for a single model profile, along with the private configuration timestamps
// observed when the test started.
type ModelTestRecord struct {
	ModelID           string
	OK                bool
	TestedAt          time.Time
	LatencyMS         *int64
	ErrorCode         string
	ModelUpdatedAt    time.Time
	ProviderUpdatedAt time.Time
}

// SanitizeModelTestErrorCode maps any error code strictly to the safe public
// allowlist for persisted administrator model test diagnostics. Raw upstream
// text, status lines, URLs, headers, or credentials can never pass through.
func SanitizeModelTestErrorCode(code string) string {
	switch strings.TrimSpace(code) {
	case "provider_auth_failed":
		return "provider_auth_failed"
	case "provider_invalid_response", "invalid_upstream_response":
		return "provider_invalid_response"
	case "upstream_rate_limited":
		return "upstream_rate_limited"
	case "upstream_timeout":
		return "upstream_timeout"
	case "upstream_unavailable":
		return "upstream_unavailable"
	case "upstream_http_error":
		return "upstream_http_error"
	case "upstream_error":
		return "upstream_error"
	default:
		return "provider_failed"
	}
}

// Normalized enforces invariants on a ModelTestRecord prior to persistence or projection.
func (r ModelTestRecord) Normalized() ModelTestRecord {
	out := ModelTestRecord{
		ModelID:           strings.TrimSpace(r.ModelID),
		OK:                r.OK,
		TestedAt:          r.TestedAt.UTC(),
		ModelUpdatedAt:    r.ModelUpdatedAt.UTC(),
		ProviderUpdatedAt: r.ProviderUpdatedAt.UTC(),
	}
	if r.LatencyMS != nil {
		ms := max(*r.LatencyMS, int64(0))
		out.LatencyMS = &ms
	}
	if !r.OK {
		out.ErrorCode = SanitizeModelTestErrorCode(r.ErrorCode)
	}
	return out
}

func sameMicrosecondTimestamp(a, b time.Time) bool {
	if a.IsZero() || b.IsZero() {
		return false
	}
	return a.UTC().Truncate(time.Microsecond).Equal(b.UTC().Truncate(time.Microsecond))
}

// FreshModelTest returns the persisted test record for m only if a record exists
// and both m.UpdatedAt and its parent provider's UpdatedAt match the private
// timestamps observed when the test started.
func (c Config) FreshModelTest(m ModelProfile, tests map[string]ModelTestRecord) (ModelTestRecord, bool) {
	if len(tests) == 0 {
		return ModelTestRecord{}, false
	}
	rec, ok := tests[m.ID]
	if !ok {
		return ModelTestRecord{}, false
	}
	p, ok := c.ProviderByID(m.ProviderID)
	if !ok {
		return ModelTestRecord{}, false
	}
	if !sameMicrosecondTimestamp(m.UpdatedAt, rec.ModelUpdatedAt) || !sameMicrosecondTimestamp(p.UpdatedAt, rec.ProviderUpdatedAt) {
		return ModelTestRecord{}, false
	}
	return rec.Normalized(), true
}
