package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	"go.uber.org/zap"
	wire "teamie/agent-protocol/kitex_gen/agent"
	"teamie/agent-protocol/modeltransport"
	"teamie/backend/pkg/observability"
)

func assistantTextWithUsage(text string, promptTokens, completionTokens int) *schema.AgenticMessage {
	msg := assistantText(text)
	msg.ResponseMeta = &schema.AgenticResponseMeta{
		TokenUsage: &schema.TokenUsage{
			PromptTokens:     promptTokens,
			CompletionTokens: completionTokens,
			TotalTokens:      promptTokens + completionTokens,
		},
	}
	return msg
}

func anchoredAssistantTextWithUsage(t *testing.T, prefix []*schema.AgenticMessage, tools, deferred []*schema.ToolInfo, text string, promptTokens, completionTokens int) *schema.AgenticMessage {
	t.Helper()
	msg := assistantTextWithUsage(text, promptTokens, completionTokens)
	visibleBytes, prefixHash, err := visibleContextBytes(prefix, tools, deferred, nil, &schema.AgenticToolChoice{Type: schema.ToolChoiceAllowed})
	if err != nil {
		t.Fatal(err)
	}
	attachUsageAnchor(msg, &contextUsageAnchor{
		Step:         1,
		MessageCount: len(prefix),
		PrefixHash:   prefixHash,
		LocalTokens:  estimateVisibleTokensV7(visibleBytes),
		PromptTokens: promptTokens,
	})
	return msg
}

func TestVisibleBytesDiv4CounterAlignsChineseAndEnglishDensity(t *testing.T) {
	chinese := strings.Repeat("中文长会话上下文容量校准测试", 1000) // 14,000 Chinese chars = 42,000 UTF-8 bytes
	english := strings.Repeat("long context token calibration test ", 1000) // 36,000 ASCII bytes

	zhMsg := schema.UserAgenticMessage(chinese)
	enMsg := schema.UserAgenticMessage(english)

	zhMeasured, err := measureContext([]*schema.AgenticMessage{zhMsg}, nil, 4096, false)
	if err != nil {
		t.Fatal(err)
	}
	zhLegacy, err := measureLegacyContext([]*schema.AgenticMessage{zhMsg}, nil, 4096, false)
	if err != nil {
		t.Fatal(err)
	}
	if estimateVisibleTokensV7(4) != 65 || estimateVisibleTokensV7(0) != 0 {
		t.Fatalf("estimateVisibleTokensV7 did not include contracted 64-token framing allowance: got %d", estimateVisibleTokensV7(4))
	}
	// 14,000 Chinese chars = 42,000 bytes -> ~10,564 tokens (0.75 tokens/char + 64 allowance), whereas legacy was ~28,000+ tokens (2.0 tokens/char).
	if zhMeasured.EstimatedTokens < 10400 || zhMeasured.EstimatedTokens > 10700 || zhMeasured.CounterMode != "local" {
		t.Fatalf("unexpected v7 Chinese token estimate: %+v", zhMeasured)
	}
	if zhLegacy.EstimatedTokens < 28000 {
		t.Fatalf("expected legacy counter to retain 2x non-ASCII estimate: %+v", zhLegacy)
	}

	enMeasured, err := measureContext([]*schema.AgenticMessage{enMsg}, nil, 4096, false)
	if err != nil {
		t.Fatal(err)
	}
	// 36,000 ASCII bytes -> ~9,064 tokens (0.25 tokens/char + 64 allowance).
	if enMeasured.EstimatedTokens < 8950 || enMeasured.EstimatedTokens > 9200 {
		t.Fatalf("unexpected v7 English token estimate: %+v", enMeasured)
	}

	// Verify internal Extra, ResponseMeta, StreamingMeta and tool result Extra are excluded from LocalTokens.
	withMeta := schema.UserAgenticMessage(chinese)
	withMeta.Extra = map[string]any{
		"_eino_msg_id":              "ephemeral-id",
		"teamie_context_archive_ref": strings.Repeat("context:v1:archive", 2000),
		"teamie_context_sources":     strings.Repeat("source-metadata", 2000),
	}
	withMeta.ResponseMeta = &schema.AgenticResponseMeta{
		TokenUsage: &schema.TokenUsage{PromptTokens: 999999, TotalTokens: 999999},
	}
	withMeta.ContentBlocks[0].Extra = map[string]any{"internal_block_meta": strings.Repeat("x", 10000)}
	withMeta.ContentBlocks[0].StreamingMeta = &schema.StreamingMeta{Index: 7}

	metaMeasured, err := measureContext([]*schema.AgenticMessage{withMeta}, nil, 4096, false)
	if err != nil {
		t.Fatal(err)
	}
	if metaMeasured.EstimatedTokens != zhMeasured.EstimatedTokens || metaMeasured.LocalTokens != zhMeasured.LocalTokens {
		t.Fatalf("internal metadata leaked into visible token count: got %+v, want %+v", metaMeasured, zhMeasured)
	}
	if metaMeasured.TransportBytes-zhMeasured.TransportBytes < 60000 {
		t.Fatalf("internal metadata was omitted from TransportBytes: got %d vs %d", metaMeasured.TransportBytes, zhMeasured.TransportBytes)
	}

	// Verify 220K real Chinese tokens (~310K Chinese chars = ~930 KB UTF-8) does NOT trigger compaction in a 1M window under v7,
	// whereas v6 prematurely compacted it.
	pV7, archiveV7, summaryV7 := testPipeline(t, 1_000_000)
	v6Budget, _ := makeContextBudgetSnapshotVersion(1_000_000, 6)
	archiveV6 := &pipelineArchiveFixture{}
	summaryV6 := &pipelineModelFixture{}
	pV6, err := NewContextPipeline(context.Background(), ContextPipelineConfig{
		Model: summaryV6, Budget: v6Budget, Backend: archiveV6, ArchivePath: archiveV6.path, ArchiveTranscript: archiveV6.transcript,
	})
	if err != nil {
		t.Fatal(err)
	}
	inputs := []Input{{EventID: "zh-fresh", ConversationID: "conv", MessageID: "msg", SenderID: "user", SenderKind: "human", Body: "继续当前中文分析任务"}}
	ctx := WithRun(context.Background(), &Run{Inputs: inputs})
	zhHistory := []*schema.AgenticMessage{schema.SystemAgenticMessage("Trusted system instructions.")}
	for i := 0; i < 100; i++ {
		// 3,400 Chinese chars per message * 100 = 340,000 Chinese chars (1,020,000 UTF-8 bytes)
		// Under v7: ~255,000 tokens (<< 781,804 trigger). Under v6: ~680,000 + framing > 651,504 trigger.
		zhHistory = append(zhHistory, schema.UserAgenticMessage(fmt.Sprintf("历史中文记录 %d %s", i, strings.Repeat("汉字内容", 850))))
	}
	zhHistory = append(zhHistory, inputMessages(inputs)...)

	_, outV7, err := pV7.BeforeModelRewriteState(ctx, &adk.TypedChatModelAgentState[*schema.AgenticMessage]{Messages: zhHistory}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(outV7.Messages) != len(zhHistory) || len(summaryV7.inputs) != 0 || len(archiveV7.content) != 0 {
		t.Fatal("v7 prematurely compacted 240K-token Chinese history in a 1M window")
	}

	_, outV6, err := pV6.BeforeModelRewriteState(ctx, &adk.TypedChatModelAgentState[*schema.AgenticMessage]{Messages: zhHistory}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(summaryV6.inputs) == 0 || len(outV6.Messages) >= len(zhHistory) {
		t.Fatal("frozen v6 did not retain its historical compaction trigger")
	}
}

func TestPromptTokensDeltaCalibrationAndPrefixInvalidation(t *testing.T) {
	p, archive, summary := testPipeline(t, 16384)
	// For window = 16384 in v7:
	// reserve = 4096, InputTokenLimit = 12032, TriggerTokens = 10828.
	sys := schema.SystemAgenticMessage("Trusted system instructions.")
	// Create a prefix whose local estimate is ~10,000 tokens (40,000 bytes), but real provider PromptTokens was 6,000.
	user1 := schema.UserAgenticMessage(strings.Repeat("甲乙丙丁", 3300)) // 13,200 chars = 39,600 bytes -> ~9,900 local tokens
	resp1 := anchoredAssistantTextWithUsage(t, []*schema.AgenticMessage{sys, user1}, nil, nil, "收到，正在处理第一阶段。", 6000, 50)
	// Add a new user input of ~1,200 local tokens (4,800 bytes):
	// Uncalibrated local total = ~9,900 + ~1,200 = ~11,100 > TriggerTokens (10,828)!
	// Calibrated effective total = 6,000 + (~11,100 - ~9,900) = ~7,200 <= TriggerTokens (10,828)!
	inputs := []Input{{EventID: "turn-2", ConversationID: "conv", MessageID: "msg-2", SenderID: "user", SenderKind: "human", Body: strings.Repeat("继续第二阶段补充说明", 160)}}
	run := &Run{ContextProfile: contextProfileV1, Inputs: inputs}
	run.ContextBudget, _ = NewContextBudgetSnapshot(16384)
	ctx := WithRun(context.Background(), run)

	messages := append([]*schema.AgenticMessage{sys, user1, resp1}, inputMessages(inputs)...)
	_, out, err := p.BeforeModelRewriteState(ctx, &adk.TypedChatModelAgentState[*schema.AgenticMessage]{Messages: messages}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Messages) != len(messages) || len(summary.inputs) != 0 || len(archive.content) != 0 {
		t.Fatal("PromptTokens calibration failed to prevent premature compaction when local estimate exceeded trigger")
	}
	m, err := p.measureMainContext(ctx, out.Messages, out.ToolInfos)
	if err != nil {
		t.Fatal(err)
	}
	if m.CounterMode != "usage_delta" || m.AnchorPromptTokens != 6000 || m.LocalTokens <= p.cfg.Budget.TriggerTokens || m.EstimatedTokens > p.cfg.Budget.TriggerTokens {
		t.Fatalf("expected usage_delta mode with local > trigger and effective <= trigger, got %+v", m)
	}

	// Also verify GatewayModel.checkContextRequest accepts a request whose local tokens > InputTokenLimit (12,032)
	// when calibrated effective tokens <= InputTokenLimit!
	user2Large := schema.UserAgenticMessage(strings.Repeat("补充材料", 750)) // ~9,000 bytes = ~2,250 local tokens -> total local ~12,200 > 12,032, effective ~8,250 <= 12,032
	largeCandidate := []*schema.AgenticMessage{sys, user1, resp1, user2Large}
	maxOut := p.cfg.Budget.initialOutputTokens()
	rawReq, err := agenticRequestForRun(run, largeCandidate, &model.Options{MaxTokens: &maxOut}, false)
	if err != nil {
		t.Fatal(err)
	}
	gw := &GatewayModel{}
	if err := gw.checkContextRequest(ctx, run, 2, modelRequestHash(rawReq), rawReq, false); err != nil {
		t.Fatalf("checkContextRequest rejected a request that fits under calibrated PromptTokens: %v", err)
	}

	// Now verify the opposite direction: if real PromptTokens was high (e.g. 10,500) so effective_input_tokens > TriggerTokens (10,828)
	// even though local tokens (~4,000) <= TriggerTokens, compaction IS triggered and invalidates the anchor!
	p2, archive2, summary2 := testPipeline(t, 16384)
	shortUser1 := schema.UserAgenticMessage(strings.Repeat("short history ", 200))
	highUsageResp := anchoredAssistantTextWithUsage(t, []*schema.AgenticMessage{sys, shortUser1}, nil, nil, "ack", 10500, 20)
	run2 := &Run{ContextProfile: contextProfileV1, Inputs: inputs}
	run2.ContextBudget, _ = NewContextBudgetSnapshot(16384)
	ctx2 := WithRun(context.Background(), run2)
	msgs2 := append([]*schema.AgenticMessage{sys, shortUser1, highUsageResp}, inputMessages(inputs)...)
	_, out2, err := p2.BeforeModelRewriteState(ctx2, &adk.TypedChatModelAgentState[*schema.AgenticMessage]{Messages: msgs2}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(summary2.inputs) == 0 || len(archive2.content) == 0 {
		t.Fatal("high calibrated PromptTokens did not trigger compaction")
	}
	afterCompact, err := p2.measureMainContext(ctx2, out2.Messages, out2.ToolInfos)
	if err != nil || afterCompact.CounterMode != "local" || afterCompact.AnchorPromptTokens != 0 {
		t.Fatalf("compaction did not invalidate the old PromptTokens anchor: %+v (%v)", afterCompact, err)
	}
}

func TestToolReductionPreservesAnchorOnNoopAndUsesConsistentDeltaOnClear(t *testing.T) {
	p, archive, _ := testPipeline(t, 16384)
	// Window 16384 -> TriggerTokens = 10828.
	// Construct a session with an older tool round and a recent assistant message with PromptTokens = 10,600
	// while local tokens of the prefix are higher (12,500), so P_a - L_a = -1,900.
	sys := schema.SystemAgenticMessage("Trusted system instructions.")
	oldCall := &schema.AgenticMessage{
		Role:          schema.AgenticRoleTypeAssistant,
		ContentBlocks: []*schema.ContentBlock{schema.NewContentBlock(&schema.FunctionToolCall{CallID: "call-1", Name: "lookup", Arguments: `{}`})},
	}
	// Clearable tool result (>= 768 bytes, ~4,000 bytes = ~1,000 local tokens).
	oldResult := &schema.AgenticMessage{
		Role: schema.AgenticRoleTypeUser,
		ContentBlocks: []*schema.ContentBlock{schema.NewContentBlock(&schema.FunctionToolResult{
			CallID:  "call-1",
			Name:    "lookup",
			Content: []*schema.FunctionToolResultContentBlock{{Type: schema.FunctionToolResultContentBlockTypeText, Text: &schema.UserInputText{Text: strings.Repeat("clearable tool observation ", 160)}}},
		})},
	}
	filler := schema.UserAgenticMessage(strings.Repeat("背景汉字内容", 2200)) // 13,200 chars = 39,600 bytes (~9,900 local tokens)
	// Add 2 recent tool-call rounds so oldCall/oldResult is outside ClearRetentionSuffixLimit (2), and total effective tokens > 10,828.
	recent1 := schema.UserAgenticMessage(strings.Repeat("近期跟进问题一", 100))
	recentRound1 := contextToolRound("r1", "short result 1")
	recentRound2 := contextToolRound("r2", "short result 2")
	prefix := append([]*schema.AgenticMessage{sys, oldCall, oldResult, filler}, recentRound1...)
	prefix = append(prefix, recentRound2...)
	resp := anchoredAssistantTextWithUsage(t, prefix, nil, nil, "First tool analyzed.", 10500, 30)

	inputs := []Input{{EventID: "e-1", ConversationID: "c-1", MessageID: "m-1", SenderID: "u-1", SenderKind: "human", Body: "继续"}}
	run := &Run{ContextProfile: contextProfileV1, Inputs: inputs}
	run.ContextBudget, _ = NewContextBudgetSnapshot(16384)
	ctx := WithRun(context.Background(), run)

	msgs := append(append([]*schema.AgenticMessage{}, prefix...), resp, recent1)
	msgs = append(msgs, inputMessages(inputs)...)
	p.seedAnchorFromHistory(ctx, msgs, nil)
	beforeMeasure, err := p.measureMainContext(ctx, msgs, nil)
	if err != nil || beforeMeasure.CounterMode != "usage_delta" || beforeMeasure.EstimatedTokens <= p.cfg.Budget.TriggerTokens {
		t.Fatalf("fixture did not start in usage_delta mode above trigger: %+v (%v)", beforeMeasure, err)
	}

	r, err := p.reducer(ctx, []string{"lookup"})
	if err != nil {
		t.Fatal(err)
	}
	_, reduced, err := r.BeforeModelRewriteState(ctx, &adk.TypedChatModelAgentState[*schema.AgenticMessage]{Messages: msgs}, nil)
	if err != nil {
		t.Fatal(err)
	}
	clearedText := reduced.Messages[2].ContentBlocks[0].FunctionToolResult.Content[0].Text.Text
	if !strings.HasPrefix(clearedText, "Archived private context: context:v1:") || len(archive.content) == 0 {
		t.Fatalf("ToolReduction aborted valid clear when P_a < L_a: %q", clearedText)
	}
	// After an actual clear, the anchor must be invalidated both in the current Run and if reloaded in a fresh Run/ContextPipeline.
	afterClear, err := p.measureMainContext(ctx, reduced.Messages, nil)
	if err != nil || afterClear.CounterMode != "local" || afterClear.AnchorPromptTokens != 0 {
		t.Fatalf("anchor remained active after ToolReduction mutated historical tool result: %+v (%v)", afterClear, err)
	}
	// Simulate reloading the persisted rewritten messages in a fresh Run / fresh ContextPipeline:
	// even if an old assistant message still had Extra or PromptTokens, the prefix hash no longer matches.
	pReload, _, _ := testPipeline(t, 16384)
	runReload := &Run{ContextProfile: contextProfileV1, Inputs: inputs}
	runReload.ContextBudget, _ = NewContextBudgetSnapshot(16384)
	ctxReload := WithRun(context.Background(), runReload)
	pReload.seedAnchorFromHistory(ctxReload, reduced.Messages, nil)
	afterReload, err := pReload.measureMainContext(ctxReload, reduced.Messages, nil)
	if err != nil || afterReload.CounterMode != "local" || afterReload.AnchorPromptTokens != 0 {
		t.Fatalf("reloaded Session after prefix rewrite reconstructed a stale PromptTokens anchor: %+v (%v)", afterReload, err)
	}

	// Now test no-op clear (when tool result is < 768 bytes): anchor must remain valid, including with DeferredToolInfos!
	smallResult := &schema.AgenticMessage{
		Role: schema.AgenticRoleTypeUser,
		ContentBlocks: []*schema.ContentBlock{schema.NewContentBlock(&schema.FunctionToolResult{
			CallID:  "call-small",
			Name:    "lookup",
			Content: []*schema.FunctionToolResultContentBlock{{Type: schema.FunctionToolResultContentBlockTypeText, Text: &schema.UserInputText{Text: "small result under 768 bytes"}}},
		})},
	}
	deferredTools := []*schema.ToolInfo{{Name: "deferred_helper", Desc: "Deferred tool schema"}}
	normalizedDeferred := contextToolInfos(deferredTools, p.cfg.Budget)
	prefixNoop := append([]*schema.AgenticMessage{sys, oldCall, smallResult, filler}, recentRound1...)
	prefixNoop = append(prefixNoop, recentRound2...)
	respNoop := anchoredAssistantTextWithUsage(t, prefixNoop, nil, normalizedDeferred, "Small tool analyzed.", 10500, 30)
	runNoop := &Run{ContextProfile: contextProfileV1, Inputs: inputs}
	runNoop.ContextBudget, _ = NewContextBudgetSnapshot(16384)
	ctxNoop := WithRun(context.Background(), runNoop)
	pNoop, _, _ := testPipeline(t, 16384)
	msgsNoop := append(append([]*schema.AgenticMessage{}, prefixNoop...), respNoop, recent1)
	msgsNoop = append(msgsNoop, inputMessages(inputs)...)
	pNoop.seedAnchorFromHistory(ctxNoop, msgsNoop, nil, normalizedDeferred)
	rNoop, err := pNoop.reducer(ctxNoop, []string{"lookup", "deferred_helper"}, normalizedDeferred)
	if err != nil {
		t.Fatal(err)
	}
	_, reducedNoop, err := rNoop.BeforeModelRewriteState(ctxNoop, &adk.TypedChatModelAgentState[*schema.AgenticMessage]{
		Messages:          msgsNoop,
		DeferredToolInfos: normalizedDeferred,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	afterNoop, err := pNoop.measureMainContext(ctxNoop, reducedNoop.Messages, reducedNoop.ToolInfos, reducedNoop.DeferredToolInfos)
	if err != nil || afterNoop.CounterMode != "usage_delta" || afterNoop.AnchorPromptTokens != 10500 {
		t.Fatalf("no-op ToolReduction clear or DeferredToolInfos destroyed a valid PromptTokens anchor: %+v (%v)", afterNoop, err)
	}

	// Also verify full p.BeforeModelRewriteState with DeferredToolInfos keeps calibration and skips premature compaction when P_a is 6000.
	respDeferred := anchoredAssistantTextWithUsage(t, prefixNoop, nil, normalizedDeferred, "Deferred tool analyzed.", 6000, 30)
	runDeferred := &Run{ContextProfile: contextProfileV1, Inputs: inputs}
	runDeferred.ContextBudget, _ = NewContextBudgetSnapshot(16384)
	ctxDeferred := WithRun(context.Background(), runDeferred)
	pDeferred, _, summaryDeferred := testPipeline(t, 16384)
	msgsDeferred := append(append([]*schema.AgenticMessage{}, prefixNoop...), respDeferred, recent1)
	msgsDeferred = append(msgsDeferred, inputMessages(inputs)...)
	_, outDeferred, err := pDeferred.BeforeModelRewriteState(ctxDeferred, &adk.TypedChatModelAgentState[*schema.AgenticMessage]{
		Messages:          msgsDeferred,
		DeferredToolInfos: deferredTools,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(summaryDeferred.inputs) != 0 || len(outDeferred.Messages) != len(msgsDeferred) {
		t.Fatal("BeforeModelRewriteState dropped calibration when DeferredToolInfos were present")
	}
}

func TestSummaryRequestsAreIsolatedFromMainPromptTokensAnchor(t *testing.T) {
	s, lease, run := contextModelRun(t)
	run.ContextBudget, _ = NewContextBudgetSnapshot(1_000_000)

	mainInput := []*schema.AgenticMessage{
		schema.SystemAgenticMessage("Trusted system instructions."),
		schema.UserAgenticMessage(strings.Repeat("主对话内容", 500)),
	}
	mainOpts := &model.Options{
		Tools:             []*schema.ToolInfo{{Name: "im_send_message", Desc: "Send IM"}},
		AgenticToolChoice: &schema.AgenticToolChoice{Type: schema.ToolChoiceAllowed},
	}
	mainReq, err := agenticRequestForRun(run, mainInput, mainOpts, false)
	if err != nil {
		t.Fatal(err)
	}
	mainResp := assistantTextWithUsage("Main response", 42000, 100)
	run.recordModelUsageAnchor(1, mainReq, mainResp)
	if anchor := run.getUsageAnchor(); anchor == nil || anchor.PromptTokens != 42000 || anchor.Step != 1 {
		t.Fatalf("main model response did not record anchor: %+v", anchor)
	}

	// Now simulate a summary request (Tools: nil, ToolChoice: ToolChoiceForbidden) returning PromptTokens = 900.
	summaryInput := []*schema.AgenticMessage{
		schema.SystemAgenticMessage("Summary system"),
		schema.UserAgenticMessage("Older prefix"),
	}
	summaryOpts := &model.Options{
		AgenticToolChoice: &schema.AgenticToolChoice{Type: schema.ToolChoiceForbidden},
	}
	summaryReq, err := agenticRequestForRun(run, summaryInput, summaryOpts, false)
	if err != nil {
		t.Fatal(err)
	}
	summaryResp := assistantTextWithUsage("Summary text", 900, 50)
	run.recordModelUsageAnchor(2, summaryReq, summaryResp)
	if anchor := run.getUsageAnchor(); anchor == nil || anchor.PromptTokens != 42000 || anchor.Step != 1 {
		t.Fatalf("summary receipt polluted main PromptTokens anchor: %+v", anchor)
	}

	budget, _ := ParseContextBudgetSnapshot(run.ContextBudget)
	// Summary measurement via measureCalibratedRequest must also ignore the main anchor and stay in "local" mode.
	summaryMeasured, err := budget.measureCalibratedRequest(run.getUsageAnchor(), summaryReq)
	if err != nil || summaryMeasured.CounterMode != "local" || summaryMeasured.AnchorPromptTokens != 0 {
		t.Fatalf("summary request used main conversation anchor: %+v (%v)", summaryMeasured, err)
	}
	_ = s
	_ = lease
}

func TestGatewayModelAnchorPersistsAndReplaysDeterministically(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%v", streaming), func(t *testing.T) {
			s, lease, run := contextModelRun(t)
			run.ContextBudget, _ = NewContextBudgetSnapshot(1_000_000)
			if _, err := s.DB.Exec(context.Background(), `UPDATE runtime_runs SET context_budget=$2 WHERE id=$1`, run.ID, run.ContextBudget); err != nil {
				t.Fatal(err)
			}
			budget, _ := ParseContextBudgetSnapshot(run.ContextBudget)

			step1Resp := assistantTextWithUsage("Step 1 tool call", 50000, 120)
			step1Raw, _ := (&schema.HumanReadableSerializer{}).Marshal(step1Resp)
			step2Resp := assistantTextWithUsage("Step 2 final", 52400, 80)
			step2Raw, _ := (&schema.HumanReadableSerializer{}).Marshal(step2Resp)

			callCount := 0
			fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				callCount++
				payload := step1Raw
				if callCount == 2 {
					payload = step2Raw
				}
				if streaming {
					_ = json.NewEncoder(w).Encode(map[string]string{"type": "chunk", "responseJson": string(payload)})
					_ = json.NewEncoder(w).Encode(map[string]string{"type": "done"})
				} else {
					text := string(payload)
					_ = json.NewEncoder(w).Encode(&wire.GatewayChatResponse{Ok: true, ResponseJson: &text})
				}
			}))
			defer fixture.Close()

			client, err := newGatewayHTTPClient(nil, fixture.URL+modeltransport.StreamPath)
			if err != nil {
				t.Fatal(err)
			}
			gateway := &GatewayModel{Store: s, Lease: lease, AgentID: lease.AgentID, Client: client, StreamURL: fixture.URL + modeltransport.StreamPath}
			tools := []*schema.ToolInfo{{Name: "im_send_message", Desc: "Send message"}}
			step1Msgs := []*schema.AgenticMessage{
				schema.SystemAgenticMessage("System policy"),
				schema.UserAgenticMessage(strings.Repeat("第一轮中文输入", 200)),
			}
			step2Msgs := append(append([]*schema.AgenticMessage{}, step1Msgs...), step1Resp, schema.UserAgenticMessage(strings.Repeat("工具返回结果", 150)))

			var liveStep1Measure, liveStep2Measure ContextMeasurement
			for attempt := 0; attempt < 2; attempt++ {
				// On attempt 1 (replay), construct a fresh Run with no in-memory anchor and Step = 0,
				// while both step 1 and step 2 receipts already exist in runtime_model_calls.
				activeRun := &Run{
					ID:             run.ID,
					Attempt:        run.Attempt,
					AgentID:        run.AgentID,
					Inputs:         run.Inputs,
					ContextProfile: run.ContextProfile,
					ContextBudget:  run.ContextBudget,
					Front:          run.Front,
				}
				ctx := WithRun(context.Background(), activeRun)

				// Before step 1: no anchor exists yet (must not peek at future step 1 or step 2 receipts in DB!).
				m1, err := budget.measureCalibratedContext(activeRun.getUsageAnchor(), step1Msgs, tools, budget.OutputReserveTokens, streaming)
				if err != nil || m1.CounterMode != "local" || m1.AnchorStep != 0 {
					t.Fatalf("attempt %d step 1 pre-call measurement used future receipt: %+v (%v)", attempt, m1, err)
				}

				if streaming {
					reader, err := gateway.Stream(ctx, step1Msgs, model.WithTools(tools))
					if err != nil {
						t.Fatal(err)
					}
					for {
						if _, err := reader.Recv(); err == io.EOF {
							break
						} else if err != nil {
							t.Fatal(err)
						}
					}
					reader.Close()
				} else {
					if _, err := gateway.Generate(ctx, step1Msgs, model.WithTools(tools)); err != nil {
						t.Fatal(err)
					}
				}

				// After step 1 completes (live or replayed from cachedModel), step 1 anchor must be active for step 2!
				m2, err := budget.measureCalibratedContext(activeRun.getUsageAnchor(), step2Msgs, tools, budget.OutputReserveTokens, streaming)
				if err != nil || m2.CounterMode != "usage_delta" || m2.AnchorStep != 1 || m2.AnchorPromptTokens != 50000 {
					t.Fatalf("attempt %d step 2 measurement did not restore step 1 anchor: %+v (%v)", attempt, m2, err)
				}

				if streaming {
					reader, err := gateway.Stream(ctx, step2Msgs, model.WithTools(tools))
					if err != nil {
						t.Fatal(err)
					}
					for {
						if _, err := reader.Recv(); err == io.EOF {
							break
						} else if err != nil {
							t.Fatal(err)
						}
					}
					reader.Close()
				} else {
					if _, err := gateway.Generate(ctx, step2Msgs, model.WithTools(tools)); err != nil {
						t.Fatal(err)
					}
				}

				if attempt == 0 {
					liveStep1Measure, liveStep2Measure = m1, m2
				} else if m1 != liveStep1Measure || m2 != liveStep2Measure {
					t.Fatalf("replay measurements diverged from live execution: got (%+v, %+v), want (%+v, %+v)", m1, m2, liveStep1Measure, liveStep2Measure)
				}
			}
			if callCount != 2 {
				t.Fatalf("replay repeated upstream model calls: got %d, want 2", callCount)
			}
		})
	}
}

func TestContextObservabilityLogsCalibrationFieldsWithoutPrivateContent(t *testing.T) {
	var logs diagnosticBuffer
	undo := zap.ReplaceGlobals(observability.New(observability.Config{Service: "teamie.agent-runtime", Version: "fixture", Output: &logs}))
	t.Cleanup(undo)

	p, _, _ := testPipeline(t, 16384)
	secretPrompt := "SECRET_PROMPT_BODY_DO_NOT_LOG"
	secretReasoning := "SECRET_REASONING_DO_NOT_LOG"
	secretToolArg := `{"secretKey":"SECRET_TOOL_ARG_DO_NOT_LOG"}`

	sys := schema.SystemAgenticMessage(secretPrompt)
	user1 := schema.UserAgenticMessage(strings.Repeat("历史观测数据", 2200))
	resp1 := &schema.AgenticMessage{
		Role: schema.AgenticRoleTypeAssistant,
		ContentBlocks: []*schema.ContentBlock{
			schema.NewContentBlock(&schema.Reasoning{Text: secretReasoning}),
			schema.NewContentBlock(&schema.FunctionToolCall{CallID: "c1", Name: "lookup", Arguments: secretToolArg}),
		},
		ResponseMeta: &schema.AgenticResponseMeta{
			TokenUsage: &schema.TokenUsage{PromptTokens: 10600, CompletionTokens: 100, TotalTokens: 10700},
		},
	}
	visBytes, pHash, err := visibleContextBytes([]*schema.AgenticMessage{sys, user1}, nil, nil, nil, &schema.AgenticToolChoice{Type: schema.ToolChoiceAllowed})
	if err != nil {
		t.Fatal(err)
	}
	attachUsageAnchor(resp1, &contextUsageAnchor{
		Step:         1,
		MessageCount: 2,
		PrefixHash:   pHash,
		LocalTokens:  estimateVisibleTokensV7(visBytes),
		PromptTokens: 10600,
	})
	toolRes := &schema.AgenticMessage{
		Role: schema.AgenticRoleTypeUser,
		ContentBlocks: []*schema.ContentBlock{schema.NewContentBlock(&schema.FunctionToolResult{
			CallID:  "c1",
			Name:    "lookup",
			Content: []*schema.FunctionToolResultContentBlock{{Type: schema.FunctionToolResultContentBlockTypeText, Text: &schema.UserInputText{Text: strings.Repeat("结果", 800)}}},
		})},
	}
	inputs := []Input{{EventID: "ev-log", ConversationID: "conv-log", MessageID: "msg-log", SenderID: "user-log", SenderKind: "human", Body: secretPrompt}}
	ctx := WithRun(context.Background(), &Run{ID: "run-log", AgentID: "agent-log", Attempt: "attempt-log", Inputs: inputs})
	msgs := append([]*schema.AgenticMessage{sys, user1, resp1, toolRes}, inputMessages(inputs)...)

	if _, _, err := p.BeforeModelRewriteState(ctx, &adk.TypedChatModelAgentState[*schema.AgenticMessage]{Messages: msgs}, nil); err != nil {
		t.Fatal(err)
	}

	rawLogs := logs.snapshot()
	for _, forbidden := range []string{secretPrompt, secretReasoning, "SECRET_TOOL_ARG_DO_NOT_LOG"} {
		if strings.Contains(rawLogs, forbidden) {
			t.Fatalf("private content leaked into structured logs: %s", forbidden)
		}
	}

	var measuredEvent, compactionEvent map[string]any
	for _, line := range strings.Split(strings.TrimSpace(rawLogs), "\n") {
		var event map[string]any
		if json.Unmarshal([]byte(line), &event) != nil {
			continue
		}
		if event["event.action"] == "context-measured" {
			measuredEvent = event
		}
		if event["event.action"] == "context-compaction" {
			compactionEvent = event
		}
	}
	if measuredEvent == nil || compactionEvent == nil {
		t.Fatalf("missing context-measured or context-compaction log events in:\n%s", rawLogs)
	}
	if measuredEvent["custom.context.counter_version"] != contextCounterVersionV7 ||
		measuredEvent["custom.context.counter_mode"] != "usage_delta" ||
		measuredEvent["custom.context.anchor_prompt_tokens"] != float64(10600) ||
		measuredEvent["custom.context.compaction_reason"] != "token_threshold" {
		t.Fatalf("unexpected context-measured diagnostic fields: %+v", measuredEvent)
	}
}
