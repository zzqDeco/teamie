// Package flags adapts shared runtime feature flags to the Chat domain FeatureFlags port.
package flags

import "teamie/pkg/config"

// Adapter implements domain.FeatureFlags over config.Flags.
type Adapter struct {
	Flags config.Flags
}

func New(f config.Flags) Adapter {
	return Adapter{Flags: f}
}

func (a Adapter) Enabled(key string) bool {
	return a.Flags.Load().Flags[key]
}

func (a Adapter) Snapshot() (string, map[string]bool) {
	snap := a.Flags.Load()
	return snap.Revision, snap.Flags
}
