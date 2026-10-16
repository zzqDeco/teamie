package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestUnsupportedCapabilitiesRemainOffAndBadReloadFailsClosed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "flags.json")
	if e := os.WriteFile(path, []byte(`{"revision":"one","flags":{"auth.public_signup":true,"im.message.edit":true,"im.attachments.upload":true,"im.presence.publish":true}}`), 0600); e != nil {
		t.Fatal(e)
	}
	f := Flags{Path: path}
	s := f.Load()
	if s.Revision != "one" || s.Flags["auth.public_signup"] || !s.Flags["im.message.edit"] || !s.Flags["im.attachments.upload"] || s.Flags["im.presence.publish"] || s.Flags["im.dm.create"] {
		t.Fatal("flags do not intersect implementation")
	}
	for _, key := range []string{"agents.enabled", "agents.create", "agents.update", "agents.delete", "agents.discover"} {
		if s.Flags[key] || (Flags{}).Load().Flags[key] {
			t.Fatalf("missing v1 flag unexpectedly enables %s", key)
		}
	}
	if e := os.WriteFile(path, []byte(`bad`), 0600); e != nil {
		t.Fatal(e)
	}
	for _, enabled := range f.Load().Flags {
		if enabled {
			t.Fatal("bad reload enabled writes")
		}
	}
}
