package eino

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
	"github.com/google/uuid"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"teamie/services/agent-runtime/internal/domain"
)

func TestMarketplaceNotificationArchiveRejectsRevokedGroup(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		for _, derived := range []string{"", "task.notification", "schedule.wakeup"} {
			t.Run(fmt.Sprintf("legacy=%v/derived=%s", legacy, derived), func(t *testing.T) {
				testMarketplaceNotificationArchiveRevocation(t, legacy, derived)
			})
		}
	}
}

func testMarketplaceNotificationArchiveRevocation(t *testing.T, legacy bool, derived string) {
	t.Helper()
	f := newContextArchiveFixture(t)
	ctx := context.Background()
	if err := f.s.FinishRun(ctx, f.l, f.run, "completed", "", false); err != nil {
		t.Fatal(err)
	}
	w := newMCPNotificationWatcher(f.s, f.l, f.a.IM, "")
	w.handleNotification(ctx, f.input.WorkspaceID, f.input.ConversationID,
		imMarketplaceApp{ID: uuid.NewString(), Slug: "board", Name: "Board"},
		mcp.JSONRPCNotification{JSONRPC: "2.0", Notification: mcp.Notification{Method: "notifications/secret-room-state"}})
	pending, err := f.s.Pending(ctx, f.l)
	if err != nil || len(pending) != 1 {
		t.Fatalf("pending=%v err=%v", pending, err)
	}
	if legacy {
		// Older persisted notification runs only carried the workspace in
		// SourceRefs; their durable envelope must recover the conversation ACL.
		pending[0].SourceRefs = []InputSource{{WorkspaceID: f.input.WorkspaceID}}
	}
	run, err := f.s.StartRunWithContextPolicy(ctx, f.l, pending, contextProfileV1, archiveContextBudget(t))
	if err != nil {
		t.Fatal(err)
	}
	if derived != "" {
		if err = f.s.FinishRun(ctx, f.l, run, "completed", "", false); err != nil {
			t.Fatal(err)
		}
		sources, overflow := inputSources(pending)
		pending = []Input{{Kind: derived, EventID: "2", Body: "private derived state", ParentRunID: run.ID, SourceRefs: sources, SourceOverflow: overflow}}
		if err = f.s.Ingest(ctx, f.l, "1", pending); err != nil {
			t.Fatal(err)
		}
		run, err = f.s.StartRunWithContextPolicy(ctx, f.l, pending, contextProfileV1, archiveContextBudget(t))
		if err != nil {
			t.Fatal(err)
		}
	}
	f.run = run
	rctx := f.ctx()
	refs, err := f.a.runSources(rctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	messages := inputMessages(pending)
	for i, message := range messages {
		f.append(t, fmt.Sprint("notification-", i), message)
	}
	transcript, err := f.a.ArchiveTranscript(rctx, messages)
	if err != nil {
		t.Fatal(err)
	}
	ref, err := f.a.PathWithSources(rctx, "tool_trunc", "notification-archive", pending[0].Body, refs)
	if err != nil {
		t.Fatal(err)
	}
	for _, archive := range []string{ref, transcript} {
		if _, err = f.a.Read(rctx, archive, 0, contextReadMaximum); err != nil {
			t.Fatalf("authorized archive was not readable: %v", err)
		}
	}
	f.mu.Lock()
	f.deniedConversations[f.input.ConversationID] = true
	f.mu.Unlock()
	for _, archive := range []string{ref, transcript} {
		page, err := f.a.Read(rctx, archive, 0, contextReadMaximum)
		if !errors.Is(err, errContextUnavailable) {
			t.Fatalf("archive still readable after group revocation: err=%v sources=%+v leaked=%q", err, refs, page.Text)
		}
	}
}

func TestMarketplaceAppNotificationsCanCompact(t *testing.T) {
	for _, kind := range []string{"task.notification", "app.notification"} {
		t.Run(kind, func(t *testing.T) {
			p, archive, ctx, _ := notificationBatchFixture(t, true)
			run, _ := CurrentRun(ctx)
			for i := range run.Inputs {
				if run.Inputs[i].Kind == "task.notification" {
					run.Inputs[i].Kind = kind
					run.Inputs[i].Body = string(mustJSON(AppNotificationBody{AppID: "77777777-7777-4777-8777-777777777777", Slug: "board", AppName: "Board", Method: "notifications/state", Params: mustJSON(map[string]string{"state": strings.Repeat("board state ", 320)})}))
					if kind == "app.notification" {
						run.Inputs[i].WorkspaceID = "11111111-1111-4111-8111-111111111111"
						run.Inputs[i].ConversationID = "22222222-2222-4222-8222-222222222222"
						run.Inputs[i].TaskID, run.Inputs[i].ParentRunID, run.Inputs[i].Status = "", "", ""
						run.Inputs[i].SourceRefs = []InputSource{{WorkspaceID: run.Inputs[i].WorkspaceID}}
					}
				}
			}
			messages := append([]*schema.AgenticMessage{schema.SystemAgenticMessage("trusted policy")}, inputMessages(run.Inputs)...)
			tools := []*schema.ToolInfo{{Name: "fixture_manifest", Desc: strings.Repeat("manifest ", 2400)}}
			out, err := p.compact(ctx, messages, tools, false)
			if err != nil {
				t.Fatalf("machine batch cannot compact: %v", err)
			}
			measurement, err := p.cfg.Budget.measureContext(out, tools, p.cfg.Budget.OutputReserveTokens, false)
			if err != nil || !p.cfg.Budget.fits(measurement) {
				t.Fatalf("compacted request exceeds capacity: %+v %v", measurement, err)
			}
			current := agenticText(messages[len(messages)-1])
			if !slices.ContainsFunc(out, func(message *schema.AgenticMessage) bool { return agenticText(message) == current }) {
				t.Fatal("compaction lost the full human request")
			}
			archived := ""
			for _, content := range archive.content {
				archived += content
			}
			retained := archived
			for _, message := range out {
				retained += agenticText(message)
			}
			for i := 0; i < 20; i++ {
				if !strings.Contains(retained, fmt.Sprintf("notification-%02d", i)) {
					t.Fatalf("notification %d disappeared from context and archive", i)
				}
			}
			if !strings.Contains(archived, "board state ") {
				t.Fatal("machine notification bodies were not archived")
			}
		})
	}
}

func TestMarketplaceNotificationPinningPreservesFrozenBudgets(t *testing.T) {
	p, _, _ := testPipeline(t, 16384)
	input := Input{Kind: "app.notification", EventID: "1", Body: "app state"}
	ctx := WithRun(context.Background(), &Run{Inputs: []Input{input}})
	messages := inputMessages([]Input{input})
	pinned, err := p.currentInputSet(ctx, messages)
	if err != nil || len(pinned) != 0 {
		t.Fatalf("new budgets pinned a machine notification: %v", err)
	}
	p.cfg.Budget, err = makeContextBudgetSnapshotVersion(16384, 4)
	if err != nil {
		t.Fatal(err)
	}
	pinned, err = p.currentInputSet(ctx, messages)
	if err != nil || !pinned[messages[0]] {
		t.Fatalf("frozen v4 pinning changed: %v", err)
	}
	if _, err = p.currentInputSet(ctx, nil); err == nil {
		t.Fatal("frozen v4 required-input rule changed")
	}
}

func TestMarketplaceMCPToolNamesAreUnique(t *testing.T) {
	identities := [][3]string{
		{"board", "foo-bar", "12345678-1234-4234-8234-123456789abc"},
		{"board", "foo.bar", "12345678-1234-4234-8234-123456789abc"},
		{"board", "foo_bar", "12345678-1234-4234-8234-123456789abc"},
		{"board", "foo_bar", "12345678-0000-4000-8000-000000000001"},
		{"board", "foo_bar", "12345678-0000-4000-8000-000000000002"},
		{"my-app", "update", "group"}, {"my_app", "update", "group"},
		{strings.Repeat("a", 64), strings.Repeat("b", 100), "group"},
		{strings.Repeat("a", 64), strings.Repeat("b", 100) + "c", "group"},
	}
	var first []string
	for attempt := 0; attempt < 2; attempt++ {
		used, names := map[string]struct{}{}, []string{}
		for _, identity := range identities {
			name := formatMCPToolName(identity[0], identity[1], used, identity[2])
			if slices.Contains(names, name) || len(name) > 64 {
				t.Fatalf("duplicate or overlong name %q for %+v", name, identity)
			}
			names = append(names, name)
		}
		if attempt == 0 {
			first = names
		} else if !slices.Equal(first, names) {
			t.Fatal("tool names changed across repeated discovery")
		}
	}
	// Even an already-used hashed candidate must be checked before insertion.
	used := map[string]struct{}{}
	for i := 0; i < 4; i++ {
		formatMCPToolName("board", "foo_bar", used, "same-group")
	}
	if len(used) != 4 {
		t.Fatal("hashed candidate collision was not resolved")
	}
}

type mcpFailReceiptRepo struct {
	domain.RuntimeRepository
	failed bool
}

func (r *mcpFailReceiptRepo) RecordToolResult(ctx context.Context, l *Lease, runID, id string, b []byte) error {
	if !r.failed {
		r.failed = true
		return errors.New("simulated database outage after remote mutation committed")
	}
	return r.RuntimeRepository.RecordToolResult(ctx, l, runID, id, b)
}

func TestMarketplaceMCPUncommittedMutationIsNotReplayed(t *testing.T) {
	for _, failure := range []string{"receipt_commit", "response_lost", "connection_lost", "explicit_tool_error"} {
		t.Run(failure, func(t *testing.T) { testMarketplaceMCPMutationRecovery(t, failure) })
	}
}

func testMarketplaceMCPMutationRecovery(t *testing.T, failure string) {
	t.Helper()
	ctx := context.Background()
	s := testStore(t)
	if failure == "receipt_commit" {
		s.DB = &mcpFailReceiptRepo{RuntimeRepository: s.DB}
	}
	agentID, wsID, convID, appID := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	var calls atomic.Int32
	var requestID atomic.Value
	m := server.NewMCPServer("mutation", "1.0.0", server.WithToolCapabilities(true))
	m.AddTool(mcp.NewTool("submit"), func(_ context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		calls.Add(1)
		requestID.Store(request.Header.Get("Idempotency-Key"))
		if failure == "explicit_tool_error" {
			return mcp.NewToolResultError("rejected by application"), nil
		}
		return mcp.NewToolResultText("committed"), nil
	})
	handler := server.NewStreamableHTTPServer(m)
	mcpHTTP := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if (failure == "response_lost" || failure == "connection_lost") && r.Method == http.MethodPost {
			body, _ := io.ReadAll(r.Body)
			r.Body = io.NopCloser(bytes.NewReader(body))
			var request struct{ Method string }
			_ = json.Unmarshal(body, &request)
			if request.Method == "tools/call" {
				// The remote operation commits, then its response is lost at an
				// intermediary. There is no reliable result to record as success.
				handler.ServeHTTP(httptest.NewRecorder(), r)
				if failure == "connection_lost" {
					conn, _, err := w.(http.Hijacker).Hijack()
					if err == nil {
						_ = conn.Close()
					}
					return
				}
				http.Error(w, "upstream response lost", http.StatusBadGateway)
				return
			}
		}
		handler.ServeHTTP(w, r)
	}))
	defer mcpHTTP.Close()
	app := imMarketplaceApp{ID: appID, Slug: "board", MCPURL: mcpHTTP.URL, Enabled: true}
	imHTTP := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/session") {
			_ = json.NewEncoder(w).Encode(imConversationAppSession{App: app, Token: "fixture-token", ExpiresAt: time.Now().Add(time.Hour).Format(time.RFC3339)})
		} else {
			_ = json.NewEncoder(w).Encode(map[string]any{"apps": []imMarketplaceApp{app}})
		}
	}))
	defer imHTTP.Close()
	lease, err := s.Acquire(ctx, agentID, "review")
	if err != nil {
		t.Fatal(err)
	}
	inputs := []Input{{EventID: "1", WorkspaceID: wsID, ConversationID: convID, MessageID: uuid.NewString(), SenderID: uuid.NewString(), SenderKind: "human", Body: "submit one action"}}
	if err = s.Ingest(ctx, lease, "1", inputs); err != nil {
		t.Fatal(err)
	}
	run, err := s.StartRunWithContextPolicy(ctx, lease, inputs, contextProfileV1, archiveContextBudget(t))
	if err != nil {
		t.Fatal(err)
	}
	rctx := WithRun(ctx, run)
	imc := &IM{BaseURL: imHTTP.URL, token: "fixture-token", expiry: time.Now().Add(time.Hour)}
	wrapper := &mcpToolWrapper{tools: &Tools{Store: s, Lease: lease, IM: imc}, spec: mcpPinnedToolSpec{AppID: appID, AppSlug: "board", ConversationID: convID, WorkspaceID: wsID, MCPURL: mcpHTTP.URL, ExposedName: "mcp__board__submit", RemoteName: "submit", ToolInfo: &schema.ToolInfo{Name: "mcp__board__submit"}}}
	node, err := compose.NewToolNode(rctx, &compose.ToolsNodeConfig{Tools: []tool.BaseTool{wrapper}, ExecuteSequentially: true})
	if err != nil {
		t.Fatal(err)
	}
	first, err := invokeContextTool(node, rctx, "stable-call", wrapper.spec.ExposedName, map[string]any{})
	expected := "mcp_result_unknown"
	if failure == "explicit_tool_error" {
		expected = "mcp_tool_error"
	}
	if failure == "receipt_commit" {
		if err == nil {
			t.Fatal("expected first receipt commit failure")
		}
	} else if err != nil || !strings.Contains(first, `"error":"`+expected+`"`) {
		t.Fatalf("incorrect remote failure outcome: %s %v", first, err)
	}
	if calls.Load() != 1 {
		t.Fatalf("first remote mutation did not execute: %d", calls.Load())
	}
	if requestID.Load() != toolRequestID(run, "stable-call") {
		t.Fatal("MCP mutation did not receive the stable request ID")
	}
	restarted, err := s.StartRunWithContextPolicy(ctx, lease, inputs, contextProfileV1, archiveContextBudget(t))
	if err != nil || restarted.ID != run.ID || restarted.Attempt == run.Attempt {
		t.Fatalf("resume failed: %v", err)
	}
	rctx = WithRun(ctx, restarted)
	out, err := invokeContextTool(node, rctx, "stable-call", wrapper.spec.ExposedName, map[string]any{})
	if err != nil || !strings.Contains(out, `"error":"`+expected+`"`) {
		t.Fatalf("incorrect recovered outcome: %s %v", out, err)
	}
	if calls.Load() != 1 {
		t.Fatalf("same logical call mutated remote state %d times; retry err=%v", calls.Load(), err)
	}
	// The unknown outcome itself becomes a stable receipt, rather than repeatedly
	// probing or re-dispatching the remote mutation on subsequent replay.
	replayed, err := invokeContextTool(node, rctx, "stable-call", wrapper.spec.ExposedName, map[string]any{})
	if err != nil || replayed != out || calls.Load() != 1 {
		t.Fatalf("unknown outcome did not replay deterministically: %s %v", replayed, err)
	}
}

func TestMarketplaceMCPSubscriptionReinitializesExpiredSession(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := newMCPInMemoryStore()
	agentID, wsID, convID, appID := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	var initializations, gets atomic.Int32
	mcpHTTP := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			gets.Add(1)
			if r.Header.Get("Mcp-Session-Id") != "session-1" {
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = fmt.Fprint(w, "event: message\ndata: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/recovered\",\"params\":{\"status\":\"ready\"}}\n\n")
				w.(http.Flusher).Flush()
				<-r.Context().Done()
				return
			}
			http.Error(w, "session expired", http.StatusNotFound)
			return
		}
		if r.Method == http.MethodDelete {
			w.WriteHeader(http.StatusOK)
			return
		}
		var req struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.Method == "initialize" {
			id := initializations.Add(1)
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Mcp-Session-Id", fmt.Sprint("session-", id))
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": map[string]any{"protocolVersion": mcp.LATEST_PROTOCOL_VERSION, "serverInfo": map[string]string{"name": "expiry-fixture", "version": "1"}, "capabilities": map[string]any{}}})
		} else {
			w.WriteHeader(http.StatusAccepted)
		}
	}))
	defer mcpHTTP.Close()
	app := imMarketplaceApp{ID: appID, Slug: "board", MCPURL: mcpHTTP.URL, Enabled: true}
	imHTTP := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/api/v1/bootstrap":
			_ = json.NewEncoder(w).Encode(Bootstrap{Conversations: []Conversation{{ID: convID, WorkspaceID: wsID, Type: "group"}}})
		case strings.HasSuffix(r.URL.Path, "/session"):
			_ = json.NewEncoder(w).Encode(imConversationAppSession{App: app, Token: "fixture-token", ExpiresAt: time.Now().Add(time.Hour).Format(time.RFC3339)})
		default:
			_ = json.NewEncoder(w).Encode(map[string]any{"apps": []imMarketplaceApp{app}})
		}
	}))
	defer imHTTP.Close()
	lease, err := s.Acquire(ctx, agentID, "review")
	if err != nil {
		t.Fatal(err)
	}
	w := newMCPNotificationWatcher(s, lease, &IM{BaseURL: imHTTP.URL, token: "fixture-token", expiry: time.Now().Add(time.Hour)}, "")
	defer w.closeAll()
	w.sync(ctx)
	deadline := time.Now().Add(4 * time.Second)
	for gets.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if gets.Load() < 2 {
		t.Fatalf("did not reach expired-session reconnect scenario: GETs=%d", gets.Load())
	}
	w.sync(ctx)
	if initializations.Load() < 2 {
		t.Fatalf("expired MCP session never reinitialized: initialize=%d GETs=%d", initializations.Load(), gets.Load())
	}
	waitFor(t, func() bool {
		pending, err := s.Pending(ctx, lease)
		return err == nil && len(pending) == 1 && strings.Contains(pending[0].Body, "notifications/recovered")
	})
}
