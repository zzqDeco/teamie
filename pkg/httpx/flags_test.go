package httpx

import (
	"os"
	"path/filepath"
	"testing"
)

func TestUnsupportedCapabilitiesRemainOffAndBadReloadFailsClosed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "flags.json")
	if e := os.WriteFile(path, []byte(`{"revision":"one","flags":{"auth.public_signup":true,"im.message.edit":true,"im.attachments.upload":true,"im.presence.publish":true,"im.threads.create":true}}`), 0600); e != nil {
		t.Fatal(e)
	}
	f := Flags{Path: path}
	s := f.Load()
	if s.Revision != "one" || s.Flags["auth.public_signup"] || !s.Flags["im.message.edit"] || !s.Flags["im.attachments.upload"] || !s.Flags["im.presence.publish"] || s.Flags["im.threads.create"] || s.Flags["im.dm.create"] {
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

func TestPrivateContextFlagDoesNotBecomeBrowserCapability(t *testing.T) {
	path := filepath.Join(t.TempDir(), "flags.json")
	if err := os.WriteFile(path, []byte(`{"revision":"context-engineering-v1","flags":{"agents.enabled":true,"agents.context.v1":true}}`), 0600); err != nil {
		t.Fatal(err)
	}
	loaded := (Flags{Path: path}).Load()
	if _, exists := loaded.Flags["agents.context.v1"]; exists {
		t.Fatal("private context execution flag entered public capabilities")
	}
	if !loaded.Flags["agents.enabled"] {
		t.Fatal("filtering private flags changed the ordinary Agent capability")
	}
	if !loaded.Flags["im.announcements.publish"] {
		t.Fatal("expected im.announcements.publish to default on when omitted from valid older flag files")
	}
	if err := os.WriteFile(path, []byte(`{"revision":"announcements-off","flags":{"im.announcements.publish":false}}`), 0600); err != nil {
		t.Fatal(err)
	}
	if (Flags{Path: path}).Load().Flags["im.announcements.publish"] {
		t.Fatal("expected explicit im.announcements.publish=false to disable announcement publishing")
	}
}

func TestDefaultFeatureFlagsEnableIMCollaborationCapabilities(t *testing.T) {
	defaultPath := filepath.Join("..", "..", "deploy", "feature-flags.default.json")
	loaded := (Flags{Path: defaultPath}).Load()
	if loaded.Revision != "im-collab-v1" {
		t.Fatalf("expected revision im-collab-v1, got %q", loaded.Revision)
	}
	for _, key := range []string{
		"im.messages.forward",
		"im.messages.pin",
		"im.messages.save",
		"im.unread.mark",
		"im.mentions.broadcast",
	} {
		if !loaded.Flags[key] {
			t.Fatalf("expected %s enabled in default feature flags", key)
		}
		if !(Flags{}).Load().Flags[key] {
			t.Fatalf("expected %s enabled in built-in default snapshot", key)
		}
	}
}
