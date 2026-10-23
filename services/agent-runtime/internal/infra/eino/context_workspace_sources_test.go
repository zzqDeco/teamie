package eino

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/cloudwego/eino/adk"
	bg "github.com/cloudwego/eino/adk/backgroundtask"
	"github.com/cloudwego/eino/adk/filesystem"
	fsmiddleware "github.com/cloudwego/eino/adk/middlewares/filesystem"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
	"github.com/cloudwego/kitex/client/callopt"
	"github.com/google/uuid"
	wire "teamie/contracts/gen/agent/kitex_gen/agent"
	"teamie/contracts/gen/agent/kitex_gen/agent/sandboxservice"
)

// Keep the real typed IM tool, native filesystem tool, receipt middleware and
// archive paths. Only the isolated Sandbox RPC transport is an in-memory fixture.
type contextWorkspaceFixture struct {
	sandboxservice.Client
	mu                 sync.Mutex
	files              map[string]string
	calls              int
	loseImportResponse bool
}

func (s *contextWorkspaceFixture) ImportFile(_ context.Context, r *wire.SandboxImportRequest, _ ...callopt.Option) (*wire.SandboxWorkspaceResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.files[r.Path] = string(r.Content)
	if s.loseImportResponse {
		s.loseImportResponse = false
		return nil, errors.New("fixture response lost after persistent import")
	}
	b, _ := json.Marshal(map[string]any{"path": r.Path, "bytes": len(r.Content), "sha256": r.Sha256})
	text := string(b)
	return &wire.SandboxWorkspaceResponse{Ok: true, DataJson: &text}, nil
}

func (s *contextWorkspaceFixture) Workspace(_ context.Context, r *wire.SandboxWorkspaceRequest, _ ...callopt.Option) (*wire.SandboxWorkspaceResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	var args struct{ Path, Content string }
	if json.Unmarshal([]byte(r.ArgsJson), &args) != nil {
		return nil, errors.New("invalid fixture arguments")
	}
	var value any
	switch r.Operation {
	case "write":
		s.files[args.Path] = args.Content
		value = map[string]any{"path": args.Path}
	case "read":
		value = map[string]any{"content": s.files[args.Path], "hasMore": false, "truncated": false}
	case "grep":
		value = map[string]any{"matches": []map[string]any{{"path": args.Path, "line": 1, "content": s.files[args.Path]}}, "hasMore": false}
	default:
		return nil, errors.New("unexpected fixture operation: " + r.Operation)
	}
	b, _ := json.Marshal(value)
	text := string(b)
	return &wire.SandboxWorkspaceResponse{Ok: true, DataJson: &text}, nil
}

type contextWorkspaceTool struct {
	tool.InvokableTool
	receipts *nativeToolReceipts
}

func (t contextWorkspaceTool) InvokableRun(ctx context.Context, args string, opts ...tool.Option) (string, error) {
	info, err := t.Info(ctx)
	if err != nil {
		return "", err
	}
	endpoint, err := t.receipts.WrapInvokableToolCall(ctx, t.InvokableTool.InvokableRun, &adk.ToolContext{Name: info.Name, CallID: compose.GetToolCallID(ctx)})
	if err != nil {
		return "", err
	}
	return endpoint(ctx, args, opts...)
}

func contextWorkspaceNode(t *testing.T, f *contextArchiveFixture, s *contextWorkspaceFixture) *compose.ToolsNode {
	t.Helper()
	backend := &SandboxBackend{Client: s, AgentID: f.l.AgentID, Store: f.s, Lease: f.l, Progressive: true}
	m, err := fsmiddleware.NewTyped[*schema.AgenticMessage](f.ctx(), &fsmiddleware.MiddlewareConfig{Backend: backend})
	if err != nil {
		t.Fatal(err)
	}
	_, state, err := m.BeforeAgent(f.ctx(), &adk.ChatModelAgentContext[*schema.AgenticMessage]{})
	if err != nil {
		t.Fatal(err)
	}
	page, err := backend.ReadPageTool()
	if err != nil {
		t.Fatal(err)
	}
	items := append(state.Tools, page)
	wrapped := []tool.BaseTool{}
	receipts := &nativeToolReceipts{Store: f.s, Lease: f.l, IM: f.a.IM}
	for _, item := range items {
		if invokable, ok := item.(tool.InvokableTool); ok {
			wrapped = append(wrapped, contextWorkspaceTool{invokable, receipts})
		}
	}
	node, err := compose.NewToolNode(f.ctx(), &compose.ToolsNodeConfig{Tools: wrapped, ExecuteSequentially: true})
	if err != nil {
		t.Fatal(err)
	}
	return node
}

func contextFinishWorkspaceRun(t *testing.T, f *contextArchiveFixture) {
	t.Helper()
	if err := f.s.FinishRun(f.ctx(), f.l, f.run, "completed", "", false); err != nil {
		t.Fatal(err)
	}
}

func contextNextWorkspaceRun(t *testing.T, f *contextArchiveFixture, event string, source Message) {
	t.Helper()
	f.input = Input{ConversationID: source.ConversationID, SenderID: source.SenderID, SenderKind: source.SenderKind}
	f.run = f.newRun(t, event)
	f.input = f.run.Inputs[0]
}

func TestContextWorkspaceImportedContentRetainsSourcesInRawArchiveAndCachedRead(t *testing.T) {
	f := newContextArchiveFixture(t)
	s := &contextWorkspaceFixture{files: map[string]string{}}
	private := contextAddForeignMessage(f, "WORKSPACE_IMPORTED_SECRET")
	content := []byte("WORKSPACE_IMPORTED_SECRET private file body")
	hash := sha256.Sum256(content)
	attachment := Attachment{ID: uuid.NewString(), Filename: "private.txt", MediaType: "text/plain", SizeBytes: len(content), SHA256: hex.EncodeToString(hash[:])}
	private.Attachments = []Attachment{attachment}
	f.mu.Lock()
	f.messages[private.ID] = private
	f.attachmentContent[attachment.ID] = content
	f.mu.Unlock()
	tools, err := (&Tools{Store: f.s, Lease: f.l, IM: f.a.IM, Sandbox: s}).BuildIM()
	if err != nil {
		t.Fatal(err)
	}
	imNode, err := compose.NewToolNode(f.ctx(), &compose.ToolsNodeConfig{Tools: tools})
	if err != nil {
		t.Fatal(err)
	}
	args := getAttachmentArgs{ConversationID: private.ConversationID, MessageID: private.ID, AttachmentID: attachment.ID, Path: "/workspace/private.txt"}
	imported, err := invokeContextTool(imNode, f.ctx(), "import-private", "im_get_attachment", args)
	if err != nil || !strings.Contains(imported, `"ok":true`) {
		t.Fatal(imported, err)
	}
	f.append(t, "import-receipt", contextRetrievalMessage("im_get_attachment", "import-private", imported))
	contextFinishWorkspaceRun(t, f)
	reader := contextAddForeignMessage(f, "reader conversation")
	contextNextWorkspaceRun(t, f, "2", reader)
	node := contextWorkspaceNode(t, f, s)
	readArgs := map[string]any{"file_path": "/workspace/private.txt"}
	wire, err := invokeContextTool(node, f.ctx(), "read-small", "read_file", readArgs)
	if err != nil || !strings.Contains(wire, "WORKSPACE_IMPORTED_SECRET") {
		t.Fatal(wire, err)
	}
	view := contextRetrievalMessage("read_file", "read-small", wire)
	view.Extra = map[string]any{"_eino_msg_id": "small-unoffloaded-file"}
	f.append(t, "small-file-result", view)
	transcript, err := f.a.ArchiveTranscript(f.ctx(), []*schema.AgenticMessage{view})
	if err != nil {
		t.Fatal(err)
	}
	ref, err := f.a.ArchiveToolPath(f.ctx(), "tool_trunc", contextRealIMDetail("read_file", "read-small", readArgs, wire), wire)
	if err != nil {
		t.Fatal(err)
	}
	derived := &schema.AgenticMessage{Role: schema.AgenticRoleTypeAssistant, ContentBlocks: []*schema.ContentBlock{schema.NewContentBlock(&schema.AssistantGenText{Text: "WORKSPACE_IMPORTED_DERIVED"})}, Extra: map[string]any{"_eino_msg_id": "derived-workspace"}}
	f.append(t, "derived-file-result", derived)
	derivedRef, err := f.a.ArchiveTranscript(f.ctx(), []*schema.AgenticMessage{derived})
	if err != nil {
		t.Fatal(err)
	}
	summary := schema.UserAgenticMessage("TEAMIE_SESSION_SUMMARY WORKSPACE_IMPORTED_SUMMARY")
	summary.Extra = map[string]any{contextArchiveRefKey: derivedRef}
	f.append(t, "workspace-native-summary", summary)
	for _, r := range []string{ref, transcript, derivedRef} {
		if _, err = f.a.Read(f.ctx(), r, 0, 0); err != nil {
			t.Fatal(err)
		}
	}
	f.mu.Lock()
	f.deniedConversations[private.ConversationID] = true
	f.mu.Unlock()
	for _, r := range []string{ref, transcript, derivedRef} {
		if _, err = f.a.Read(f.ctx(), r, 0, 0); !errors.Is(err, errContextUnavailable) {
			t.Errorf("workspace content escaped original source revocation: %s %v", r, err)
		}
	}
	if _, err = invokeContextTool(node, f.ctx(), "read-small", "read_file", readArgs); err == nil {
		t.Error("cached native file read bypassed current source ACL")
	}
	if _, err = invokeContextTool(node, f.ctx(), "read-page", "workspace_read_page", map[string]any{"operation": "read", "path": "/workspace/private.txt"}); err == nil {
		t.Error("page read bypassed original import source ACL")
	}
	for _, name := range []string{"ls", "glob", "grep"} {
		if _, err = invokeContextTool(node, f.ctx(), "revoked-"+name, name, map[string]any{"path": "/workspace", "pattern": "private"}); err == nil {
			t.Errorf("%s bypassed original workspace source ACL", name)
		}
	}
	page, err := f.a.Search(f.ctx(), "WORKSPACE_IMPORTED", "", 8)
	if err != nil || len(page.Hits) != 0 {
		t.Fatal("raw file event/derived search leaked revoked bytes", page, err)
	}
}

func TestContextWorkspaceWriterRetainsEarlierSummaryButNotLaterUnrelatedInput(t *testing.T) {
	f := newContextArchiveFixture(t)
	s := &contextWorkspaceFixture{files: map[string]string{}}
	private := contextAddForeignMessage(f, "PREVIOUS_SESSION_PRIVATE_FACT")
	args := getMessageArgs{ConversationID: private.ConversationID, MessageID: private.ID}
	result, err := invokeContextTool(contextRealIMNode(t, f), f.ctx(), "previous-private-read", "im_get_message", args)
	if err != nil {
		t.Fatal(err)
	}
	view := contextRetrievalMessage("im_get_message", "previous-private-read", result)
	f.append(t, "previous-private-view", view)
	ref, err := f.a.ArchiveTranscript(f.ctx(), []*schema.AgenticMessage{view})
	if err != nil {
		t.Fatal(err)
	}
	summary := schema.UserAgenticMessage("TEAMIE_SESSION_SUMMARY learned private fact")
	summary.Extra = map[string]any{contextArchiveRefKey: ref}
	f.append(t, "previous-summary", summary)
	contextFinishWorkspaceRun(t, f)
	writer := contextAddForeignMessage(f, "writer request uses earlier Session")
	contextNextWorkspaceRun(t, f, "2", writer)
	if _, err = invokeContextTool(contextWorkspaceNode(t, f, s), f.ctx(), "write-from-summary", "write_file", map[string]any{"file_path": "/workspace/derived.txt", "content": "PREVIOUS_SESSION_PRIVATE_FACT copied from known summary"}); err != nil {
		t.Fatal(err)
	}
	contextFinishWorkspaceRun(t, f)
	later := contextAddForeignMessage(f, "unrelated input after writer")
	contextNextWorkspaceRun(t, f, "3", later)
	f.append(t, "unrelated-input-only", schema.UserAgenticMessage("later conversation; no workspace writer"))
	contextFinishWorkspaceRun(t, f)
	reader := contextAddForeignMessage(f, "new reader")
	contextNextWorkspaceRun(t, f, "4", reader)
	node := contextWorkspaceNode(t, f, s)
	readArgs := map[string]any{"file_path": "/workspace/derived.txt"}
	wire, err := invokeContextTool(node, f.ctx(), "read-derived", "read_file", readArgs)
	if err != nil || !strings.Contains(wire, "PREVIOUS_SESSION_PRIVATE_FACT") {
		t.Fatal(wire, err)
	}
	ref, err = f.a.ArchiveToolPath(f.ctx(), "tool_trunc", contextRealIMDetail("read_file", "read-derived", readArgs, wire), wire)
	if err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	f.deniedConversations[later.ConversationID] = true
	f.mu.Unlock()
	if _, err = f.a.Read(f.ctx(), ref, 0, 0); err != nil {
		t.Fatal("later unrelated input contaminated an earlier writer's fixed view", err)
	}
	if _, err = invokeContextTool(node, f.ctx(), "read-derived", "read_file", readArgs); err != nil {
		t.Fatal("frozen cached read changed after unrelated later input", err)
	}
	f.mu.Lock()
	f.deniedConversations[private.ConversationID] = true
	f.mu.Unlock()
	if _, err = f.a.Read(f.ctx(), ref, 0, 0); !errors.Is(err, errContextUnavailable) {
		t.Fatal("writer lost the prior Session summary's private origin", err)
	}
	if _, err = invokeContextTool(node, f.ctx(), "read-derived", "read_file", readArgs); err == nil {
		t.Fatal("cached read ignored writer's causal source revocation")
	}
}

func TestContextWorkspaceNativeReductionNoticeRetainsWriterSources(t *testing.T) {
	f := newContextArchiveFixture(t)
	s := &contextWorkspaceFixture{files: map[string]string{}}
	origin := f.input.ConversationID
	text := "WORKSPACE_NATIVE_NOTICE " + strings.Repeat("private file content ", 900)
	if _, err := invokeContextTool(contextWorkspaceNode(t, f, s), f.ctx(), "native-writer", "write_file", map[string]any{"file_path": "/workspace/large.txt", "content": text}); err != nil {
		t.Fatal(err)
	}
	contextFinishWorkspaceRun(t, f)
	contextNextWorkspaceRun(t, f, "2", contextAddForeignMessage(f, "notice reader"))
	args := map[string]any{"file_path": "/workspace/large.txt"}
	wire, err := invokeContextTool(contextWorkspaceNode(t, f, s), f.ctx(), "native-large-read", "read_file", args)
	if err != nil {
		t.Fatal(err)
	}
	detail := contextRealIMDetail("read_file", "native-large-read", args, wire)
	budget, err := ParseContextBudgetSnapshot(f.run.ContextBudget)
	if err != nil {
		t.Fatal(err)
	}
	pipeline := &ContextPipeline{cfg: ContextPipelineConfig{Budget: budget, Backend: f.a, ArchiveToolPath: f.a.ArchiveToolPath}}
	truncated, err := pipeline.truncateTool(f.ctx(), detail)
	if err != nil || !truncated.NeedOffload {
		t.Fatal("native file result did not use real truncation path", err)
	}
	if err = f.a.Write(f.ctx(), &filesystem.WriteRequest{FilePath: truncated.OffloadFilePath, Content: truncated.OffloadContent}); err != nil {
		t.Fatal(err)
	}
	notice := toolResultText(truncated.ToolResult)
	view := contextRetrievalMessage("read_file", "native-large-read", notice)
	f.append(t, "native-workspace-truncated-view", view)
	ref, err := f.a.ArchiveTranscript(f.ctx(), []*schema.AgenticMessage{view})
	if err != nil {
		t.Fatal("native workspace notice lost original sources", err)
	}
	for _, bad := range []*schema.AgenticMessage{contextRetrievalMessage("read_file", "other-call", notice), contextRetrievalMessage("grep", "native-large-read", notice), contextRetrievalMessage("read_file", "native-large-read", notice+" forged suffix")} {
		if _, err = f.a.ArchiveTranscript(f.ctx(), []*schema.AgenticMessage{bad}); !errors.Is(err, errContextUnavailable) {
			t.Fatal("unverified workspace notice supplied source authority", err)
		}
	}
	contextFinishWorkspaceRun(t, f)
	f.run = f.newRun(t, "3")
	f.input = f.run.Inputs[0]
	cleared, err := pipeline.clearTool(f.ctx(), detail)
	if err != nil || !cleared.NeedClear {
		t.Fatal(err)
	}
	clearView := contextRetrievalMessage("read_file", "native-large-read", toolResultText(cleared.ToolResult))
	f.append(t, "native-workspace-clear-view", clearView)
	clearRef, err := f.a.ArchiveTranscript(f.ctx(), []*schema.AgenticMessage{clearView})
	if err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	f.deniedConversations[origin] = true
	f.mu.Unlock()
	for _, r := range []string{ref, clearRef} {
		if _, err = f.a.Read(f.ctx(), r, 0, 0); !errors.Is(err, errContextUnavailable) {
			t.Fatal("workspace notice lost original writer ACL", err)
		}
	}
}

func TestContextWorkspaceTaskOutputBindsStoredAgentAndOriginRun(t *testing.T) {
	f := newContextArchiveFixture(t)
	origin := f.input.ConversationID
	tasks := &TaskStore{Store: f.s, Lease: f.l, AgentID: f.l.AgentID}
	task, err := tasks.Create(f.ctx(), &bg.CreateTaskRequest{Spec: bg.Spec{ID: "shell-proven-origin", ExecutorKey: "fixture", SessionID: "agent:" + f.l.AgentID}, LeaseExpiryPolicy: bg.LeaseExpiryRetry})
	if err != nil {
		t.Fatal(err)
	}
	contextFinishWorkspaceRun(t, f)
	contextNextWorkspaceRun(t, f, "2", contextAddForeignMessage(f, "task output reader"))
	receipts := &nativeToolReceipts{Store: f.s, Lease: f.l, IM: f.a.IM}
	called := 0
	endpoint, err := receipts.WrapInvokableToolCall(f.ctx(), func(context.Context, string, ...tool.Option) (string, error) {
		called++
		return "TASK_OUTPUT_ORIGIN_PRIVATE", nil
	}, &adk.ToolContext{Name: "task_output", CallID: "stored-task-output"})
	if err != nil {
		t.Fatal(err)
	}
	args := `{"task_id":"` + task.Spec.ID + `"}`
	result, err := endpoint(f.ctx(), args)
	if err != nil {
		t.Fatal(err)
	}
	view := contextRetrievalMessage("task_output", "stored-task-output", result)
	f.append(t, "task-output-result", view)
	ref, err := f.a.ArchiveTranscript(f.ctx(), []*schema.AgenticMessage{view})
	if err != nil {
		t.Fatal(err)
	}
	// The page tool consumes taskId; a misleading ignored task_id cannot
	// replace its actual source binding. Both the real and forged targets are
	// persisted so this checks the schema boundary rather than a missing row.
	page, err := receipts.WrapInvokableToolCall(f.ctx(), func(context.Context, string, ...tool.Option) (string, error) { return "actual task page", nil }, &adk.ToolContext{Name: "workspace_read_page", CallID: "task-page-schema"})
	if err != nil {
		t.Fatal(err)
	}
	pageArgs := `{"operation":"output","taskId":"` + task.Spec.ID + `","task_id":"ignored-marker"}`
	if _, err = page(f.ctx(), pageArgs); err != nil {
		t.Fatal("page taskId did not match its native schema", err)
	}
	// A genuine task owned by another Agent is still not this Agent's source.
	other := uuid.NewString()
	if _, err = f.s.DB.Exec(f.ctx(), `INSERT INTO runtime_tasks(id,agent_id,run_id,snapshot) VALUES('shell-foreign',$1,$2,'{}')`, other, f.run.ID); err != nil {
		t.Fatal(err)
	}
	for _, request := range []string{`{"task_id":"shell-foreign"}`, `{"task_id":"context:v1:copied-model-marker"}`} {
		bad, err := receipts.WrapInvokableToolCall(f.ctx(), func(context.Context, string, ...tool.Option) (string, error) {
			t.Fatal("unproven task reached execution")
			return "", nil
		}, &adk.ToolContext{Name: "task_output", CallID: "reject-" + digest([]byte(request))})
		if err != nil {
			t.Fatal(err)
		}
		if _, err = bad(f.ctx(), request); !errors.Is(err, errContextUnavailable) {
			t.Fatal("unproven task ID acquired origin", err)
		}
	}
	f.mu.Lock()
	f.deniedConversations[origin] = true
	f.mu.Unlock()
	if _, err = f.a.Read(f.ctx(), ref, 0, 0); !errors.Is(err, errContextUnavailable) {
		t.Fatal("task output escaped its actual origin Run", err)
	}
	if _, err = endpoint(f.ctx(), args); err == nil || called != 1 {
		t.Fatal("cached task output bypassed original source ACL", called, err)
	}
	if _, err = page(f.ctx(), pageArgs); err == nil {
		t.Fatal("output page source did not follow actual taskId")
	}
}

func TestContextWorkspaceObservationFreezesBeforeLaterImportAndAttemptReplay(t *testing.T) {
	f := newContextArchiveFixture(t)
	s := &contextWorkspaceFixture{files: map[string]string{"/workspace/public.txt": "stable original page"}}
	node := contextWorkspaceNode(t, f, s)
	args := map[string]any{"file_path": "/workspace/public.txt"}
	first, err := invokeContextTool(node, f.ctx(), "frozen-before-later-import", "read_file", args)
	if err != nil {
		t.Fatal(err)
	}
	private := contextAddForeignMessage(f, "LATER_IMPORT_PRIVATE")
	content := []byte("later private content")
	hash := sha256.Sum256(content)
	attachment := Attachment{ID: uuid.NewString(), Filename: "later.txt", MediaType: "text/plain", SizeBytes: len(content), SHA256: hex.EncodeToString(hash[:])}
	private.Attachments = []Attachment{attachment}
	f.mu.Lock()
	f.messages[private.ID] = private
	f.attachmentContent[attachment.ID] = content
	f.mu.Unlock()
	items, err := (&Tools{Store: f.s, Lease: f.l, IM: f.a.IM, Sandbox: s}).BuildIM()
	if err != nil {
		t.Fatal(err)
	}
	imNode, err := compose.NewToolNode(f.ctx(), &compose.ToolsNodeConfig{Tools: items})
	if err != nil {
		t.Fatal(err)
	}
	importArgs := getAttachmentArgs{ConversationID: private.ConversationID, MessageID: private.ID, AttachmentID: attachment.ID, Path: "/workspace/later.txt"}
	if _, err = invokeContextTool(imNode, f.ctx(), "later-import", "im_get_attachment", importArgs); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	f.deniedConversations[private.ConversationID] = true
	f.mu.Unlock()
	// The original successful page is fixed even when another writer arrives,
	// and a recovered execution attempt must not recapture a broader workspace.
	f.run.Attempt = uuid.NewString()
	if _, err = f.s.DB.Exec(f.ctx(), `UPDATE runtime_runs SET attempt_id=$2 WHERE id=$1`, f.run.ID, f.run.Attempt); err != nil {
		t.Fatal(err)
	}
	second, err := invokeContextTool(node, f.ctx(), "frozen-before-later-import", "read_file", args)
	if err != nil || second != first {
		t.Fatal("stable native receipt/source scope changed on replay", second, err)
	}
	if _, err = invokeContextTool(node, f.ctx(), "future-read", "read_file", args); err == nil {
		t.Fatal("new read omitted the later real workspace writer")
	}
	if _, err = invokeContextTool(node, f.ctx(), "frozen-before-later-import", "read_file", map[string]any{"file_path": "/workspace/later.txt"}); !errors.Is(err, ErrReplayConflict) {
		t.Fatal("same call ID accepted changed arguments", err)
	}
}

func TestContextWorkspaceLegacyNativeReceiptBytesRemainUnchanged(t *testing.T) {
	f := newContextArchiveFixture(t)
	contextFinishWorkspaceRun(t, f)
	input := f.input
	input.EventID = "2"
	if err := f.s.Ingest(f.ctx(), f.l, "2", []Input{input}); err != nil {
		t.Fatal(err)
	}
	var err error
	f.run, err = f.s.StartRun(context.Background(), f.l, []Input{input})
	if err != nil {
		t.Fatal(err)
	}
	receipts := &nativeToolReceipts{Store: f.s, Lease: f.l}
	endpoint, err := receipts.WrapInvokableToolCall(f.ctx(), func(context.Context, string, ...tool.Option) (string, error) { return "legacy exact wire", nil }, &adk.ToolContext{Name: "read_file", CallID: "legacy-native"})
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		value, err := endpoint(f.ctx(), `{"file_path":"/workspace/old"}`)
		if err != nil || value != "legacy exact wire" {
			t.Fatal(value, err)
		}
	}
	var raw []byte
	if err = f.s.DB.QueryRow(f.ctx(), `SELECT result FROM runtime_tool_calls WHERE run_id=$1 AND call_id='legacy-native'`, f.run.ID).Scan(&raw); err != nil || string(raw) != `"legacy exact wire"` {
		t.Fatal(string(raw), err)
	}
	if count(t, f.s, `SELECT count(*) FROM runtime_tool_observations WHERE observation_key=$1`, workspaceSourcesKey) != 0 {
		t.Fatal("legacy native call acquired new-profile provenance semantics")
	}
}

func TestContextWorkspaceImportProofSurvivesLostResponseAndUnknownLegacyFailsClosed(t *testing.T) {
	f := newContextArchiveFixture(t)
	s := &contextWorkspaceFixture{files: map[string]string{}, loseImportResponse: true}
	private := contextAddForeignMessage(f, "UNKNOWN_IMPORT_PRIVATE")
	content := []byte("UNKNOWN_IMPORT_PRIVATE preserved bytes")
	hash := sha256.Sum256(content)
	attachment := Attachment{ID: uuid.NewString(), SizeBytes: len(content), SHA256: hex.EncodeToString(hash[:])}
	private.Attachments = []Attachment{attachment}
	f.mu.Lock()
	f.messages[private.ID] = private
	f.attachmentContent[attachment.ID] = content
	f.mu.Unlock()
	items, err := (&Tools{Store: f.s, Lease: f.l, IM: f.a.IM, Sandbox: s}).BuildIM()
	if err != nil {
		t.Fatal(err)
	}
	imNode, err := compose.NewToolNode(f.ctx(), &compose.ToolsNodeConfig{Tools: items})
	if err != nil {
		t.Fatal(err)
	}
	args := getAttachmentArgs{ConversationID: private.ConversationID, MessageID: private.ID, AttachmentID: attachment.ID, Path: "/workspace/unknown.txt"}
	if _, err = invokeContextTool(imNode, f.ctx(), "response-lost-import", "im_get_attachment", args); err == nil {
		t.Fatal("fixture did not interrupt after persistent import")
	}
	native := contextWorkspaceNode(t, f, s)
	readArgs := map[string]any{"file_path": "/workspace/unknown.txt"}
	if wire, err := invokeContextTool(native, f.ctx(), "read-after-import-loss", "read_file", readArgs); err != nil || !strings.Contains(wire, "UNKNOWN_IMPORT_PRIVATE") {
		t.Fatal("authenticated import proof was not recoverable", wire, err)
	}
	f.mu.Lock()
	f.deniedConversations[private.ConversationID] = true
	f.mu.Unlock()
	if _, err = invokeContextTool(native, f.ctx(), "read-after-import-loss", "read_file", readArgs); err == nil {
		t.Fatal("unknown import effect lost its authenticated origin")
	}
	f.mu.Lock()
	f.deniedConversations[private.ConversationID] = false
	f.mu.Unlock()
	payload, _ := json.Marshal(args)
	if _, err = f.s.ToolIntent(f.ctx(), f.l, f.run, "unproven-legacy-import", "im_get_attachment", payload); err != nil {
		t.Fatal(err)
	}
	if _, err = invokeContextTool(native, f.ctx(), "read-after-legacy-unknown", "read_file", readArgs); !errors.Is(err, errContextUnavailable) {
		t.Fatal("unproven legacy import was silently relabelled", err)
	}
}

func TestContextWorkspaceOldPersistedCopiesRevalidateWithoutChangingReceipts(t *testing.T) {
	f := newContextArchiveFixture(t)
	s := &contextWorkspaceFixture{files: map[string]string{}}
	origin := f.input.ConversationID
	if _, err := invokeContextTool(contextWorkspaceNode(t, f, s), f.ctx(), "old-writer", "write_file", map[string]any{"file_path": "/workspace/old.txt", "content": "OLD_PERSISTED_WORKSPACE_SECRET"}); err != nil {
		t.Fatal(err)
	}
	contextFinishWorkspaceRun(t, f)
	contextNextWorkspaceRun(t, f, "2", contextAddForeignMessage(f, "old reader"))
	args := map[string]any{"file_path": "/workspace/old.txt"}
	wire, err := invokeContextTool(contextWorkspaceNode(t, f, s), f.ctx(), "old-native-read", "read_file", args)
	if err != nil {
		t.Fatal(err)
	}
	view := contextRetrievalMessage("read_file", "old-native-read", wire)
	f.append(t, "old-read-original", view)
	detail := contextRealIMDetail("read_file", "old-native-read", args, wire)
	budget, err := ParseContextBudgetSnapshot(f.run.ContextBudget)
	if err != nil {
		t.Fatal(err)
	}
	pipeline := &ContextPipeline{cfg: ContextPipelineConfig{Budget: budget, Backend: f.a, ArchiveToolPath: f.a.ArchiveToolPath}}
	oldTool, _, err := pipeline.archiveTool(f.ctx(), "tool_trunc", detail, detail.ToolResult)
	if err != nil {
		t.Fatal(err)
	}
	derived := &schema.AgenticMessage{Role: schema.AgenticRoleTypeAssistant, ContentBlocks: []*schema.ContentBlock{schema.NewContentBlock(&schema.AssistantGenText{Text: "OLD_PERSISTED_WORKSPACE_SUMMARY"})}, Extra: map[string]any{"_eino_msg_id": "old-private-derived"}}
	f.append(t, "old-derived-original", derived)
	oldSummary, err := f.a.ArchiveTranscript(f.ctx(), []*schema.AgenticMessage{derived})
	if err != nil {
		t.Fatal(err)
	}
	var seq int64
	if err = f.s.DB.QueryRow(f.ctx(), `SELECT seq FROM runtime_session_events WHERE event_id='old-read-original' AND run_id=$1`, f.run.ID).Scan(&seq); err != nil {
		t.Fatal(err)
	}
	oldEvent, err := f.a.put(f.ctx(), f.run, contextArtifact{RunID: f.run.ID, Attempt: f.run.Attempt, Key: "old-event-fixture", Kind: "transcript", Text: string(contextProjectionBytes(view)), Sources: []InputSource{contextMessageSource(f.messages[f.input.MessageID])}, EventSeq: seq})
	if err != nil {
		t.Fatal(err)
	}
	reads := contextToolNode(t, f.a)
	readArgs := map[string]any{"ref": oldTool}
	first, err := invokeContextTool(reads, f.ctx(), "cached-old-offload", "context_read", readArgs)
	if err != nil {
		t.Fatal(err)
	}
	var beforeHash string
	var beforeReceipt []byte
	if err = f.s.DB.QueryRow(f.ctx(), `SELECT request_hash,result FROM runtime_tool_calls WHERE run_id=$1 AND call_id='old-native-read'`, f.run.ID).Scan(&beforeHash, &beforeReceipt); err != nil {
		t.Fatal(err)
	}
	// Reproduce the precise old on-disk bug: intact native receipts, but copied
	// archive/index metadata labels private file bytes only with reader B.
	readerSources, _ := f.a.runSources(f.ctx(), f.run.ID)
	badSources, _ := json.Marshal(readerSources)
	for _, ref := range []string{oldTool, oldSummary, oldEvent} {
		run, _, key, _ := parseContextRef(ref)
		if _, err = f.s.DB.Exec(f.ctx(), `DELETE FROM runtime_tool_observations WHERE run_id=$1 AND call_id=$2 AND observation_key=$3`, run, "context-artifact:"+key, contextSourceVersionKey); err != nil {
			t.Fatal(err)
		}
		if _, err = f.s.DB.Exec(f.ctx(), `UPDATE runtime_context_artifacts SET sources=$3,source_artifact_refs='[]' WHERE run_id=$1 AND artifact_key=$2`, run, key, badSources); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = f.s.DB.Exec(f.ctx(), `DELETE FROM runtime_tool_observations WHERE observation_key=$1`, workspaceSourcesKey); err != nil {
		t.Fatal(err)
	}
	if _, err = f.a.Read(f.ctx(), oldTool, 0, 0); err != nil {
		t.Fatal("real old tool receipt did not reconstruct authorized workspace provenance", err)
	}
	if _, err = f.a.Read(f.ctx(), oldEvent, 0, 0); err != nil {
		t.Fatal("old raw native history could not reconstruct source", err)
	}
	if _, err = f.a.Read(f.ctx(), oldSummary, 0, 0); !errors.Is(err, errContextUnavailable) {
		t.Fatal("unproven old derived summary was trusted", err)
	}
	second, err := invokeContextTool(reads, f.ctx(), "cached-old-offload", "context_read", readArgs)
	if err != nil || second != first {
		t.Fatal("old native receipt bytes changed during source reconstruction", second, err)
	}
	f.mu.Lock()
	f.deniedConversations[origin] = true
	f.mu.Unlock()
	for _, ref := range []string{oldTool, oldEvent, oldSummary} {
		if _, err = f.a.Read(f.ctx(), ref, 0, 0); !errors.Is(err, errContextUnavailable) {
			t.Fatal("old persisted copy bypassed source revocation", ref, err)
		}
	}
	if _, err = invokeContextTool(reads, f.ctx(), "cached-old-offload", "context_read", readArgs); err == nil {
		t.Fatal("old cached archive bypassed reconstructed source ACL")
	}
	page, err := f.a.Search(f.ctx(), "OLD_PERSISTED_WORKSPACE", "", 8)
	if err != nil || len(page.Hits) != 0 {
		t.Fatal("old offload/summary/raw index leaked through search", page, err)
	}
	var afterHash string
	var afterReceipt []byte
	if err = f.s.DB.QueryRow(f.ctx(), `SELECT request_hash,result FROM runtime_tool_calls WHERE run_id=$1 AND call_id='old-native-read'`, f.run.ID).Scan(&afterHash, &afterReceipt); err != nil || beforeHash != afterHash || string(beforeReceipt) != string(afterReceipt) {
		t.Fatal("source validation rewrote immutable native receipt", err)
	}
}

func TestContextWorkspaceLegacySynchronousWriterAliasRetainsPersistentSources(t *testing.T) {
	f := newContextArchiveFixture(t)
	s := &contextWorkspaceFixture{files: map[string]string{}}
	origin := f.input.ConversationID
	legacyRun := f.run.ID
	// v0.2 persisted this exact unprefixed typed intent before Sandbox.Exec.
	args := []byte(`{"command":"printf LEGACY_WORKSPACE_SECRET > /workspace/old.txt"}`)
	if _, err := f.s.ToolIntent(f.ctx(), f.l, f.run, "legacy-sync-effect", "workspace_execute", args); err != nil {
		t.Fatal(err)
	}
	s.files["/workspace/old.txt"] = "LEGACY_WORKSPACE_SECRET"
	result := []byte(`{"ok":true,"data":{"exitCode":0,"stdout":"LEGACY_WORKSPACE_SECRET","stderr":"","truncated":false}}`)
	if err := f.s.ToolResult(f.ctx(), f.l, f.run, "legacy-sync-effect", result); err != nil {
		t.Fatal(err)
	}
	f.append(t, "legacy-sync-result", contextRetrievalMessage("workspace_execute", "legacy-sync-effect", string(result)))
	contextFinishWorkspaceRun(t, f)
	contextNextWorkspaceRun(t, f, "2", contextAddForeignMessage(f, "reader after old synchronous command"))
	node := contextWorkspaceNode(t, f, s)
	readArgs := map[string]any{"file_path": "/workspace/old.txt"}
	wire, err := invokeContextTool(node, f.ctx(), "read-legacy-shared-file", "read_file", readArgs)
	if err != nil || !strings.Contains(wire, "LEGACY_WORKSPACE_SECRET") {
		t.Fatal(wire, err)
	}
	ref, err := f.a.ArchiveToolPath(f.ctx(), "tool_trunc", contextRealIMDetail("read_file", "read-legacy-shared-file", readArgs, wire), wire)
	if err != nil {
		t.Fatal(err)
	}
	page, err := f.a.Search(f.ctx(), "LEGACY_WORKSPACE_SECRET", "", 8)
	if err != nil || len(page.Hits) == 0 {
		t.Fatal("provable old synchronous native history became unavailable", err)
	}
	f.mu.Lock()
	f.deniedConversations[origin] = true
	f.mu.Unlock()
	if _, err = f.a.Read(f.ctx(), ref, 0, 0); !errors.Is(err, errContextUnavailable) {
		t.Fatal("unprefixed writer lost persistent file origins", err)
	}
	if _, err = invokeContextTool(node, f.ctx(), "read-legacy-shared-file", "read_file", readArgs); err == nil {
		t.Fatal("cached read ignored legacy synchronous writer origin")
	}
	page, err = f.a.Search(f.ctx(), "LEGACY_WORKSPACE_SECRET", "", 8)
	if err != nil || len(page.Hits) != 0 {
		t.Fatal("legacy synchronous result leaked after revocation", page, err)
	}
	var afterHash string
	var afterResult []byte
	if err = f.s.DB.QueryRow(f.ctx(), `SELECT request_hash,result FROM runtime_tool_calls WHERE run_id=$1 AND call_id='legacy-sync-effect'`, legacyRun).Scan(&afterHash, &afterResult); err != nil || afterHash != digest(args) || string(afterResult) != string(result) {
		t.Fatal("legacy request/result bytes changed", err)
	}
}

func TestContextWorkspaceLegacyWriterWithoutFrozenEarlierSessionIsUnavailable(t *testing.T) {
	f := newContextArchiveFixture(t)
	s := &contextWorkspaceFixture{files: map[string]string{}}
	f.append(t, "earlier-legacy-session", schema.UserAgenticMessage("earlier private context"))
	contextFinishWorkspaceRun(t, f)
	contextNextWorkspaceRun(t, f, "2", contextAddForeignMessage(f, "old writer trigger alone is insufficient"))
	// Match migrated v0.2 rows: no frozen Session/acceptance head was saved.
	if _, err := f.s.DB.Exec(f.ctx(), `UPDATE runtime_runs SET context_head=0,acceptance_head=0 WHERE id=$1`, f.run.ID); err != nil {
		t.Fatal(err)
	}
	args := []byte(`{"command":"printf private > /workspace/old.txt"}`)
	if _, err := f.s.ToolIntent(f.ctx(), f.l, f.run, "legacy-unknown-view", "workspace_execute", args); err != nil {
		t.Fatal(err)
	}
	s.files["/workspace/old.txt"] = "unproven earlier-context-derived bytes"
	if err := f.s.ToolResult(f.ctx(), f.l, f.run, "legacy-unknown-view", []byte(`{"ok":true,"data":{"exitCode":0}}`)); err != nil {
		t.Fatal(err)
	}
	contextFinishWorkspaceRun(t, f)
	contextNextWorkspaceRun(t, f, "3", contextAddForeignMessage(f, "later reader"))
	if _, err := invokeContextTool(contextWorkspaceNode(t, f, s), f.ctx(), "read-unknown-old-view", "read_file", map[string]any{"file_path": "/workspace/old.txt"}); !errors.Is(err, errContextUnavailable) {
		t.Fatal("unknown old writer Session was treated as trigger-only provenance", err)
	}
}
