package domain

import "testing"

func TestAppNotificationSourcesSurviveDerivedMachineInputs(t *testing.T) {
	const workspace = "11111111-1111-4111-8111-111111111111"
	const conversation = "22222222-2222-4222-8222-222222222222"
	for _, legacy := range []bool{false, true} {
		refs := []InputSource{{WorkspaceID: workspace, ConversationID: conversation}}
		if legacy {
			refs[0].ConversationID = ""
		}
		input := Input{Kind: "app.notification", WorkspaceID: workspace, ConversationID: conversation, SourceRefs: refs}
		sources, overflow := ExtractInputSources([]Input{input})
		for _, kind := range []string{"task.notification", "schedule.wakeup"} {
			sources, overflow = ExtractInputSources([]Input{{Kind: kind, SourceRefs: sources, SourceOverflow: overflow}})
			canonical, err := CanonicalContextSources(sources)
			if err != nil || overflow {
				t.Fatalf("legacy=%v kind=%s lost provenance: %v", legacy, kind, err)
			}
			found := false
			for _, ref := range canonical {
				found = found || (ref.WorkspaceID == workspace && ref.ConversationID == conversation && ref.MessageID == "" && ref.EventID == "")
			}
			if !found {
				t.Fatalf("legacy=%v kind=%s dropped conversation ACL: %+v", legacy, kind, canonical)
			}
		}
	}
}
