package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/cloudwego/eino/schema"
	"github.com/google/uuid"
	wire "teamie/agent-protocol/kitex_gen/agent"
)

func TestEventDrivenPolicyKeepsPreviousPromptBytes(t *testing.T) {
	// Captured from f1f1f149 before adding the optional selection. In particular,
	// both summary strings contribute to a persisted Run's model-request hash.
	descriptions, err := json.Marshal(contextV1Descriptions)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct{ name, text, hash string }{
		{"legacy main", agentInstruction, "2b4fe9d078d0f3a258466c7cf17067971e25c8e6ee3d941d6672aabb91ad60c9"},
		{"legacy summary", summaryInstruction, "d8d7ae2b30d82f19258666559d2072ec9ad8b591579bfccd9f7de17a5f837977"},
		{"context main", contextV1Behavior, "e550ba1b13c962d79ef18d4362586dedbdf27c0f33194cbd24b41ae0befa3127"},
		{"context descriptions", string(descriptions), "4fbac272ce879599ec126f1d336c1ca9479facd28d0a12f1a4ed10c4a243e8d7"},
		{"context summary", contextSummaryInstruction(ContextBudgetSnapshot{}), "01d22e43e03ec7baea933b26efeea3dc13cea482a99607cfaf15f19790b7ea8e"},
		{"context management", contextManagementInstruction(ContextBudgetSnapshot{}), "cfe4e4f8bd734abc0108163ffd55257112782b776e2fbe96b0af27913b9931ac"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := digest([]byte(test.text)); got != test.hash {
				t.Fatalf("frozen prompt changed: got %s, want %s", got, test.hash)
			}
		})
	}
	def := &wire.AgentDefinition{Id: "agent", OwnerId: "owner", Name: "untrusted\nname"}
	if contextInstruction(def, ContextBudgetSnapshot{}) != contextV1Instruction(def) {
		t.Fatal("omitted selection changed the composed old instruction")
	}
}

func TestEventDrivenSelectionPreservesHistoricalSnapshotsAndCheckpoints(t *testing.T) {
	oldRecoveryHashes := []string{
		"96c05e020e1c7440d6abe7a6eab93cd7287f160872167bd30ce10b5498a842d7",
		"7d191938313b409a179ac76f13bbc4fac5212225468ad2ac8dc6482a8a492bcd",
		"06b21131bc2350b814b9206ae52e9b25a454ef990cfd244fbe36b13521a056ae",
		"ce2447fcc25cecdc6c40859a0e098e29618b91f3f839a7f255ad69ea62041dbf",
		"5758e020d4d5fb67c821a0b043093cc02ac4c5e44f0c5f6a649e863c95386a69",
		"a7b2237805e78139ce23681417ab16c608a2021781581e6fa7d5f17cfcf15f04",
	}
	for version := 1; version <= contextBudgetVersion; version++ {
		t.Run(fmt.Sprint(version), func(t *testing.T) {
			raw, err := os.ReadFile(fmt.Sprintf("../../../ops/fixtures/runtime-context-budget-v%d.json", version))
			if err != nil {
				t.Fatal(err)
			}
			var previous map[string]any
			if err = json.Unmarshal(raw, &previous); err != nil {
				t.Fatal(err)
			}
			previousBytes, _ := json.Marshal(previous)
			_, canonical, err := canonicalContextPolicy(contextProfileV1, raw)
			if err != nil || string(canonical) != string(previousBytes) {
				t.Fatal("old snapshot canonical bytes changed", err)
			}
			budget, err := ParseContextBudgetSnapshot(canonical)
			if err != nil || budget.EventDrivenInputs {
				t.Fatal("old snapshot selected new guidance", err)
			}
			run := &Run{ID: uuid.NewString(), Attempt: uuid.NewString(), toolProfile: currentToolProfile, ContextProfile: contextProfileV1, ContextBudget: canonical}
			recovery, err := recoveryDecision(run)
			if err != nil || digest(recovery) != oldRecoveryHashes[version-1] {
				t.Fatal("nested recovery budget bytes changed", err)
			}
			metadata := &checkpointMetadata{Version: nativeRuntimeVersion, Revision: 1, RunID: run.ID, Attempt: run.Attempt, Targets: []string{"model"}, ToolProfile: currentToolProfile, ContextProfile: contextProfileV1, ContextBudgetHash: digest(previousBytes)}
			if !resumeCompatibleForRun(metadata, 1, run) {
				t.Fatal("existing checkpoint became incompatible")
			}
			budget.EventDrivenInputs = true
			updated, _ := json.Marshal(budget)
			run.ContextBudget = updated
			if resumeCompatibleForRun(metadata, 1, run) {
				t.Fatal("old checkpoint accepted changed behavior selection")
			}
			budget.EventDrivenInputs = false
			omitted, _ := json.Marshal(budget)
			if strings.Contains(string(omitted), "eventDrivenInputs") {
				t.Fatal("false selection changed an omitted old field")
			}
			previous["eventDrivenInputs"] = false
			explicitFalse, _ := json.Marshal(previous)
			parsed, err := ParseContextBudgetSnapshot(explicitFalse)
			if err != nil || parsed != budget {
				t.Fatal("explicit false is not compatible with omitted selection", err)
			}
		})
	}
}

func TestEventDrivenGuidanceSelectsDescriptionsWithoutChangingToolContracts(t *testing.T) {
	param := schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{"conversationId": {Type: schema.String, Required: true}})
	infos := []*schema.ToolInfo{nil, {Name: "im_send_message", Desc: "unchanged delivery schema", ParamsOneOf: param}, {Name: "external", Desc: "external description", ParamsOneOf: param}}
	for name := range eventDrivenDescriptions {
		infos = append(infos, &schema.ToolInfo{Name: name, Desc: "original", ParamsOneOf: param})
	}
	before, _ := json.Marshal(infos)
	old := contextToolInfos(infos, ContextBudgetSnapshot{})
	current := contextToolInfos(infos, ContextBudgetSnapshot{EventDrivenInputs: true})
	after, _ := json.Marshal(infos)
	if string(before) != string(after) || !reflect.DeepEqual(old, contextV1ToolInfos(infos)) || len(current) != len(old) {
		t.Fatal("guidance mutated the previous manifest or available tool set")
	}
	for i, info := range current {
		if info == nil {
			continue
		}
		if info.ParamsOneOf != infos[i].ParamsOneOf || info.Name != infos[i].Name {
			t.Fatal("guidance changed tool parameters or routing")
		}
		if description, ok := eventDrivenDescriptions[info.Name]; ok {
			if info.Desc != description || info.Desc == old[i].Desc {
				t.Fatal("new retrieval description was not selected", info.Name)
			}
		} else if info.Desc != old[i].Desc {
			t.Fatal("unrelated tool description changed", info.Name)
		}
	}
	budget := ContextBudgetSnapshot{EventDrivenInputs: true}
	policy := contextInstruction(&wire.AgentDefinition{Id: "agent", OwnerId: "owner", Name: "Agent"}, budget)
	behaviorHasGuidance(t, policy, "serial TurnLoop", "before starting work", "periodically during work", "final check", "specific missing fact", "explicitly asks to verify", "concrete evidence", "vary search keywords", "then end the turn", "tool ACL checks", "Runtime's own sync polling")
	behaviorHasGuidance(t, policy, "all requested fields", "no conflicting evidence", "required pages left unread", "final", "latest", "confirm", "advertised keyword argument")
	behaviorHasGuidance(t, eventDrivenDescriptions["im_list_messages"], "keyword/aroundSeq", "all requested fields", "Final/latest/confirm", "unread required pages")
	behaviorHasGuidance(t, contextSummaryInstruction(budget), "history already inspected", "explicit requests to verify", "settled facts", "ending the turn", "which requested fields are resolved", "conflicting evidence", "unread required page")
	behaviorHasGuidance(t, contextManagementInstruction(budget), "sufficient facts already in context", "explicit verification", "do not routinely reconfirm", "Runtime delivers new inputs")
}

func TestEventDrivenTaskContextSnapshotRoundTrip(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(fmt.Sprint(enabled), func(t *testing.T) {
			budget, err := makeContextBudgetSnapshot(16384)
			if err != nil {
				t.Fatal(err)
			}
			budget.EventDrivenInputs = enabled
			raw, _ := json.Marshal(budget)
			run := &Run{AgentID: "agent", ID: "run", Attempt: "attempt", Revision: 2, ContextProfile: contextProfileV1, ContextBudget: raw, Inputs: []Input{{EventID: "1", WorkspaceID: "workspace", ConversationID: "conversation", MessageID: "message", Body: "PRIVATE_BODY"}}}
			snapshotter := taskContexts{agentID: run.AgentID, revision: run.Revision}
			encoded, err := snapshotter.CaptureContext(WithRun(context.Background(), run))
			if err != nil || strings.Contains(string(encoded), "PRIVATE_BODY") || strings.Contains(string(encoded), "eventDrivenInputs") != enabled {
				t.Fatal("task snapshot lost selection or copied a private body", err)
			}
			ctx, err := snapshotter.RestoreContext(context.Background(), encoded)
			if err != nil {
				t.Fatal(err)
			}
			restored, err := CurrentRun(ctx)
			if err != nil {
				t.Fatal(err)
			}
			selection, err := ParseContextBudgetSnapshot(restored.ContextBudget)
			if err != nil || selection != budget {
				t.Fatal("restored task changed its original policy", err)
			}
			refs, overflow := inputSources(restored.Inputs)
			wantRefs, _ := inputSources(run.Inputs)
			if overflow || !reflect.DeepEqual(refs, wantRefs) {
				t.Fatal("task policy roundtrip changed source attribution")
			}
		})
	}
}
