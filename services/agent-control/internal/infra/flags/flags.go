// Package flags implements the Agent Control Plane FeatureFlags outbound port.
package flags

import (
	"encoding/json"
	"os"
)

// Flags loads the feature-flags JSON file on demand and fails closed on invalid or missing revisions.
type Flags struct {
	Path string
}

func (f Flags) Enabled(key string) bool {
	if f.Path == "" {
		return false
	}
	b, err := os.ReadFile(f.Path)
	if err != nil {
		return false
	}
	var v struct {
		Revision string          `json:"revision"`
		Flags    map[string]bool `json:"flags"`
	}
	if json.Unmarshal(b, &v) != nil || v.Revision == "" {
		return false
	}
	return v.Flags[key]
}
