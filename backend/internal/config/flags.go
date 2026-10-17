package config

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

type Snapshot struct {
	Revision string          `json:"revision"`
	Flags    map[string]bool `json:"flags"`
}
type Flags struct{ Path string }

var Implemented = map[string]bool{
	"agents.enabled": true, "agents.create": true, "agents.update": true, "agents.delete": true, "agents.discover": true,
	"agents.runtime.v3": true, "agents.tasks.execute": true,
	"auth.public_signup": false, "auth.password_reset": true, "im.dm.create": true, "im.group.create": true, "im.channel.create": true,
	"im.message.edit": true, "im.message.retract": true, "im.message.mentions": true, "im.typing.publish": true,
	"im.presence.publish": false, "im.attachments.upload": true, "im.reactions.create": false, "im.threads.create": false,
}

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
func (f Flags) Validate() error {
	if f.Path != "" && f.Load().Revision == "invalid-flags" {
		return fmt.Errorf("FEATURE_FLAGS_FILE is missing or invalid")
	}
	return nil
}
