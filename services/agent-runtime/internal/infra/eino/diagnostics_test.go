package eino

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/cloudwego/eino/schema"
	"github.com/cloudwego/kitex/client/callopt"
	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.uber.org/zap"
	wire "teamie/contracts/gen/agent/kitex_gen/agent"
	"teamie/contracts/gen/agent/kitex_gen/agent/modelgatewayservice"
	"teamie/contracts/gen/agent/hertz_gen/agent/modeltransport"
	"teamie/pkg/observability"
)

type diagnosticBuffer struct {
	mu   sync.Mutex
	data bytes.Buffer
}

func (b *diagnosticBuffer) Write(value []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.data.Write(value)
}
func (b *diagnosticBuffer) snapshot() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.data.String()
}
func (b *diagnosticBuffer) modelEvents() []map[string]any {
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(b.snapshot()), "\n") {
		var event map[string]any
		if json.Unmarshal([]byte(line), &event) == nil && event["event.action"] == "model-call" {
			out = append(out, event)
		}
	}
	return out
}

func TestStreamDiagnosticsIdentifyFailureStageAndPreservePrivacy(t *testing.T) {
	for _, test := range []struct {
		name, stage, reason, code string
		receipts                  int
	}{
		{"transport", "frame-decode", "invalid_transport_frame", "invalid_upstream_response", 0},
		{"decode", "message-decode", "native_deserialize_failed", "invalid_upstream_response", 0},
		{"concat", "aggregate-final", "tool_call_id_conflict", "invalid_upstream_response", 0},
		{"validate", "validate-result", "unsupported_content_block", "invalid_upstream_response", 0},
		{"empty", "stream-terminal", "empty_stream", "invalid_upstream_response", 0},
		{"interrupted", "stream-read", "terminal_frame_missing", "gateway_stream_interrupted", 0},
		{"read-error", "stream-read", "transport_read_failed", "gateway_stream_interrupted", 0},
		{"frame-limit", "stream-read", "transport_frame_limit_exceeded", "gateway_stream_interrupted", 0},
		{"persist", "persist-receipt", "", "lease_lost", 0},
		{"success", "persist-receipt", "", "", 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			s, lease, run := nativeModelRun(t)
			var logs diagnosticBuffer
			undo := zap.ReplaceGlobals(observability.New(observability.Config{Service: "teamie.agent-runtime", Version: "fixture", Output: &logs}))
			t.Cleanup(undo)
			previous := otel.GetTracerProvider()
			provider := sdktrace.NewTracerProvider(sdktrace.WithSampler(sdktrace.NeverSample()))
			otel.SetTracerProvider(provider)
			t.Cleanup(func() { otel.SetTracerProvider(previous); _ = provider.Shutdown(context.Background()) })
			fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				w.Header().Set("Content-Type", "application/x-ndjson")
				if test.name == "read-error" {
					// A valid chunk followed by a truncated HTTP body is a read
					// failure, distinct from a clean EOF without a terminal frame.
					w.Header().Set("Content-Length", "10000")
				}
				emit := func(message *schema.AgenticMessage) {
					raw, err := (&schema.HumanReadableSerializer{}).Marshal(message)
					if err != nil {
						t.Error(err)
						return
					}
					_ = json.NewEncoder(w).Encode(map[string]string{"type": "chunk", "responseJson": string(raw)})
				}
				chunk := func(block *schema.ContentBlock) *schema.AgenticMessage {
					return &schema.AgenticMessage{Role: schema.AgenticRoleTypeAssistant, ContentBlocks: []*schema.ContentBlock{block}}
				}
				switch test.name {
				case "frame-limit":
					_, _ = io.WriteString(w, strings.Repeat("x", modeltransport.MaxHTTPStreamFrameBytes+1))
					return
				case "transport":
					_, _ = io.WriteString(w, `{"invalid-private-frame`+"\n")
					return
				case "decode":
					_ = json.NewEncoder(w).Encode(map[string]string{"type": "chunk", "responseJson": "private-native-data"})
					return
				case "concat":
					for _, id := range []string{"private-call-a", "private-call-b"} {
						emit(chunk(schema.NewContentBlockChunk(&schema.FunctionToolCall{CallID: id, Name: "write_file", Arguments: "private-arguments"}, &schema.StreamingMeta{Index: 0})))
					}
				case "validate":
					emit(chunk(schema.NewContentBlock(&schema.UserInputText{Text: "private-model-text"})))
				case "empty":
				default:
					emit(chunk(schema.NewContentBlockChunk(&schema.AssistantGenText{Text: "private-model-text"}, &schema.StreamingMeta{Index: 0})))
				}
				if test.name == "interrupted" || test.name == "read-error" {
					return
				}
				if test.name == "persist" {
					if _, err := s.DB.Exec(context.Background(), `UPDATE runtime_agents SET lease_owner='fixture-new-owner' WHERE agent_id=$1`, lease.AgentID); err != nil {
						t.Error(err)
					}
				}
				_ = json.NewEncoder(w).Encode(map[string]string{"type": "done"})
			}))
			defer fixture.Close()
			gateway := &GatewayModel{Store: s, Lease: lease, AgentID: lease.AgentID, StreamURL: fixture.URL}
			reader, err := gateway.Stream(WithRun(context.Background(), run), inputMessages(run.Inputs))
			if err != nil {
				t.Fatal(err)
			}
			for {
				_, err = reader.Recv()
				if err != nil {
					break
				}
			}
			reader.Close()
			if test.code == "" {
				if !errors.Is(err, io.EOF) {
					t.Fatal(err)
				}
			} else if SafeError(err) != test.code {
				t.Fatalf("safe error changed: %v", err)
			}
			waitFor(t, func() bool { return len(logs.modelEvents()) == 1 })
			event := logs.modelEvents()[0]
			if event["custom.model.failure_stage"] != test.stage || event["custom.runtime.run_id"] != run.ID || event["custom.runtime.attempt_id"] != run.Attempt || event["custom.model.step"] != float64(1) {
				t.Fatal("missing exact diagnostic scope", event)
			}
			if test.reason != "" && event["custom.model.failure_reason"] != test.reason {
				t.Fatal("native cause classification lost", event)
			}
			if test.name == "concat" {
				var model *ModelError
				if !errors.As(err, &model) || model.Cause == nil {
					t.Fatal("native concat cause discarded")
				}
			}
			if test.name == "read-error" && !errors.Is(err, io.ErrUnexpectedEOF) {
				t.Fatal("underlying transport failure discarded", err)
			}
			if test.name == "frame-limit" && !errors.Is(err, bufio.ErrTooLong) {
				t.Fatal("underlying framing failure discarded", err)
			}
			if event["trace.id"] == nil || event["span.id"] == nil || event["event.duration"] == nil {
				t.Fatal("uncorrelated diagnostic", event)
			}
			if got := count(t, s, `SELECT count(*) FROM runtime_model_calls`); got != test.receipts {
				t.Fatalf("receipt outcome changed: %d", got)
			}
			for _, private := range []string{"private-native-data", "private-model-text", "private-arguments", "private-call-a", "private-call-b", "invalid-private-frame"} {
				if strings.Contains(logs.snapshot(), private) {
					t.Fatalf("private response data logged: %s", private)
				}
			}
		})
	}
}

func TestStreamConcatCauseKeepsPublicErrorStable(t *testing.T) {
	_, err := concatStreamSegments([]*schema.AgenticMessage{
		{Role: schema.AgenticRoleTypeAssistant, ContentBlocks: []*schema.ContentBlock{schema.NewContentBlockChunk(&schema.FunctionToolCall{CallID: "first", Name: "write_file"}, &schema.StreamingMeta{Index: 0})}},
		{Role: schema.AgenticRoleTypeAssistant, ContentBlocks: []*schema.ContentBlock{schema.NewContentBlockChunk(&schema.FunctionToolCall{CallID: "second", Name: "write_file"}, &schema.StreamingMeta{Index: 0})}},
	})
	var failure *ModelError
	if !errors.As(err, &failure) || failure.Cause == nil || failure.Reason != "tool_call_id_conflict" || failure.Error() != "model gateway: invalid_upstream_response" || !failure.Retry {
		t.Fatal("diagnostics altered public/replay failure semantics", err)
	}
}

type blockedDiagnosticFailure struct {
	modelgatewayservice.Client
	entered, release chan struct{}
	once             sync.Once
}

func (g *blockedDiagnosticFailure) Chat(context.Context, *wire.GatewayChatRequest, ...callopt.Option) (*wire.GatewayChatResponse, error) {
	g.once.Do(func() { close(g.entered) })
	<-g.release
	return nil, &ModelError{Code: "invalid_upstream_response", Retry: true}
}

func TestQueuedPreemptionDoesNotHideActualRunFailureInLogs(t *testing.T) {
	previous := otel.GetTracerProvider()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSampler(sdktrace.NeverSample()))
	otel.SetTracerProvider(provider)
	t.Cleanup(func() { otel.SetTracerProvider(previous); _ = provider.Shutdown(context.Background()) })
	s := testStore(t)
	e, source, base, _ := fixtureEngine(t, s)
	enableContextFixture(t, e)
	behaviorSource(t, source, "private-first-request", "", "")
	var logs diagnosticBuffer
	undo := zap.ReplaceGlobals(observability.New(observability.Config{Service: "teamie.agent-runtime", Output: &logs}))
	t.Cleanup(undo)
	gateway := &blockedDiagnosticFailure{Client: base, entered: make(chan struct{}), release: make(chan struct{})}
	e.Gateway = gateway
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(gateway.release) }) }
	defer release()
	nextPoll := make(chan struct{})
	var pollOnce sync.Once
	im := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/sync" && r.URL.Query().Get("cursor") == "2" {
			pollOnce.Do(func() { close(nextPoll) })
		}
		source.serve(w, r)
	}))
	defer im.Close()
	e.Config.IMURL = im.URL
	cancel, done := startFixtureWorker(t, e, source)
	defer func() { release(); cancel(); <-done }()
	waitFor(t, func() bool {
		select {
		case <-gateway.entered:
			return true
		default:
			return false
		}
	})
	behaviorSource(t, source, "private-second-request", "", "")
	waitFor(t, func() bool {
		select {
		case <-nextPoll:
			return true
		default:
			return false
		}
	})
	release()
	var terminal map[string]any
	waitFor(t, func() bool {
		for _, line := range strings.Split(logs.snapshot(), "\n") {
			var event map[string]any
			if json.Unmarshal([]byte(line), &event) == nil && event["event.action"] == "agent-run" && event["custom.runtime.preempted"] == true {
				terminal = event
				return true
			}
		}
		return false
	})
	if terminal["event.outcome"] != "failure" || terminal["custom.runtime.state"] != "interrupted" || terminal["custom.runtime.failure_code"] != "invalid_upstream_response" {
		t.Fatal("fatal failure was hidden by new_input", terminal)
	}
	if strings.Contains(logs.snapshot(), "private-first-request") || strings.Contains(logs.snapshot(), "private-second-request") {
		t.Fatal("request text reached run logs")
	}
}
