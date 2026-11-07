package domain

import (
	"bytes"
	"encoding/json"
	"slices"
	"testing"

	"github.com/google/uuid"
)

func TestMentionSpanLosslessJSONRoundTripAndLegacyUserParity(t *testing.T) {
	u1 := uuid.NewString()
	u2 := uuid.NewString()
	zero := 0
	three := 3

	userSpan := MentionSpan{UserID: u1, Start: 0, End: 6}
	rawUser, err := json.Marshal(userSpan)
	if err != nil {
		t.Fatal(err)
	}
	wantUser := `{"userId":"` + u1 + `","start":0,"end":6}`
	if string(rawUser) != wantUser {
		t.Fatalf("user MentionSpan JSON = %s, want %s", rawUser, wantUser)
	}
	var decodedUser MentionSpan
	if err := json.Unmarshal(rawUser, &decodedUser); err != nil || !MentionSpanEqual(decodedUser, userSpan) {
		t.Fatalf("user MentionSpan round-trip mismatch: %+v err=%v", decodedUser, err)
	}

	cases := []struct {
		name string
		span MentionSpan
		want string
	}{
		{
			name: "all_with_recipients",
			span: MentionSpan{Kind: "all", Start: 0, End: 4, RecipientIDs: []string{u1, u2}},
			want: `{"kind":"all","start":0,"end":4,"recipientIds":["` + u1 + `","` + u2 + `"]}`,
		},
		{
			name: "all_empty_recipients_slice",
			span: MentionSpan{Kind: "all", Start: 0, End: 4, RecipientIDs: []string{}},
			want: `{"kind":"all","start":0,"end":4,"recipientIds":[]}`,
		},
		{
			name: "all_display_only_nil_recipients",
			span: MentionSpan{Kind: "all", Start: 0, End: 4},
			want: `{"kind":"all","start":0,"end":4}`,
		},
		{
			name: "here_zero_recipients",
			span: MentionSpan{Kind: "here", Start: 2, End: 7, RecipientCount: &zero},
			want: `{"kind":"here","start":2,"end":7,"recipientCount":0}`,
		},
		{
			name: "here_three_recipients",
			span: MentionSpan{Kind: "here", Start: 2, End: 7, RecipientCount: &three},
			want: `{"kind":"here","start":2,"end":7,"recipientCount":3}`,
		},
		{
			name: "here_display_only_nil_count",
			span: MentionSpan{Kind: "here", Start: 2, End: 7},
			want: `{"kind":"here","start":2,"end":7}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw, err := json.Marshal(tc.span)
			if err != nil {
				t.Fatal(err)
			}
			if string(raw) != tc.want {
				t.Fatalf("got %s, want %s", raw, tc.want)
			}
			var got MentionSpan
			if err := json.Unmarshal(raw, &got); err != nil {
				t.Fatal(err)
			}
			if !MentionSpanEqual(got, tc.span) {
				t.Fatalf("round-trip mismatch: got %+v, want %+v", got, tc.span)
			}
		})
	}
}

func TestAttachmentImageMetadataAndValueEquality(t *testing.T) {
	id := uuid.NewString()
	plain := Attachment{
		ID:        id,
		Filename:  "doc.pdf",
		MediaType: "application/pdf",
		SizeBytes: 512,
		SHA256:    "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
	}
	rawPlain, err := json.Marshal(plain)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(rawPlain, []byte(`"image"`)) {
		t.Fatalf("non-image attachment emitted image field: %s", rawPlain)
	}

	imgA := plain
	imgA.Filename = "chart.png"
	imgA.MediaType = "image/png"
	imgA.Image = &AttachmentImage{Width: 1280, Height: 720}
	rawImg, err := json.Marshal(imgA)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(rawImg, []byte(`"image":{"width":1280,"height":720}`)) {
		t.Fatalf("image metadata missing from JSON: %s", rawImg)
	}
	var imgB Attachment
	if err := json.Unmarshal(rawImg, &imgB); err != nil {
		t.Fatal(err)
	}
	if imgA.Image == imgB.Image {
		t.Fatal("expected distinct AttachmentImage pointers after unmarshal")
	}
	if !AttachmentsEqual([]Attachment{imgA}, []Attachment{imgB}) {
		t.Fatal("AttachmentsEqual rejected identical image dimensions across distinct pointers")
	}
	if AttachmentsEqual([]Attachment{plain}, []Attachment{imgA}) {
		t.Fatal("AttachmentsEqual ignored nil vs non-nil Image")
	}
	imgDiff := imgB
	imgDiff.Image = &AttachmentImage{Width: 640, Height: 720}
	if AttachmentsEqual([]Attachment{imgA}, []Attachment{imgDiff}) {
		t.Fatal("AttachmentsEqual ignored width difference")
	}
}

func TestForwardedMessageProvenanceClosureAndDisplayOnlyMentions(t *testing.T) {
	agentID := uuid.NewString()
	targetWS := uuid.NewString()
	targetConv := uuid.NewString()
	targetMsg := uuid.NewString()
	targetSender := uuid.NewString()
	copiedAtt := uuid.NewString()
	directAtt := uuid.NewString()

	untrustedSourceConv := uuid.NewString()
	untrustedSourceMsg := uuid.NewString()
	untrustedOrigSender := uuid.NewString()

	in := Input{
		EventID:        "42",
		WorkspaceID:    targetWS,
		ConversationID: targetConv,
		MessageID:      targetMsg,
		SenderID:       targetSender,
		SenderName:     "Forwarder",
		SenderKind:     "human",
		Body:           "", // empty-comment forward
		Attachments: []Attachment{
			{ID: directAtt, Filename: "note.txt", MediaType: "text/plain", SizeBytes: 4},
		},
		Forwarded: &ForwardedMessage{
			SourceConversationID: untrustedSourceConv,
			SourceMessageID:      untrustedSourceMsg,
			SenderID:             untrustedOrigSender,
			SenderName:           "Original Author",
			SenderKind:           "human",
			CreatedAt:            "2026-11-07T00:00:00Z",
			Body:                 "@Agent @all @here check secret source",
			Mentions: []MentionSpan{
				{UserID: agentID, Start: 0, End: 6},
				{Kind: "all", Start: 7, End: 11, RecipientIDs: []string{agentID}},
				{Kind: "here", Start: 12, End: 17},
			},
			Attachments: []Attachment{
				{
					ID:        copiedAtt,
					Filename:  "diagram.webp",
					MediaType: "image/webp",
					SizeBytes: 1024,
					Image:     &AttachmentImage{Width: 800, Height: 600},
				},
			},
		},
		Seq:       7,
		CreatedAt: "2026-11-07T00:01:00Z",
	}

	if in.AddressedTo(agentID) {
		t.Fatal("display-only mentions inside Forwarded must never address the Agent")
	}

	refs, overflow := ExtractInputSources([]Input{in})
	if overflow || len(refs) != 1 {
		t.Fatalf("ExtractInputSources = %+v, overflow=%v", refs, overflow)
	}
	got := refs[0]
	if got.WorkspaceID != targetWS || got.ConversationID != targetConv || got.MessageID != targetMsg || got.SenderID != targetSender {
		t.Fatalf("unexpected source identity: %+v", got)
	}
	if got.ConversationID == untrustedSourceConv || got.MessageID == untrustedSourceMsg || got.SenderID == untrustedOrigSender {
		t.Fatalf("untrusted forward snapshot leaked into provenance: %+v", got)
	}
	if !slices.Contains(got.AttachmentIDs, directAtt) || !slices.Contains(got.AttachmentIDs, copiedAtt) || len(got.AttachmentIDs) != 2 {
		t.Fatalf("expected target message + copied forward attachment IDs, got %+v", got.AttachmentIDs)
	}
	canonical, err := CanonicalContextSources(refs)
	if err != nil || len(canonical) != 1 {
		t.Fatalf("CanonicalContextSources failed: %+v err=%v", canonical, err)
	}

	// Preemption must use only target conversation, never Forwarded.SourceConversationID.
	sourceActive := &InputSource{WorkspaceID: targetWS, ConversationID: untrustedSourceConv}
	if RelevantPreemptInput(in, nil, sourceActive) {
		t.Fatal("Forwarded.SourceConversationID falsely preempted an unrelated active conversation")
	}
	targetActive := &InputSource{WorkspaceID: targetWS, ConversationID: targetConv}
	if !RelevantPreemptInput(in, nil, targetActive) {
		t.Fatal("target conversation forward failed to preempt active target conversation")
	}

	// Top-level @all and BroadcastAddressed address the Agent identically to @agent.
	withAll := in
	withAll.Mentions = []MentionSpan{{Kind: "all", Start: 0, End: 4, RecipientIDs: []string{agentID}}}
	if !withAll.AddressedTo(agentID) {
		t.Fatal("top-level @all containing agentID did not address Agent")
	}
	withHere := in
	withHere.Mentions = []MentionSpan{{Kind: "here", Start: 0, End: 5}}
	if withHere.AddressedTo(agentID) {
		t.Fatal("unresolved @here without BroadcastAddressed must not address Agent")
	}
	withHere.BroadcastAddressed = true
	if !withHere.AddressedTo(agentID) {
		t.Fatal("resolved BroadcastAddressed @here did not address Agent")
	}
}
