package eino

import (
	"context"
	"encoding/json"
	"path"
	"sort"
	"strings"
	"sync"
	"testing"

	fs "github.com/cloudwego/eino/adk/filesystem"
	"github.com/cloudwego/kitex/client/callopt"
	wire "teamie/contracts/gen/agent/kitex_gen/agent"
)

type workspaceFixture struct {
	asyncSandboxFixture
	mu       sync.Mutex
	files    map[string]string
	receipts map[string]string
	effects  int
}

func (f *workspaceFixture) Workspace(_ context.Context, r *wire.SandboxWorkspaceRequest, _ ...callopt.Option) (*wire.SandboxWorkspaceResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.files == nil {
		f.files = map[string]string{}
		f.receipts = map[string]string{}
	}
	if r.GetRequestId() != "" {
		if old, ok := f.receipts[r.GetRequestId()]; ok {
			return &wire.SandboxWorkspaceResponse{Ok: true, DataJson: &old}, nil
		}
	}
	var args struct{ Path, Content string }
	if err := json.Unmarshal([]byte(r.ArgsJson), &args); err != nil {
		return nil, err
	}
	var result any
	switch r.Operation {
	case "ls":
		files := []fs.FileInfo{}
		for name, value := range f.files {
			if path.Dir(name) == args.Path {
				files = append(files, fs.FileInfo{Path: name, Size: int64(len(value))})
			}
		}
		sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
		result = map[string]any{"files": files}
	case "read":
		value, ok := f.files[args.Path]
		if !ok {
			code := "file_not_found"
			return &wire.SandboxWorkspaceResponse{ErrorCode: &code}, nil
		}
		result = map[string]string{"content": value}
	case "write":
		if r.GetRequestId() == "" {
			panic("file mutation without stable receipt")
		}
		f.effects++
		f.files[args.Path] = args.Content
		result = map[string]any{"path": args.Path, "bytes": len(args.Content)}
	case "delete":
		delete(f.files, args.Path)
		result = map[string]any{"path": args.Path}
	default:
		panic("unexpected file operation")
	}
	b, _ := json.Marshal(result)
	raw := string(b)
	if r.GetRequestId() != "" {
		f.receipts[r.GetRequestId()] = raw
	}
	return &wire.SandboxWorkspaceResponse{Ok: true, DataJson: &raw}, nil
}

type planGatewayFixture struct {
	*fixtureGateway
	mu      sync.Mutex
	calls   int
	failure string
}

func (g *planGatewayFixture) Chat(_ context.Context, r *wire.GatewayChatRequest, _ ...callopt.Option) (*wire.GatewayChatResponse, error) {
	g.fixtureGateway.mu.Lock()
	g.fixtureGateway.requests = append(g.fixtureGateway.requests, []byte(r.RequestJson))
	g.fixtureGateway.mu.Unlock()
	g.mu.Lock()
	defer g.mu.Unlock()
	g.calls++
	var request toolPathRequest
	_ = decodeFixtureRequest(r.RequestJson, &request)
	last := request.Messages[len(request.Messages)-1]
	var result chatMessage
	if last.Role != "tool" {
		result, _ = toolPathCall("TaskCreate", "native-plan-create", map[string]any{"subject": "PRIVATE_PLAN_SUBJECT", "description": "Preserve this plan"})
	} else if last.ToolCallID == "native-plan-create" {
		if !strings.Contains(last.Content, "Task #1 created") {
			g.failure = "plan creation did not recover exact ID"
		}
		result, _ = toolPathCall("TaskList", "native-plan-list", map[string]any{})
	} else {
		if !strings.Contains(last.Content, "PRIVATE_PLAN_SUBJECT") {
			g.failure = "persisted plan not visible after restart"
		}
		result = chatMessage{Role: "assistant", Content: "PRIVATE_PLAN_FINAL"}
	}
	b, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"message": result, "finish_reason": "stop"}}})
	raw := string(b)
	return &wire.GatewayChatResponse{Ok: true, ResponseJson: modelFixtureResponse(raw)}, nil
}
func TestV3CutoverPlanTaskPersistsAndCrashReplayKeepsID(t *testing.T) {
	s := testStore(t)
	e, f, base, _ := fixtureEngine(t, s)
	sb := &workspaceFixture{}
	e.Sandbox = sb
	gateway := &planGatewayFixture{fixtureGateway: base}
	e.Gateway = gateway
	if _, err := s.DB.Exec(context.Background(), `CREATE FUNCTION crash_plan_receipt() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.name='native:TaskCreate' AND NEW.result IS NOT NULL THEN RAISE EXCEPTION 'fixture disconnect' USING ERRCODE='08006';END IF;RETURN NEW;END $$;CREATE TRIGGER crash_plan_receipt BEFORE UPDATE ON runtime_tool_calls FOR EACH ROW EXECUTE FUNCTION crash_plan_receipt()`); err != nil {
		t.Fatal(err)
	}
	f.add("silent native plan", "")
	cancel, done := startFixtureWorker(t, e, f)
	waitFor(t, func() bool { return count(t, s, `SELECT count(*) FROM runtime_runs WHERE state='retry'`) == 1 })
	cancel()
	<-done
	fixtureV3Cutover(t, s, base.requests[0])
	if _, err := s.DB.Exec(context.Background(), `DROP TRIGGER crash_plan_receipt ON runtime_tool_calls;UPDATE runtime_runs SET next_attempt=now()`); err != nil {
		t.Fatal(err)
	}
	cancel, done = startFixtureWorker(t, e, f)
	defer func() { cancel(); <-done }()
	waitFor(t, func() bool { return count(t, s, `SELECT count(*) FROM runtime_runs WHERE state='completed'`) == 1 })
	gateway.mu.Lock()
	failure := gateway.failure
	gateway.mu.Unlock()
	if failure != "" {
		t.Fatal(failure)
	}
	sb.mu.Lock()
	defer sb.mu.Unlock()
	if sb.effects != 2 || len(sb.files) != 2 || sb.files["/workspace/.teamie/plan/.highwatermark"] != "1" {
		t.Fatalf("plan replay duplicated ID/files: effects=%d files=%v", sb.effects, sb.files)
	}
	if count(t, s, `SELECT count(*) FROM runtime_tool_observations`) == 0 {
		t.Fatal("native plan read boundary was not persisted")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.posts != 0 {
		t.Fatal("private plan became public IM")
	}
}
