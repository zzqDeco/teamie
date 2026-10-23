package eino

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/cloudwego/eino/schema"
	"github.com/cloudwego/kitex/client/callopt"
	"github.com/google/uuid"
	wire "teamie/contracts/gen/agent/kitex_gen/agent"
)

type toolPathRequest struct {
	Messages []chatMessage `json:"messages"`
	Tools    []chatTool    `json:"tools"`
}

type toolPathObservation struct {
	OK    bool            `json:"ok"`
	Error string          `json:"error"`
	Data  json.RawMessage `json:"data"`
}

type toolPathPage struct {
	Messages      []Message `json:"messages"`
	HasMore       bool      `json:"hasMore"`
	NextBeforeSeq int64     `json:"nextBeforeSeq"`
}

// The real adapter and native ChatModelAgent execute this protocol sequence;
// only the Gateway transport is replaced. Tool results must drive the next call.
type toolPathGateway struct {
	*fixtureGateway
	pathMu   sync.Mutex
	steps    []func(toolPathRequest) (chatMessage, error)
	requests []wire.GatewayChatRequest
	failure  error
}

func (g *toolPathGateway) Chat(_ context.Context, r *wire.GatewayChatRequest, _ ...callopt.Option) (*wire.GatewayChatResponse, error) {
	g.pathMu.Lock()
	defer g.pathMu.Unlock()
	var request toolPathRequest
	if err := decodeFixtureRequest(r.RequestJson, &request); err != nil {
		g.failure = err
		return nil, err
	}
	index := len(g.requests)
	g.requests = append(g.requests, *r)
	if index >= len(g.steps) {
		g.failure = fmt.Errorf("unexpected extra model call %d", index+1)
		return nil, g.failure
	}
	message, err := g.steps[index](request)
	if err != nil {
		g.failure = fmt.Errorf("model fixture step %d: %w", index+1, err)
		return nil, g.failure
	}
	raw, err := json.Marshal(map[string]any{"choices": []any{map[string]any{"message": message, "finish_reason": "stop"}}})
	if err != nil {
		g.failure = err
		return nil, err
	}
	response := string(raw)
	return &wire.GatewayChatResponse{Ok: true, ResponseJson: modelFixtureResponse(response)}, nil
}

func toolPathCall(name, id string, args any) (chatMessage, error) {
	raw, err := json.Marshal(args)
	if err != nil {
		return chatMessage{}, err
	}
	return chatMessage{Role: "assistant", ToolCalls: []schema.ToolCall{{ID: id, Type: "function", Function: schema.FunctionCall{Name: name, Arguments: string(raw)}}}}, nil
}

func toolPathLast(request toolPathRequest, id string) (toolPathObservation, error) {
	var observation toolPathObservation
	if len(request.Messages) == 0 {
		return observation, fmt.Errorf("missing tool observation")
	}
	last := request.Messages[len(request.Messages)-1]
	if last.Role != "tool" || last.ToolCallID != id {
		return observation, fmt.Errorf("expected observation for %s, got role=%s call=%s", id, last.Role, last.ToolCallID)
	}
	if err := json.Unmarshal([]byte(last.Content), &observation); err != nil {
		return observation, err
	}
	return observation, nil
}

func toolPathSchemas(request toolPathRequest) error {
	expected := map[string][]string{
		"im_list_messages":  {"conversationId", "beforeSeq", "limit", "keyword"},
		"im_get_message":    {"conversationId", "messageId"},
		"workspace_execute": {"command", "run_in_background", "timeout"},
	}
	for _, definition := range request.Tools {
		fields, wanted := expected[definition.Function.Name]
		if !wanted {
			continue
		}
		var params struct {
			Type       string                     `json:"type"`
			Properties map[string]json.RawMessage `json:"properties"`
		}
		if err := json.Unmarshal(definition.Function.Parameters, &params); err != nil {
			return err
		}
		if definition.Type != "function" || params.Type != "object" {
			return fmt.Errorf("invalid native tool schema for %s", definition.Function.Name)
		}
		for _, field := range fields {
			if _, ok := params.Properties[field]; !ok {
				return fmt.Errorf("%s schema omitted %s", definition.Function.Name, field)
			}
		}
		delete(expected, definition.Function.Name)
	}
	if len(expected) != 0 {
		return fmt.Errorf("required native tool schemas missing: %v", expected)
	}
	return nil
}

type toolPathHTTPRequest struct {
	method, path string
	before       int64
	limit        int
}

// History is deliberately absent from the new-message sync/bootstrap fixture.
// It is available only when a model explicitly reads the source conversation.
type toolPathIM struct {
	base                 *fixtureIM
	conversation         string
	older, tombstone     Message
	mu                   sync.Mutex
	requests             []toolPathHTTPRequest
	revokeAfterFirstPage bool
	revoked              bool
}

func newToolPathIM(t *testing.T, e *Engine, base *fixtureIM) *toolPathIM {
	t.Helper()
	f := &toolPathIM{base: base, conversation: uuid.NewString()}
	f.older = Message{ID: uuid.NewString(), ConversationID: f.conversation, SenderID: uuid.NewString(), SenderName: "Historian", SenderKind: "human", Body: "Distant NeEdLe record outside recent context", Seq: 2, CreatedAt: "2026-10-01T00:00:00Z"}
	retracted := "2026-10-02T00:00:00Z"
	f.tombstone = Message{ID: uuid.NewString(), ConversationID: f.conversation, SenderID: base.agent, SenderName: "Agent", SenderKind: "agent", Seq: 3, CreatedAt: "2026-10-01T00:01:00Z", RetractedAt: &retracted}
	server := httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(server.Close)
	e.Config.IMURL = server.URL
	return f
}

func (f *toolPathIM) serve(w http.ResponseWriter, r *http.Request) {
	prefix := "/api/v1/conversations/" + f.conversation + "/messages"
	if r.URL.Path != prefix && !strings.HasPrefix(r.URL.Path, prefix+"/") {
		f.base.serve(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	write := func(value any) { _ = json.NewEncoder(w).Encode(value) }
	if r.Header.Get("Authorization") != "Bearer "+strings.Repeat("a", 64) || r.Header.Get("X-Teamie-Service-Key") != "fixture-im-secret" {
		w.WriteHeader(http.StatusUnauthorized)
		write(map[string]any{"error": map[string]string{"code": "unauthenticated"}})
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	before, _ := strconv.ParseInt(r.URL.Query().Get("beforeSeq"), 10, 64)
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	f.requests = append(f.requests, toolPathHTTPRequest{r.Method, r.URL.Path, before, limit})
	if f.revoked {
		w.WriteHeader(http.StatusForbidden)
		write(map[string]any{"error": map[string]string{"code": "forbidden", "message": "PRIVATE_ACL_INTERNAL_DETAIL"}})
		return
	}
	if r.Method == http.MethodGet && r.URL.Path == prefix {
		if limit != 2 || (before != 0 && before != 40) {
			w.WriteHeader(http.StatusBadRequest)
			write(map[string]any{"error": map[string]string{"code": "unexpected_history_page"}})
			return
		}
		if r.URL.Query().Get("q") != "" {
			if !strings.Contains(strings.ToLower(f.older.Body), strings.ToLower(r.URL.Query().Get("q"))) {
				write(map[string]any{"messages": []Message{}, "hasMore": false})
			} else {
				write(map[string]any{"messages": []Message{f.older}, "hasMore": false})
			}
			return
		}
		if before == 0 {
			first, second := f.older, f.older
			first.ID, first.Seq, first.Body = uuid.NewString(), 40, "A recent unrelated message"
			second.ID, second.Seq, second.Body = uuid.NewString(), 41, "Another unrelated message"
			write(map[string]any{"messages": []Message{first, second}, "hasMore": true})
			f.revoked = f.revokeAfterFirstPage
			return
		}
		write(map[string]any{"messages": []Message{f.older, f.tombstone}, "hasMore": false})
		return
	}
	if r.Method == http.MethodGet {
		for _, message := range []Message{f.older, f.tombstone} {
			if r.URL.Path == prefix+"/"+message.ID {
				write(map[string]any{"message": message})
				return
			}
		}
	}
	w.WriteHeader(http.StatusNotFound)
	write(map[string]any{"error": map[string]string{"code": "not_found"}})
}

func runToolPathWorker(t *testing.T, e *Engine, f *fixtureIM, g *toolPathGateway) {
	t.Helper()
	cancel, done := startFixtureWorker(t, e, f)
	waitFor(t, func() bool {
		g.pathMu.Lock()
		failure := g.failure
		g.pathMu.Unlock()
		if failure != nil {
			t.Fatal(failure)
		}
		return count(t, e.Store, "SELECT count(*) FROM runtime_runs WHERE state='completed'") == 1
	})
	cancel()
	<-done
	g.pathMu.Lock()
	defer g.pathMu.Unlock()
	if len(g.requests) != len(g.steps) {
		t.Fatalf("model sequence incomplete: %d/%d", len(g.requests), len(g.steps))
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.posts != 0 || len(f.sent) != 0 {
		t.Fatalf("tool observations or final output were automatically posted: attempts=%d messages=%d", f.posts, len(f.sent))
	}
}

func toolPathReceipt(t *testing.T, s *Store, agent, call, name string) toolPathObservation {
	t.Helper()
	var actualName string
	var raw []byte
	if err := s.DB.QueryRow(context.Background(), `SELECT t.name,t.result FROM runtime_tool_calls t JOIN runtime_runs r ON r.id=t.run_id WHERE r.agent_id=$1 AND t.call_id=$2`, agent, call).Scan(&actualName, &raw); err != nil {
		t.Fatal(err)
	}
	if actualName != name || len(raw) == 0 {
		t.Fatalf("missing attributed receipt for %s/%s", name, call)
	}
	var result toolPathObservation
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func TestNativeEinoExplicitServerHistorySearchAndReferences(t *testing.T) {
	s := testStore(t)
	e, f, baseGateway, _ := fixtureEngine(t, s)
	history := newToolPathIM(t, e, f)
	gateway := &toolPathGateway{fixtureGateway: baseGateway}
	e.Gateway = gateway
	gateway.steps = []func(toolPathRequest) (chatMessage, error){
		func(request toolPathRequest) (chatMessage, error) {
			if err := toolPathSchemas(request); err != nil {
				return chatMessage{}, err
			}
			for _, message := range request.Messages {
				if strings.Contains(message.Content, history.older.Body) {
					return chatMessage{}, fmt.Errorf("history loaded without explicit read")
				}
			}
			return toolPathCall("im_list_messages", "history-search", listMessagesArgs{ConversationID: history.conversation, Limit: 2, Keyword: "NEEDLE"})
		},
		func(request toolPathRequest) (chatMessage, error) {
			observation, err := toolPathLast(request, "history-search")
			if err != nil {
				return chatMessage{}, err
			}
			var page toolPathPage
			if err = json.Unmarshal(observation.Data, &page); err != nil {
				return chatMessage{}, err
			}
			if !observation.OK || page.HasMore || len(page.Messages) != 1 || !reflect.DeepEqual(page.Messages[0], history.older) {
				return chatMessage{}, fmt.Errorf("server search or source provenance lost: %+v", page)
			}
			return toolPathCall("im_get_message", "history-reference", getMessageArgs{ConversationID: history.conversation, MessageID: history.older.ID})
		},
		func(request toolPathRequest) (chatMessage, error) {
			observation, err := toolPathLast(request, "history-reference")
			if err != nil {
				return chatMessage{}, err
			}
			var message Message
			_ = json.Unmarshal(observation.Data, &message)
			if !observation.OK || !reflect.DeepEqual(message, history.older) {
				return chatMessage{}, fmt.Errorf("reference lost source")
			}
			return toolPathCall("im_get_message", "history-tombstone", getMessageArgs{ConversationID: history.conversation, MessageID: history.tombstone.ID})
		},
		func(request toolPathRequest) (chatMessage, error) {
			observation, err := toolPathLast(request, "history-tombstone")
			if err != nil {
				return chatMessage{}, err
			}
			var message Message
			_ = json.Unmarshal(observation.Data, &message)
			if !observation.OK || message.ID != history.tombstone.ID || message.RetractedAt == nil || message.Body != "" {
				return chatMessage{}, fmt.Errorf("retracted reference not attributable tombstone")
			}
			return chatMessage{Role: "assistant", Content: "PRIVATE_HISTORY_RESULT_NEVER_POST"}, nil
		},
	}
	f.add("Read distant history explicitly", "")
	runToolPathWorker(t, e, f, gateway)
	for call, name := range map[string]string{"history-search": "im_list_messages", "history-reference": "im_get_message", "history-tombstone": "im_get_message"} {
		if !toolPathReceipt(t, s, f.agent, call, name).OK {
			t.Fatal("missing history receipt")
		}
	}
	history.mu.Lock()
	defer history.mu.Unlock()
	if len(history.requests) != 3 {
		t.Fatalf("unexpected explicit reads: %d", len(history.requests))
	}
}

func TestNativeEinoCurrentACLDenialsRemainToolObservations(t *testing.T) {
	s := testStore(t)
	e, f, baseGateway, _ := fixtureEngine(t, s)
	history := newToolPathIM(t, e, f)
	history.revokeAfterFirstPage = true
	gateway := &toolPathGateway{fixtureGateway: baseGateway}
	e.Gateway = gateway
	denied := func(request toolPathRequest, call, code string) error {
		observation, err := toolPathLast(request, call)
		if err != nil {
			return err
		}
		if observation.OK || observation.Error != code || strings.Contains(request.Messages[len(request.Messages)-1].Content, "PRIVATE_ACL_INTERNAL_DETAIL") {
			return fmt.Errorf("denial must be a safe failed observation for %s: %+v", call, observation)
		}
		return nil
	}
	gateway.steps = []func(toolPathRequest) (chatMessage, error){
		func(request toolPathRequest) (chatMessage, error) {
			return toolPathCall("im_list_messages", "before-revocation", listMessagesArgs{ConversationID: history.conversation, Limit: 2})
		},
		func(request toolPathRequest) (chatMessage, error) {
			observation, err := toolPathLast(request, "before-revocation")
			var page toolPathPage
			if err != nil {
				return chatMessage{}, err
			}
			if err = json.Unmarshal(observation.Data, &page); err != nil {
				return chatMessage{}, err
			}
			if !observation.OK || len(page.Messages) != 2 || page.NextBeforeSeq != 40 {
				return chatMessage{}, fmt.Errorf("initially authorized history read failed")
			}
			return toolPathCall("im_list_messages", "revoked-history", listMessagesArgs{ConversationID: history.conversation, BeforeSeq: page.NextBeforeSeq, Limit: 2})
		},
		func(request toolPathRequest) (chatMessage, error) {
			if err := denied(request, "revoked-history", "forbidden"); err != nil {
				return chatMessage{}, err
			}
			return toolPathCall("im_get_message", "revoked-reference", getMessageArgs{ConversationID: history.conversation, MessageID: history.older.ID})
		},
		func(request toolPathRequest) (chatMessage, error) {
			if err := denied(request, "revoked-reference", "forbidden"); err != nil {
				return chatMessage{}, err
			}
			return toolPathCall("im_send_message", "revoked-send", sendArgs{ConversationID: history.conversation, Body: "This explicit send must be rejected by current membership"})
		},
		func(request toolPathRequest) (chatMessage, error) {
			if err := denied(request, "revoked-send", "forbidden"); err != nil {
				return chatMessage{}, err
			}
			return chatMessage{Role: "assistant", Content: "PRIVATE_DENIAL_SUMMARY_NEVER_POST"}, nil
		},
	}
	f.add("Observe current permissions without posting tool errors to chat", "")
	runToolPathWorker(t, e, f, gateway)
	for call, name := range map[string]string{"revoked-history": "im_list_messages", "revoked-reference": "im_get_message", "revoked-send": "im_send_message"} {
		receipt := toolPathReceipt(t, s, f.agent, call, name)
		code := "forbidden"
		if call == "denied-workspace" {
			code = "sandbox_denied"
		}
		if receipt.OK || receipt.Error != code {
			t.Fatalf("failed observation receipt was not preserved for %s", call)
		}
	}
	history.mu.Lock()
	defer history.mu.Unlock()
	if len(history.requests) != 4 || history.requests[0].method != http.MethodGet || history.requests[3].method != http.MethodPost {
		t.Fatalf("current ACL checks were skipped or retried instead of observed: %+v", history.requests)
	}
	if count(t, s, "SELECT count(*) FROM runtime_runs WHERE state IN ('retry','failed')") != 0 {
		t.Fatal("permanent ACL denials incorrectly failed or retried the Agent run")
	}
}
