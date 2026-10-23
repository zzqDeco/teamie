package eino

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cloudwego/eino-ext/components/model/agenticopenai"
	"github.com/cloudwego/eino/schema"
	wire "teamie/contracts/gen/agent/kitex_gen/agent"
	"teamie/contracts/gen/agent/hertz_gen/agent/modeltransport"
)

func TestNativeHTTPGenerateRetainsRichReceiptAndReplaysWithoutRequest(t *testing.T) {
	s, lease, run := nativeModelRun(t)
	output := &schema.AgenticMessage{Role: schema.AgenticRoleTypeAssistant, ContentBlocks: []*schema.ContentBlock{schema.NewContentBlock(&schema.Reasoning{Text: "private reasoning"}), schema.NewContentBlock(&schema.AssistantGenText{Text: "private answer"}), schema.NewContentBlock(&schema.FunctionToolCall{CallID: "http-stable-call", Name: "im_send_message", Arguments: `{"body":"explicit"}`})}, ResponseMeta: &schema.AgenticResponseMeta{TokenUsage: &schema.TokenUsage{PromptTokens: 8, CompletionTokens: 3, TotalTokens: 11}, Extension: &agenticopenai.ChatResponseMetaExtension{ID: "http-generation", FinishReason: "tool_calls"}}}
	raw, err := (&schema.HumanReadableSerializer{}).Marshal(output)
	if err != nil {
		t.Fatal(err)
	}
	serialized := string(raw)
	var calls atomic.Int32
	fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != modeltransport.GeneratePath || r.Header.Get("Authorization") != "Bearer fixture-service-token" {
			t.Error("runtime constructor did not route generation to authenticated HTTP")
		}
		var in modeltransport.CallRequest
		dec := json.NewDecoder(r.Body)
		dec.DisallowUnknownFields()
		if dec.Decode(&in) != nil || in.AgentID != lease.AgentID || in.RunID != executionID(run) {
			t.Error("HTTP generation lost durable attribution")
		}
		var envelope modeltransport.Request
		if json.Unmarshal([]byte(in.RequestJSON), &envelope) != nil || envelope.Protocol != modeltransport.Version || envelope.Stream {
			t.Error("HTTP generation changed native request identity or mode")
		}
		_ = json.NewEncoder(w).Encode(&wire.GatewayChatResponse{Ok: true, ResponseJson: &serialized})
	}))
	defer fixture.Close()
	// Exercise production construction: only bounded Ping/Cancel retain their
	// unused RPC address. Generate cannot silently fall back to that address.
	e, err := New(s, Config{ControlAddr: "127.0.0.1:1", GatewayAddr: "127.0.0.1:1", SandboxAddr: "127.0.0.1:1", IdentityAddr: "127.0.0.1:1", GatewayStreamURL: fixture.URL + modeltransport.StreamPath})
	if err != nil {
		t.Fatal(err)
	}
	gateway := &GatewayModel{Client: e.Gateway, Token: "fixture-service-token", Store: s, Lease: lease, AgentID: lease.AgentID}
	ctx := WithRun(context.Background(), run)
	result, err := gateway.Generate(ctx, inputMessages(run.Inputs))
	if err != nil {
		t.Fatal(err)
	}
	run.Step.Store(0)
	cached, err := gateway.Generate(ctx, inputMessages(run.Inputs))
	if err != nil || calls.Load() != 1 {
		t.Fatal("HTTP model receipt caused another request", err)
	}
	for _, message := range []*schema.AgenticMessage{result, cached} {
		meta, ok := message.ResponseMeta.Extension.(*agenticopenai.ChatResponseMetaExtension)
		if len(message.ContentBlocks) != 3 || message.ContentBlocks[0].Reasoning.Text != "private reasoning" || message.ContentBlocks[2].FunctionToolCall.CallID != "http-stable-call" || !ok || meta.ID != "http-generation" || message.ResponseMeta.TokenUsage.TotalTokens != 11 {
			t.Fatal("HTTP generation flattened native blocks or metadata")
		}
	}
	if count(t, s, `SELECT count(*) FROM runtime_model_calls`) != 1 {
		t.Fatal("complete HTTP generation lacks a durable receipt")
	}
}

func TestNativeHTTPGenerateCancelStopsSilentHeadersOrBodyWithoutReceipt(t *testing.T) {
	for _, headers := range []bool{false, true} {
		t.Run(map[bool]string{false: "waiting_for_headers", true: "reading_body"}[headers], func(t *testing.T) {
			s, lease, run := nativeModelRun(t)
			entered, canceled := make(chan struct{}), make(chan struct{})
			fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				if headers {
					w.Header().Set("Content-Type", "application/json")
					_, _ = io.WriteString(w, `{"ok":true,"responseJson":"`)
					w.(http.Flusher).Flush()
				}
				close(entered)
				<-r.Context().Done()
				close(canceled)
			}))
			ctx, cancel := context.WithCancel(WithRun(context.Background(), run))
			defer func() { cancel(); fixture.Close() }()
			client, err := newGatewayHTTPClient(nil, fixture.URL+modeltransport.StreamPath)
			if err != nil {
				t.Fatal(err)
			}
			gateway := &GatewayModel{Client: client, Store: s, Lease: lease, AgentID: lease.AgentID}
			finished := make(chan error, 1)
			go func() { _, err := gateway.Generate(ctx, inputMessages(run.Inputs)); finished <- err }()
			select {
			case <-entered:
			case <-time.After(2 * time.Second):
				t.Fatal("HTTP generation did not start")
			}
			cancel()
			select {
			case err := <-finished:
				if err == nil {
					t.Fatal("canceled generation reported success")
				}
			case <-time.After(time.Second):
				t.Fatal("caller cancel left HTTP generation blocked")
			}
			select {
			case <-canceled:
			case <-time.After(time.Second):
				t.Fatal("HTTP generation did not propagate cancellation")
			}
			if count(t, s, `SELECT count(*) FROM runtime_model_calls`) != 0 {
				t.Fatal("canceled HTTP generation persisted a model receipt")
			}
		})
	}
}

func TestGatewayHTTPResponseLimitAccountsForDoubleEncodedNativeJSON(t *testing.T) {
	for _, tooLarge := range []bool{false, true} {
		t.Run(map[bool]string{false: "maximum_escaped_response", true: "over_limit"}[tooLarge], func(t *testing.T) {
			n := modeltransport.MaxResponseBytes
			if tooLarge {
				n += 4096
			}
			value := strings.Repeat(`"`, n)
			fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_ = json.NewEncoder(w).Encode(&wire.GatewayChatResponse{Ok: true, ResponseJson: &value})
			}))
			defer fixture.Close()
			client, err := newGatewayHTTPClient(nil, fixture.URL+modeltransport.StreamPath)
			if err != nil {
				t.Fatal(err)
			}
			response, err := client.Chat(context.Background(), &wire.GatewayChatRequest{})
			if tooLarge {
				if err == nil {
					t.Fatal("oversized double-encoded response was accepted")
				}
			} else if err != nil || response.GetResponseJson() != value {
				t.Fatal("valid maximum escaped native response was truncated", err)
			}
		})
	}
}

func TestGatewayHTTPRejectsMalformedRepliesAndUsesNoProxyOrFixedBudget(t *testing.T) {
	if gatewayStreamHTTP.Timeout != 0 || gatewayStreamHTTP.Transport.(*http.Transport).Proxy != nil || gatewayStreamHTTP.Transport.(*http.Transport).ResponseHeaderTimeout != 0 {
		t.Fatal("generation inherited an environment proxy or fixed inference budget")
	}
	for _, reply := range []string{`{"ok":true} {"ok":true}`, `{"ok":true,"providerSecret":"must-not-be-parsed"}`} {
		fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, reply) }))
		client, err := newGatewayHTTPClient(nil, fixture.URL+modeltransport.StreamPath)
		if err != nil {
			t.Fatal(err)
		}
		_, err = client.Chat(context.Background(), &wire.GatewayChatRequest{})
		fixture.Close()
		if err == nil || strings.Contains(err.Error(), "must-not-be-parsed") {
			t.Fatal("malformed gateway reply was accepted or exposed its contents")
		}
	}
}
