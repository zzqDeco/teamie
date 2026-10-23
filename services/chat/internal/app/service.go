// Package app orchestrates Chat use cases and transaction boundaries against
// pure domain models and outbound repository ports.
package app

import (
	"context"

	"teamie/services/chat/internal/domain"
)

// Service coordinates all Chat application use cases.
type Service struct {
	Tx             domain.TxManager
	Workspaces     domain.WorkspaceRepository
	Conversations  domain.ConversationRepository
	Memberships    domain.MembershipRepository
	Messages       domain.MessageRepository
	Attachments    domain.AttachmentRepository
	Outbox         domain.OutboxRepository
	Identity       domain.IdentityDirectory
	Flags          domain.FeatureFlags
	SniffMediaType func([]byte) string
	ControlToken   string
}

// MessageApp is an alias for Service focusing on message send/edit/retract/locate use cases.
type MessageApp = Service

// ConversationApp is an alias for Service focusing on channel/DM/group and membership use cases.
type ConversationApp = Service

// AttachmentApp is an alias for Service focusing on idempotent attachment upload and retrieval use cases.
type AttachmentApp = Service

// WorkspaceApp is an alias for Service focusing on workspace and delegated membership use cases.
type WorkspaceApp = Service

func (s *Service) requireFlag(key string) error {
	if s.Flags == nil || !s.Flags.Enabled(key) {
		return domain.ErrFeatureDisabled()
	}
	return nil
}

// Ping checks whether the Chat store is ready.
func (s *Service) Ping(ctx context.Context) error {
	return s.Tx.Ping(ctx)
}

// CheckDependencies checks both the Chat store and Identity RPC dependency.
func (s *Service) CheckDependencies(ctx context.Context) error {
	if err := s.Tx.Ping(ctx); err != nil {
		return err
	}
	if s.Identity != nil {
		return s.Identity.Ping(ctx)
	}
	return nil
}
