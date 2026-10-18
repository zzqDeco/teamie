package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/adk/middlewares/reduction"
	"github.com/cloudwego/eino/schema"
	"github.com/google/uuid"
)

func contextReceipt(t *testing.T, f *contextArchiveFixture, call, argument, result string) *reduction.ToolDetail {
	t.Helper()
	if _, err := f.s.ToolIntent(f.ctx(), f.l, f.run, call, "native:read_file", []byte(argument)); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(result)
	if err := f.s.ToolResult(f.ctx(), f.l, f.run, call, raw); err != nil {
		t.Fatal(err)
	}
	return &reduction.ToolDetail{ToolContext: &adk.ToolContext{Name: "read_file", CallID: call}, ToolArgument: &schema.ToolArgument{Text: argument}, ToolResult: &schema.ToolResult{Parts: []schema.ToolOutputPart{{Type: schema.ToolPartTypeText, Text: result}}}}
}

func TestContextToolAndTranscriptSourcesExcludeUnrelatedRevokedConversation(t *testing.T) {
	f := newContextArchiveFixture(t)
	oldConversation := f.input.ConversationID
	oldDetail := contextReceipt(t, f, "reused-provider-id", `{"file_path":"/workspace/old"}`, "old private result")
	f.append(t, "unrelated-old", schema.UserAgenticMessage("unrelated old conversation"))
	if err := f.s.FinishRun(f.ctx(), f.l, f.run, "completed", "", false); err != nil {
		t.Fatal(err)
	}
	f.input.ConversationID, f.input.WorkspaceID = uuid.NewString(), uuid.NewString()
	f.run = f.newRun(t, "2")
	f.input = f.run.Inputs[0]
	newDetail := contextReceipt(t, f, "reused-provider-id", `{"file_path":"/workspace/old"}`, "new task result")
	ref, err := f.a.ArchiveToolPath(f.ctx(), "tool_trunc", newDetail, "new task result")
	if err != nil {
		t.Fatal(err)
	}
	artifact, err := f.a.load(f.ctx(), f.run, ref)
	if err != nil || len(artifact.Sources) != 1 || artifact.Sources[0].ConversationID != f.input.ConversationID {
		t.Fatal("new tool acquired unrelated old conversation sources", err)
	}
	f.mu.Lock()
	f.deniedConversations[oldConversation] = true
	f.mu.Unlock()
	if _, err = f.a.Read(f.ctx(), ref, 0, 0); err != nil {
		t.Fatal("unrelated old revocation blocked a new tool result", err)
	}
	oldRef, err := f.a.ArchiveToolPath(f.ctx(), "tool_clear", oldDetail, "old private result")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.a.Read(f.ctx(), oldRef, 0, 0); !errors.Is(err, errContextUnavailable) {
		t.Fatal("old tool result escaped its actual source revocation", err)
	}
	cleared := &schema.AgenticMessage{Role: schema.AgenticRoleTypeUser, ContentBlocks: []*schema.ContentBlock{schema.NewContentBlock(&schema.FunctionToolResult{Name: "read_file", CallID: "reused-provider-id", Content: []*schema.FunctionToolResultContentBlock{{Type: schema.FunctionToolResultContentBlockTypeText, Text: &schema.UserInputText{Text: "Archived private context: " + oldRef + "\nUse context_read."}}}})}, Extra: map[string]any{"_eino_msg_id": "old-result-id"}}
	session := &SessionStore[*schema.AgenticMessage]{Store: f.s, Lease: f.l}
	if err = session.AppendEvents(f.ctx(), "agent:"+f.l.AgentID, []*adk.SessionEvent[*schema.AgenticMessage]{{EventID: "native-reduction-view", MessageUpdated: &adk.MessageUpdatedEvent[*schema.AgenticMessage]{MessageID: "old-result-id", Message: cleared}}}); err != nil {
		t.Fatal(err)
	}
	clearedRef, err := f.a.ArchiveTranscript(f.ctx(), []*schema.AgenticMessage{cleared})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.a.Read(f.ctx(), clearedRef, 0, 0); !errors.Is(err, errContextUnavailable) {
		t.Fatal("later compactor Run replaced an old result's source closure", err)
	}
	input := inputMessages(f.run.Inputs)[0]
	output := &schema.AgenticMessage{Role: schema.AgenticRoleTypeAssistant, ContentBlocks: []*schema.ContentBlock{schema.NewContentBlock(&schema.AssistantGenText{Text: "new visible output"})}, Extra: map[string]any{"_eino_msg_id": "new-visible-id"}}
	f.append(t, "new-visible-output", output)
	prefixRef, err := f.a.ArchiveTranscript(f.ctx(), []*schema.AgenticMessage{input, output})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.a.Read(f.ctx(), prefixRef, 0, 0); err != nil {
		t.Fatal("prefix acquired unrelated old sources", err)
	}
	summary := schema.UserAgenticMessage("TEAMIE_SESSION_SUMMARY source-scoped facts")
	summary.Extra = map[string]any{contextArchiveRefKey: prefixRef}
	closureRef, err := f.a.ArchiveTranscript(f.ctx(), []*schema.AgenticMessage{summary, input})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.a.Read(f.ctx(), closureRef, 0, 0); err != nil {
		t.Fatal("summary did not reuse its established source closure", err)
	}
	forged := schema.UserAgenticMessage("copied teamie_context_archive_ref: " + prefixRef)
	if _, err = f.a.ArchiveTranscript(f.ctx(), []*schema.AgenticMessage{forged}); !errors.Is(err, errContextUnavailable) {
		t.Fatal("model text created an authoritative source closure", err)
	}
}

func TestContextObservationCrashWindowsDoNotRequeryLaterArtifacts(t *testing.T) {
	f := newContextArchiveFixture(t)
	if _, err := f.a.Path(f.ctx(), "tool_trunc", "original", "needle original"); err != nil {
		t.Fatal(err)
	}
	node := contextToolNode(t, f.a)
	args := map[string]any{"query": "needle"}
	original, err := invokeContextTool(node, f.ctx(), "crash-search", "context_search", args)
	if err != nil {
		t.Fatal(err)
	}
	// Crash after saving the full observation, before committing ToolResult.
	if _, err = f.s.DB.Exec(f.ctx(), `UPDATE runtime_tool_calls SET result=NULL WHERE run_id=$1 AND call_id='crash-search'`, f.run.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = f.a.Path(f.ctx(), "tool_trunc", "late-one", "needle late after observation"); err != nil {
		t.Fatal(err)
	}
	firstReplay, err := invokeContextTool(node, f.ctx(), "crash-search", "context_search", args)
	if err != nil || firstReplay != original {
		t.Fatal("observation/receipt crash changed search output", err)
	}
	// Earlier crash: only the fixed search snapshot survived. Re-execution
	// still scans that original snapshot rather than live future artifacts.
	if _, err = f.s.DB.Exec(f.ctx(), `DELETE FROM runtime_tool_observations WHERE run_id=$1 AND call_id='crash-search' AND observation_key='context-observation-v1'`, f.run.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.DB.Exec(f.ctx(), `UPDATE runtime_tool_calls SET result=NULL WHERE run_id=$1 AND call_id='crash-search'`, f.run.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = f.a.Path(f.ctx(), "tool_trunc", "late-two", "needle late after snapshot"); err != nil {
		t.Fatal(err)
	}
	secondReplay, err := invokeContextTool(node, f.ctx(), "crash-search", "context_search", args)
	if err != nil || secondReplay != original {
		t.Fatal("snapshot/observation crash changed search output", err)
	}
}

func TestContextConcurrentImmutableWritersCannotOverwrite(t *testing.T) {
	f := newContextArchiveFixture(t)
	errorsOut := make(chan error, 2)
	start := make(chan struct{})
	for _, content := range []string{"first bytes", "other bytes"} {
		go func(content string) {
			<-start
			_, err := f.a.Path(f.ctx(), "message", "same-logical-key", content)
			errorsOut <- err
		}(content)
	}
	close(start)
	success, conflict := 0, 0
	for range 2 {
		err := <-errorsOut
		if err == nil {
			success++
		} else if errors.Is(err, ErrReplayConflict) {
			conflict++
		} else {
			t.Fatal(err)
		}
	}
	if success != 1 || conflict != 1 || count(t, f.s, `SELECT count(*) FROM runtime_context_artifacts`) != 1 {
		t.Fatal("concurrent writes did not preserve one immutable value")
	}
}

func TestContextRepeatedCompactionKeepsLinearSourceEdgesAndRechecksAncestors(t *testing.T) {
	f := newContextArchiveFixture(t)
	firstConversation := f.input.ConversationID
	previous := ""
	const rounds = 6
	for round := 0; round < rounds; round++ {
		messages := []*schema.AgenticMessage{}
		if previous != "" {
			summary := schema.UserAgenticMessage(fmt.Sprintf("source summary %d", round))
			summary.Extra = map[string]any{contextArchiveRefKey: previous}
			messages = append(messages, summary)
		}
		messages = append(messages, inputMessages(f.run.Inputs)...)
		var err error
		previous, err = f.a.ArchiveTranscript(f.ctx(), messages)
		if err != nil {
			t.Fatal(err)
		}
		artifact, err := f.a.load(f.ctx(), f.run, previous)
		if err != nil || len(artifact.Sources) != 1 || round > 0 && len(artifact.ArtifactRefs) != 1 {
			t.Fatal("compaction flattened a previous source closure", err)
		}
		if round+1 < rounds {
			if err = f.s.FinishRun(f.ctx(), f.l, f.run, "completed", "", false); err != nil {
				t.Fatal(err)
			}
			f.input.ConversationID = uuid.NewString()
			f.run = f.newRun(t, fmt.Sprint(round+2))
			f.input = f.run.Inputs[0]
		}
	}
	var sources, edges int
	if err := f.s.DB.QueryRow(f.ctx(), `SELECT sum(jsonb_array_length(sources)),sum(jsonb_array_length(source_artifact_refs)) FROM runtime_context_artifacts WHERE kind='transcript'`).Scan(&sources, &edges); err != nil || sources != rounds || edges != rounds-1 {
		t.Fatal("source metadata grew with the complete lifetime prefix", err)
	}
	node := contextToolNode(t, f.a)
	if _, err := invokeContextTool(node, f.ctx(), "deep-read", "context_read", map[string]any{"ref": previous}); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	f.deniedConversations[firstConversation] = true
	f.mu.Unlock()
	if _, err := invokeContextTool(node, f.ctx(), "deep-read", "context_read", map[string]any{"ref": previous}); err == nil {
		t.Fatal("cached deep context read bypassed an ancestor source revocation")
	}
}

func contextRetrievalMessage(name, call, wire string) *schema.AgenticMessage {
	return &schema.AgenticMessage{Role: schema.AgenticRoleTypeUser, ContentBlocks: []*schema.ContentBlock{schema.NewContentBlock(&schema.FunctionToolResult{Name: name, CallID: call, Content: []*schema.FunctionToolResultContentBlock{{Type: schema.FunctionToolResultContentBlockTypeText, Text: &schema.UserInputText{Text: wire}}}})}}
}

func TestEmptyContextSearchCanBeArchivedWithItsActualRequestSource(t *testing.T) {
	for _, historical := range []bool{false, true} {
		t.Run(fmt.Sprintf("historical=%t", historical), func(t *testing.T) {
			f := newContextArchiveFixture(t)
			origin := f.input.ConversationID
			wire, err := invokeContextTool(contextToolNode(t, f.a), f.ctx(), "empty-search", "context_search", map[string]any{"query": "no-matching-private-artifact"})
			if err != nil || !strings.Contains(wire, `"hits":[]`) {
				t.Fatal("fixture search is not an authenticated empty result", wire, err)
			}
			if historical {
				if err = f.s.FinishRun(f.ctx(), f.l, f.run, "completed", "", false); err != nil {
					t.Fatal(err)
				}
				f.input.ConversationID = uuid.NewString()
				f.run = f.newRun(t, "2")
				f.input = f.run.Inputs[0]
			}
			result := contextRetrievalMessage("context_search", "empty-search", wire)
			ref, err := f.a.ArchiveTranscript(f.ctx(), []*schema.AgenticMessage{result})
			if err != nil {
				t.Fatal("a valid empty search stopped historical compaction", err)
			}
			artifact, err := f.a.load(f.ctx(), f.run, ref)
			if err != nil || len(artifact.Sources) != 1 || artifact.Sources[0].ConversationID != origin || len(artifact.ArtifactRefs) != 0 || artifact.Text != "[]" {
				t.Fatal("empty result acquired data or the wrong request source", artifact, err)
			}
			if _, err = f.a.ArchiveTranscript(f.ctx(), []*schema.AgenticMessage{contextRetrievalMessage("context_search", "forged-call", wire)}); !errors.Is(err, errContextUnavailable) {
				t.Fatal("uncommitted retrieval acquired trusted provenance", err)
			}
			f.mu.Lock()
			f.deniedConversations[origin] = true
			f.mu.Unlock()
			if _, err = f.a.Read(f.ctx(), ref, 0, 0); !errors.Is(err, errContextUnavailable) {
				t.Fatal("empty-result archive bypassed its original source ACL", err)
			}
		})
	}
}

func TestContextRetrievalCompactionInheritsObservationParentsAndRevocation(t *testing.T) {
	for _, name := range []string{"context_read", "context_search"} {
		for _, historical := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/historical=%t", name, historical), func(t *testing.T) {
				f := newContextArchiveFixture(t)
				sourceConversation := f.input.ConversationID
				const privateText = "needle-source-A-private-data"
				original, err := f.a.Path(f.ctx(), "tool_trunc", "source-A", privateText)
				if err != nil {
					t.Fatal(err)
				}
				if err = f.s.FinishRun(f.ctx(), f.l, f.run, "completed", "", false); err != nil {
					t.Fatal(err)
				}
				f.input.ConversationID = uuid.NewString()
				f.run = f.newRun(t, "2")
				f.input = f.run.Inputs[0]
				node := contextToolNode(t, f.a)
				args := map[string]any{"ref": original}
				if name == "context_search" {
					args = map[string]any{"query": "needle-source-A"}
				}
				wire, err := invokeContextTool(node, f.ctx(), "source-retrieval", name, args)
				if err != nil || !strings.Contains(wire, privateText) {
					t.Fatal("retrieval did not return the original private data", err)
				}
				if historical {
					if err = f.s.FinishRun(f.ctx(), f.l, f.run, "completed", "", false); err != nil {
						t.Fatal(err)
					}
					f.run = f.newRun(t, "3")
					f.input = f.run.Inputs[0]
				}
				result := contextRetrievalMessage(name, "source-retrieval", wire)
				messages := append(inputMessages(f.run.Inputs), result)
				archived, err := f.a.ArchiveTranscript(f.ctx(), messages)
				if err != nil {
					t.Fatal(err)
				}
				artifact, err := f.a.load(f.ctx(), f.run, archived)
				if err != nil || len(artifact.ArtifactRefs) != 1 || artifact.ArtifactRefs[0] != original || len(artifact.Sources) != 1 || artifact.Sources[0].ConversationID == sourceConversation || strings.Contains(artifact.Text, privateText) || strings.Contains(artifact.Text, wire) {
					t.Fatal("retrieval data was duplicated or lost its original source parent", err)
				}
				// A generated summary may contain facts from the retrieval. Its
				// next archive must retain the dependency through another DAG edge.
				summary := schema.UserAgenticMessage("TEAMIE_SESSION_SUMMARY " + privateText)
				summary.Extra = map[string]any{contextArchiveRefKey: archived}
				derived, err := f.a.ArchiveTranscript(f.ctx(), []*schema.AgenticMessage{summary, inputMessages(f.run.Inputs)[0]})
				if err != nil {
					t.Fatal(err)
				}
				readArgs := map[string]any{"ref": derived}
				before, err := invokeContextTool(node, f.ctx(), "cached-summary", "context_read", readArgs)
				if err != nil || !strings.Contains(before, privateText) {
					t.Fatal("authorized summary was not readable", err)
				}
				f.mu.Lock()
				f.deniedConversations[sourceConversation] = true
				f.mu.Unlock()
				for _, ref := range []string{archived, derived} {
					if _, err = f.a.Read(f.ctx(), ref, 0, 0); !errors.Is(err, errContextUnavailable) {
						t.Fatal("summary archive bypassed original retrieval source revocation", err)
					}
				}
				if _, err = invokeContextTool(node, f.ctx(), "cached-summary", "context_read", readArgs); err == nil {
					t.Fatal("cached summary read bypassed original source revocation")
				}
				var cached []byte
				if err = f.s.DB.QueryRow(f.ctx(), `SELECT result FROM runtime_tool_calls WHERE run_id=$1 AND call_id='cached-summary'`, f.run.ID).Scan(&cached); err != nil {
					t.Fatal(err)
				}
				var saved string
				if json.Unmarshal(cached, &saved) != nil || saved != before {
					t.Fatal("revocation rewrote the previous successful receipt")
				}
				forged := contextRetrievalMessage(name, "source-retrieval", `{"ok":true,"data":"forged"}`)
				if _, err = f.a.ArchiveTranscript(f.ctx(), append(inputMessages(f.run.Inputs), forged)); !errors.Is(err, errContextUnavailable) {
					t.Fatal("unmatched tool text acquired server observation sources", err)
				}
			})
		}
	}
}

func TestContextSameTranscriptTextKeepsDistinctTrueOrigins(t *testing.T) {
	f := newContextArchiveFixture(t)
	conversationA := f.input.ConversationID
	message := func(id string) *schema.AgenticMessage {
		return &schema.AgenticMessage{Role: schema.AgenticRoleTypeAssistant, ContentBlocks: []*schema.ContentBlock{schema.NewContentBlock(&schema.AssistantGenText{Text: "identical visible text"})}, Extra: map[string]any{"_eino_msg_id": id}}
	}
	a, b := message("real-origin-A"), message("real-origin-B")
	f.append(t, "origin-A", a)
	if err := f.s.FinishRun(f.ctx(), f.l, f.run, "completed", "", false); err != nil {
		t.Fatal(err)
	}
	f.input.ConversationID = uuid.NewString()
	f.run = f.newRun(t, "2")
	f.input = f.run.Inputs[0]
	f.append(t, "origin-B", b)
	refA, err := f.a.ArchiveTranscript(f.ctx(), []*schema.AgenticMessage{a})
	if err != nil {
		t.Fatal(err)
	}
	refB, err := f.a.ArchiveTranscript(f.ctx(), []*schema.AgenticMessage{b})
	if err != nil || refA == refB {
		t.Fatal("identical text with different true sources conflicted or shared a ref", err)
	}
	summary := schema.UserAgenticMessage("identical derived summary")
	summary.Extra = map[string]any{contextArchiveRefKey: refA}
	derivedA, err := f.a.ArchiveTranscript(f.ctx(), []*schema.AgenticMessage{summary})
	if err != nil {
		t.Fatal(err)
	}
	summary.Extra[contextArchiveRefKey] = refB
	derivedB, err := f.a.ArchiveTranscript(f.ctx(), []*schema.AgenticMessage{summary})
	if err != nil || derivedA == derivedB {
		t.Fatal("identical text with different parent closures conflicted or shared a ref", err)
	}
	f.mu.Lock()
	f.deniedConversations[conversationA] = true
	f.mu.Unlock()
	for _, ref := range []string{refA, derivedA} {
		if _, err = f.a.Read(f.ctx(), ref, 0, 0); !errors.Is(err, errContextUnavailable) {
			t.Fatal("origin A archive lost its source revocation", err)
		}
	}
	for _, ref := range []string{refB, derivedB} {
		if _, err = f.a.Read(f.ctx(), ref, 0, 0); err != nil {
			t.Fatal("origin A revocation incorrectly blocked identical origin B text", err)
		}
	}
}

func TestContextArtifactFirstFailedAttemptSurvivesSuccessfulReplayAndNextRun(t *testing.T) {
	f := newContextArchiveFixture(t)
	first := f.run
	ref, err := f.a.Path(f.ctx(), "tool_trunc", "stable-artifact", "needle immutable first attempt")
	if err != nil {
		t.Fatal(err)
	}
	// This Run's acceptance snapshot predates the eventual successful replay.
	pinnedBeforeSuccess := f.newRun(t, "2")
	f.run, err = f.s.StartRunWithContextPolicy(context.Background(), f.l, []Input{f.input}, contextProfileV1, json.RawMessage(`{}`))
	if err != nil || first.Attempt == f.run.Attempt {
		t.Fatal("same-Run replay was not created", err)
	}
	ref2, err := f.a.Path(f.ctx(), "tool_trunc", "stable-artifact", "needle immutable first attempt")
	if err != nil || ref2 != ref {
		t.Fatal("replay rewrote artifact identity", err)
	}
	var originalAttempt string
	if err = f.s.DB.QueryRow(f.ctx(), `SELECT attempt_id::text FROM runtime_context_artifacts WHERE run_id=$1`, first.ID).Scan(&originalAttempt); err != nil || originalAttempt != first.Attempt {
		t.Fatal("replay overwrote first artifact metadata", err)
	}
	if err = f.s.FinishRun(f.ctx(), f.l, f.run, "completed", "", false); err != nil {
		t.Fatal(err)
	}
	if count(t, f.s, `SELECT count(*) FROM runtime_session_acceptances WHERE attempt_id=$1`, first.Attempt) != 0 {
		t.Fatal("fixture accidentally accepted the failed attempt")
	}
	// Simulate the legacy ledger's missing linkage and run the actual migration.
	if _, err = f.s.DB.Exec(context.Background(), `UPDATE runtime_session_acceptances SET run_id=NULL WHERE run_id=$1`, first.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.DB.Exec(context.Background(), schemaV3SQL); err != nil {
		t.Fatal(err)
	}
	if count(t, f.s, `SELECT count(*) FROM runtime_session_acceptances WHERE run_id=$1`, first.ID) != 1 {
		t.Fatal("legacy acceptance was not backfilled by logical Run")
	}
	f.run = pinnedBeforeSuccess
	if _, err = f.a.Read(f.ctx(), ref, 0, 0); !errors.Is(err, errContextUnavailable) {
		t.Fatal("future acceptance changed an older pinned view", err)
	}
	f.run = f.newRun(t, "3")
	if _, err = f.a.Read(f.ctx(), ref, 0, 0); err != nil {
		t.Fatal("successful replay did not make the immutable artifact readable", err)
	}
	page, err := f.a.Search(f.ctx(), "needle", "", 4)
	if err != nil || len(page.Hits) != 1 || page.Hits[0].Ref != ref {
		t.Fatal("next Run search lost a replayed artifact", err)
	}
}

func TestContextSearchCursorPinsSnapshotAndBadReferencesAreObservations(t *testing.T) {
	f := newContextArchiveFixture(t)
	for _, key := range []string{"first", "second"} {
		if _, err := f.a.Path(f.ctx(), "tool_trunc", key, "needle "+key); err != nil {
			t.Fatal(err)
		}
	}
	page, err := f.a.Search(f.ctx(), "needle", "", 1)
	if err != nil || !page.HasMore {
		t.Fatal("fixture did not produce a continuation", err)
	}
	if _, err = f.a.Path(f.ctx(), "tool_trunc", "late", "needle late-snapshot"); err != nil {
		t.Fatal(err)
	}
	second, err := f.a.Search(f.ctx(), "needle", page.Cursor, 8)
	if err != nil || len(second.Hits) != 1 || strings.Contains(second.Hits[0].Excerpt, "late-snapshot") {
		t.Fatal("cursor mixed in late artifacts", err)
	}
	cursor, _ := decodeContextCursor(page.Cursor, f.run)
	cursor.ArtifactHead++
	if _, err = f.a.Search(f.ctx(), "needle", encodeContextCursor(cursor), 8); !errors.Is(err, errContextArguments) {
		t.Fatal("cursor broadened its persisted snapshot", err)
	}
	if _, err = f.a.Search(f.ctx(), "other", page.Cursor, 8); !errors.Is(err, errContextArguments) {
		t.Fatal("cursor changed its query", err)
	}
	node := contextToolNode(t, f.a)
	missing := contextRef(uuid.NewString(), "message", "unavailable")
	bad, err := invokeContextTool(node, f.ctx(), "bad-ref", "context_read", map[string]any{"ref": missing})
	if err != nil || !strings.Contains(bad, `"error":"context_unavailable"`) || strings.Contains(bad, "needle") {
		t.Fatal("first bad ref aborted the native Run or leaked content", err)
	}
	again, err := invokeContextTool(node, f.ctx(), "bad-ref", "context_read", map[string]any{"ref": missing})
	if err != nil || again != bad {
		t.Fatal("failure observation changed on replay", err)
	}
	if excerpt := contextExcerpt(strings.Repeat("Ⱥ", 100)+"needle suffix", "needle"); !utf8.ValidString(excerpt) || !strings.Contains(excerpt, "needle") {
		t.Fatal("Unicode case-folding produced a bad excerpt")
	}
}
