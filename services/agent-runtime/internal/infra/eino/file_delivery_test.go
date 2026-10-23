package eino

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
	"github.com/cloudwego/kitex/client/callopt"
	"github.com/google/uuid"
	wire "teamie/contracts/gen/agent/kitex_gen/agent"
	"teamie/contracts/gen/agent/kitex_gen/agent/sandboxservice"
)

type deliverySandboxFixture struct {
	sandboxservice.Client
	mu       sync.Mutex
	content  []byte
	exports  int
	onExport func()
}

func (*deliverySandboxFixture) Cancel(context.Context, *wire.CancelRequest, ...callopt.Option) (*wire.Ack, error) {
	return &wire.Ack{Ok: true}, nil
}

func (f *deliverySandboxFixture) ExportFile(_ context.Context, r *wire.SandboxExportRequest, _ ...callopt.Option) (*wire.SandboxExportResponse, error) {
	f.mu.Lock()
	f.exports++
	content, callback := bytes.Clone(f.content), f.onExport
	f.mu.Unlock()
	if r.ServiceToken != "delivery-sandbox-token" || r.Path != "/workspace/results/report.bin" {
		return nil, errors.New("unexpected export identity")
	}
	if callback != nil {
		callback()
	}
	hash := sha256.Sum256(content)
	value := hex.EncodeToString(hash[:])
	return &wire.SandboxExportResponse{Ok: true, Content: content, Sha256: &value}, nil
}

type deliveryHTTPFixture struct {
	context                      *contextArchiveFixture
	sandbox                      *deliverySandboxFixture
	mu                           sync.Mutex
	uploads                      map[string]Attachment
	bytes                        map[string][]byte
	sends                        map[string]Message
	lostUpload, lostSend, denied bool
	posts                        int
}

func newDeliveryFixture(t *testing.T, mentions ...bool) (*contextArchiveFixture, *deliveryHTTPFixture) {
	t.Helper()
	budget, err := newContextBudgetWithFiles(16384, true, mentions...)
	if err != nil {
		t.Fatal(err)
	}
	f := newContextArchiveFixture(t, budget)
	f.a.IM.ServiceKey = "delivery-im-service-key"
	f.a.IM.token = strings.Repeat("a", 64)
	state := &deliveryHTTPFixture{context: f, sandbox: &deliverySandboxFixture{content: []byte{0, 255, 128, 1, 13, 10}}, uploads: map[string]Attachment{}, bytes: map[string][]byte{}, sends: map[string]Message{}}
	target, err := url.Parse(f.a.IM.BaseURL)
	if err != nil {
		t.Fatal(err)
	}
	proxy := httputil.NewSingleHostReverseProxy(target)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+strings.Repeat("a", 64) || r.Header.Get("X-Teamie-Service-Key") != "delivery-im-service-key" {
			t.Error("delivery bypassed ordinary IM principal authentication")
			w.WriteHeader(401)
			return
		}
		prefix := "/api/v1/conversations/" + f.input.ConversationID
		if r.Method == http.MethodGet && r.URL.Path == "/api/v1/sync" {
			_ = json.NewEncoder(w).Encode(map[string]any{"events": []any{}, "cursor": "1", "hasMore": false})
			return
		}
		state.mu.Lock()
		defer state.mu.Unlock()
		if state.denied && strings.HasPrefix(r.URL.Path, prefix) {
			w.WriteHeader(404)
			_, _ = w.Write([]byte(`{"error":{"code":"not_found"}}`))
			return
		}
		if r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, prefix+"/attachment-uploads/") {
			key := strings.TrimPrefix(r.URL.Path, prefix+"/attachment-uploads/")
			if descriptor, ok := state.uploads[key]; ok {
				_ = json.NewEncoder(w).Encode(map[string]any{"attachment": descriptor})
			} else {
				w.WriteHeader(404)
				_, _ = w.Write([]byte(`{"error":{"code":"attachment_upload_not_found"}}`))
			}
			return
		}
		if r.Method == http.MethodPost && r.URL.Path == prefix+"/attachments" {
			state.posts++
			reader, err := r.MultipartReader()
			if err != nil {
				t.Error(err)
				w.WriteHeader(400)
				return
			}
			part, err := reader.NextPart()
			if err != nil {
				t.Error(err)
				w.WriteHeader(400)
				return
			}
			content, _ := io.ReadAll(part)
			hash := sha256.Sum256(content)
			key := r.Header.Get("Idempotency-Key")
			if key == "" {
				t.Error("missing stable upload identity")
			}
			descriptor, found := state.uploads[key]
			if !found {
				descriptor = Attachment{ID: uuid.NewString(), Filename: part.FileName(), MediaType: "application/octet-stream", SizeBytes: len(content), SHA256: hex.EncodeToString(hash[:])}
				state.uploads[key] = descriptor
				state.bytes[descriptor.ID] = bytes.Clone(content)
			} else if descriptor.SHA256 != hex.EncodeToString(hash[:]) {
				t.Error("retry attempted to replace already uploaded bytes")
				w.WriteHeader(409)
				return
			}
			if state.lostUpload {
				conn, _, _ := w.(http.Hijacker).Hijack()
				_ = conn.Close()
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"attachment": descriptor})
			return
		}
		if r.Method == http.MethodPost && r.URL.Path == prefix+"/messages" {
			var input struct {
				ClientMessageID string   `json:"clientMessageId"`
				Body            string   `json:"body"`
				ReplyToID       string   `json:"replyToId"`
				AttachmentIDs   []string `json:"attachmentIds"`
			}
			if json.NewDecoder(r.Body).Decode(&input) != nil || len(input.AttachmentIDs) != 1 || state.bytes[input.AttachmentIDs[0]] == nil {
				t.Error("file send did not use a committed ordinary attachment")
				w.WriteHeader(400)
				return
			}
			message, found := state.sends[input.ClientMessageID]
			if !found {
				message = Message{ID: uuid.NewString(), ConversationID: f.input.ConversationID, SenderID: f.l.AgentID, SenderKind: "agent", Body: input.Body, ReplyToID: input.ReplyToID, Seq: 2}
				for _, descriptor := range state.uploads {
					if descriptor.ID == input.AttachmentIDs[0] {
						message.Attachments = []Attachment{descriptor}
					}
				}
				state.sends[input.ClientMessageID] = message
				f.mu.Lock()
				f.messages[message.ID] = message
				f.mu.Unlock()
			}
			if state.lostSend {
				conn, _, _ := w.(http.Hijacker).Hijack()
				_ = conn.Close()
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"message": message})
			return
		}
		proxy.ServeHTTP(w, r)
	}))
	t.Cleanup(server.Close)
	f.a.IM.BaseURL = server.URL
	return f, state
}

func deliveryNode(t *testing.T, f *contextArchiveFixture, state *deliveryHTTPFixture, authorize func(context.Context) error) *compose.ToolsNode {
	t.Helper()
	tools := &Tools{Store: f.s, Lease: f.l, IM: f.a.IM, Sandbox: state.sandbox, SandboxToken: "delivery-sandbox-token", Authorize: authorize}
	items, err := tools.BuildIMWithFiles()
	if err != nil {
		t.Fatal(err)
	}
	node, err := compose.NewToolNode(f.ctx(), &compose.ToolsNodeConfig{Tools: items, ExecuteSequentially: true})
	if err != nil {
		t.Fatal(err)
	}
	return node
}

func fileCall(name, id string, args any) *schema.Message {
	encoded, _ := json.Marshal(args)
	return &schema.Message{Role: schema.Assistant, ToolCalls: []schema.ToolCall{{ID: id, Type: "function", Function: schema.FunctionCall{Name: name, Arguments: string(encoded)}}}}
}

func TestAgentFileDeliveryRecoversLostUploadAndSendAcrossAttemptWithoutDuplicateBytes(t *testing.T) {
	f, state := newDeliveryFixture(t)
	node := deliveryNode(t, f, state, nil)
	call := fileCall("im_upload_file", "upload-result", uploadFileArgs{ConversationID: f.input.ConversationID, Path: "/workspace/results/report.bin"})
	state.mu.Lock()
	state.lostUpload = true
	state.mu.Unlock()
	if _, err := node.Invoke(f.ctx(), call); err == nil {
		t.Fatal("lost upload response reported a completed tool")
	}
	state.mu.Lock()
	if len(state.uploads) != 1 || len(state.sends) != 0 {
		t.Fatal("upload automatically sent a message or duplicated files")
	}
	state.lostUpload = false
	state.mu.Unlock()
	state.sandbox.mu.Lock()
	state.sandbox.content = nil // The original file was removed after upload.
	state.sandbox.mu.Unlock()
	originalRun, originalAttempt := f.run.ID, f.run.Attempt
	f.s.Release(context.Background(), f.l)
	var err error
	f.l, err = f.s.Acquire(context.Background(), f.l.AgentID, "replacement-file-worker")
	if err != nil {
		t.Fatal(err)
	}
	// The new process/live policy offers no file tools, but this old Run must
	// recover its immutable admitted tools rather than adopting the new default.
	f.run, err = f.s.StartRunWithContextPolicy(context.Background(), f.l, []Input{f.input}, contextProfileLegacy, json.RawMessage(`{}`))
	if err != nil || f.run.ID != originalRun || f.run.Attempt == originalAttempt || !fileDeliveryForRun(f.run) {
		t.Fatal("file tool policy changed across attempt recovery", err)
	}
	f.a.Lease = f.l
	node = deliveryNode(t, f, state, nil)
	result, err := node.Invoke(f.ctx(), call)
	if err != nil || len(result) != 1 {
		t.Fatal("uploaded-file discovery after restart", err)
	}
	var observation struct {
		OK   bool                  `json:"ok"`
		Data fileUploadObservation `json:"data"`
	}
	if err = json.Unmarshal([]byte(result[0].Content), &observation); err != nil || !observation.OK {
		t.Fatal(result, err)
	}
	if strings.Contains(result[0].Content, "/workspace") || strings.Contains(result[0].Content, "base64") || strings.Contains(result[0].Content, "delivery-sandbox-token") {
		t.Fatal("private file bytes/path or service credentials entered an observation")
	}
	state.sandbox.mu.Lock()
	if state.sandbox.exports != 1 {
		t.Fatal("retry re-exported an already committed file", state.sandbox.exports)
	}
	state.sandbox.mu.Unlock()
	state.mu.Lock()
	state.lostSend = true
	state.mu.Unlock()
	send := fileCall("im_send_message", "send-result", fileMessageArgs{ConversationID: f.input.ConversationID, ReplyToID: f.input.MessageID, AttachmentIDs: []string{observation.Data.Attachment.ID}})
	if _, err = node.Invoke(f.ctx(), send); err == nil {
		t.Fatal("lost send response reported a completed tool")
	}
	state.mu.Lock()
	state.lostSend = false
	state.mu.Unlock()
	if _, err = node.Invoke(f.ctx(), send); err != nil {
		t.Fatal("retry after committed send", err)
	}
	receipts := cancellationReplayReceipts(t, f.s, f.run.ID)
	if _, err = node.Invoke(f.ctx(), call); err != nil {
		t.Fatal("cached upload", err)
	}
	if _, err = node.Invoke(f.ctx(), send); err != nil {
		t.Fatal("cached file send", err)
	}
	if !reflect.DeepEqual(receipts, cancellationReplayReceipts(t, f.s, f.run.ID)) {
		t.Fatal("cached replay rewrote committed upload/send receipts")
	}
	state.mu.Lock()
	if len(state.uploads) != 1 || len(state.sends) != 1 {
		t.Fatal("delivery duplicated a file or IM message")
	}
	for _, sent := range state.sends {
		if sent.Body != "" || sent.SenderKind != "agent" || sent.ReplyToID != f.input.MessageID || len(sent.Attachments) != 1 || !bytes.Equal(state.bytes[sent.Attachments[0].ID], []byte{0, 255, 128, 1, 13, 10}) {
			t.Fatal("file-only IM message changed its result or origin", sent)
		}
	}
	state.mu.Unlock()
	f.mu.Lock()
	f.deniedConversations[f.input.ConversationID] = true
	f.mu.Unlock()
	if _, err = node.Invoke(f.ctx(), call); err == nil {
		t.Fatal("cached file upload bypassed revoked workspace sources")
	}
}

func TestAgentFileDeliveryRechecksDestinationAndLeaseBeforePublishing(t *testing.T) {
	for _, failure := range []string{"destination", "lease", "configuration"} {
		t.Run(failure, func(t *testing.T) {
			f, state := newDeliveryFixture(t)
			var denied bool
			if failure == "destination" {
				state.denied = true
			} else {
				state.sandbox.onExport = func() {
					if failure == "lease" {
						f.s.Release(context.Background(), f.l)
					} else {
						denied = true
					}
				}
			}
			node := deliveryNode(t, f, state, func(context.Context) error {
				if denied {
					return errors.New("configuration changed")
				}
				return nil
			})
			_, _ = node.Invoke(f.ctx(), fileCall("im_upload_file", "file-result", uploadFileArgs{ConversationID: f.input.ConversationID, Path: "/workspace/results/report.bin"}))
			state.mu.Lock()
			defer state.mu.Unlock()
			if state.posts != 0 || len(state.uploads) != 0 || len(state.sends) != 0 {
				t.Fatal("revoked delivery published bytes", state.posts)
			}
		})
	}
}

func TestAgentFileDeliveryAdmissionPreservesOldManifests(t *testing.T) {
	old, err := NewContextBudgetSnapshot(16384)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(old, []byte("fileDelivery")) || fileDeliveryForRun(&Run{ContextProfile: contextProfileV1, ContextBudget: old}) {
		t.Fatal("old snapshots silently gained file delivery")
	}
	newBudget, err := newContextBudgetWithFiles(16384, true)
	if err != nil || !fileDeliveryForRun(&Run{ContextProfile: contextProfileV1, ContextBudget: newBudget}) {
		t.Fatal("new policy did not pin file delivery", err)
	}
	if fileDeliveryForRun(&Run{ContextProfile: contextProfileLegacy, ContextBudget: json.RawMessage(`{}`)}) {
		t.Fatal("legacy policy gained file tools")
	}
	tools := &Tools{}
	previous, err := tools.BuildIM()
	if err != nil {
		t.Fatal(err)
	}
	current, err := tools.BuildIMWithFiles()
	if err != nil {
		t.Fatal(err)
	}
	if len(previous) != 5 || len(current) != 6 {
		t.Fatal("unexpected IM tool counts", len(previous), len(current))
	}
	for mode, items := range map[bool][]tool.BaseTool{false: previous, true: current} {
		for _, item := range items {
			info, err := item.Info(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if info.Name == "im_send_message" {
				schema, err := info.ParamsOneOf.ToJSONSchema()
				if err != nil {
					t.Fatal(err)
				}
				raw, _ := json.Marshal(schema)
				if bytes.Contains(raw, []byte(`"attachmentIds"`)) != mode {
					t.Fatal("send manifest did not match immutable file policy", string(raw))
				}
			}
		}
	}
}

func TestAgentFileDeliveryNativeTurnLoopPublishesOnlyExplicitFiles(t *testing.T) {
	f, state := newDeliveryFixture(t)
	gateway := &toolPathGateway{fixtureGateway: &fixtureGateway{ready: true}}
	var attachmentID string
	gateway.steps = []func(toolPathRequest) (chatMessage, error){
		func(req toolPathRequest) (chatMessage, error) {
			found := false
			for _, definition := range req.Tools {
				found = found || definition.Function.Name == "im_upload_file"
			}
			if !found || !strings.Contains(req.Messages[0].Content, "File results") {
				return chatMessage{}, errors.New("native Agent did not receive its admitted file tools and guidance")
			}
			return toolPathCall("im_upload_file", "native-upload", uploadFileArgs{ConversationID: f.input.ConversationID, Path: "/workspace/results/report.bin", Filename: "report.bin"})
		},
		func(req toolPathRequest) (chatMessage, error) {
			observation, err := toolPathLast(req, "native-upload")
			var result fileUploadObservation
			if err != nil || !observation.OK || json.Unmarshal(observation.Data, &result) != nil || !validFileDescriptor(result.Attachment) {
				return chatMessage{}, errors.New("native upload result did not feed the next model step")
			}
			attachmentID = result.Attachment.ID
			return toolPathCall("im_send_message", "native-send", fileMessageArgs{ConversationID: f.input.ConversationID, ReplyToID: f.input.MessageID, AttachmentIDs: []string{attachmentID}})
		},
		func(req toolPathRequest) (chatMessage, error) {
			observation, err := toolPathLast(req, "native-send")
			var result Message
			if err != nil || !observation.OK || json.Unmarshal(observation.Data, &result) != nil || len(result.Attachments) != 1 || result.Attachments[0].ID != attachmentID {
				return chatMessage{}, errors.New("native file send did not return the committed message")
			}
			return chatMessage{Role: "assistant", Content: "PRIVATE_FILE_DELIVERY_FINAL"}, nil
		},
	}
	e := &Engine{Store: f.s, Config: Config{IMURL: f.a.IM.BaseURL, IMToken: f.a.IM.ServiceKey, SandboxToken: "delivery-sandbox-token"}, Identity: &fixtureIdentity{}, Control: &fixtureControl{}, Gateway: gateway, Sandbox: state.sandbox, slots: make(chan struct{}, 1)}
	// The running Run already admitted files. Recovery must keep that policy
	// even though the current flag file only admits text-only new Runs.
	enableContextFixture(t, e)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		e.work(ctx, &wire.AgentDefinition{Id: f.l.AgentID, OwnerId: "fixture-owner", Name: "Agent", Enabled: true, Revision: 1}, f.l)
	}()
	t.Cleanup(func() { cancel(); <-done })
	waitFor(t, func() bool {
		return count(t, f.s, `SELECT count(*) FROM runtime_runs WHERE state IN ('completed','failed')`) == 1
	})
	cancel()
	<-done
	gateway.pathMu.Lock()
	defer gateway.pathMu.Unlock()
	if gateway.failure != nil || len(gateway.requests) != 3 || count(t, f.s, `SELECT count(*) FROM runtime_runs WHERE state='completed'`) != 1 {
		t.Fatal("native file delivery did not complete", gateway.failure, len(gateway.requests))
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if len(state.uploads) != 1 || len(state.sends) != 1 || count(t, f.s, `SELECT count(DISTINCT session_id) FROM runtime_session_events`) != 1 {
		t.Fatal("delivery duplicated effects, published private final text or introduced another Session")
	}
	for _, sent := range state.sends {
		if sent.Body != "" || sent.SenderKind != "agent" || sent.ReplyToID != f.input.MessageID {
			t.Fatal("private final or wrong origin entered IM", sent)
		}
	}
}

func TestAgentFileDeliveryArchiveRetainsWorkspaceSourcesAndRevocation(t *testing.T) {
	f, state := newDeliveryFixture(t)
	b := contextAddForeignMessage(f, "PRIVATE_FILE_ORIGIN_B")
	node := deliveryNode(t, f, state, nil)
	read, err := invokeContextTool(node, f.ctx(), "read-origin-B", "im_get_message", getMessageArgs{ConversationID: b.ConversationID, MessageID: b.ID})
	if err != nil {
		t.Fatal(err)
	}
	f.append(t, "origin-B-result", contextRetrievalMessage("im_get_message", "read-origin-B", read))
	writerArgs := []byte(`{"file_path":"/workspace/results/report.bin","content":"derived file"}`)
	if _, err = f.s.ToolIntent(f.ctx(), f.l, f.run, "write-result", "native:write_file", writerArgs); err != nil {
		t.Fatal(err)
	}
	if err = f.a.captureWorkspaceSources(f.ctx(), f.run, "write-result", "native:write_file", writerArgs); err != nil {
		t.Fatal(err)
	}
	args := uploadFileArgs{ConversationID: f.input.ConversationID, Path: "/workspace/results/report.bin"}
	wire, err := invokeContextTool(node, f.ctx(), "upload-derived", "im_upload_file", args)
	if err != nil || !strings.Contains(wire, `"ok":true`) {
		t.Fatal(wire, err)
	}
	ref, err := f.a.ArchiveToolPath(f.ctx(), "tool_clear", contextRealIMDetail("im_upload_file", "upload-derived", args, wire), wire)
	if err != nil {
		t.Fatal(err)
	}
	artifact, err := f.a.load(f.ctx(), f.run, ref)
	if err != nil {
		t.Fatal(err)
	}
	seenB := false
	for _, source := range artifact.Sources {
		seenB = seenB || source.ConversationID == b.ConversationID && source.MessageID == b.ID
	}
	if !seenB {
		t.Fatal("delivery receipt lost the actual workspace source after archival")
	}
	if _, err = f.a.Read(f.ctx(), ref, 0, 0); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	f.deniedConversations[b.ConversationID] = true
	f.mu.Unlock()
	if _, err = f.a.Read(f.ctx(), ref, 0, 0); err == nil {
		t.Fatal("archived upload escaped the revoked workspace source")
	}
	if _, err = invokeContextTool(node, f.ctx(), "upload-derived", "im_upload_file", args); err == nil {
		t.Fatal("cached upload escaped the revoked workspace source")
	}
	var observation struct {
		Data fileUploadObservation `json:"data"`
	}
	if err = json.Unmarshal([]byte(wire), &observation); err != nil {
		t.Fatal(err)
	}
	if _, err = invokeContextTool(node, f.ctx(), "send-after-revocation", "im_send_message", fileMessageArgs{ConversationID: f.input.ConversationID, AttachmentIDs: []string{observation.Data.Attachment.ID}}); err == nil {
		t.Fatal("a source revoked between upload and send could still publish its file")
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if len(state.sends) != 0 {
		t.Fatal("revoked source file entered IM")
	}
}
