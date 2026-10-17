package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/adk/filesystem"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
	"github.com/google/uuid"
)

type contextArchiveFixture struct {
	s                   *Store
	l                   *Lease
	run                 *Run
	a                   *ContextArchive
	input               Input
	mu                  sync.Mutex
	messages            map[string]Message
	deniedConversations map[string]bool
	denied              atomic.Bool
	checks              atomic.Int64
}

func archiveContextBudget(t *testing.T) json.RawMessage {
	t.Helper()
	budget, err := NewContextBudgetSnapshot(16384)
	if err != nil {
		t.Fatal(err)
	}
	return budget
}

func newContextArchiveFixture(t *testing.T) *contextArchiveFixture {
	t.Helper()
	s := testStore(t)
	if _, err := s.DB.Exec(context.Background(), contextArchiveSchemaSQL); err != nil {
		t.Fatal(err)
	}
	f := &contextArchiveFixture{s: s, messages: map[string]Message{}, deniedConversations: map[string]bool{}}
	agent := uuid.NewString()
	var err error
	f.l, err = s.Acquire(context.Background(), agent, "context-archive-test")
	if err != nil {
		t.Fatal(err)
	}
	f.input = Input{EventID: "1", WorkspaceID: uuid.NewString(), ConversationID: uuid.NewString(), MessageID: uuid.NewString(), SenderID: uuid.NewString(), SenderKind: "human", Body: "original request"}
	f.addMessage(f.input)
	if err = s.Ingest(context.Background(), f.l, "1", []Input{f.input}); err != nil {
		t.Fatal(err)
	}
	f.run, err = s.StartRunWithContextPolicy(context.Background(), f.l, []Input{f.input}, contextProfileV1, archiveContextBudget(t))
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		f.checks.Add(1)
		if f.denied.Load() {
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"error":{"code":"forbidden"}}`))
			return
		}
		parts := strings.Split(request.URL.Path, "/")
		if request.Method != http.MethodGet || len(parts) != 7 || parts[3] != "conversations" || parts[5] != "messages" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		f.mu.Lock()
		message, exists := f.messages[parts[6]]
		denied := f.deniedConversations[parts[4]]
		f.mu.Unlock()
		if denied {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		if !exists || message.ConversationID != parts[4] {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"message": message})
	}))
	t.Cleanup(server.Close)
	f.a = &ContextArchive{Store: s, Lease: f.l, IM: &IM{BaseURL: server.URL, AgentID: agent, token: "fixture-context-token", expiry: time.Now().Add(time.Hour)}}
	return f
}

func (f *contextArchiveFixture) addMessage(input Input) {
	f.mu.Lock()
	f.messages[input.MessageID] = Message{ID: input.MessageID, ConversationID: input.ConversationID, SenderID: input.SenderID, Body: input.Body}
	f.mu.Unlock()
}

func (f *contextArchiveFixture) ctx() context.Context { return WithRun(context.Background(), f.run) }

func (f *contextArchiveFixture) append(t *testing.T, id string, message *schema.AgenticMessage) {
	t.Helper()
	session := &SessionStore[*schema.AgenticMessage]{Store: f.s, Lease: f.l}
	if err := session.AppendEvents(f.ctx(), "agent:"+f.l.AgentID, []*adk.SessionEvent[*schema.AgenticMessage]{{EventID: id, Message: message}}); err != nil {
		t.Fatal(err)
	}
}

func (f *contextArchiveFixture) newRun(t *testing.T, event string) *Run {
	t.Helper()
	input := f.input
	input.EventID, input.MessageID, input.Body = event, uuid.NewString(), "next request"
	f.addMessage(input)
	if err := f.s.Ingest(context.Background(), f.l, event, []Input{input}); err != nil {
		t.Fatal(err)
	}
	run, err := f.s.StartRunWithContextPolicy(context.Background(), f.l, []Input{input}, contextProfileV1, archiveContextBudget(t))
	if err != nil {
		t.Fatal(err)
	}
	return run
}

func contextToolNode(t *testing.T, archive *ContextArchive) *compose.ToolsNode {
	t.Helper()
	tools, err := archive.Tools(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	node, err := compose.NewToolNode(context.Background(), &compose.ToolsNodeConfig{Tools: tools, ExecuteSequentially: true})
	if err != nil {
		t.Fatal(err)
	}
	return node
}

func invokeContextTool(node *compose.ToolsNode, ctx context.Context, id, name string, args any) (string, error) {
	raw, _ := json.Marshal(args)
	out, err := node.Invoke(ctx, &schema.Message{Role: schema.Assistant, ToolCalls: []schema.ToolCall{{ID: id, Type: "function", Function: schema.FunctionCall{Name: name, Arguments: string(raw)}}}})
	if err != nil {
		return "", err
	}
	if len(out) != 1 {
		return "", errors.New("unexpected context tool output count")
	}
	return out[0].Content, nil
}

func TestContextArchiveImmutableUTF8BytePagesAndWireBound(t *testing.T) {
	f := newContextArchiveFixture(t)
	text := `{"singleLine":"` + strings.Repeat("你好🙂\x00", 2200) + `"}`
	ref, err := f.a.Path(f.ctx(), "tool_trunc", "stable-read-call", text)
	if err != nil {
		t.Fatal(err)
	}
	var joined strings.Builder
	for offset := 0; ; {
		page, err := f.a.Read(f.ctx(), ref, offset, contextReadMaximum)
		if err != nil {
			t.Fatal(err)
		}
		wire, _ := json.Marshal(contextToolResponse{OK: true, Data: page})
		if len(wire) > contextResponseMaximum || !utf8.ValidString(page.Text) || page.Offset != offset || page.NextOffset != offset+len(page.Text) || len(page.Text) > contextReadMaximum {
			t.Fatal("page exceeded its byte/wire contract")
		}
		joined.WriteString(page.Text)
		if !page.HasMore {
			break
		}
		if page.NextOffset <= offset {
			t.Fatal("page did not progress")
		}
		offset = page.NextOffset
	}
	if joined.String() != text {
		t.Fatal("byte pagination lost or duplicated content")
	}
	if err = f.a.Write(f.ctx(), &filesystem.WriteRequest{FilePath: ref, Content: text}); err != nil {
		t.Fatal(err)
	}
	if _, err = f.a.Path(f.ctx(), "tool_trunc", "stable-read-call", text+"changed"); !errors.Is(err, ErrReplayConflict) {
		t.Fatalf("same key accepted changed content: %v", err)
	}
	refs, _ := f.a.runSources(f.ctx(), f.run.ID)
	refs[0].SenderKind = "agent"
	if _, err = f.a.PathWithSources(f.ctx(), "tool_trunc", "stable-read-call", text, refs); !errors.Is(err, ErrReplayConflict) {
		t.Fatalf("same key accepted changed provenance: %v", err)
	}
	if err = f.a.Write(f.ctx(), &filesystem.WriteRequest{FilePath: ref, Content: "changed"}); !errors.Is(err, ErrReplayConflict) {
		t.Fatal("backend accepted changed content", err)
	}
	if err = f.a.Write(f.ctx(), &filesystem.WriteRequest{FilePath: "/workspace/arbitrary", Content: "data"}); !errors.Is(err, errContextArguments) {
		t.Fatal("backend accepted mutable workspace path", err)
	}
	if _, err = f.a.Read(f.ctx(), ref, len(`{"singleLine":"`)+1, 10); !errors.Is(err, errContextArguments) {
		t.Fatal("accepted a byte offset inside a Unicode code point", err)
	}
}

func TestContextArchiveFailedCommitHasNoReferenceAndProjectionIgnoresEphemeralState(t *testing.T) {
	f := newContextArchiveFixture(t)
	input := inputMessages([]Input{f.input})[0]
	input.Extra = map[string]any{"_eino_msg_id": "first-random"}
	output := &schema.AgenticMessage{Role: schema.AgenticRoleTypeAssistant, ContentBlocks: []*schema.ContentBlock{schema.NewContentBlock(&schema.Reasoning{Text: "NEVER_ARCHIVE_REASONING"}), schema.NewContentBlock(&schema.AssistantGenText{Text: "visible result"})}, Extra: map[string]any{"signature": "NEVER_ARCHIVE_SIGNATURE"}}
	f.append(t, "actual-visible-output", output)
	ref, err := f.a.ArchiveTranscript(f.ctx(), []*schema.AgenticMessage{schema.SystemAgenticMessage("NEVER_ARCHIVE_PLATFORM_POLICY"), input, output})
	if err != nil {
		t.Fatal(err)
	}
	input.Extra["_eino_msg_id"] = "second-random"
	ref2, err := f.a.ArchiveTranscript(f.ctx(), []*schema.AgenticMessage{schema.SystemAgenticMessage("NEVER_ARCHIVE_PLATFORM_POLICY"), input, output})
	if err != nil || ref != ref2 {
		t.Fatal("ephemeral native IDs changed archive identity", err)
	}
	page, err := f.a.Read(f.ctx(), ref, 0, 0)
	if err != nil || !strings.Contains(page.Text, "visible result") || strings.Contains(page.Text, "NEVER_ARCHIVE") || strings.Contains(page.Text, "_eino_msg_id") {
		t.Fatal("private native metadata entered the projection", err)
	}
	if _, err = f.s.DB.Exec(f.ctx(), `CREATE FUNCTION reject_context_artifact() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'fixture storage failure'; END $$; CREATE TRIGGER reject_context_artifact BEFORE INSERT ON runtime_context_artifacts FOR EACH ROW EXECUTE FUNCTION reject_context_artifact()`); err != nil {
		t.Fatal(err)
	}
	before := count(t, f.s, `SELECT count(*) FROM runtime_context_artifacts`)
	failedRef, err := f.a.Path(f.ctx(), "message", "must-not-escape", "committed only")
	if err == nil || failedRef != "" || count(t, f.s, `SELECT count(*) FROM runtime_context_artifacts`) != before {
		t.Fatal("failed persistence returned a dangling reference")
	}
}

func TestContextToolsReplayAcrossAttemptsAndRecheckCachedACL(t *testing.T) {
	f := newContextArchiveFixture(t)
	ref, err := f.a.Path(f.ctx(), "tool_trunc", "first", "needle original")
	if err != nil {
		t.Fatal(err)
	}
	node := contextToolNode(t, f.a)
	readArgs := map[string]any{"ref": ref}
	firstRead, err := invokeContextTool(node, f.ctx(), "stable-context-read", "context_read", readArgs)
	if err != nil {
		t.Fatal(err)
	}
	searchArgs := map[string]any{"query": "needle"}
	firstSearch, err := invokeContextTool(node, f.ctx(), "stable-context-search", "context_search", searchArgs)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.a.Path(f.ctx(), "tool_trunc", "late", "needle LATE_ARTIFACT"); err != nil {
		t.Fatal(err)
	}
	f.append(t, "late-event", schema.UserAgenticMessage("needle LATE_EVENT"))
	replayedSearch, err := invokeContextTool(node, f.ctx(), "stable-context-search", "context_search", searchArgs)
	if err != nil || replayedSearch != firstSearch || strings.Contains(replayedSearch, "LATE_") {
		t.Fatal("replayed search mixed in late history", err)
	}
	oldRun := f.run
	f.run, err = f.s.StartRunWithContextPolicy(context.Background(), f.l, []Input{f.input}, contextProfileV1, archiveContextBudget(t))
	if err != nil || oldRun.ID != f.run.ID || oldRun.Attempt == f.run.Attempt {
		t.Fatal("fixture did not restart the same logical Run", err)
	}
	if _, err = f.a.Read(WithRun(context.Background(), oldRun), ref, 0, 0); !errors.Is(err, ErrLeaseLost) {
		t.Fatal("old execution attempt retained context access", err)
	}
	f.a = &ContextArchive{Store: f.s, Lease: f.l, IM: f.a.IM}
	node = contextToolNode(t, f.a)
	read, err := invokeContextTool(node, f.ctx(), "stable-context-read", "context_read", readArgs)
	if err != nil || read != firstRead {
		t.Fatal("restart changed a committed read", err)
	}
	search, err := invokeContextTool(node, f.ctx(), "stable-context-search", "context_search", searchArgs)
	if err != nil || search != firstSearch {
		t.Fatal("restart changed a committed search", err)
	}
	checks := f.checks.Load()
	f.denied.Store(true)
	if _, err = invokeContextTool(node, f.ctx(), "stable-context-read", "context_read", readArgs); err == nil {
		t.Fatal("cached read bypassed current ACL")
	}
	if _, err = invokeContextTool(node, f.ctx(), "stable-context-search", "context_search", searchArgs); err == nil {
		t.Fatal("cached search bypassed current ACL")
	}
	if f.checks.Load() < checks+2 {
		t.Fatal("cached observations did not revalidate their source refs")
	}
	if count(t, f.s, `SELECT count(*) FROM runtime_tool_calls WHERE run_id=$1`, f.run.ID) != 2 {
		t.Fatal("context replay created additional logical calls")
	}
}

func TestContextSearchLazyBackfillsCompactedHistoryButPinsLateRuns(t *testing.T) {
	f := newContextArchiveFixture(t)
	oldRun := f.run
	f.append(t, "original-body", inputMessages([]Input{f.input})[0])
	f.append(t, "old-observation", &schema.AgenticMessage{Role: schema.AgenticRoleTypeAssistant, ContentBlocks: []*schema.ContentBlock{schema.NewContentBlock(&schema.Reasoning{Text: "reasoning needle excluded"}), schema.NewContentBlock(&schema.AssistantGenText{Text: "needle OLDER_EXACT_DETAIL"})}})
	// More than one search scan, followed by compaction, proves retrieval uses
	// old journal events rather than only the current working message suffix.
	for i := 0; i < contextSearchScan+3; i++ {
		f.append(t, fmt.Sprintf("filler-%d", i), schema.UserAgenticMessage("ordinary filler"))
	}
	summary := []*schema.AgenticMessage{schema.UserAgenticMessage("small summary without the exact detail")}
	session := &SessionStore[*schema.AgenticMessage]{Store: f.s, Lease: f.l}
	if err := session.AppendEvents(f.ctx(), "agent:"+f.l.AgentID, []*adk.SessionEvent[*schema.AgenticMessage]{{EventID: "compacted", MessagesReplaced: &summary}}); err != nil {
		t.Fatal(err)
	}
	if err := f.s.FinishRun(f.ctx(), f.l, f.run, "completed", "", false); err != nil {
		t.Fatal(err)
	}
	f.run = f.newRun(t, "2")
	fixed := f.run
	// A later Run/event/acceptance exists physically but is outside fixed's view.
	f.run = f.newRun(t, "3")
	f.append(t, "future-observation", schema.UserAgenticMessage("needle FUTURE_PRIVATE_DETAIL"))
	lateRef, err := f.a.PathWithSources(f.ctx(), "tool_trunc", "future", "needle FUTURE_ARTIFACT", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.s.FinishRun(f.ctx(), f.l, f.run, "completed", "", false); err != nil {
		t.Fatal(err)
	}
	f.run = fixed
	if _, err = f.a.Read(f.ctx(), lateRef, 0, 0); !errors.Is(err, errContextUnavailable) {
		t.Fatal("fixed Run read a later accepted artifact", err)
	}
	hits := []ContextSearchHit{}
	cursor := ""
	for {
		page, err := f.a.Search(f.ctx(), "needle", cursor, 1)
		if err != nil {
			t.Fatal(err)
		}
		hits = append(hits, page.Hits...)
		if !page.HasMore {
			break
		}
		if page.Cursor == cursor {
			t.Fatal("search cursor stalled")
		}
		cursor = page.Cursor
	}
	if len(hits) != 1 || !strings.Contains(hits[0].Excerpt, "OLDER_EXACT_DETAIL") || strings.Contains(hits[0].Excerpt, "reasoning") {
		t.Fatalf("search lost old history or exposed a later/private block: %#v", hits)
	}
	page, err := f.a.Read(f.ctx(), hits[0].Ref, 0, 0)
	if err != nil || !strings.Contains(page.Text, "OLDER_EXACT_DETAIL") {
		t.Fatal("lazy backfill ref was not durable/readable", err)
	}
	if count(t, f.s, `SELECT count(*) FROM runtime_context_artifacts WHERE run_id=$1 AND source_event_seq>0`, oldRun.ID) != 1 {
		t.Fatal("old transcript was not lazily committed")
	}
}

func TestContextArchiveAgentLeaseAndSourceLineageIsolation(t *testing.T) {
	f := newContextArchiveFixture(t)
	ref, err := f.a.Path(f.ctx(), "message", "private", "private result")
	if err != nil {
		t.Fatal(err)
	}
	foreign := &Run{ID: uuid.NewString(), Attempt: uuid.NewString(), AgentID: uuid.NewString()}
	if _, err = f.a.Read(WithRun(context.Background(), foreign), ref, 0, 0); !errors.Is(err, errContextUnavailable) {
		t.Fatal("foreign Agent passed archive scope", err)
	}
	if _, err = f.s.DB.Exec(f.ctx(), `UPDATE runtime_agents SET lease_until=now()-interval '1 second' WHERE agent_id=$1`, f.l.AgentID); err != nil {
		t.Fatal(err)
	}
	newLease, err := f.s.Acquire(context.Background(), f.l.AgentID, "new-context-writer")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.a.Read(f.ctx(), ref, 0, 0); !errors.Is(err, ErrLeaseLost) {
		t.Fatal("fenced writer retained context read access", err)
	}
	f.l, f.a.Lease = newLease, newLease
	if _, err = f.a.Read(f.ctx(), ref, 0, 0); err != nil {
		t.Fatal("new fenced owner could not read immutable content", err)
	}
	parent := f.run
	notification := Input{EventID: "2", Kind: "task.notification", ParentRunID: parent.ID, SourceOverflow: true}
	if err = f.s.Ingest(context.Background(), f.l, "2", []Input{notification}); err != nil {
		t.Fatal(err)
	}
	f.run, err = f.s.StartRunWithContextPolicy(context.Background(), f.l, []Input{notification}, contextProfileV1, archiveContextBudget(t))
	if err != nil {
		t.Fatal(err)
	}
	sources, err := f.a.runSources(f.ctx(), f.run.ID)
	if err != nil || len(sources) != 1 || sources[0].MessageID != f.input.MessageID {
		t.Fatal("overflow did not resolve the complete parent Run sources", err)
	}
	bad := []Input{{EventID: "2", Kind: "task.notification", SourceOverflow: true}}
	raw, _ := json.Marshal(bad)
	if _, err = f.s.DB.Exec(f.ctx(), `UPDATE runtime_runs SET input_json=$2 WHERE id=$1`, f.run.ID, raw); err != nil {
		t.Fatal(err)
	}
	if _, err = f.a.Path(f.ctx(), "tool_clear", "unknown-origin", "must remain private"); !errors.Is(err, errContextUnavailable) {
		t.Fatal("unresolved source became an unrestricted artifact", err)
	}
}
