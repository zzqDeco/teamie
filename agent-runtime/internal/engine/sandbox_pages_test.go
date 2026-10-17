package engine

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/cloudwego/eino/adk"
	fs "github.com/cloudwego/eino/adk/filesystem"
	fsmiddleware "github.com/cloudwego/eino/adk/middlewares/filesystem"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
	"github.com/cloudwego/kitex/client/callopt"
	"github.com/google/uuid"
	wire "teamie/agent-protocol/kitex_gen/agent"
	"teamie/agent-protocol/kitex_gen/agent/sandboxservice"
)

type sandboxPageFixture struct {
	sandboxservice.Client
	calls int
	args  map[string]any
	data  string
}

func TestSandboxNativeToolInfoStableAcrossProfiles(t *testing.T) {
	var baseline string
	for _, progressive := range []bool{false, true} {
		m, e := fsmiddleware.NewTyped[*schema.AgenticMessage](context.Background(), &fsmiddleware.MiddlewareConfig{Backend: &SandboxBackend{Progressive: progressive}})
		if e != nil {
			t.Fatal(e)
		}
		_, out, e := m.BeforeAgent(context.Background(), &adk.ChatModelAgentContext[*schema.AgenticMessage]{})
		if e != nil {
			t.Fatal(e)
		}
		infos := []any{}
		for _, tt := range out.Tools {
			info, e := tt.Info(context.Background())
			if e != nil {
				t.Fatal(e)
			}
			params, e := info.ParamsOneOf.ToJSONSchema()
			if e != nil {
				t.Fatal(e)
			}
			infos = append(infos, map[string]any{"name": info.Name, "desc": info.Desc, "parameters": params})
		}
		b, _ := json.Marshal(infos)
		if !progressive {
			baseline = digest(b)
		} else if digest(b) != baseline {
			t.Fatal("native ToolInfo/Args schema changed with profile")
		}
	}
}

func TestSandboxPageObservationReplayDoesNotReadFutureOutputAndRechecksAuthorization(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	agentID := uuid.NewString()
	lease, e := s.Acquire(ctx, agentID, uuid.NewString())
	if e != nil {
		t.Fatal(e)
	}
	defer s.Release(ctx, lease)
	run, e := s.StartRunWithContextPolicy(ctx, lease, []Input{{EventID: uuid.NewString()}}, contextProfileV1, json.RawMessage(`{}`))
	if e != nil {
		t.Fatal(e)
	}
	f := &sandboxPageFixture{data: `{"taskId":"shell-test","nextOffset":4,"outputBytes":4,"hasMore":false,"outputStatus":"recording","stdout":"past"}`}
	revoked := false
	b := &SandboxBackend{Client: f, Store: s, Lease: lease, Progressive: true, Authorize: func(context.Context) error {
		if revoked {
			return errors.New("revoked")
		}
		return nil
	}}
	tt, e := b.ReadPageTool()
	if e != nil {
		t.Fatal(e)
	}
	node, e := compose.NewToolNode(ctx, &compose.ToolsNodeConfig{Tools: []tool.BaseTool{tt}})
	if e != nil {
		t.Fatal(e)
	}
	input := &schema.Message{Role: schema.Assistant, ToolCalls: []schema.ToolCall{{ID: "fixed-output-page", Function: schema.FunctionCall{Name: "workspace_read_page", Arguments: `{"operation":"output","taskId":"shell-test","byteOffset":0}`}}}}
	ctx = WithRun(ctx, run)
	first, e := node.Invoke(ctx, input)
	if e != nil {
		t.Fatal(e)
	}
	f.data = `{"stdout":"future","nextOffset":6,"outputBytes":6}`
	second, e := node.Invoke(ctx, input)
	if e != nil || first[0].Content != second[0].Content || f.calls != 1 {
		t.Fatal("observation replay read future output", e, f.calls, second)
	}
	revoked = true
	if _, e = node.Invoke(ctx, input); e == nil || f.calls != 1 {
		t.Fatal("cached observation bypassed revocation", e, f.calls)
	}
	revoked = false
	if _, e = s.DB.Exec(context.Background(), `UPDATE runtime_agents SET lease_until=now()-interval '1 minute' WHERE agent_id=$1`, agentID); e != nil {
		t.Fatal(e)
	}
	if _, e = node.Invoke(ctx, input); e == nil {
		t.Fatal("cached observation bypassed expired lease")
	}
}

func (f *sandboxPageFixture) Workspace(_ context.Context, r *wire.SandboxWorkspaceRequest, _ ...callopt.Option) (*wire.SandboxWorkspaceResponse, error) {
	f.calls++
	f.args = nil
	json.Unmarshal([]byte(r.ArgsJson), &f.args)
	return &wire.SandboxWorkspaceResponse{Ok: true, DataJson: &f.data}, nil
}

func TestSandboxNativePagingMetadataCannotDisappear(t *testing.T) {
	f := &sandboxPageFixture{data: `{"files":[],"matches":[],"content":"partial","hasMore":true,"truncated":true}`}
	b := &SandboxBackend{Client: f, Progressive: true}
	checks := []func() error{func() error { _, e := b.Read(context.Background(), &fs.ReadRequest{FilePath: "a"}); return e }, func() error { _, e := b.LsInfo(context.Background(), &fs.LsInfoRequest{Path: "a"}); return e }, func() error {
		_, e := b.GlobInfo(context.Background(), &fs.GlobInfoRequest{Path: "a", Pattern: "**"})
		return e
	}, func() error {
		_, e := b.GrepRaw(context.Background(), &fs.GrepRequest{Path: "a", Pattern: "x"})
		return e
	}}
	for _, check := range checks {
		e := check()
		if e == nil || !strings.Contains(e.Error(), "workspace_read_page") {
			t.Fatal("partial native result appeared complete", e)
		}
		if f.args["pageVersion"] != float64(1) {
			t.Fatal(f.args)
		}
	}
	b.Progressive = false
	if _, e := b.Read(context.Background(), &fs.ReadRequest{FilePath: "a"}); e != nil {
		t.Fatal("legacy behavior changed", e)
	}
	if _, ok := f.args["pageVersion"]; ok {
		t.Fatal("legacy args changed")
	}
}

func TestSandboxPageToolProfileAndCurrentAuthorization(t *testing.T) {
	f := &sandboxPageFixture{data: `{"content":"bounded","hasMore":false,"truncated":false}`}
	denied := false
	b := &SandboxBackend{Client: f, ProgressiveForRun: func(r *Run) bool { return r.ContextProfile == contextProfileV1 }, Authorize: func(context.Context) error {
		if denied {
			return errors.New("revoked")
		}
		return nil
	}}
	tt, e := b.ReadPageTool()
	if e != nil {
		t.Fatal(e)
	}
	invoke := tt.(tool.InvokableTool)
	ctx := WithRun(context.Background(), &Run{ContextProfile: contextProfileV1})
	out, e := invoke.InvokableRun(ctx, `{"operation":"read","path":"/workspace/a"}`)
	if e != nil || !strings.Contains(out, "bounded") || f.args["pageVersion"] != float64(1) {
		t.Fatal(out, e, f.args)
	}
	denied = true
	if _, e = invoke.InvokableRun(ctx, `{"operation":"read","path":"/workspace/a"}`); e == nil || f.calls != 1 {
		t.Fatal("revocation bypass", e, f.calls)
	}
	if _, e = invoke.InvokableRun(WithRun(context.Background(), &Run{}), `{"operation":"read"}`); e == nil {
		t.Fatal("legacy profile admitted new tool")
	}
}
