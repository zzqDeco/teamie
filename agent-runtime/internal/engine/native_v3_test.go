package engine

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cloudwego/eino/adk"
	bg "github.com/cloudwego/eino/adk/backgroundtask"
	bgshell "github.com/cloudwego/eino/adk/backgroundtask/shell"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	"github.com/cloudwego/kitex/client/callopt"
	"github.com/google/uuid"
	wire "teamie/agent-protocol/kitex_gen/agent"
	"teamie/agent-protocol/kitex_gen/agent/sandboxservice"
)

type asyncSandboxFixture struct {
	sandboxservice.Client
	mu            sync.Mutex
	starts        map[string]int
	busy          int
	active        string
	released      bool
	unknownStart  bool
	stops         int
	startDelay    time.Duration
	loseReplyOnce bool
	queuedUntil   map[string]time.Time
}

func (f *asyncSandboxFixture) Start(_ context.Context, r *wire.SandboxStartRequest, _ ...callopt.Option) (*wire.SandboxTaskResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.starts == nil {
		f.starts = map[string]int{}
	}
	if f.unknownStart {
		return nil, errors.New("response lost")
	}
	if f.active != "" && f.active != r.TaskId && !f.released {
		f.busy++
		code := "sandbox_busy"
		return &wire.SandboxTaskResponse{TaskId: r.TaskId, State: "not_started", ErrorCode: &code}, nil
	}
	if f.starts[r.TaskId] == 0 {
		f.starts[r.TaskId]++
		f.active = r.TaskId
		if f.queuedUntil == nil {
			f.queuedUntil = map[string]time.Time{}
		}
		f.queuedUntil[r.TaskId] = time.Now().Add(f.startDelay)
	}
	if f.loseReplyOnce {
		f.loseReplyOnce = false
		return nil, errors.New("accepted start response lost")
	}
	state := "running"
	if time.Now().Before(f.queuedUntil[r.TaskId]) {
		state = "not_started"
	}
	return &wire.SandboxTaskResponse{Ok: true, TaskId: r.TaskId, State: state}, nil
}
func (f *asyncSandboxFixture) Get(_ context.Context, r *wire.SandboxTaskRequest, _ ...callopt.Option) (*wire.SandboxTaskResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.starts[r.TaskId] == 0 {
		code := "task_not_found"
		return &wire.SandboxTaskResponse{TaskId: r.TaskId, State: "not_started", ErrorCode: &code}, nil
	}
	if time.Now().Before(f.queuedUntil[r.TaskId]) {
		return &wire.SandboxTaskResponse{Ok: true, TaskId: r.TaskId, State: "not_started"}, nil
	}
	if !f.released {
		return &wire.SandboxTaskResponse{Ok: true, TaskId: r.TaskId, State: "running"}, nil
	}
	stdout := "PRIVATE_BACKGROUND_STDOUT"
	exit := int32(0)
	return &wire.SandboxTaskResponse{Ok: true, TaskId: r.TaskId, State: "completed", Stdout: &stdout, ExitCode: &exit}, nil
}
func (f *asyncSandboxFixture) ReadOutput(ctx context.Context, r *wire.SandboxTaskRequest, opts ...callopt.Option) (*wire.SandboxTaskResponse, error) {
	out, err := f.Get(ctx, r, opts...)
	if err != nil {
		return nil, err
	}
	total := int64(len(out.GetStdout()) + len(out.GetStderr()))
	offset := r.GetOffset()
	text := out.GetStdout() + out.GetStderr()
	if offset > total {
		offset = total
	}
	next := total
	if r.GetLimit() > 0 && next-offset > int64(r.GetLimit()) {
		next = offset + int64(r.GetLimit())
	}
	chunk := text[offset:next]
	out.Stdout = &chunk
	empty := ""
	out.Stderr = &empty
	out.NextOffset = &next
	return out, nil
}
func (f *asyncSandboxFixture) CancelTask(_ context.Context, r *wire.SandboxTaskRequest, _ ...callopt.Option) (*wire.SandboxTaskResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stops++
	f.released = true
	return &wire.SandboxTaskResponse{Ok: true, TaskId: r.TaskId, State: "canceled"}, nil
}
func (f *asyncSandboxFixture) Cancel(context.Context, *wire.CancelRequest, ...callopt.Option) (*wire.Ack, error) {
	return &wire.Ack{Ok: true}, nil
}
func (f *asyncSandboxFixture) release() { f.mu.Lock(); defer f.mu.Unlock(); f.released = true }

type nativeGatewayFixture struct {
	*fixtureGateway
	mu           sync.Mutex
	calls        int
	taskSeen     bool
	privateFinal string
}

func (f *nativeGatewayFixture) Chat(_ context.Context, r *wire.GatewayChatRequest, _ ...callopt.Option) (*wire.GatewayChatResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	var req toolPathRequest
	_ = json.Unmarshal([]byte(r.RequestJson), &req)
	message := chatMessage{Role: "assistant", Content: "PRIVATE_NATIVE_FINAL"}
	last := req.Messages[len(req.Messages)-1]
	if last.Role == "tool" {
		if strings.Contains(last.Content, "task_id") {
			f.taskSeen = true
		}
	} else if !strings.Contains(last.Content, "TEAMIE_TASK_NOTIFICATION") && f.calls == 1 {
		message, _ = toolPathCall("workspace_execute", "native-command", map[string]any{"command": "printf PRIVATE_NATIVE_COMMAND", "run_in_background": false})
	}
	b, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"message": message, "finish_reason": "stop"}}})
	raw := string(b)
	return &wire.GatewayChatResponse{Ok: true, ResponseJson: &raw}, nil
}
func TestNativeTaskCrashBeforeToolReceiptAndCompletionWakeup(t *testing.T) {
	s := testStore(t)
	e, f, base, _ := fixtureEngine(t, s)
	sb := &asyncSandboxFixture{}
	e.Sandbox = sb
	gateway := &nativeGatewayFixture{fixtureGateway: base}
	e.Gateway = gateway
	// This fails the persistence boundary after native Manager.Submit and external
	// Start but before the native launch result becomes a durable tool receipt.
	_, err := s.DB.Exec(context.Background(), `CREATE FUNCTION crash_native_receipt() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.name='native:workspace_execute' AND NEW.result IS NOT NULL THEN RAISE EXCEPTION 'fixture database disconnect' USING ERRCODE='08006';END IF;RETURN NEW;END $$;CREATE TRIGGER crash_native_receipt BEFORE UPDATE ON runtime_tool_calls FOR EACH ROW EXECUTE FUNCTION crash_native_receipt()`)
	if err != nil {
		t.Fatal(err)
	}
	f.add("silent native task", "")
	cancel, done := startFixtureWorker(t, e, f)
	waitFor(t, func() bool {
		return count(t, s, `SELECT count(*) FROM runtime_tasks`) == 1 && count(t, s, `SELECT count(*) FROM runtime_runs WHERE state='retry'`) == 1
	})
	cancel()
	<-done
	if count(t, s, `SELECT count(*) FROM runtime_tool_calls WHERE name='native:workspace_execute' AND result IS NOT NULL`) != 0 {
		t.Fatal("fixture did not cut before launch receipt")
	}
	if count(t, s, `SELECT count(*) FROM runtime_session_events WHERE kind=$1`, string(bg.SessionEventTaskCreated)) != 1 {
		t.Fatal("task creation was not durably in the native timeline")
	}
	if _, err = s.DB.Exec(context.Background(), `DROP TRIGGER crash_native_receipt ON runtime_tool_calls;UPDATE runtime_runs SET next_attempt=now()`); err != nil {
		t.Fatal(err)
	}
	sb.release()
	cancel, done = startFixtureWorker(t, e, f)
	defer func() { cancel(); <-done }()
	waitFor(t, func() bool {
		return count(t, s, `SELECT count(*) FROM runtime_runs WHERE state='completed'`) >= 2 && count(t, s, `SELECT count(*) FROM runtime_tasks WHERE snapshot->>'Status'='completed'`) == 1 && count(t, s, `SELECT count(*) FROM runtime_inputs WHERE notification_id IS NOT NULL`) == 2
	})
	if count(t, s, `SELECT count(*) FROM runtime_tasks`) != 1 || count(t, s, `SELECT count(*) FROM runtime_session_events WHERE kind=$1`, string(bg.SessionEventTaskCreated)) != 1 {
		t.Fatal("restart duplicated a durable task or creation event")
	}
	sb.mu.Lock()
	starts := 0
	for _, n := range sb.starts {
		starts += n
	}
	sb.mu.Unlock()
	if starts != 1 {
		t.Fatalf("external effect started %d times", starts)
	}
	if count(t, s, `SELECT count(*) FROM runtime_inputs WHERE notification_id IS NOT NULL`) != 2 {
		t.Fatal("task-created and completion notifications did not become deduped inputs")
	}
	f.mu.Lock()
	posts := f.posts
	f.mu.Unlock()
	if posts != 0 {
		t.Fatal("native task/model output became public IM")
	}
}
func TestSandboxBusyWaitsDurablyAndUnknownDoesNotRestart(t *testing.T) {
	s := testStore(t)
	agent := uuid.NewString()
	lease, err := s.Acquire(context.Background(), agent, "worker")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Release(context.Background(), lease)
	inputs := []Input{{EventID: "1", Body: "silent"}}
	if err = s.Ingest(context.Background(), lease, "1", inputs); err != nil {
		t.Fatal(err)
	}
	run, err := s.StartRun(context.Background(), lease, inputs)
	if err != nil {
		t.Fatal(err)
	}
	store := &TaskStore{Store: s, AgentID: agent, Lease: lease}
	for _, id := range []string{"first", "second"} {
		task, err := store.Create(context.Background(), &bg.CreateTaskRequest{Spec: bg.Spec{ID: id, ExecutorKey: "fixture"}, LeaseExpiryPolicy: bg.LeaseExpiryRetry})
		if err != nil {
			t.Fatal(err)
		}
		if _, err = store.Start(context.Background(), &bg.StartTaskRequest{TaskID: id, ExpectedVersion: task.Version}); err != nil {
			t.Fatal(err)
		}
	}
	sb := &asyncSandboxFixture{}
	backend := &SandboxBackend{Client: sb, Token: "fixture", AgentID: agent, Store: s, Lease: lease}
	ctx := WithRun(context.Background(), run)
	first, err := backend.StartCommand(ctx, &bgshellStart{TaskID: "first", Command: "one"})
	if err != nil {
		t.Fatal(err)
	}
	_ = first
	result := make(chan error, 1)
	go func() {
		_, err := backend.StartCommand(ctx, &bgshellStart{TaskID: "second", Command: "two"})
		result <- err
	}()
	waitFor(t, func() bool { sb.mu.Lock(); defer sb.mu.Unlock(); return sb.busy > 0 })
	select {
	case err := <-result:
		t.Fatalf("busy prematurely ended task: %v", err)
	default:
	}
	sb.release()
	select {
	case err := <-result:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("queued command did not start after capacity became free")
	}
	sb.mu.Lock()
	if sb.starts["first"] != 1 || sb.starts["second"] != 1 {
		t.Fatal("busy queue duplicated commands")
	}
	sb.unknownStart = true
	sb.mu.Unlock()
	ambiguous, err := store.Create(ctx, &bg.CreateTaskRequest{Spec: bg.Spec{ID: "ambiguous", ExecutorKey: "fixture"}, LeaseExpiryPolicy: bg.LeaseExpiryRetry})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.Start(ctx, &bg.StartTaskRequest{TaskID: ambiguous.Spec.ID, ExpectedVersion: ambiguous.Version}); err != nil {
		t.Fatal(err)
	}
	short, cancel := context.WithTimeout(ctx, 400*time.Millisecond)
	defer cancel()
	if _, err = backend.StartCommand(short, &bgshellStart{TaskID: "ambiguous", Command: "side effect"}); err == nil {
		t.Fatal("ambiguous start reported success")
	}
	sb.mu.Lock()
	defer sb.mu.Unlock()
	if sb.starts["ambiguous"] != 0 {
		t.Fatal("unknown start was replayed")
	}
}

// Adapter alias retains the real native protocol type in compile-time tests.
type bgshellStart = bgshell.StartCommandRequest

func TestGatewayActualIncrementalStreamAndTape(t *testing.T) {
	s := testStore(t)
	agent := uuid.NewString()
	l, err := s.Acquire(context.Background(), agent, "stream")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Release(context.Background(), l)
	if err = s.Ingest(context.Background(), l, "1", []Input{{EventID: "1", Body: "silent"}}); err != nil {
		t.Fatal(err)
	}
	run, err := s.StartRun(context.Background(), l, []Input{{EventID: "1", Body: "silent"}})
	if err != nil {
		t.Fatal(err)
	}
	firstSent := make(chan struct{})
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer fixture-stream" {
			t.Error("missing stream token")
		}
		var request map[string]string
		_ = json.NewDecoder(r.Body).Decode(&request)
		if !strings.Contains(request["requestJson"], `"stream":true`) {
			t.Error("not upstream streaming")
		}
		send := func(content string) {
			chunk, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"role": "assistant", "content": content}}}})
			_ = json.NewEncoder(w).Encode(map[string]string{"type": "chunk", "responseJson": string(chunk)})
			w.(http.Flusher).Flush()
		}
		send("private ")
		close(firstSent)
		<-release
		send("stream")
		_ = json.NewEncoder(w).Encode(map[string]string{"type": "done"})
	}))
	defer server.Close()
	gateway := &GatewayModel{Store: s, Lease: l, AgentID: agent, OwnerID: uuid.NewString(), Token: "fixture-stream", StreamURL: server.URL}
	stream, err := gateway.Stream(WithRun(context.Background(), run), []*schema.Message{{Role: schema.User, Content: "silent"}}, model.WithMaxTokens(20))
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	<-firstSent
	chunk, err := stream.Recv()
	if err != nil || chunk.Content != "private " {
		t.Fatal("first chunk was buffered until completion", err)
	}
	if count(t, s, `SELECT count(*) FROM runtime_model_calls`) != 0 {
		t.Fatal("partial stream became durable complete response")
	}
	close(release)
	var text strings.Builder
	text.WriteString(chunk.Content)
	for {
		chunk, err = stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		text.WriteString(chunk.Content)
	}
	if text.String() != "private stream" || count(t, s, `SELECT count(*) FROM runtime_model_calls`) != 1 {
		t.Fatal("complete stream was not committed as one model tape")
	}
}

type blockingGatewayFixture struct {
	*fixtureGateway
	first    chan struct{}
	release  chan struct{}
	mu       sync.Mutex
	requests []toolPathRequest
}

func (g *blockingGatewayFixture) Chat(ctx context.Context, r *wire.GatewayChatRequest, _ ...callopt.Option) (*wire.GatewayChatResponse, error) {
	var request toolPathRequest
	_ = json.Unmarshal([]byte(r.RequestJson), &request)
	g.mu.Lock()
	g.requests = append(g.requests, request)
	first := len(g.requests) == 1
	g.mu.Unlock()
	if first {
		close(g.first)
		select {
		case <-g.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	raw := `{"choices":[{"message":{"role":"assistant","content":"PRIVATE_INPUT_RESULT"},"finish_reason":"stop"}]}`
	return &wire.GatewayChatResponse{Ok: true, ResponseJson: &raw}, nil
}
func TestInputArrivesWhileModelRunsBeyondProcessingPage(t *testing.T) {
	s := testStore(t)
	e, f, base, _ := fixtureEngine(t, s)
	gateway := &blockingGatewayFixture{fixtureGateway: base, first: make(chan struct{}), release: make(chan struct{})}
	e.Gateway = gateway
	for i := 0; i < 4; i++ {
		f.add("silent initial "+string(rune('a'+i)), "")
	}
	cancel, done := startFixtureWorker(t, e, f)
	defer func() { cancel(); <-done }()
	select {
	case <-gateway.first:
	case <-time.After(5 * time.Second):
		t.Fatal("model did not start")
	}
	f.add("silent relevant new input", "")
	waitFor(t, func() bool { return count(t, s, `SELECT count(*) FROM runtime_inputs WHERE state='pending'`) == 1 })
	// The processing batch fills the former LIMIT 4 page. The fifth input must
	// already be pushed into the active native loop before the model completes.
	time.Sleep(100 * time.Millisecond)
	close(gateway.release)
	waitFor(t, func() bool {
		return count(t, s, `SELECT count(*) FROM runtime_runs WHERE state='interrupted' AND safe_error='new_input'`) == 1 && count(t, s, `SELECT count(*) FROM runtime_runs WHERE state='completed'`) == 1
	})
	if count(t, s, `SELECT count(*) FROM runtime_inputs WHERE state='done'`) != 5 {
		t.Fatal("preemption lost source inputs")
	}
	gateway.mu.Lock()
	defer gateway.mu.Unlock()
	if len(gateway.requests) != 2 {
		t.Fatalf("unexpected generation count %d", len(gateway.requests))
	}
	body, _ := json.Marshal(gateway.requests[1])
	for _, id := range []string{f.workspace, f.conversation, "silent relevant new input"} {
		if !strings.Contains(string(body), id) {
			t.Fatalf("fresh model context lost %s", id)
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.posts != 0 {
		t.Fatal("private final text was posted")
	}
}

func TestNativeTargetedResumeUsesCompatibleCheckpoint(t *testing.T) {
	s := testStore(t)
	e, f, base, _ := fixtureEngine(t, s)
	gateway := &blockingGatewayFixture{fixtureGateway: base, first: make(chan struct{}), release: make(chan struct{})}
	e.Gateway = gateway
	path := t.TempDir() + "/flags.json"
	e.Config.FlagsFile = path
	writeFlags := func(enabled bool) {
		t.Helper()
		b, _ := json.Marshal(map[string]any{"flags": map[string]bool{"agents.runtime.v3": enabled, "agents.tasks.execute": true}})
		if err := os.WriteFile(path, b, 0600); err != nil {
			t.Fatal(err)
		}
	}
	writeFlags(true)
	f.add("silent resumable source", "")
	cancel, done := startFixtureWorker(t, e, f)
	defer func() { cancel(); <-done }()
	select {
	case <-gateway.first:
	case <-time.After(5 * time.Second):
		t.Fatal("model did not start")
	}
	writeFlags(false)
	var metadata *checkpointMetadata
	waitFor(t, func() bool {
		m, err := s.ResumeMetadata(context.Background(), "turnloop:"+f.agent)
		if err == nil && m != nil && len(m.Targets) > 0 {
			metadata = m
			return true
		}
		return false
	})
	if metadata.Version != nativeRuntimeVersion || metadata.Revision != 1 || metadata.RunID == "" || metadata.ModelStep < 1 {
		t.Fatalf("incomplete native recovery provenance: %+v", metadata)
	}
	if _, err := s.DB.Exec(context.Background(), `UPDATE runtime_runs SET next_attempt=now()`); err != nil {
		t.Fatal(err)
	}
	writeFlags(true)
	waitFor(t, func() bool {
		return count(t, s, `SELECT count(*) FROM runtime_runs WHERE state='completed' AND cardinality(context_attempts)>0`) == 1
	})
	if s.checkpointExists(context.Background(), "turnloop:"+f.agent) {
		t.Fatal("successful native resume retained consumed checkpoint")
	}
}

func TestSummaryUsesModelWindowAndBoundedTransport(t *testing.T) {
	if summaryTrigger(4096) != 3072 || summaryTrigger(16384) != 12000 || summaryTrigger(2_000_000) != 12000 {
		t.Fatal("compression trigger ignores model or transport capacity")
	}
	small := &GatewayModel{ContextWindow: func() int { return 4096 }}
	options := &model.Options{}
	small.capOutput(options)
	if options.MaxTokens == nil || *options.MaxTokens != 1024 {
		t.Fatal("small model output reserve not respected")
	}
	large := []Input{}
	for i := 0; i < 8; i++ {
		large = append(large, Input{EventID: strconv.Itoa(i), Body: strings.Repeat("界", 8000)})
	}
	batch, remaining := boundedInputBatch(large)
	if len(batch) != 2 || len(remaining) != 6 {
		t.Fatal("transport batch not bounded")
	}
	sources := []*schema.Message{}
	for _, in := range batch {
		in.WorkspaceID = uuid.NewString()
		in.ConversationID = uuid.NewString()
		in.MessageID = uuid.NewString()
		in.SenderID = uuid.NewString()
		sources = append(sources, inputMessages([]Input{in})...)
	}
	compacted, err := finalizeSummary(context.Background(), sources, &schema.Message{Role: schema.Assistant, Content: "A semantic summary omitting every source ID"})
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range sources {
		var in Input
		_ = json.Unmarshal([]byte(strings.TrimPrefix(m.Content, "TEAMIE_NEW_MESSAGE ")), &in)
		if !strings.Contains(compacted[0].Content, in.MessageID) || !strings.Contains(compacted[0].Content, in.ConversationID) || !strings.Contains(compacted[0].Content, in.SenderID) {
			t.Fatal("compaction relied on model to retain provenance")
		}
	}
}

func TestNativeTaskAcceptedSlowSandboxStartWaits(t *testing.T) {
	s := testStore(t)
	e, f, base, _ := fixtureEngine(t, s)
	sb := &asyncSandboxFixture{startDelay: time.Second, released: true}
	e.Sandbox = sb
	e.Gateway = &nativeGatewayFixture{fixtureGateway: base}
	f.add("silent slow native command", "")
	cancel, done := startFixtureWorker(t, e, f)
	defer func() { cancel(); <-done }()
	waitFor(t, func() bool {
		return count(t, s, `SELECT count(*) FROM runtime_tasks WHERE snapshot->>'Status'='running'`) == 1
	})
	if count(t, s, `SELECT count(*) FROM runtime_tasks WHERE snapshot->>'Status'='failed'`) != 0 {
		t.Fatal("accepted queued command was falsely failed")
	}
	waitFor(t, func() bool {
		return count(t, s, `SELECT count(*) FROM runtime_tasks WHERE snapshot->>'Status'='completed'`) == 1
	})
	if count(t, s, `SELECT count(*) FROM runtime_task_events`) == 0 {
		t.Fatal("real Sandbox progress was not persisted by native task event store")
	}
	sb.mu.Lock()
	defer sb.mu.Unlock()
	starts := 0
	for _, n := range sb.starts {
		starts += n
	}
	if starts != 1 {
		t.Fatal("slow start was repeated")
	}
}

type retryAfterFreshGateway struct {
	*fixtureGateway
	mu     sync.Mutex
	failed bool
}

func (g *retryAfterFreshGateway) Chat(_ context.Context, r *wire.GatewayChatRequest, _ ...callopt.Option) (*wire.GatewayChatResponse, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	var request toolPathRequest
	_ = json.Unmarshal([]byte(r.RequestJson), &request)
	var input Input
	for _, message := range request.Messages {
		if strings.HasPrefix(message.Content, "TEAMIE_NEW_MESSAGE ") {
			_ = json.Unmarshal([]byte(strings.TrimPrefix(message.Content, "TEAMIE_NEW_MESSAGE ")), &input)
		}
	}
	last := request.Messages[len(request.Messages)-1]
	result := chatMessage{Role: "assistant", Content: "PRIVATE_RETRY_FINAL"}
	if input.Body == "first source" {
		if last.Role != "tool" {
			result, _ = toolPathCall("im_send_message", "retry-stable-send", sendArgs{ConversationID: input.ConversationID, Body: "one explicit durable send", ReplyToID: input.MessageID})
		} else if !g.failed {
			g.failed = true
			code := "invalid_request"
			return &wire.GatewayChatResponse{ErrorCode: &code}, nil
		}
	}
	b, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"message": result, "finish_reason": "stop"}}})
	raw := string(b)
	return &wire.GatewayChatResponse{Ok: true, ResponseJson: &raw}, nil
}
func TestOwnerRetryWakesExistingIdleLoopAndUsesPinnedTape(t *testing.T) {
	s := testStore(t)
	e, f, base, _ := fixtureEngine(t, s)
	e.Gateway = &retryAfterFreshGateway{fixtureGateway: base}
	owner := uuid.NewString()
	e.Control = &rpcOwnerControl{definition: &wire.AgentDefinition{Id: f.agent, OwnerId: owner, Enabled: true, Revision: 1}}
	f.add("first source", "")
	cancel, done := startFixtureWorker(t, e, f)
	defer func() { cancel(); <-done }()
	waitFor(t, func() bool { return count(t, s, `SELECT count(*) FROM runtime_runs WHERE state='failed'`) == 1 })
	var runID string
	if err := s.DB.QueryRow(context.Background(), `SELECT id::text FROM runtime_runs WHERE state='failed'`).Scan(&runID); err != nil {
		t.Fatal(err)
	}
	f.add("silent newer successful input", "")
	waitFor(t, func() bool { return count(t, s, `SELECT count(*) FROM runtime_runs WHERE state='completed'`) == 1 })
	rpc := &RuntimeRPC{Engine: e}
	response, err := rpc.RetryRun(context.Background(), &wire.RuntimeRequest{ServiceToken: e.Config.ControlToken, AgentId: f.agent, OwnerId: owner, ResourceId: &runID})
	if err != nil || !response.Ok {
		t.Fatal("owner retry rejected", response, err)
	}
	waitFor(t, func() bool {
		return count(t, s, `SELECT count(*) FROM runtime_runs WHERE id=$1 AND state='completed'`, runID) == 1
	})
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.posts != 1 || len(f.sent) != 1 {
		t.Fatalf("later-head retry duplicated durable send: posts=%d sends=%d", f.posts, len(f.sent))
	}
	if count(t, s, `SELECT count(*) FROM runtime_model_calls WHERE run_id=$1`, runID) != 2 {
		t.Fatal("original model tape was not reused/completed")
	}
}

func TestSessionReplayPinsAcceptanceWatermark(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	agent := uuid.NewString()
	lease, err := s.Acquire(ctx, agent, "acceptance")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Release(ctx, lease)
	inputs := []Input{{EventID: "1", Body: "first"}, {EventID: "2", Body: "second"}, {EventID: "3", Body: "third"}}
	if err = s.Ingest(ctx, lease, "3", inputs); err != nil {
		t.Fatal(err)
	}
	session := &SessionStore{Store: s, Lease: lease}
	first, err := s.StartRun(ctx, lease, inputs[:1])
	if err != nil {
		t.Fatal(err)
	}
	appendEvent := func(run *Run, text string) {
		t.Helper()
		event := &adk.SessionEvent[*schema.Message]{EventID: uuid.NewString(), Message: &schema.Message{Role: schema.User, Content: text}}
		if err := session.AppendEvents(WithRun(ctx, run), "agent:"+agent, []*adk.SessionEvent[*schema.Message]{event}); err != nil {
			t.Fatal(err)
		}
	}
	appendEvent(first, "late-accepted old attempt")
	if err = s.FinishRun(ctx, lease, first, "retry", "gateway_unavailable", true); err != nil {
		t.Fatal(err)
	}
	second, err := s.StartRun(ctx, lease, inputs[1:2])
	if err != nil {
		t.Fatal(err)
	}
	appendEvent(second, "newer committed context")
	if err = s.FinishRun(ctx, lease, second, "completed", "", false); err != nil {
		t.Fatal(err)
	}
	// Later acceptance of an older, formerly invisible attempt must not rewrite
	// what the second run's model tape originally saw at its pinned head.
	if err = s.FinishRun(ctx, lease, first, "completed", "", false); err != nil {
		t.Fatal(err)
	}
	events, err := session.LoadEvents(WithRun(ctx, second), "agent:"+agent, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(events.Events) != 1 || events.Events[0].Message.Content != "newer committed context" {
		t.Fatal("late acceptance changed a prior run's replay context")
	}
	third, err := s.StartRun(ctx, lease, inputs[2:])
	if err != nil {
		t.Fatal(err)
	}
	events, err = session.LoadEvents(WithRun(ctx, third), "agent:"+agent, nil)
	if err != nil || len(events.Events) != 2 {
		t.Fatal("new turn did not see append-only accepted history", err)
	}
}

func TestCanceledRunDoesNotResumeWhenFreshInputArrives(t *testing.T) {
	s := testStore(t)
	e, f, base, _ := fixtureEngine(t, s)
	owner := uuid.NewString()
	e.Control = &rpcOwnerControl{definition: &wire.AgentDefinition{Id: f.agent, OwnerId: owner, Enabled: true, Revision: 1}}
	gateway := &blockingGatewayFixture{fixtureGateway: base, first: make(chan struct{}), release: make(chan struct{})}
	e.Gateway = gateway
	f.add("silent canceled source", "")
	cancel, done := startFixtureWorker(t, e, f)
	defer func() { cancel(); <-done }()
	select {
	case <-gateway.first:
	case <-time.After(5 * time.Second):
		t.Fatal("model did not start")
	}
	var id string
	if err := s.DB.QueryRow(context.Background(), `SELECT id::text FROM runtime_runs WHERE state='running'`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	rpc := &RuntimeRPC{Engine: e}
	response, err := rpc.CancelRun(context.Background(), &wire.RuntimeRequest{ServiceToken: e.Config.ControlToken, AgentId: f.agent, OwnerId: owner, ResourceId: &id})
	if err != nil || !response.Ok {
		t.Fatal("cancel rejected", err, response)
	}
	waitFor(t, func() bool {
		return count(t, s, `SELECT count(*) FROM runtime_runs WHERE id=$1 AND state='canceled'`, id) == 1
	})
	f.add("silent fresh source after cancel", "")
	waitFor(t, func() bool { return count(t, s, `SELECT count(*) FROM runtime_runs WHERE state='completed'`) == 1 })
	if count(t, s, `SELECT count(*) FROM runtime_runs WHERE id=$1 AND state='canceled'`, id) != 1 {
		t.Fatal("canceled checkpoint restarted old work")
	}
}

func TestNativeTaskLostStartReplyAdoptsAcceptedQueuedReceipt(t *testing.T) {
	s := testStore(t)
	e, f, base, _ := fixtureEngine(t, s)
	sb := &asyncSandboxFixture{startDelay: 3 * time.Second, released: true, loseReplyOnce: true}
	e.Sandbox = sb
	e.Gateway = &nativeGatewayFixture{fixtureGateway: base}
	f.add("silent accepted start reply lost", "")
	cancel, done := startFixtureWorker(t, e, f)
	defer func() { cancel(); <-done }()
	waitFor(t, func() bool {
		return count(t, s, `SELECT count(*) FROM runtime_tasks WHERE snapshot->>'Status'='completed'`) == 1
	})
	if count(t, s, `SELECT count(*) FROM runtime_tasks WHERE snapshot->>'Status'='failed'`) != 0 {
		t.Fatal("known queued receipt became an unknown effect")
	}
	sb.mu.Lock()
	defer sb.mu.Unlock()
	starts := 0
	for _, n := range sb.starts {
		starts += n
	}
	if starts != 1 {
		t.Fatal("lost Start reply duplicated side effects")
	}
}
