package eino

import (
	"bytes"
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

func summaryProvenanceInput(eventID string) Input {
	return Input{EventID: eventID, WorkspaceID: "workspace-" + eventID, ConversationID: "conversation-" + eventID,
		MessageID: "message-" + eventID, ReplyToID: "parent-" + eventID, SenderID: "sender-" + eventID,
		SenderName: "Fixture sender", SenderKind: "human", Body: "source fixture", Seq: 7, CreatedAt: "2026-10-17T00:00:00Z"}
}

func requireSummaryProvenance(t *testing.T, messages []*schema.AgenticMessage, inputs []Input, attachmentIDs [][]string) {
	t.Helper()
	var sources []map[string]json.RawMessage
	for _, message := range messages {
		text := agenticText(message)
		if !strings.HasPrefix(text, "TEAMIE_SESSION_SUMMARY ") {
			continue
		}
		const prefix = "\nTEAMIE_SOURCE_REFERENCES "
		index := strings.LastIndex(text, prefix)
		if index < 0 {
			t.Fatal("summary omitted its source references")
		}
		if err := json.Unmarshal([]byte(text[index+len(prefix):]), &sources); err != nil {
			t.Fatal(err)
		}
		break
	}
	if len(sources) != len(inputs) {
		t.Fatalf("source count = %d, want %d", len(sources), len(inputs))
	}
	for i, in := range inputs {
		for key, want := range map[string]string{"eventId": in.EventID, "workspaceId": in.WorkspaceID,
			"conversationId": in.ConversationID, "messageId": in.MessageID, "replyToId": in.ReplyToID,
			"senderId": in.SenderID, "senderKind": in.SenderKind} {
			var got string
			if err := json.Unmarshal(sources[i][key], &got); err != nil || got != want {
				t.Fatalf("source %d lost %s: got %q, want %q, error %v", i, key, got, want, err)
			}
		}
		raw, present := sources[i]["attachmentIds"]
		if len(attachmentIDs[i]) == 0 {
			if present {
				t.Fatalf("source %d added an empty attachment placeholder: %s", i, raw)
			}
			continue
		}
		var got []string
		if err := json.Unmarshal(raw, &got); err != nil || !reflect.DeepEqual(got, attachmentIDs[i]) {
			t.Fatalf("source %d changed attachment IDs: got %v, want %v, error %v", i, got, attachmentIDs[i], err)
		}
	}
}

func TestSummaryProvenanceKeepsV034RequestBytesWithoutAttachments(t *testing.T) {
	// Frozen from finalizeSummary and chatRequest at 0ce504f (v0.3.4), before
	// attachment support. An empty attachment field changes the receipt hash.
	const expected = `{"max_tokens":2048,"messages":[{"role":"system","content":"policy fixture"},{"role":"user","content":"TEAMIE_SESSION_SUMMARY (source-scoped memory; verify current access using tools):\nsource fixture\nTEAMIE_SOURCE_REFERENCES [{\"conversationId\":\"conversation-17\",\"eventId\":\"17\",\"messageId\":\"message-17\",\"replyToId\":\"parent-17\",\"senderId\":\"sender-17\",\"senderKind\":\"human\",\"workspaceId\":\"workspace-17\"}]"},{"role":"user","content":"TEAMIE_NEW_MESSAGE {\"eventId\":\"17\",\"workspaceId\":\"workspace-17\",\"conversationId\":\"conversation-17\",\"messageId\":\"message-17\",\"senderId\":\"sender-17\",\"senderName\":\"Fixture sender\",\"senderKind\":\"human\",\"body\":\"source fixture\",\"replyToId\":\"parent-17\",\"seq\":7,\"createdAt\":\"2026-10-17T00:00:00Z\"}"}],"stream":false}`
	in := summaryProvenanceInput("17")
	original := append([]*schema.AgenticMessage{schema.SystemAgenticMessage("policy fixture")}, inputMessages([]Input{in})...)
	for round := 0; round < 3; round++ {
		compacted, err := finalizeV034Summary(context.Background(), original, schema.UserAgenticMessage("source fixture"))
		if err != nil {
			t.Fatal(err)
		}
		requireSummaryProvenance(t, compacted, []Input{in}, [][]string{nil})
		classic, err := legacyMessages(compacted)
		if err != nil {
			t.Fatal(err)
		}
		request, err := legacyChatRequest(classic, nil, &model.Options{})
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(request, []byte(expected)) {
			t.Fatalf("compaction %d changed the v0.3.4 request bytes:\n%s", round, request)
		}
		original = compacted
	}
}

func TestSummaryProvenanceKeepsV035RequestHashes(t *testing.T) {
	const first = "00000000-0000-4000-8000-000000000001"
	const second = "00000000-0000-4000-8000-000000000002"
	// Frozen by running finalizeSummary at 99df6bd (v0.3.5) through its native
	// request encoder. Fresh [] and parsed previous summaries have
	// different existing representations; neither may drift on native replay.
	for _, test := range []struct {
		name        string
		attachments []Attachment
		hashes      [][2]string // Generate and stream receipt identities.
	}{
		{"without files", nil, [][2]string{
			{"2ed3d99cbabe1e2dbbe1cd58cec7f9f5baa1ae2159505c5094781bb9e54a6ad7", "23ba92ae90c52c791b07431945a7bfc6767ad977fb10490da0899c5e2ce9dec6"},
			{"deee490ee596019d9253b64da6594f008163fe3e4fa382e1ccb2085d4d8ee872", "480735a6feaf361b834e37dd4ff08ad0a5c637acf50c3afb25bb9750ec288747"},
			{"d7fc5cd9c299b3efb01b3991a47ff21fcb66c96a4a9b30ff5d787f974ef14b5c", "780f10e7712ec0708be7cee676a9b49ac7b500fd12a87a1e1bab2d12798028ac"},
		}},
		{"with files", []Attachment{{ID: first}, {ID: second}}, [][2]string{
			{"8cd0f7f83bb9cccd1857a86546f9db175ca2050e213e6d0e048dcd5443e192f0", "3a09f3e67dad5eb487af4c91803cf300b2868dd6a3a7aabefa711dd8109eb9f6"},
			{"8cd0f7f83bb9cccd1857a86546f9db175ca2050e213e6d0e048dcd5443e192f0", "3a09f3e67dad5eb487af4c91803cf300b2868dd6a3a7aabefa711dd8109eb9f6"},
		}},
		{"existing fresh representation", []Attachment{{ID: ""}, {ID: second}, {ID: "invalid"}, {ID: first}}, [][2]string{
			{"49838544faaccd5b0d7547785aa532c2f2ee649a5151cfaac90a3bfd3604d003", "d4dcb6ef5dc1f9af55605363968522e402e7982aee354e9ee9616780be8e125f"},
			{"49aad161c1a42630ee9afc25d64e3bc4116ad1e1dfbaddcca753e257b380f923", "aec30def2461b2696ceef1316010243e402f0637ff7c7715e08687540c79d365"},
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			in := summaryProvenanceInput("17")
			in.Attachments = test.attachments
			original := append([]*schema.AgenticMessage{schema.SystemAgenticMessage("policy fixture")}, inputMessages([]Input{in})...)
			for round, expected := range test.hashes {
				if round == 2 {
					original = append(original, inputMessages([]Input{summaryProvenanceInput("18")})...)
				}
				compacted, err := finalizeSummary(context.Background(), original, schema.UserAgenticMessage("source fixture"))
				if err != nil {
					t.Fatal(err)
				}
				for i, streaming := range []bool{false, true} {
					request, err := agenticRequest(compacted, &model.Options{}, streaming)
					if err != nil {
						t.Fatal(err)
					}
					if got := modelRequestHash(request); got != expected[i] {
						t.Fatalf("compaction %d (stream=%t) changed the v0.3.5 request hash: got %s, want %s", round, streaming, got, expected[i])
					}
				}
				original = compacted
			}
		})
	}
}

func TestSummaryProvenanceKeepsAttachmentIDsAcrossFreshAndRepeatedCompaction(t *testing.T) {
	const first = "00000000-0000-4000-8000-000000000001"
	const second = "00000000-0000-4000-8000-000000000002"
	withFiles, withoutFiles := summaryProvenanceInput("17"), summaryProvenanceInput("18")
	withFiles.Attachments = []Attachment{{ID: first}, {ID: second}}
	inputs := []Input{withFiles, withoutFiles}
	original := inputMessages(inputs)
	for round := 0; round < 3; round++ {
		compacted, err := finalizeV034Summary(context.Background(), original, schema.UserAgenticMessage("source fixture"))
		if err != nil {
			t.Fatal(err)
		}
		requireSummaryProvenance(t, compacted, inputs, [][]string{{first, second}, nil})
		// Only the last, attachment-free input is retained verbatim. Older file
		// IDs must come from the summary's provenance after the first round.
		original = compacted
	}
}

func TestSummaryProvenanceRemovesEmptyPlaceholdersAndKeepsValidAttachmentIDs(t *testing.T) {
	const first = "00000000-0000-4000-8000-000000000001"
	const second = "00000000-0000-4000-8000-000000000002"
	inputs := []Input{summaryProvenanceInput("11"), summaryProvenanceInput("12"), summaryProvenanceInput("13"), summaryProvenanceInput("17")}
	previous := []map[string]any{}
	for i, in := range inputs[:3] {
		source := map[string]any{"eventId": in.EventID, "workspaceId": in.WorkspaceID,
			"conversationId": in.ConversationID, "messageId": in.MessageID, "replyToId": in.ReplyToID,
			"senderId": in.SenderID, "senderKind": in.SenderKind}
		switch i {
		case 0:
			source["attachmentIds"] = []string{}
		case 1:
			source["attachmentIds"] = nil
		case 2:
			source["attachmentIds"] = []any{"", nil, first, "invalid", 17, second}
		}
		previous = append(previous, source)
	}
	provenance, err := json.Marshal(previous)
	if err != nil {
		t.Fatal(err)
	}
	inputs[3].Attachments = []Attachment{{ID: ""}, {ID: second}, {ID: "invalid"}, {ID: first}}
	original := append([]*schema.AgenticMessage{schema.UserAgenticMessage("TEAMIE_SESSION_SUMMARY previous\nTEAMIE_SOURCE_REFERENCES " + string(provenance))}, inputMessages(inputs[3:])...)
	for round := 0; round < 3; round++ {
		compacted, err := finalizeV034Summary(context.Background(), original, schema.UserAgenticMessage("source fixture"))
		if err != nil {
			t.Fatal(err)
		}
		requireSummaryProvenance(t, compacted, inputs, [][]string{nil, nil, {first, second}, {second, first}})
		original = compacted
	}
}
