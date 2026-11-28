// Package flags adapts shared httpx.Flags to the Push domain FeatureFlags port.
package flags

import "teamie/pkg/httpx"

// Provider wraps httpx.Flags for Push application use cases.
type Provider struct {
	Flags httpx.Flags
}

func New(f httpx.Flags) Provider {
	return Provider{Flags: f}
}

func (p Provider) Enabled(key string) bool {
	return p.Flags.Load().Flags[key]
}
