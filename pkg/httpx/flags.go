package httpx

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// Snapshot represents a loaded feature-flag configuration snapshot.
type Snapshot struct {
	Revision string          `json:"revision"`
	Flags    map[string]bool `json:"flags"`
}

// Flags loads and validates feature flags from an optional JSON file path.
type Flags struct {
	Path string
}

// Implemented lists all feature flags supported by this binary build.
var Implemented = map[string]bool{
	"agents.enabled": true, "agents.create": true, "agents.update": true, "agents.delete": true, "agents.discover": true,
	"agents.runtime.v3": true, "agents.tasks.execute": true,
	"auth.public_signup": false, "auth.password_reset": true, "im.dm.create": true, "im.group.create": true, "im.channel.create": true,
	"im.message.edit": true, "im.message.retract": true, "im.message.mentions": true, "im.typing.publish": true,
	"im.presence.publish": true, "im.attachments.upload": true, "im.reactions.create": true, "im.threads.create": false,
	"im.read_receipts.view": true, "im.notifications.configure": true,
}

// Load reads and intersects the feature-flag file with Implemented capabilities, failing closed on error.
func (f Flags) Load() Snapshot {
	s := Snapshot{Revision: "default-v1", Flags: map[string]bool{}}
	for k, v := range Implemented {
		s.Flags[k] = v && k != "auth.public_signup" && !strings.HasPrefix(k, "agents.")
	}
	if f.Path == "" {
		return s
	}
	b, err := os.ReadFile(f.Path)
	var requested Snapshot
	if err != nil || json.Unmarshal(b, &requested) != nil || requested.Revision == "" || requested.Flags == nil {
		for k := range s.Flags {
			s.Flags[k] = false
		}
		s.Revision = "invalid-flags"
		return s
	}
	s.Revision = requested.Revision
	for k, v := range Implemented {
		s.Flags[k] = v && requested.Flags[k]
	}
	return s
}

// Validate checks that a configured FEATURE_FLAGS_FILE can be loaded cleanly.
func (f Flags) Validate() error {
	if f.Path != "" && f.Load().Revision == "invalid-flags" {
		return fmt.Errorf("FEATURE_FLAGS_FILE is missing or invalid")
	}
	return nil
}
