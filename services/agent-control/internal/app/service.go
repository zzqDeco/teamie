// Package app contains application use-case orchestration for the Agent Control
// Plane bounded context. It depends only on the Go standard library and
// teamie/services/agent-control/internal/domain.
package app

import (
	"context"

	"teamie/services/agent-control/internal/domain"
)

// Service coordinates Agent lifecycle, visibility revisions, workspace discovery,
// and IM reconciliation.
type Service struct {
	Tx           domain.TxManager
	Agents       domain.AgentRepository
	IM           domain.IMAuthority
	Flags        domain.FeatureFlags
	Notifier     domain.RuntimeNotifier
	RuntimeToken string
}

func (s *Service) flagEnabled(key string) bool {
	return s.Flags != nil && s.Flags.Enabled(key)
}

func (s *Service) requireFlag(key string) error {
	if !s.flagEnabled(key) {
		return domain.ErrFeatureDisabled()
	}
	return nil
}

// GlobalEnabled reports whether the global agents.enabled feature flag is active.
func (s *Service) GlobalEnabled() bool {
	return s.flagEnabled("agents.enabled")
}

// Ping checks database readiness.
func (s *Service) Ping(ctx context.Context) error {
	return s.Tx.Ping(ctx)
}

// Ready checks both database and IM dependency readiness.
func (s *Service) Ready(ctx context.Context) error {
	if err := s.Tx.Ping(ctx); err != nil {
		return err
	}
	return s.IM.Ping(ctx)
}
