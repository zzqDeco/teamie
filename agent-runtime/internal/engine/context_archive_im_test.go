package engine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/adk/middlewares/reduction"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
	"github.com/google/uuid"
)

func contextRealIMNode(t *testing.T, f *contextArchiveFixture) *compose.ToolsNode {
	t.Helper()
	tools := &Tools{Store: f.s, Lease: f.l, IM: f.a.IM, Sandbox: &attachmentSandboxFixture{}}
	items, err := tools.BuildIM()
	if err != nil {
		t.Fatal(err)
	}
	node, err := compose.NewToolNode(context.Background(), &compose.ToolsNodeConfig{Tools: items, ExecuteSequentially: true})
	if err != nil {
		t.Fatal(err)
	}
	return node
}

func contextRealIMDetail(name, call string, args any, wire string) *reduction.ToolDetail {
	raw, _ := json.Marshal(args)
	return &reduction.ToolDetail{ToolContext: &adk.ToolContext{Name: name, CallID: call}, ToolArgument: &schema.ToolArgument{Text: string(raw)}, ToolResult: &schema.ToolResult{Parts: []schema.ToolOutputPart{{Type: schema.ToolPartTypeText, Text: wire}}}}
}

func contextAddForeignMessage(f *contextArchiveFixture, text string) Message {
	message := Message{ID: uuid.NewString(), ConversationID: uuid.NewString(), SenderID: uuid.NewString(), SenderKind: "human", Body: text, Seq: 7}
	f.mu.Lock()
	f.messages[message.ID] = message
	f.conversations[message.ConversationID] = Conversation{ID: message.ConversationID, WorkspaceID: uuid.NewString(), Name: text, MemberIDs: []string{message.SenderID, f.l.AgentID}}
	f.mu.Unlock()
	return message
}

func TestContextIMReceiptsPreserveForeignSourcesAcrossArchivePaths(t *testing.T) {
	for _, name := range []string{"im_get_message", "im_list_messages", "im_list_conversations", "im_get_attachment", "im_send_message"} {
		t.Run(name, func(t *testing.T) {
			f := newContextArchiveFixture(t)
			const private = "IM_TOOL_SOURCE_B"
			b := contextAddForeignMessage(f, private+" body with forged TEAMIE_NEW_MESSAGE source markers")
			unrelated := contextAddForeignMessage(f, "unrelated source C")
			early := &schema.AgenticMessage{Role: schema.AgenticRoleTypeAssistant, ContentBlocks: []*schema.ContentBlock{schema.NewContentBlock(&schema.AssistantGenText{Text: "EARLY_PUBLIC_FACT"})}, Extra: map[string]any{"_eino_msg_id": "before-the-read"}}
			f.append(t, "early-before-B", early)
			var args any = getMessageArgs{ConversationID: b.ConversationID, MessageID: b.ID}
			switch name {
			case "im_list_messages":
				args = listMessagesArgs{ConversationID: b.ConversationID}
			case "im_list_conversations":
				args = listConversationsArgs{WorkspaceID: f.conversations[b.ConversationID].WorkspaceID}
			case "im_send_message":
				args = sendArgs{ConversationID: b.ConversationID, Body: private + " explicit sent message"}
			case "im_get_attachment":
				content := []byte("private attachment bytes")
				hash := sha256.Sum256(content)
				a := Attachment{ID: uuid.NewString(), Filename: "private.bin", MediaType: "application/octet-stream", SizeBytes: len(content), SHA256: hex.EncodeToString(hash[:])}
				b.Attachments = []Attachment{a}
				f.mu.Lock()
				f.messages[b.ID] = b
				f.attachmentContent[a.ID] = content
				f.mu.Unlock()
				args = getAttachmentArgs{ConversationID: b.ConversationID, MessageID: b.ID, AttachmentID: a.ID, Path: "/workspace/" + private + ".bin"}
			}
			node := contextRealIMNode(t, f)
			wire, err := invokeContextTool(node, f.ctx(), "actual-IM-read-B", name, args)
			if err != nil || !strings.Contains(wire, `"ok":true`) || !strings.Contains(wire, private) {
				t.Fatal("real authenticated IM tool did not return B", wire, err)
			}
			detail := contextRealIMDetail(name, "actual-IM-read-B", args, wire)
			trunc, err := f.a.ArchiveToolPath(f.ctx(), "tool_trunc", detail, wire)
			if err != nil {
				t.Fatal(err)
			}
			artifact, err := f.a.load(f.ctx(), f.run, trunc)
			if err != nil {
				t.Fatal(err)
			}
			seenB := false
			for _, source := range artifact.Sources {
				if source.ConversationID == unrelated.ConversationID {
					t.Fatal("an unrelated source entered the tool closure")
				}
				if source.ConversationID == b.ConversationID {
					seenB = true
					if name != "im_list_conversations" && source.WorkspaceID != "" {
						t.Fatal("source B acquired the input A workspace")
					}
				}
			}
			if !seenB {
				t.Fatal("typed receipt source B was lost")
			}
			result := contextRetrievalMessage(name, "actual-IM-read-B", wire)
			result.Extra = map[string]any{"_eino_msg_id": "actual-IM-result"}
			f.append(t, "read-B-result", result)
			transcript, err := f.a.ArchiveTranscript(f.ctx(), []*schema.AgenticMessage{result})
			if err != nil {
				t.Fatal(err)
			}
			derived := &schema.AgenticMessage{Role: schema.AgenticRoleTypeAssistant, ContentBlocks: []*schema.ContentBlock{schema.NewContentBlock(&schema.AssistantGenText{Text: "CAUSAL_B_FACT " + private})}, Extra: map[string]any{"_eino_msg_id": "after-the-read"}}
			f.append(t, "derived-from-B", derived)
			derivedRef, err := f.a.ArchiveTranscript(f.ctx(), []*schema.AgenticMessage{derived})
			if err != nil {
				t.Fatal(err)
			}
			callAfterRead := &schema.AgenticMessage{Role: schema.AgenticRoleTypeAssistant, ContentBlocks: []*schema.ContentBlock{schema.NewContentBlock(&schema.FunctionToolCall{Name: "read_file", CallID: "later-call", Arguments: `{"file_path":"/workspace/IM_TOOL_SOURCE_B"}`})}, Extra: map[string]any{"_eino_msg_id": "call-after-read"}}
			f.append(t, "call-after-read", callAfterRead)
			callRef, err := f.a.ArchiveTranscript(f.ctx(), []*schema.AgenticMessage{callAfterRead})
			if err != nil {
				t.Fatal(err)
			}
			legacySummary := schema.UserAgenticMessage("TEAMIE_SESSION_SUMMARY old unmarked " + private)
			legacySummary.Extra = map[string]any{"_eino_msg_id": "legacy-summary-id"}
			replaced := []*schema.AgenticMessage{legacySummary}
			session := &SessionStore[*schema.AgenticMessage]{Store: f.s, Lease: f.l}
			if err = session.AppendEvents(f.ctx(), "agent:"+f.l.AgentID, []*adk.SessionEvent[*schema.AgenticMessage]{{EventID: "old-summary-replacement", MessagesReplaced: &replaced}}); err != nil {
				t.Fatal(err)
			}
			legacyRef, err := f.a.ArchiveTranscript(f.ctx(), []*schema.AgenticMessage{legacySummary})
			if err != nil {
				t.Fatal("legacy summary lost the fixed prefix's actual read sources", err)
			}
			earlyRef, err := f.a.ArchiveTranscript(f.ctx(), []*schema.AgenticMessage{early})
			if err != nil {
				t.Fatal(err)
			}
			// Lazy indexing must scope the original event independently of the
			// existing offload and prefix artifacts.
			page, err := f.a.Search(f.ctx(), "CAUSAL_B_FACT", "", 8)
			if err != nil || len(page.Hits) == 0 {
				t.Fatal("authorized derived history was not searchable", err)
			}
			lazy := ""
			for _, hit := range page.Hits {
				item, err := f.a.load(f.ctx(), f.run, hit.Ref)
				if err != nil {
					t.Fatal(err)
				}
				if item.EventSeq > 0 {
					lazy = hit.Ref
				}
			}
			if lazy == "" {
				t.Fatal("fixture did not exercise the original native event path")
			}
			if err = f.s.FinishRun(f.ctx(), f.l, f.run, "completed", "", false); err != nil {
				t.Fatal(err)
			}
			f.run = f.newRun(t, "2")
			f.input = f.run.Inputs[0]
			cleared, err := f.a.ArchiveToolPath(f.ctx(), "tool_clear", detail, wire)
			if err != nil {
				t.Fatal("historical clear lost the actual IM receipt", err)
			}
			reads := contextToolNode(t, f.a)
			readArgs := map[string]any{"ref": trunc}
			before, err := invokeContextTool(reads, f.ctx(), "cached-B-archive", "context_read", readArgs)
			if err != nil {
				t.Fatal(err)
			}
			f.mu.Lock()
			f.deniedConversations[unrelated.ConversationID] = true
			f.mu.Unlock()
			if _, err = f.a.Read(f.ctx(), trunc, 0, 0); err != nil {
				t.Fatal("unrelated C revocation blocked B", err)
			}
			f.mu.Lock()
			f.deniedConversations[b.ConversationID] = true
			f.mu.Unlock()
			for _, ref := range []string{trunc, transcript, derivedRef, callRef, legacyRef, lazy, cleared} {
				if _, err = f.a.Read(f.ctx(), ref, 0, 0); !errors.Is(err, errContextUnavailable) {
					t.Fatal("B revocation did not deny an archive path", ref, err)
				}
			}
			if _, err = f.a.Read(f.ctx(), earlyRef, 0, 0); err != nil {
				t.Fatal("later B receipt changed the earlier native event's causal scope", err)
			}
			if _, err = invokeContextTool(reads, f.ctx(), "cached-B-archive", "context_read", readArgs); err == nil {
				t.Fatal("cached archive read ignored B revocation")
			}
			var saved []byte
			if err = f.s.DB.QueryRow(f.ctx(), `SELECT result FROM runtime_tool_calls WHERE run_id=$1 AND call_id='cached-B-archive'`, f.run.ID).Scan(&saved); err != nil {
				t.Fatal(err)
			}
			var text string
			if json.Unmarshal(saved, &text) != nil || text != before {
				t.Fatal("revocation changed a successful receipt")
			}
			page, err = f.a.Search(f.ctx(), private, "", 8)
			if err != nil || len(page.Hits) != 0 {
				t.Fatal("raw historical tool or generated text search leaked revoked B", err)
			}
		})
	}
}

func TestContextIMEmptyHistoryAndMissingAttachmentProofFailClosed(t *testing.T) {
	f := newContextArchiveFixture(t)
	b := contextAddForeignMessage(f, "empty conversation")
	f.mu.Lock()
	delete(f.messages, b.ID)
	f.mu.Unlock()
	args := listMessagesArgs{ConversationID: b.ConversationID}
	wire, err := invokeContextTool(contextRealIMNode(t, f), f.ctx(), "empty-B", "im_list_messages", args)
	if err != nil {
		t.Fatal(err)
	}
	ref, err := f.a.ArchiveToolPath(f.ctx(), "tool_trunc", contextRealIMDetail("im_list_messages", "empty-B", args, wire), wire)
	if err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	f.deniedConversations[b.ConversationID] = true
	f.mu.Unlock()
	if _, err = f.a.Read(f.ctx(), ref, 0, 0); !errors.Is(err, errContextUnavailable) {
		t.Fatal("empty history cursor metadata lost its actual conversation scope", err)
	}
	// Legacy attachment receipts contain only path/size/hash. Without the
	// server's argument binding they cannot silently inherit source A.
	attachmentArgs := getAttachmentArgs{ConversationID: b.ConversationID, MessageID: b.ID, AttachmentID: uuid.NewString(), Path: "/workspace/old.bin"}
	payload, _ := json.Marshal(attachmentArgs)
	if _, err = f.s.ToolIntent(f.ctx(), f.l, f.run, "old-attachment", "im_get_attachment", payload); err != nil {
		t.Fatal(err)
	}
	oldWire := `{"ok":true,"data":{"path":"/workspace/old.bin","bytes":7,"sha256":"old"}}`
	if err = f.s.ToolResult(f.ctx(), f.l, f.run, "old-attachment", []byte(oldWire)); err != nil {
		t.Fatal(err)
	}
	if _, err = f.a.ArchiveToolPath(f.ctx(), "tool_clear", contextRealIMDetail("im_get_attachment", "old-attachment", attachmentArgs, oldWire), oldWire); !errors.Is(err, errContextUnavailable) {
		t.Fatal("unproven legacy attachment became readable under input A", err)
	}
}

func TestContextSourceBatchLimitDoesNotLimitParentDAG(t *testing.T) {
	f := newContextArchiveFixture(t)
	sources := []InputSource{}
	for range 129 {
		message := Message{ID: uuid.NewString(), ConversationID: f.input.ConversationID, SenderID: f.input.SenderID}
		f.mu.Lock()
		f.messages[message.ID] = message
		f.mu.Unlock()
		sources = append(sources, contextMessageSource(message))
	}
	if _, err := f.a.PathWithSources(f.ctx(), "message", "overflow", "private", sources); !errors.Is(err, errContextSourceOverflow) {
		t.Fatal("one oversized source batch was silently truncated", err)
	}
	duplicated := make([]InputSource, 300)
	for i := range duplicated {
		duplicated[i] = sources[0]
	}
	if refs, err := boundedContextSources(duplicated); err != nil || len(refs) != 1 {
		t.Fatal("source count was bounded before deduplication", err)
	}
	parent := ""
	for i, source := range sources {
		parents := []string{}
		if parent != "" {
			parents = append(parents, parent)
		}
		var err error
		parent, err = f.a.put(f.ctx(), f.run, contextArtifact{RunID: f.run.ID, Attempt: f.run.Attempt, Key: fmt.Sprintf("layer/%d", i), Kind: "transcript", Text: "private facts", Sources: []InputSource{source}, ArtifactRefs: parents})
		if err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.a.Read(f.ctx(), parent, 0, 0); err != nil {
		t.Fatal("valid long-lived source DAG was limited to 128 total ancestors", err)
	}
}

func TestContextDerivedRetrievalSearchAndSourceIndexPreserveParentACL(t *testing.T) {
	f := newContextArchiveFixture(t)
	b := f.input.ConversationID
	original, err := f.a.Path(f.ctx(), "tool_trunc", "original-B", "private historical B data")
	if err != nil {
		t.Fatal(err)
	}
	if err = f.s.FinishRun(f.ctx(), f.l, f.run, "completed", "", false); err != nil {
		t.Fatal(err)
	}
	f.input.ConversationID = uuid.NewString()
	f.run = f.newRun(t, "2")
	f.input = f.run.Inputs[0]
	wire, err := invokeContextTool(contextToolNode(t, f.a), f.ctx(), "read-old-B", "context_read", map[string]any{"ref": original})
	if err != nil {
		t.Fatal(err)
	}
	f.append(t, "retrieval-B", contextRetrievalMessage("context_read", "read-old-B", wire))
	derived := &schema.AgenticMessage{Role: schema.AgenticRoleTypeAssistant, ContentBlocks: []*schema.ContentBlock{schema.NewContentBlock(&schema.AssistantGenText{Text: "RETRIEVAL_CAUSAL_PRIVATE_FACT"})}, Extra: map[string]any{"_eino_msg_id": "derived-from-retrieval-B"}}
	f.append(t, "derived-retrieval-B", derived)
	first, err := f.a.ArchiveTranscript(f.ctx(), []*schema.AgenticMessage{derived})
	if err != nil {
		t.Fatal(err)
	}
	second, err := f.a.ArchiveTranscript(f.ctx(), []*schema.AgenticMessage{derived})
	if err != nil || first != second {
		t.Fatal("source-index replay changed or lost the retrieval parent", err)
	}
	page, err := f.a.Search(f.ctx(), "RETRIEVAL_CAUSAL", "", 8)
	if err != nil || len(page.Hits) == 0 {
		t.Fatal(err)
	}
	f.mu.Lock()
	f.deniedConversations[b] = true
	f.mu.Unlock()
	if _, err = f.a.Read(f.ctx(), second, 0, 0); !errors.Is(err, errContextUnavailable) {
		t.Fatal("cached source index lost the retrieval parent ACL", err)
	}
	page, err = f.a.Search(f.ctx(), "RETRIEVAL_CAUSAL", "", 8)
	if err != nil || len(page.Hits) != 0 {
		t.Fatal("search excerpt bypassed the generated event's retrieval parent ACL", err)
	}
}

func TestContextIMCompatiblePartialSourceEvidenceMerges(t *testing.T) {
	for _, pair := range []string{"conversation-and-empty-history", "message-and-selected-attachment"} {
		t.Run(pair, func(t *testing.T) {
			f := newContextArchiveFixture(t)
			b := contextAddForeignMessage(f, "partial private source")
			node := contextRealIMNode(t, f)
			messages := []*schema.AgenticMessage{}
			call := func(name, id string, args any) {
				t.Helper()
				wire, err := invokeContextTool(node, f.ctx(), id, name, args)
				if err != nil || !strings.Contains(wire, `"ok":true`) {
					t.Fatal("real IM call failed", wire, err)
				}
				messages = append(messages, contextRetrievalMessage(name, id, wire))
			}
			if pair == "conversation-and-empty-history" {
				call("im_list_conversations", "metadata-B", listConversationsArgs{WorkspaceID: f.conversations[b.ConversationID].WorkspaceID})
				f.mu.Lock()
				delete(f.messages, b.ID)
				f.mu.Unlock()
				call("im_list_messages", "empty-page-B", listMessagesArgs{ConversationID: b.ConversationID})
			} else {
				content := []byte("selected B attachment")
				hash := sha256.Sum256(content)
				first := Attachment{ID: uuid.NewString(), Filename: "first.bin", SizeBytes: len(content), SHA256: hex.EncodeToString(hash[:])}
				second := Attachment{ID: uuid.NewString(), Filename: "second.bin", SizeBytes: 1, SHA256: strings.Repeat("a", 64)}
				b.Attachments = []Attachment{second, first}
				f.mu.Lock()
				f.messages[b.ID] = b
				f.attachmentContent[first.ID] = content
				f.mu.Unlock()
				call("im_get_message", "message-B", getMessageArgs{ConversationID: b.ConversationID, MessageID: b.ID})
				call("im_get_attachment", "file-B", getAttachmentArgs{ConversationID: b.ConversationID, MessageID: b.ID, AttachmentID: first.ID, Path: "/workspace/selected.bin"})
			}
			ref, err := f.a.ArchiveTranscript(f.ctx(), messages)
			if err != nil {
				t.Fatal("compatible authenticated partial evidence conflicted", err)
			}
			replayed, err := f.a.ArchiveTranscript(f.ctx(), messages)
			if err != nil || replayed != ref {
				t.Fatal("merged source evidence changed on replay", err)
			}
			artifact, err := f.a.load(f.ctx(), f.run, ref)
			if err != nil || len(artifact.Sources) != 2 {
				t.Fatal("compatible evidence was not deduplicated", err)
			}
			for _, source := range artifact.Sources {
				if source.ConversationID != b.ConversationID {
					continue
				}
				if pair == "conversation-and-empty-history" && source.WorkspaceID != f.conversations[b.ConversationID].WorkspaceID || pair == "message-and-selected-attachment" && (source.SenderID != b.SenderID || len(source.AttachmentIDs) != 2) {
					t.Fatal("merged evidence dropped known source fields")
				}
			}
			if _, err = f.a.Read(f.ctx(), ref, 0, 0); err != nil {
				t.Fatal("authorized merged source was unreadable", err)
			}
			f.mu.Lock()
			f.deniedConversations[b.ConversationID] = true
			f.mu.Unlock()
			if _, err = f.a.Read(f.ctx(), ref, 0, 0); !errors.Is(err, errContextUnavailable) {
				t.Fatal("merged source lost its current ACL", err)
			}
		})
	}
	base := InputSource{ConversationID: uuid.NewString(), MessageID: uuid.NewString(), SenderID: uuid.NewString(), WorkspaceID: uuid.NewString()}
	for _, conflict := range []InputSource{{ConversationID: base.ConversationID, MessageID: base.MessageID, SenderID: uuid.NewString()}, {ConversationID: base.ConversationID, MessageID: base.MessageID, WorkspaceID: uuid.NewString()}} {
		if _, err := canonicalContextSources([]InputSource{base, conflict}); !errors.Is(err, ErrReplayConflict) {
			t.Fatal("conflicting known source identities were merged", err)
		}
	}
}
