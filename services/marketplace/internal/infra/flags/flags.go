// Package flags adapts shared runtime feature flags to the Marketplace domain FeatureFlags port.
package flags

import "teamie/pkg/httpx"

// Adapter implements domain.FeatureFlags over httpx.Flags.
type Adapter struct {
	Flags httpx.Flags
}

func New(f httpx.Flags) Adapter {
	return Adapter{Flags: f}
}

func (a Adapter) Enabled(key string) bool {
	return a.Flags.Load().Flags[key]
}
