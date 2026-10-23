// Package app orchestrates Identity use cases and transaction boundaries
// against pure domain models and outbound repository ports.
package app

import (
	"context"
	"teamie/services/identity/internal/domain"
)

// Service coordinates all Identity application use cases.
type Service struct {
	Tx                  domain.TxManager
	Users               domain.UserRepository
	Sessions            domain.SessionRepository
	Invites             domain.InviteRepository
	PasswordResets      domain.PasswordResetRepository
	Principals          domain.PrincipalRepository
	Passwords           domain.PasswordHasher
	Tokens              domain.TokenGenerator
	Flags               domain.FeatureFlags
	BootstrapInviteCode string
	ControlToken        string
	RuntimeToken        string
}

// BootstrapCode returns the configured bootstrap invitation code or the default.
func (s *Service) BootstrapCode() string {
	if s.BootstrapInviteCode == "" {
		return domain.DefaultBootstrapInviteCode
	}
	return s.BootstrapInviteCode
}

// Ping checks whether the identity store is ready.
func (s *Service) Ping(ctx context.Context) error {
	return s.Tx.Ping(ctx)
}
