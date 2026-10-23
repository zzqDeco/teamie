package app

import (
	"context"
	"encoding/json"

	"teamie/services/agent-runtime/internal/domain"
)

// ValidateLease verifies that a worker lease is well-formed and currently held in storage.
func (s *Service) ValidateLease(ctx context.Context, lease *domain.Lease) error {
	return s.policy().ValidateLease(ctx, lease)
}

// CheckRevisionPreemption reports whether an active worker must be canceled due to
// agent disablement, deletion, or revision advancement.
func (s *Service) CheckRevisionPreemption(enabled, deleted bool, currentRevision, workerRevision int64) bool {
	return s.policy().CheckRevisionPreemption(enabled, deleted, currentRevision, workerRevision)
}

// ResolveRunContextPolicy validates and canonicalizes a Run's context profile and budget snapshot.
func (s *Service) ResolveRunContextPolicy(profile string, raw json.RawMessage) (string, json.RawMessage, error) {
	return s.policy().ResolveRunContextPolicy(profile, raw)
}
