package engine

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
	"github.com/google/uuid"
	"teamie/agent-protocol/modeltransport"
)

func TestWebToolsReceiptReplayAndContextArchiveIntegration(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	if _, err := s.DB.Exec(ctx, contextArchiveSchemaSQL); err != nil {
		t.Fatal(err)
	}
	lease, err := s.Acquire(ctx, uuid.NewString(), "web-tools-test")
	if err != nil {
		t.Fatal(err)
	}

	var searchCalls, extractCalls atomic.Int32
	longMarkdown := "# Extracted Article\n\n" + strings.Repeat("Section paragraph with detailed technical content for archival verification.\n", 400)

	gatewaySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer fixture-gateway-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case modeltransport.WebSearchPath:
			searchCalls.Add(1)
			var req modeltransport.WebSearchRequest
			_ = json.NewDecoder(r.Body).Decode(&req)
			if req.Query == "unconfigured-check" {
				_ = json.NewEncoder(w).Encode(modeltransport.WebSearchResponse{
					OK:         false,
					ErrorCode:  "web_search_unconfigured",
					StatusCode: 409,
				})
				return
			}
			_ = json.NewEncoder(w).Encode(modeltransport.WebSearchResponse{
				OK: true,
				Results: []modeltransport.WebSearchResult{
					{
						Title:         "Teamie Web Search Docs",
						URL:           "https://docs.example.com/web-search",
						Snippet:       "Overview of Tavily search and extract integration.",
						PublishedDate: "2026-10-20",
						Score:         0.98,
					},
				},
				StatusCode: 200,
			})
		case modeltransport.WebExtractPath:
			extractCalls.Add(1)
			var req modeltransport.WebExtractRequest
			_ = json.NewDecoder(r.Body).Decode(&req)
			if len(req.URLs) == 1 && req.URLs[0] == "https://blocked.example.com/article" {
				_ = json.NewEncoder(w).Encode(modeltransport.WebExtractResponse{
					OK:            false,
					FailedResults: []modeltransport.WebExtractFailure{{URL: req.URLs[0], Error: "extract_failed"}},
					ErrorCode:     "web_extract_failed",
					StatusCode:    422,
				})
				return
			}
			_ = json.NewEncoder(w).Encode(modeltransport.WebExtractResponse{
				OK: true,
				Results: []modeltransport.WebExtractResult{
					{
						URL:     req.URLs[0],
						Content: longMarkdown,
					},
				},
				StatusCode: 200,
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer gatewaySrv.Close()

	convID := uuid.NewString()
	msgID := uuid.NewString()
	wsID := uuid.NewString()
	senderID := uuid.NewString()
	imSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/v1/conversations/"+convID+"/messages/"+msgID {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"message": Message{
					ID:             msgID,
					ConversationID: convID,
					SenderID:       senderID,
					SenderName:     "Alice",
					SenderKind:     "human",
					Body:           "Please search and summarize the docs",
					Seq:            1,
				},
			})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer imSrv.Close()

	imc := &IM{
		BaseURL:    imSrv.URL,
		ServiceKey: "fixture-im-service-key-0123456789",
		AgentID:    lease.AgentID,
		HTTP:       imSrv.Client(),
		token:      "fixture-principal-token",
		expiry:     time.Now().Add(time.Hour),
	}

	budgetRaw, err := newRunContextBudget(16384, true, true, true)
	if err != nil {
		t.Fatal(err)
	}
	inputs := []Input{{
		EventID:        "101",
		WorkspaceID:    wsID,
		ConversationID: convID,
		MessageID:      msgID,
		SenderID:       senderID,
		SenderName:     "Alice",
		SenderKind:     "human",
		Body:           "Please search and summarize the docs",
		Seq:            1,
	}}
	if err := s.Ingest(ctx, lease, "101", inputs); err != nil {
		t.Fatal(err)
	}
	run, err := s.StartRunWithContextPolicy(ctx, lease, inputs, contextProfileV1, budgetRaw)
	if err != nil {
		t.Fatal(err)
	}
	runCtx := WithRun(ctx, run)

	toolsBuilder := &Tools{
		Store:            s,
		Lease:            lease,
		IM:               imc,
		GatewayStreamURL: gatewaySrv.URL + "/internal/v1/chat/stream",
		GatewayToken:     "fixture-gateway-token",
		GatewayHTTP:      gatewaySrv.Client(),
		OwnerID:          "owner-1",
		AgentID:          lease.AgentID,
	}
	webTools, err := toolsBuilder.BuildWebTools()
	if err != nil || len(webTools) != 2 {
		t.Fatalf("BuildWebTools failed: %v len=%d", err, len(webTools))
	}
	node, err := compose.NewToolNode(ctx, &compose.ToolsNodeConfig{Tools: webTools, ExecuteSequentially: true})
	if err != nil {
		t.Fatal(err)
	}
	invokeWeb := func(callID, name, rawArgs string) (string, error) {
		out, err := node.Invoke(runCtx, &schema.Message{
			Role: schema.Assistant,
			ToolCalls: []schema.ToolCall{{
				ID:       callID,
				Type:     "function",
				Function: schema.FunctionCall{Name: name, Arguments: rawArgs},
			}},
		})
		if err != nil {
			return "", err
		}
		return out[0].Content, nil
	}

	// 1. Execute web_search and verify receipt replay does not call gateway a second time
	searchArgsJSON := `{"query":"teamie tavily docs","maxResults":3}`
	out1, err := invokeWeb("call-search-1", "web_search", searchArgsJSON)
	if err != nil {
		t.Fatalf("web_search run failed: %v", err)
	}
	if searchCalls.Load() != 1 || !strings.Contains(out1, `"ok":true`) || !strings.Contains(out1, "https://docs.example.com/web-search") {
		t.Fatalf("unexpected web_search output: calls=%d out=%s", searchCalls.Load(), out1)
	}
	out1Replay, err := invokeWeb("call-search-1", "web_search", searchArgsJSON)
	if err != nil || out1Replay != out1 || searchCalls.Load() != 1 {
		t.Fatalf("web_search replay did not reuse durable receipt: calls=%d err=%v", searchCalls.Load(), err)
	}

	// 2. Execute web_fetch on a failing URL and verify structured web_extract_failed observation
	failOut, err := invokeWeb("call-fetch-fail", "web_fetch", `{"urls":["https://blocked.example.com/article"]}`)
	if err != nil || !strings.Contains(failOut, `"error":"web_extract_failed"`) {
		t.Fatalf("expected web_extract_failed observation, got out=%s err=%v", failOut, err)
	}

	// 3. Execute web_fetch returning large Markdown and verify ContextPipeline truncation + context_read pagination
	fetchArgsJSON := `{"urls":["https://docs.example.com/web-search"]}`
	fetchOut, err := invokeWeb("call-fetch-large", "web_fetch", fetchArgsJSON)
	if err != nil {
		t.Fatalf("web_fetch run failed: %v", err)
	}
	if extractCalls.Load() != 2 {
		t.Fatalf("expected 2 extract calls, got %d", extractCalls.Load())
	}
	fetchReplay, err := invokeWeb("call-fetch-large", "web_fetch", fetchArgsJSON)
	if err != nil || fetchReplay != fetchOut || extractCalls.Load() != 2 {
		t.Fatalf("web_fetch replay did not reuse durable receipt: calls=%d err=%v", extractCalls.Load(), err)
	}

	budget, err := ParseContextBudgetSnapshot(budgetRaw)
	if err != nil {
		t.Fatal(err)
	}
	archive := &ContextArchive{Store: s, Lease: lease, IM: imc}
	pipeline, err := NewContextPipeline(runCtx, ContextPipelineConfig{
		Model:             &pipelineModelFixture{configID: testModelConfigID},
		Budget:            budget,
		Backend:           archive,
		ArchivePath:       archive.Path,
		ArchiveToolPath:   archive.ArchiveToolPath,
		ArchiveTranscript: archive.ArchiveTranscript,
	})
	if err != nil {
		t.Fatal(err)
	}
	wrapped, err := pipeline.WrapInvokableToolCall(runCtx, func(context.Context, string, ...tool.Option) (string, error) {
		return fetchOut, nil
	}, &adk.ToolContext{Name: "web_fetch", CallID: "call-fetch-large"})
	if err != nil {
		t.Fatal(err)
	}
	reducedView, err := wrapped(runCtx, fetchArgsJSON)
	if err != nil || len(reducedView) > 2048 || !strings.Contains(reducedView, "context:v1:") {
		t.Fatalf("large web_fetch result was not truncated and archived: view=%s err=%v", reducedView, err)
	}
	searchRes, err := archive.Search(runCtx, "Extracted Article", "", 5)
	if err != nil || len(searchRes.Hits) == 0 {
		t.Fatalf("context_search failed on archived web_fetch content: res=%+v err=%v", searchRes, err)
	}
	page, err := archive.Read(runCtx, searchRes.Hits[0].Ref, 0, 4096)
	if err != nil || !page.HasMore || !strings.Contains(page.Text, "Extracted Article") {
		t.Fatalf("context_read failed on archived web_fetch content: page=%+v err=%v", page, err)
	}
}

func TestWebSearchBudgetAdmissionAndProtocolHelpers(t *testing.T) {
	rawFalse, err := newRunContextBudget(16384, true, true, false)
	if err != nil {
		t.Fatal(err)
	}
	bFalse, err := ParseContextBudgetSnapshot(rawFalse)
	if err != nil || bFalse.WebSearch || webSearchForRun(&Run{ContextProfile: contextProfileV1, ContextBudget: rawFalse}) {
		t.Fatalf("expected webSearch=false, got %+v err=%v", bFalse, err)
	}

	rawTrue, err := newRunContextBudget(16384, true, true, true)
	if err != nil {
		t.Fatal(err)
	}
	bTrue, err := ParseContextBudgetSnapshot(rawTrue)
	if err != nil || !bTrue.WebSearch || !webSearchForRun(&Run{ContextProfile: contextProfileV1, ContextBudget: rawTrue}) {
		t.Fatalf("expected webSearch=true, got %+v err=%v", bTrue, err)
	}

	// Verify contextArgumentHashes covers web_search and web_fetch canonical argument hashing
	searchHashes := contextArgumentHashes("web_search", `{"maxResults": 5, "query": "tavily"}`)
	if len(searchHashes) < 2 {
		t.Fatalf("expected canonical hash for web_search, got %v", searchHashes)
	}
	fetchHashes := contextArgumentHashes("web_fetch", `{"urls": ["https://example.com/a"]}`)
	if len(fetchHashes) < 2 {
		t.Fatalf("expected canonical hash for web_fetch, got %v", fetchHashes)
	}

	// Verify gatewayWebEndpoint derives /internal/v1/web/search and /internal/v1/web/extract from stream URL
	if got, err := gatewayWebEndpoint("http://llm-gateway:9094/internal/v1/chat/stream", modeltransport.WebSearchPath); err != nil || got != "http://llm-gateway:9094/internal/v1/web/search" {
		t.Fatalf("unexpected search URL: %s err=%v", got, err)
	}
	if got, err := gatewayWebEndpoint("http://llm-gateway:9094/internal/v1/chat/stream", modeltransport.WebExtractPath); err != nil || got != "http://llm-gateway:9094/internal/v1/web/extract" {
		t.Fatalf("unexpected extract URL: %s err=%v", got, err)
	}
}
