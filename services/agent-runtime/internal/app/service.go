// Package app orchestrates Agent Runtime application use cases and transaction
// boundaries against pure domain models and outbound repository/gateway ports.
package app

import (
	"context"

	"teamie/services/agent-runtime/internal/domain"
)

// Service coordinates Agent Runtime application use cases across session leases,
// durable inbox ingestion, schedule wakeup validation, provenance/context policy,
// and runtime inspection/lifecycle commands.
type Service struct {
	Tx           domain.TxManager
	Sessions     domain.SessionRepository
	Schedules    domain.ScheduleRepository
	Archive      domain.ArchiveRepository
	ToolReceipts domain.ToolReceiptRepository
	Inspect      domain.InspectRepository
	IM           domain.IMGateway
	Control      domain.ControlGateway
	Model        domain.ModelGateway
	Sandbox      domain.SandboxGateway
	Engine       domain.RuntimeEngine
	ControlToken string
}

func (s *Service) policy() *domain.PolicyEngine {
	if s == nil {
		return &domain.PolicyEngine{}
	}
	return &domain.PolicyEngine{
		Tx:           s.Tx,
		Sessions:     s.Sessions,
		Schedules:    s.Schedules,
		Archive:      s.Archive,
		ToolReceipts: s.ToolReceipts,
		IM:           s.IM,
		Control:      s.Control,
		Model:        s.Model,
		Sandbox:      s.Sandbox,
	}
}

// Ready checks whether the runtime engine and its upstream dependencies are ready.
func (s *Service) Ready(ctx context.Context) error {
	if s != nil && s.Engine != nil {
		return s.Engine.Ready(ctx)
	}
	if s != nil && s.Tx != nil {
		return s.Tx.Ping(ctx)
	}
	return nil
}

// Run starts the background runtime TurnLoop scheduler until ctx is canceled.
func (s *Service) Run(ctx context.Context) {
	if s != nil && s.Engine != nil {
		s.Engine.Run(ctx)
	}
}
