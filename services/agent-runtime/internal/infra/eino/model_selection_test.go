package eino

import (
	"testing"

	"teamie/services/agent-runtime/internal/domain"
)

func TestWorkerDefinitionPreservesSelectedModel(t *testing.T) {
	for _, selected := range []string{"", "GPT4", "_model", "other-model"} {
		def := agentDefinition(domain.AgentProfile{ID: "agent", OwnerID: "owner", ModelID: selected, Enabled: true, Revision: 7})
		if def.GetModelId() != selected {
			t.Fatalf("worker lost selection: got %q, want %q", def.GetModelId(), selected)
		}
	}
}
