package engine

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
	"github.com/cloudwego/kitex/client/callopt"
	"github.com/google/uuid"
	wire "teamie/agent-protocol/kitex_gen/agent"
	"teamie/agent-protocol/kitex_gen/agent/sandboxservice"
)

type attachmentSandboxFixture struct {
	sandboxservice.Client
	mu                sync.Mutex
	requests          []wire.SandboxImportRequest
	effects           int
	loseFirstResponse bool
}

func (s *attachmentSandboxFixture) ImportFile(_ context.Context, r *wire.SandboxImportRequest, _ ...callopt.Option) (*wire.SandboxWorkspaceResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	copyRequest := *r
	copyRequest.Content = bytes.Clone(r.Content)
	s.requests = append(s.requests, copyRequest)
	if len(s.requests) == 1 {
		s.effects++
	}
	if s.loseFirstResponse && len(s.requests) == 1 {
		return nil, errors.New("fixture response lost after import")
	}
	raw, _ := json.Marshal(map[string]any{"path": r.Path, "bytes": len(r.Content), "sha256": r.Sha256})
	out := string(raw)
	return &wire.SandboxWorkspaceResponse{Ok: true, DataJson: &out}, nil
}

func TestExplicitAttachmentToolBinaryImportReplayAndLeaseRevocation(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	id := uuid.NewString()
	conversation := uuid.NewString()
	messageID := uuid.NewString()
	attachmentID := uuid.NewString()
	raw := bytes.Repeat([]byte{0, 255, 128, 0, 'a'}, 75000)
	hash := sha256.Sum256(raw)
	descriptor := Attachment{ID: attachmentID, Filename: "private.bin", MediaType: "application/octet-stream", SizeBytes: len(raw), SHA256: hex.EncodeToString(hash[:])}
	var downloads atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+strings.Repeat("a", 64) || r.Header.Get("X-Teamie-Service-Key") != "fixture-im-secret" {
			w.WriteHeader(401)
			return
		}
		switch r.URL.Path {
		case "/api/v1/conversations/" + conversation + "/messages/" + messageID:
			_ = json.NewEncoder(w).Encode(map[string]any{"message": Message{ID: messageID, ConversationID: conversation, Attachments: []Attachment{descriptor}}})
		case "/api/v1/conversations/" + conversation + "/attachments/" + attachmentID + "/content":
			downloads.Add(1)
			_, _ = w.Write(raw)
		default:
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	lease, e := s.Acquire(ctx, id, "attachment-worker")
	if e != nil {
		t.Fatal(e)
	}
	input := Input{EventID: "1", ConversationID: conversation, MessageID: messageID, Attachments: []Attachment{descriptor}}
	if e = s.Ingest(ctx, lease, "1", []Input{input}); e != nil {
		t.Fatal(e)
	}
	run, e := s.StartRun(ctx, lease, []Input{input})
	if e != nil {
		t.Fatal(e)
	}
	ctx = WithRun(ctx, run)
	sandbox := &attachmentSandboxFixture{loseFirstResponse: true}
	im := &IM{BaseURL: server.URL, AgentID: id, ServiceKey: "fixture-im-secret", Identity: &fixtureIdentity{}}
	tools := &Tools{Store: s, Lease: lease, IM: im, Sandbox: sandbox, SandboxToken: "fixture-sandbox-token"}
	attachment, e := tools.attachmentTool()
	if e != nil {
		t.Fatal(e)
	}
	node, e := compose.NewToolNode(ctx, &compose.ToolsNodeConfig{Tools: []tool.BaseTool{attachment}, ExecuteSequentially: true})
	if e != nil {
		t.Fatal(e)
	}
	args, _ := json.Marshal(getAttachmentArgs{ConversationID: conversation, MessageID: messageID, AttachmentID: attachmentID, Path: "/workspace/private.bin"})
	call := &schema.Message{Role: schema.Assistant, ToolCalls: []schema.ToolCall{{ID: "stable-import-call", Type: "function", Function: schema.FunctionCall{Name: "im_get_attachment", Arguments: string(args)}}}}
	if _, e = node.Invoke(ctx, call); e == nil {
		t.Fatal("lost import response was reported complete")
	}
	result, e := node.Invoke(ctx, call)
	if e != nil || len(result) != 1 {
		t.Fatal(e)
	}
	var observation Observation
	if e = json.Unmarshal([]byte(result[0].Content), &observation); e != nil || !observation.OK {
		t.Fatal(result, e)
	}
	if strings.Contains(result[0].Content, "content") || strings.Contains(result[0].Content, "base64") || strings.Contains(result[0].Content, "fixture-sandbox-token") {
		t.Fatal("binary or credential entered tool observation")
	}
	if _, e = node.Invoke(ctx, call); e != nil {
		t.Fatal("committed receipt replay", e)
	}
	sandbox.mu.Lock()
	if len(sandbox.requests) != 2 || sandbox.effects != 1 || sandbox.requests[0].RequestId != sandbox.requests[1].RequestId || !bytes.Equal(sandbox.requests[0].Content, raw) || sandbox.requests[0].AgentId != id || sandbox.requests[0].RunId != run.ID {
		t.Fatal("stable binary import replay", len(sandbox.requests), sandbox.effects)
	}
	sandbox.mu.Unlock()
	if downloads.Load() != 2 {
		t.Fatal("committed tool replay redownloaded content", downloads.Load())
	}
	if _, e = s.DB.Exec(ctx, "UPDATE runtime_agents SET lease_until=now()-interval '1 second' WHERE agent_id=$1", id); e != nil {
		t.Fatal(e)
	}
	if _, e = s.Acquire(ctx, id, "replacement-worker"); e != nil {
		t.Fatal(e)
	}
	if _, e = node.Invoke(ctx, call); e == nil {
		t.Fatal("stale lease replay accepted")
	}
}

func TestFileOnlyInboxDeliversDescriptorsWithoutBinary(t *testing.T) {
	s := testStore(t)
	e, f, _, _ := fixtureEngine(t, s)
	f.add("", "")
	f.mu.Lock()
	var descriptor Attachment
	for _, m := range f.messages {
		descriptor = Attachment{ID: uuid.NewString(), Filename: "binary.dat", MediaType: "application/octet-stream", SizeBytes: 3, SHA256: strings.Repeat("a", 64)}
		m.Attachments = []Attachment{descriptor}
	}
	f.mu.Unlock()
	lease, err := s.Acquire(context.Background(), f.agent, "inbox-worker")
	if err != nil {
		t.Fatal(err)
	}
	im := &IM{BaseURL: e.Config.IMURL, AgentID: f.agent, Identity: e.Identity, ServiceKey: e.Config.IMToken}
	if err = e.ingest(context.Background(), im, lease); err != nil {
		t.Fatal(err)
	}
	var payload []byte
	if err = s.DB.QueryRow(context.Background(), "SELECT payload FROM runtime_inputs WHERE agent_id=$1", f.agent).Scan(&payload); err != nil {
		t.Fatal("file-only input dropped", err)
	}
	var in Input
	if err = json.Unmarshal(payload, &in); err != nil || len(in.Attachments) != 1 || in.Attachments[0] != descriptor || in.Body != "" {
		t.Fatal("file descriptors lost", err)
	}
	messages := inputMessages([]Input{in})
	if len(messages) != 1 || !strings.Contains(messages[0].Content, descriptor.ID) || strings.Contains(messages[0].Content, "base64") {
		t.Fatal("invalid model input")
	}
}

func TestAttachmentDownloadRejectsRedirectsAndHashMismatch(t *testing.T) {
	var leaked atomic.Bool
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { leaked.Store(true) }))
	defer destination.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, destination.URL, http.StatusFound) }))
	defer redirect.Close()
	im := &IM{BaseURL: redirect.URL, AgentID: uuid.NewString(), Identity: &fixtureIdentity{}, ServiceKey: "fixture-im-secret"}
	a := Attachment{ID: uuid.NewString(), SizeBytes: 1, SHA256: strings.Repeat("a", 64)}
	if _, e := im.downloadAttachment(context.Background(), uuid.NewString(), a); e == nil || leaked.Load() {
		t.Fatal("credential redirect followed", e)
	}
	content := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte{1}) }))
	defer content.Close()
	im.BaseURL = content.URL
	if _, e := im.downloadAttachment(context.Background(), uuid.NewString(), a); e == nil {
		t.Fatal("corrupt binary accepted")
	}
}

func TestAttachmentReferencesSurviveRepeatedCompaction(t *testing.T) {
	id := uuid.NewString()
	in := Input{EventID: "1", WorkspaceID: uuid.NewString(), ConversationID: uuid.NewString(), MessageID: uuid.NewString(), Attachments: []Attachment{{ID: id, Filename: "source.bin", SizeBytes: 1}}}
	first, e := finalizeSummary(context.Background(), inputMessages([]Input{in}), &schema.Message{Content: "A referenced source file remains available through explicit tools."})
	if e != nil || len(first) == 0 || !strings.Contains(first[0].Content, id) {
		t.Fatal("attachment provenance lost", e)
	}
	second, e := finalizeSummary(context.Background(), first, &schema.Message{Content: "Keep the file's originating conversation."})
	if e != nil || len(second) == 0 || !strings.Contains(second[0].Content, id) || !strings.Contains(second[0].Content, in.ConversationID) {
		t.Fatal("repeat compaction lost file source", e)
	}
}
