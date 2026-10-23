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

func init() {
	domain.DefaultScheduleCoordinator = func(
		tx domain.TxManager,
		sessions domain.SessionRepository,
		schedules domain.ScheduleRepository,
		archive domain.ArchiveRepository,
		receipts domain.ToolReceiptRepository,
		im domain.IMGateway,
	) domain.ScheduleCoordinator {
		return &Service{
			Tx:           tx,
			Sessions:     sessions,
			Schedules:    schedules,
			Archive:      archive,
			ToolReceipts: receipts,
			IM:           im,
		}
	}
}

// WithIM returns a ScheduleCoordinator scoped to the given IMGateway.
func (s *Service) WithIM(im domain.IMGateway) domain.ScheduleCoordinator {
	if s == nil {
		return &Service{IM: im}
	}
	cp := *s
	cp.IM = im
	return &cp
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
