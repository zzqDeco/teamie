// Package flags adapts shared runtime feature flags to the Identity domain FeatureFlags port.
package flags

import "teamie/pkg/config"

// Adapter implements domain.FeatureFlags over config.Flags.
type Adapter struct {
	Flags config.Flags
}

func New(f config.Flags) Adapter {
	return Adapter{Flags: f}
}

func (a Adapter) PasswordResetEnabled() bool {
	return a.Flags.Load().Flags["auth.password_reset"]
}
