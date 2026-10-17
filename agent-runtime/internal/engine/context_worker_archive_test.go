package engine

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/cloudwego/kitex/client/callopt"
	wire "teamie/agent-protocol/kitex_gen/agent"
	"teamie/agent-protocol/kitex_gen/agent/sandboxservice"
)

type archiveWorkerSandbox struct {
	sandboxservice.Client
	mu      sync.Mutex
	calls   int
	content string
}

func (*archiveWorkerSandbox) Cancel(context.Context, *wire.CancelRequest, ...callopt.Option) (*wire.Ack, error) {
	return &wire.Ack{Ok: true}, nil
}

func (s *archiveWorkerSandbox) Workspace(_ context.Context, _ *wire.SandboxWorkspaceRequest, _ ...callopt.Option) (*wire.SandboxWorkspaceResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	raw, _ := json.Marshal(map[string]any{"content": s.content, "hasMore": false, "truncated": false, "coverage": map[string]bool{"complete": true}})
	text := string(raw)
	return &wire.SandboxWorkspaceResponse{Ok: true, DataJson: &text}, nil
}

type archiveWorkerGateway struct {
	*fixtureGateway
	mu      sync.Mutex
	step    int
	ref     string
	failure error
}

func (g *archiveWorkerGateway) Chat(_ context.Context, r *wire.GatewayChatRequest, _ ...callopt.Option) (*wire.GatewayChatResponse, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.step++
	var req toolPathRequest
	if err := decodeFixtureRequest(r.RequestJson, &req); err != nil {
		return nil, err
	}
	response := chatMessage{Role: "assistant", Content: "private archive completion"}
	switch g.step {
	case 1:
		response, _ = toolPathCall("read_file", "archive-large-file", map[string]any{"file_path": "/workspace/large.txt"})
	case 2:
		last := req.Messages[len(req.Messages)-1].Content
		g.ref = regexp.MustCompile(`context:v1:[a-f0-9-]+:tool_trunc:[a-f0-9]+`).FindString(last)
		if g.ref == "" || !strings.Contains(last, "Archived private context: ") || strings.Contains(last, "COMPLETE_FILE_TAIL") {
			g.failure = errors.New("full native tool observation was not reduced to a retrievable archive")
			return nil, g.failure
		}
		response, _ = toolPathCall("context_read", "read-archived-file", map[string]any{"ref": g.ref, "limit": 4096})
	case 3:
		last := req.Messages[len(req.Messages)-1].Content
		if !strings.Contains(last, "ARCHIVE_PAYLOAD_MARKER") || !strings.Contains(last, `"hasMore":true`) || strings.Contains(last, "Archived private context: ") {
			g.failure = errors.New("bounded archive read was lost or recursively archived")
			return nil, g.failure
		}
	default:
		g.failure = errors.New("unexpected compaction or additional model call in archive integration")
		return nil, g.failure
	}
	b, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"message": response, "finish_reason": "stop"}}})
	return &wire.GatewayChatResponse{Ok: true, ResponseJson: modelFixtureResponse(string(b))}, nil
}

func TestContextWorkerOffloadsNativeToolAndReadsOriginalArchive(t *testing.T) {
	s := testStore(t)
	e, f, base, _ := fixtureEngine(t, s)
	enableContextFixture(t, e)
	gw := &archiveWorkerGateway{fixtureGateway: base}
	sb := &archiveWorkerSandbox{content: "ARCHIVE_PAYLOAD_MARKER\n" + strings.Repeat("完整文件观察内容。", 1800) + "\nCOMPLETE_FILE_TAIL"}
	e.Gateway, e.Sandbox = gw, sb
	f.add("read and inspect the large file privately", "")
	cancel, done := startFixtureWorker(t, e, f)
	waitFor(t, func() bool {
		return count(t, s, `SELECT count(*) FROM runtime_runs WHERE state IN ('completed','failed')`) == 1
	})
	cancel()
	<-done
	gw.mu.Lock()
	defer gw.mu.Unlock()
	var state, code string
	if err := s.DB.QueryRow(context.Background(), `SELECT state,safe_error FROM runtime_runs`).Scan(&state, &code); err != nil {
		t.Fatal(err)
	}
	if state != "completed" || gw.step != 3 || gw.failure != nil {
		t.Fatal("native reduction/archive/read did not complete one Run", state, code, gw.step, gw.failure)
	}
	sb.mu.Lock()
	defer sb.mu.Unlock()
	if sb.calls != 1 || count(t, s, `SELECT count(*) FROM runtime_tool_calls WHERE result IS NOT NULL`) != 2 {
		t.Fatal("native or context tool observation was not persisted exactly once", sb.calls)
	}
	var content []byte
	var sources []InputSource
	if err := s.DB.QueryRow(context.Background(), `SELECT content,sources FROM runtime_context_artifacts WHERE kind='tool_trunc'`).Scan(&content, &sources); err != nil {
		t.Fatal(err)
	}
	if len(content) < len(sb.content) || !strings.Contains(string(content), "ARCHIVE_PAYLOAD_MARKER") || !strings.Contains(string(content), "COMPLETE_FILE_TAIL") || len(sources) != 1 || sources[0].ConversationID != f.conversation || sources[0].MessageID == "" {
		t.Fatal("archive lost complete data or the actual input source")
	}
	if count(t, s, `SELECT count(*) FROM runtime_context_artifacts WHERE kind='tool_trunc'`) != 1 {
		t.Fatal("reading the archive created another truncated archive")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.posts != 0 {
		t.Fatal("private archive content became an IM post")
	}
}
