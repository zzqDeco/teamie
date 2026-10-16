package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	"github.com/cloudwego/kitex/client/callopt"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	wire "teamie/agent-protocol/kitex_gen/agent"
	"teamie/agent-protocol/kitex_gen/agent/agentcontrolservice"
	"teamie/agent-protocol/kitex_gen/agent/modelgatewayservice"
	"teamie/agent-protocol/kitex_gen/agent/sandboxservice"
	"teamie/backend/kitex_gen/im"
	"teamie/backend/kitex_gen/im/identityservice"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	dsn := os.Getenv("TEAMIE_RUNTIME_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEAMIE_RUNTIME_TEST_DATABASE_URL is unset; disposable PostgreSQL required")
	}
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	name := "runtime_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err = conn.Exec(ctx, "CREATE SCHEMA "+name); err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	q.Set("search_path", name)
	u.RawQuery = q.Encode()
	s, err := Open(ctx, u.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.DB.Close(); _, _ = conn.Exec(ctx, "DROP SCHEMA "+name+" CASCADE"); _ = conn.Close(ctx) })
	return s
}
func waitFor(t *testing.T, fn func() bool) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if fn() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("condition did not become true")
}
func count(t *testing.T, s *Store, query string, args ...any) int {
	t.Helper()
	var n int
	if err := s.DB.QueryRow(context.Background(), query, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

type fixtureControl struct {
	agentcontrolservice.Client
	mu     sync.Mutex
	defs   []*wire.AgentDefinition
	states []string
}

func (f *fixtureControl) ListAgents(context.Context, *wire.AgentListRequest, ...callopt.Option) (*wire.AgentListResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return &wire.AgentListResponse{Agents: f.defs}, nil
}
func (f *fixtureControl) Ping(context.Context, *wire.PingRequest, ...callopt.Option) (*wire.PingResponse, error) {
	return &wire.PingResponse{Ready: true}, nil
}
func (f *fixtureControl) ReportRuntime(_ context.Context, r *wire.RuntimeReport, _ ...callopt.Option) (*wire.Ack, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.states = append(f.states, r.RuntimeState)
	return &wire.Ack{Ok: true}, nil
}

func (f *fixtureControl) GetAgent(_ context.Context, r *wire.AgentRequest, _ ...callopt.Option) (*wire.AgentResponse, error) {
	return &wire.AgentResponse{Agent: &wire.AgentDefinition{Id: r.AgentId, Enabled: true, Revision: 1}}, nil
}

type fixtureIdentity struct{ identityservice.Client }

func (*fixtureIdentity) IssuePrincipalToken(context.Context, *im.PrincipalTokenRequest, ...callopt.Option) (*im.PrincipalTokenResponse, error) {
	return &im.PrincipalTokenResponse{Ok: true, Token: strings.Repeat("a", 64), ExpiresAt: time.Now().Add(15 * time.Minute).Format(time.RFC3339Nano)}, nil
}

type fixtureSandbox struct {
	sandboxservice.Client
	mu    sync.Mutex
	calls int
}

func (f *fixtureSandbox) Exec(_ context.Context, r *wire.SandboxExecRequest, _ ...callopt.Option) (*wire.SandboxExecResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	return &wire.SandboxExecResponse{Ok: true, Stdout: "workspace-ready\n"}, nil
}
func (*fixtureSandbox) Cancel(context.Context, *wire.CancelRequest, ...callopt.Option) (*wire.Ack, error) {
	return &wire.Ack{Ok: true}, nil
}

type fixtureGateway struct {
	modelgatewayservice.Client
	mu           sync.Mutex
	ready        bool
	calls        int
	requests     [][]byte
	summaryCalls int
}

func (f *fixtureGateway) Ping(context.Context, *wire.PingRequest, ...callopt.Option) (*wire.PingResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	enabled := f.ready
	return &wire.PingResponse{Ready: true, Configured: &enabled, Enabled: &enabled}, nil
}
func (*fixtureGateway) Cancel(context.Context, *wire.CancelRequest, ...callopt.Option) (*wire.Ack, error) {
	return &wire.Ack{Ok: true}, nil
}
func (f *fixtureGateway) Chat(_ context.Context, r *wire.GatewayChatRequest, _ ...callopt.Option) (*wire.GatewayChatResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	f.requests = append(f.requests, []byte(r.RequestJson))
	var request struct {
		Messages []chatMessage `json:"messages"`
		Tools    []chatTool    `json:"tools"`
	}
	if err := json.Unmarshal([]byte(r.RequestJson), &request); err != nil {
		return nil, err
	}
	result := chatMessage{Role: "assistant", Content: "PRIVATE_FINAL_NEVER_POST"}
	if len(request.Tools) == 0 {
		f.summaryCalls++
		result.Content = "Continuous memory MEMO-42 with original source conversation IDs retained."
	} else {
		var input Input
		for _, m := range request.Messages {
			if strings.HasPrefix(m.Content, "TEAMIE_NEW_MESSAGE ") {
				_ = json.Unmarshal([]byte(strings.TrimPrefix(m.Content, "TEAMIE_NEW_MESSAGE ")), &input)
			}
		}
		last := request.Messages[len(request.Messages)-1]
		if last.Role != "tool" && !strings.Contains(input.Body, "silent") {
			args, _ := json.Marshal(sendArgs{ConversationID: input.ConversationID, Body: "ack " + input.Body, ReplyToID: input.MessageID})
			result.Content = ""
			result.ToolCalls = []schema.ToolCall{{ID: "call_" + input.MessageID, Type: "function", Function: schema.FunctionCall{Name: "im_send_message", Arguments: string(args)}}}
		}
	}
	b, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"message": result, "finish_reason": "stop"}}, "usage": map[string]int{"prompt_tokens": 20, "completion_tokens": 5, "total_tokens": 25}})
	raw := string(b)
	return &wire.GatewayChatResponse{Ok: true, ResponseJson: &raw}, nil
}

type fixtureIM struct {
	mu                             sync.Mutex
	agent, workspace, conversation string
	events                         []map[string]any
	messages                       map[string]*Message
	sent                           map[string]Message
	failAfterSend                  bool
	posts                          int
	reads                          int
	failReadAt                     int
}

func newFixtureIM(t *testing.T) (*fixtureIM, *httptest.Server) {
	t.Helper()
	f := &fixtureIM{agent: uuid.NewString(), workspace: uuid.NewString(), conversation: uuid.NewString(), messages: map[string]*Message{}, sent: map[string]Message{}}
	server := httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(server.Close)
	return f, server
}
func (f *fixtureIM) add(body, conversation string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if conversation == "" {
		conversation = f.conversation
	}
	id := uuid.NewString()
	m := &Message{ID: id, ConversationID: conversation, SenderID: uuid.NewString(), SenderName: "Alice", SenderKind: "human", Body: body, Seq: int64(len(f.events) + 1), CreatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	f.messages[id] = m
	f.events = append(f.events, map[string]any{"id": fmt.Sprint(len(f.events) + 1), "type": "message.created", "workspaceId": f.workspace, "conversationId": conversation, "message": m})
}
func (f *fixtureIM) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.Header.Get("Authorization") != "Bearer "+strings.Repeat("a", 64) || r.Header.Get("X-Teamie-Service-Key") != "fixture-im-secret" {
		w.WriteHeader(401)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	write := func(v any) { _ = json.NewEncoder(w).Encode(v) }
	if r.URL.Path == "/api/v1/sync" {
		cursor := 0
		_, _ = fmt.Sscan(r.URL.Query().Get("cursor"), &cursor)
		if cursor > len(f.events) {
			w.WriteHeader(400)
			return
		}
		write(map[string]any{"events": f.events[cursor:], "cursor": fmt.Sprint(len(f.events)), "hasMore": false})
		return
	}
	if r.URL.Path == "/api/v1/bootstrap" {
		write(Bootstrap{Conversations: []Conversation{{ID: f.conversation, WorkspaceID: f.workspace}}, SyncCursor: fmt.Sprint(len(f.events))})
		return
	}
	parts := strings.Split(r.URL.Path, "/")
	if len(parts) == 7 && r.Method == "GET" {
		f.reads++
		if f.failReadAt == f.reads {
			w.WriteHeader(503)
			return
		}
		m := f.messages[parts[6]]
		if m == nil || m.ConversationID != parts[4] {
			w.WriteHeader(404)
			return
		}
		write(map[string]any{"message": m})
		return
	}
	if len(parts) == 6 && r.Method == "POST" {
		var body struct {
			ClientMessageID string  `json:"clientMessageId"`
			Body            string  `json:"body"`
			ReplyToID       *string `json:"replyToId"`
		}
		if json.NewDecoder(r.Body).Decode(&body) != nil {
			w.WriteHeader(400)
			return
		}
		f.posts++
		m, exists := f.sent[body.ClientMessageID]
		if !exists {
			m = Message{ID: uuid.NewString(), ConversationID: parts[4], SenderID: f.agent, SenderKind: "agent", Body: body.Body}
			f.sent[body.ClientMessageID] = m
		}
		if f.failAfterSend {
			f.failAfterSend = false
			w.WriteHeader(503)
			return
		}
		write(map[string]any{"message": m})
		return
	}
	w.WriteHeader(404)
}
func fixtureEngine(t *testing.T, s *Store) (*Engine, *fixtureIM, *fixtureGateway, *fixtureControl) {
	t.Helper()
	imf, httpserver := newFixtureIM(t)
	gw := &fixtureGateway{ready: true}
	cp := &fixtureControl{}
	return &Engine{Store: s, Config: Config{IMURL: httpserver.URL, IMToken: "fixture-im-secret", ControlToken: "fixture-cp-secret", GatewayToken: "fixture-gw-secret", SandboxToken: "fixture-sb-secret"}, Control: cp, Gateway: gw, Sandbox: &fixtureSandbox{}, Identity: &fixtureIdentity{}, slots: make(chan struct{}, 1), owner: uuid.NewString()}, imf, gw, cp
}
func startFixtureWorker(t *testing.T, e *Engine, f *fixtureIM) (context.CancelFunc, chan struct{}) {
	t.Helper()
	lease, err := e.Store.Acquire(context.Background(), f.agent, e.owner)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		e.work(ctx, &wire.AgentDefinition{Id: f.agent, OwnerId: "fixture-owner", Name: "Agent", Enabled: true, Revision: 1}, lease)
		e.Store.Release(context.Background(), lease)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Error("worker did not stop")
		}
	})
	return cancel, done
}

func TestNativeEinoToolsOnlyReplyAndGlobalContinuousSession(t *testing.T) {
	s := testStore(t)
	e, f, gw, _ := fixtureEngine(t, s)
	f.add("MEMO-42", "")
	cancel, done := startFixtureWorker(t, e, f)
	waitFor(t, func() bool { return count(t, s, "SELECT count(*) FROM runtime_runs WHERE state='completed'") == 1 })
	f.mu.Lock()
	if len(f.sent) != 1 {
		t.Fatalf("expected one explicit tool reply, got %d", len(f.sent))
	}
	for _, m := range f.sent {
		if strings.Contains(m.Body, "PRIVATE_FINAL") {
			t.Fatal("ordinary model text posted")
		}
	}
	f.mu.Unlock()
	f.add("silent in second conversation", uuid.NewString())
	waitFor(t, func() bool { return count(t, s, "SELECT count(*) FROM runtime_runs WHERE state='completed'") == 2 })
	gw.mu.Lock()
	last := string(gw.requests[len(gw.requests)-1])
	gw.mu.Unlock()
	if !strings.Contains(last, "MEMO-42") || !strings.Contains(last, "second conversation") {
		t.Fatal("continuous session lost prior conversation context")
	}
	f.mu.Lock()
	if len(f.sent) != 1 {
		t.Fatal("silence generated an automatic IM message")
	}
	f.mu.Unlock()
	cancel()
	<-done
	if count(t, s, "SELECT count(DISTINCT session_id) FROM runtime_session_events") != 1 {
		t.Fatal("Agent split into multiple sessions")
	}
}
func TestRestartReplaysCommittedModelAndSendIdempotently(t *testing.T) {
	s := testStore(t)
	e, f, gw, _ := fixtureEngine(t, s)
	f.failAfterSend = true
	f.add("crash window", "")
	cancel, done := startFixtureWorker(t, e, f)
	waitFor(t, func() bool { return count(t, s, "SELECT count(*) FROM runtime_runs WHERE state='retry'") == 1 })
	cancel()
	<-done
	gw.mu.Lock()
	calls := gw.calls
	gw.mu.Unlock()
	if calls != 1 {
		t.Fatalf("expected only cached first model call, got %d", calls)
	}
	_, err := s.DB.Exec(context.Background(), "UPDATE runtime_runs SET next_attempt=now()")
	if err != nil {
		t.Fatal(err)
	}
	cancel2, done2 := startFixtureWorker(t, e, f)
	waitFor(t, func() bool { return count(t, s, "SELECT count(*) FROM runtime_runs WHERE state='completed'") == 1 })
	cancel2()
	<-done2
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.sent) != 1 || f.posts != 2 {
		t.Fatalf("send recovery: unique=%d attempts=%d", len(f.sent), f.posts)
	}
	gw.mu.Lock()
	defer gw.mu.Unlock()
	if gw.calls != calls+1 {
		t.Fatalf("recovery recalled completed first generation: %d", gw.calls)
	}
}
func TestUnconfiguredPersistsInputsWithoutModelCalls(t *testing.T) {
	s := testStore(t)
	e, f, gw, cp := fixtureEngine(t, s)
	gw.ready = false
	f.add("wait for model", "")
	cancel, done := startFixtureWorker(t, e, f)
	waitFor(t, func() bool {
		cp.mu.Lock()
		defer cp.mu.Unlock()
		for _, v := range cp.states {
			if v == "waiting_configuration" {
				return true
			}
		}
		return false
	})
	if count(t, s, "SELECT count(*) FROM runtime_inputs WHERE state='pending'") != 1 {
		t.Fatal("unconfigured input was lost")
	}
	gw.mu.Lock()
	if gw.calls != 0 {
		t.Fatal("model called before configuration")
	}
	gw.ready = true
	gw.mu.Unlock()
	waitFor(t, func() bool { return count(t, s, "SELECT count(*) FROM runtime_runs WHERE state='completed'") == 1 })
	cancel()
	<-done
}
func TestLeaseFencingAndImmutableReplay(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	id := uuid.NewString()
	first, err := s.Acquire(ctx, id, "first")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Acquire(ctx, id, "second"); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatal("parallel lease accepted")
	}
	_, err = s.DB.Exec(ctx, "UPDATE runtime_agents SET lease_until=now()-interval '1 second' WHERE agent_id=$1", id)
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.Acquire(ctx, id, "second")
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Ingest(ctx, first, "1", []Input{{EventID: "1"}}); !errors.Is(err, ErrLeaseLost) {
		t.Fatal("stale fence wrote")
	}
	if err = s.Ingest(ctx, second, "1", []Input{{EventID: "1", Body: "original"}}); err != nil {
		t.Fatal(err)
	}
	run, err := s.StartRun(ctx, second, []Input{{EventID: "1", Body: "original"}})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.SaveModel(ctx, second, run, 1, "hash", []byte("response")); err != nil {
		t.Fatal(err)
	}
	if _, err = s.CachedModel(ctx, run, 1, "changed"); !errors.Is(err, ErrReplayConflict) {
		t.Fatal("divergent model replay accepted")
	}
}
func TestSummaryKeepsCompleteToolGroupAndSourceIdentity(t *testing.T) {
	input := []*schema.Message{{Role: schema.System, Content: "system"}, {Role: schema.User, Content: "source conversation ABC"}, {Role: schema.Assistant, ToolCalls: []schema.ToolCall{{ID: "x"}}}, {Role: schema.Tool, ToolCallID: "x", Content: "receipt"}}
	out, err := finalizeSummary(context.Background(), input, &schema.Message{Content: "source ABC retained"})
	if err != nil || len(out) != 4 || out[1] != input[1] {
		t.Fatal("summary lost current source/tool group", err)
	}
	if _, err = finalizeSummary(context.Background(), input, &schema.Message{}); err == nil {
		t.Fatal("empty summary accepted")
	}
}
func TestOpenAIAdapterOmitsPrivateFieldsAndRejectsBrokenTools(t *testing.T) {
	b, err := chatRequest([]*schema.Message{{Role: schema.User, Content: "hello", Extra: map[string]any{"secret": "do-not-send"}}}, nil, &model.Options{})
	if err != nil || strings.Contains(string(b), "do-not-send") {
		t.Fatal("private schema metadata leaked")
	}
	_, err = parseCompletion([]byte(`{"choices":[{"message":{"tool_calls":[{"id":"","function":{"name":"im_send_message","arguments":"{}"}}]}}]}`))
	if err == nil {
		t.Fatal("unidentified tool call accepted")
	}
	var cancelled error = &adk.CancelError{}
	if !retryError(cancelled) {
		t.Fatal("cancelled work must remain durable and retryable")
	}
}

func TestTransientFreshnessFailureRetainsWholeDurableBatch(t *testing.T) {
	s := testStore(t)
	e, f, _, _ := fixtureEngine(t, s)
	f.add("silent first", "")
	f.add("silent second", "")
	f.failReadAt = 3
	cancel, done := startFixtureWorker(t, e, f)
	waitFor(t, func() bool { return count(t, s, "SELECT count(*) FROM runtime_runs WHERE state='retry'") == 1 })
	if count(t, s, "SELECT count(*) FROM runtime_inputs WHERE state='processing'") != 2 || count(t, s, "SELECT count(*) FROM runtime_inputs WHERE state='failed'") != 0 {
		t.Fatal("temporary freshness outage dropped durable inputs")
	}
	_, err := s.DB.Exec(context.Background(), "UPDATE runtime_runs SET next_attempt=now()")
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return count(t, s, "SELECT count(*) FROM runtime_inputs WHERE state='done'") == 2 })
	cancel()
	<-done
}
func TestNativeCompactionPersistsAcrossWorkerRestart(t *testing.T) {
	s := testStore(t)
	e, f, gw, _ := fixtureEngine(t, s)
	for i := 0; i < 40; i++ {
		f.add(fmt.Sprintf("silent MEMO-42 batch %d", i), "")
	}
	cancel, done := startFixtureWorker(t, e, f)
	waitFor(t, func() bool { return count(t, s, "SELECT count(*) FROM runtime_inputs WHERE state='done'") == 40 })
	cancel()
	<-done
	if count(t, s, "SELECT count(*) FROM runtime_session_events WHERE kind='messages_replaced'") == 0 {
		t.Fatal("native summarization did not persist its replacement event")
	}
	f.add("silent after restart", "")
	cancel2, done2 := startFixtureWorker(t, e, f)
	waitFor(t, func() bool { return count(t, s, "SELECT count(*) FROM runtime_inputs WHERE state='done'") == 41 })
	cancel2()
	<-done2
	gw.mu.Lock()
	defer gw.mu.Unlock()
	if gw.summaryCalls == 0 || !strings.Contains(string(gw.requests[len(gw.requests)-1]), "TEAMIE_SESSION_SUMMARY") {
		t.Fatal("compressed session did not survive restart")
	}
}
func TestInvalidatedBatchRequeuesUnrelatedInputs(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	l, err := s.Acquire(ctx, uuid.NewString(), "worker")
	if err != nil {
		t.Fatal(err)
	}
	inputs := []Input{{EventID: "1", Body: "valid"}, {EventID: "2", Body: "revoked"}}
	if err = s.Ingest(ctx, l, "2", inputs); err != nil {
		t.Fatal(err)
	}
	r, err := s.StartRun(ctx, l, inputs)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.InvalidateRun(ctx, l, r, []string{"2"}); err != nil {
		t.Fatal(err)
	}
	pending, err := s.Pending(ctx, l)
	if err != nil || len(pending) != 1 || pending[0].EventID != "1" {
		t.Fatal("unrelated input lost", err)
	}
	fresh, err := s.StartRun(ctx, l, pending)
	if err != nil || fresh.ID == r.ID {
		t.Fatal("invalid replay tape reused", err)
	}
}

func TestHistoryBudgetPreservesPaginationAndFilteredCursor(t *testing.T) {
	messages := []Message{}
	for i := int64(1); i <= 8; i++ {
		messages = append(messages, Message{Seq: i, Body: strings.Repeat("文", 8000)})
	}
	out, cursor, more := historyPage(messages, false, "")
	if len(out) != 2 || cursor != 7 || !more || out[0].Seq != 7 || out[1].Seq != 8 {
		t.Fatalf("history page lost/skipped sequence: %d %d %v", len(out), cursor, more)
	}
	out, cursor, more = historyPage(messages, false, "absent")
	if len(out) != 0 || cursor != 7 || !more {
		t.Fatal("empty filter page cannot advance")
	}
}
func TestGatewayAttemptIdentityAllowsRetryAfterCancellation(t *testing.T) {
	r := &Run{ID: uuid.NewString(), Attempt: uuid.NewString()}
	first := executionID(r)
	r.Attempt = uuid.NewString()
	if executionID(r) == first || !strings.HasPrefix(executionID(r), r.ID+":") {
		t.Fatal("attempt cancellation poisons a future retry")
	}
}
