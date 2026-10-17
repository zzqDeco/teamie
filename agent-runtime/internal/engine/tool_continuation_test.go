package engine

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/cloudwego/kitex/client/callopt"
	wire "teamie/agent-protocol/kitex_gen/agent"
	"teamie/agent-protocol/kitex_gen/agent/sandboxservice"
)

type continuationSandbox struct {
	sandboxservice.Client
	mu    sync.Mutex
	calls int
}

func (s *continuationSandbox) Workspace(_ context.Context, _ *wire.SandboxWorkspaceRequest, _ ...callopt.Option) (*wire.SandboxWorkspaceResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	raw := `{"content":"PARTIAL_NOT_A_COMPLETE_FILE","hasMore":true,"truncated":true}`
	if s.calls > 1 {
		raw = `{"content":"COMPLETE_PAGE","hasMore":false,"truncated":false,"coverage":{"complete":true}}`
	}
	return &wire.SandboxWorkspaceResponse{Ok: true, DataJson: &raw}, nil
}

type continuationGateway struct {
	*fixtureGateway
	mu      sync.Mutex
	step    int
	failure error
}

func (g *continuationGateway) Chat(_ context.Context, r *wire.GatewayChatRequest, _ ...callopt.Option) (*wire.GatewayChatResponse, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.step++
	var req toolPathRequest
	if err := decodeFixtureRequest(r.RequestJson, &req); err != nil {
		return nil, err
	}
	response := chatMessage{Role: "assistant", Content: "private completion"}
	if g.step == 1 {
		response, _ = toolPathCall("read_file", "native-needs-page", map[string]any{"file_path": "/workspace/long.txt"})
	} else if g.step == 2 {
		last := req.Messages[len(req.Messages)-1]
		var hint struct {
			Error, Tool string
			Resume      map[string]any
		}
		if json.Unmarshal([]byte(last.Content), &hint) != nil || hint.Error != "workspace_page_required" || hint.Tool != "workspace_read_page" || hint.Resume["operation"] != "read" || hint.Resume["path"] != "/workspace/long.txt" || strings.Contains(last.Content, "PARTIAL_NOT_A_COMPLETE_FILE") {
			g.failure = errors.New("native partial read terminated or lost the typed continuation")
			return nil, g.failure
		}
		response, _ = toolPathCall("workspace_read_page", "explicit-page", hint.Resume)
	} else if g.step == 3 {
		if !strings.Contains(req.Messages[len(req.Messages)-1].Content, "COMPLETE_PAGE") {
			g.failure = errors.New("model did not receive the continued page")
			return nil, g.failure
		}
	}
	b, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"message": response, "finish_reason": "stop"}}})
	return &wire.GatewayChatResponse{Ok: true, ResponseJson: modelFixtureResponse(string(b))}, nil
}

func TestNativePartialReadContinuesThroughExplicitPageWithoutFailingRun(t *testing.T) {
	s := testStore(t)
	e, f, base, _ := fixtureEngine(t, s)
	enableContextFixture(t, e)
	gw := &continuationGateway{fixtureGateway: base}
	sb := &continuationSandbox{}
	e.Gateway, e.Sandbox = gw, sb
	f.add("read the file", "")
	cancel, done := startFixtureWorker(t, e, f)
	waitFor(t, func() bool { return count(t, s, `SELECT count(*) FROM runtime_runs WHERE state='completed'`) == 1 })
	cancel()
	<-done
	gw.mu.Lock()
	defer gw.mu.Unlock()
	if gw.step != 3 || gw.failure != nil {
		t.Fatal("partial read did not remain a usable tool observation", gw.step, gw.failure)
	}
	if count(t, s, `SELECT count(*) FROM runtime_tool_calls WHERE result IS NOT NULL`) != 2 {
		t.Fatal("continuation hint/page receipts were not persisted")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.posts != 0 {
		t.Fatal("private file observation became an IM post")
	}
}

func TestOnlyTypedNewPolicyContinuationIsRecoverable(t *testing.T) {
	ctx := WithRun(context.Background(), &Run{ContextProfile: contextProfileV1})
	hint := &workspacePageRequiredError{Operation: "grep", Resume: map[string]any{"operation": "grep", "path": "/workspace", "pattern": "needle", "glob": "**/*.go"}}
	text, ok := nativePageContinuation(ctx, hint)
	if !ok || !strings.Contains(text, `"pattern":"needle"`) {
		t.Fatal(text, ok)
	}
	for _, err := range []error{errors.New("workspace_page_required"), &ModelError{Code: "sandbox_unavailable", Retry: true}, nil} {
		if _, ok = nativePageContinuation(ctx, err); ok {
			t.Fatal("unrelated failure became a continuation", err)
		}
	}
	if _, ok = nativePageContinuation(WithRun(context.Background(), &Run{}), hint); ok {
		t.Fatal("legacy behavior changed")
	}
}
