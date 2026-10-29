package domain

import "testing"

func TestRelevantPreemptInputUsesPresenceAndConversationSources(t *testing.T) {
	a := InputSource{WorkspaceID: "workspace", ConversationID: "a"}
	b := InputSource{WorkspaceID: "workspace", ConversationID: "b"}
	digest := Input{Kind: "im.unread_digest", SourceRefs: []InputSource{a}}
	human := Input{WorkspaceID: a.WorkspaceID, ConversationID: a.ConversationID, SenderKind: "human"}
	app := Input{Kind: "app.notification", SourceRefs: []InputSource{a}}
	for _, tc := range []struct {
		name     string
		input    Input
		consumed []Input
		active   *InputSource
		want     bool
	}{
		{"human after digest", human, []Input{digest}, &a, true},
		{"app after digest", app, []Input{digest}, &a, true},
		{"overlapping digest", digest, []Input{human}, &a, true},
		{"switched to another conversation", human, []Input{human}, &b, false},
		{"new active conversation not in original input", human, []Input{{ConversationID: "b"}}, &a, true},
		{"unrelated digest", digest, []Input{human}, &b, false},
		{"left all conversations", human, []Input{human}, &InputSource{}, false},
		{"legacy human", human, []Input{human}, nil, true},
		{"legacy digest provenance", human, []Input{digest}, nil, true},
		{"legacy app provenance", app, []Input{human}, nil, true},
		{"workspace mismatch", human, []Input{human}, &InputSource{WorkspaceID: "other", ConversationID: "a"}, false},
		{"agent does not preempt", Input{ConversationID: "a", SenderKind: "agent"}, []Input{human}, &a, false},
		{"task remains deferred", Input{Kind: "task.notification", SourceRefs: []InputSource{a}}, []Input{human}, &a, false},
		{"schedule remains deferred", Input{Kind: "schedule.wakeup", SourceRefs: []InputSource{a}}, []Input{human}, &a, false},
		{"overflow is not trusted", Input{Kind: "im.unread_digest", SourceRefs: []InputSource{a}, SourceOverflow: true}, []Input{human}, &a, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := RelevantPreemptInput(tc.input, tc.consumed, tc.active); got != tc.want {
				t.Fatalf("preempt=%v, want %v", got, tc.want)
			}
		})
	}
}
