package rpcclient

import (
	"testing"

	wire "teamie/contracts/gen/agent/kitex_gen/agent"
)

func TestControlProfilePreservesModelSelection(t *testing.T) {
	for _, selected := range []string{"", "GPT4", "_model", "other-model"} {
		def := &wire.AgentDefinition{Id: "agent", OwnerId: "owner", ModelId: &selected, Revision: 7}
		got := toDomainProfile(def)
		if got.ModelID != selected {
			t.Fatalf("selection lost at runtime control boundary: got %q, want %q", got.ModelID, selected)
		}
	}
}
