package config

import (
	"encoding/json"
	"fmt"
	"os"
)

type Snapshot struct {
	Revision string          `json:"revision"`
	Flags    map[string]bool `json:"flags"`
}
type Flags struct{ Path string }

var Implemented = map[string]bool{
	"auth.public_signup": true, "im.dm.create": true, "im.group.create": true, "im.channel.create": true,
	"im.message.edit": true, "im.message.retract": true, "im.typing.publish": true,
	"im.presence.publish": false, "im.attachments.upload": false, "im.reactions.create": false, "im.threads.create": false,
}

func (f Flags) Load() Snapshot {
	s := Snapshot{Revision: "default-v1", Flags: map[string]bool{}}
	for k, v := range Implemented {
		s.Flags[k] = v && k != "auth.public_signup"
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
