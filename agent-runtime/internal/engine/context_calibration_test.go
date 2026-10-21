package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"weak"

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
		ConfigID:     testModelConfigID,
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

	// Verify that if a subsequent assistant response omits TokenUsage (PromptTokens == 0),
	// the earlier valid anchor on resp1 remains active for the intact prefix.
	usagelessAssistant := assistantText("Intermediate reply without usage.")
	messagesWithUsageless := append([]*schema.AgenticMessage{sys, user1, resp1, schema.UserAgenticMessage("short follow-up"), usagelessAssistant}, inputMessages(inputs)...)
	_, outUsageless, err := p.BeforeModelRewriteState(ctx, &adk.TypedChatModelAgentState[*schema.AgenticMessage]{Messages: messagesWithUsageless}, nil)
	if err != nil {
		t.Fatal(err)
	}
	mUsageless, err := p.measureMainContext(ctx, outUsageless.Messages, outUsageless.ToolInfos)
	if err != nil || mUsageless.CounterMode != "usage_delta" || mUsageless.AnchorPromptTokens != 6000 {
		t.Fatalf("earlier valid anchor was lost after a later assistant response omitted usage: %+v (%v)", mUsageless, err)
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
	gw := &GatewayModel{ModelConfigID: func() string { return testModelConfigID }}
	if _, err := gw.checkContextRequest(ctx, run, 2, modelRequestHash(rawReq), rawReq, false); err != nil {
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
	run.recordModelUsageAnchor(1, mainReq, mainResp, testModelConfigID)
	if anchor := run.getUsageAnchorForConfig(testModelConfigID); anchor == nil || anchor.PromptTokens != 42000 || anchor.Step != 1 {
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
	run.recordModelUsageAnchor(2, summaryReq, summaryResp, testModelConfigID)
	if anchor := run.getUsageAnchorForConfig(testModelConfigID); anchor == nil || anchor.PromptTokens != 42000 || anchor.Step != 1 {
		t.Fatalf("summary receipt polluted main PromptTokens anchor: %+v", anchor)
	}

	budget, _ := ParseContextBudgetSnapshot(run.ContextBudget)
	// Summary measurement via measureCalibratedRequest must also ignore the main anchor and stay in "local" mode.
	summaryMeasured, err := budget.measureCalibratedRequest(run.getUsageAnchorForConfig(testModelConfigID), summaryReq)
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
				if r.Method == http.MethodGet {
					cfg, en := true, true
					id := testModelConfigID
					_ = json.NewEncoder(w).Encode(&wire.PingResponse{Ready: true, Configured: &cfg, Enabled: &en, ModelConfigId: &id})
					return
				}
				callCount++
				payload := step1Raw
				if callCount == 2 {
					payload = step2Raw
				}
				w.Header().Set(modeltransport.ModelConfigIDHeader, testModelConfigID)
				if streaming {
					_ = json.NewEncoder(w).Encode(map[string]string{"type": "chunk", "responseJson": string(payload)})
					_ = json.NewEncoder(w).Encode(map[string]string{"type": "done", "modelConfigId": testModelConfigID})
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
			gateway := &GatewayModel{Store: s, Lease: lease, AgentID: lease.AgentID, Client: client, StreamURL: fixture.URL + modeltransport.StreamPath, ModelConfigID: func() string { return testModelConfigID }}
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
				activeConfigID, _ := gateway.CurrentModelConfigID(ctx)
				m1, err := budget.measureCalibratedContext(activeRun.getUsageAnchorForConfig(activeConfigID), step1Msgs, tools, budget.OutputReserveTokens, streaming)
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
				m2, err := budget.measureCalibratedContext(activeRun.getUsageAnchorForConfig(activeConfigID), step2Msgs, tools, budget.OutputReserveTokens, streaming)
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
		ConfigID:     testModelConfigID,
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

func retainsAgenticMessagePointer(root any, target *schema.AgenticMessage) bool {
	visited := map[uintptr]bool{}
	targetPtr := reflect.ValueOf(target).Pointer()
	var walk func(v reflect.Value) bool
	walk = func(v reflect.Value) bool {
		if !v.IsValid() {
			return false
		}
		switch v.Kind() {
		case reflect.Pointer:
			if v.IsNil() {
				return false
			}
			ptr := v.Pointer()
			if v.Type() == reflect.TypeOf(target) && ptr == targetPtr {
				return true
			}
			if visited[ptr] {
				return false
			}
			visited[ptr] = true
			return walk(v.Elem())
		case reflect.Interface:
			if v.IsNil() {
				return false
			}
			return walk(v.Elem())
		case reflect.Struct:
			for i := 0; i < v.NumField(); i++ {
				if walk(v.Field(i)) {
					return true
				}
			}
		case reflect.Slice, reflect.Array:
			for i := 0; i < v.Len(); i++ {
				if walk(v.Index(i)) {
					return true
				}
			}
		case reflect.Map:
			iter := v.MapRange()
			for iter.Next() {
				if walk(iter.Key()) || walk(iter.Value()) {
					return true
				}
			}
		}
		return false
	}
	return walk(reflect.ValueOf(root))
}

func TestReviewPR53CompactionRetainsDiscardedMessages(t *testing.T) {
	p, _, summary := testPipeline(t, 16384)
	run := &Run{ContextProfile: contextProfileV1}
	run.ContextBudget, _ = NewContextBudgetSnapshot(16384)
	ctx := WithRun(context.Background(), run)

	var weakDiscarded []weak.Pointer[schema.AgenticMessage]
	for cycle := 0; cycle < 3; cycle++ {
		messages := []*schema.AgenticMessage{schema.SystemAgenticMessage("policy")}
		var cycleMsgs []*schema.AgenticMessage
		for i := 0; i < 6; i++ {
			msg := anchoredAssistantTextWithUsage(
				t,
				messages,
				nil,
				nil,
				strings.Repeat(fmt.Sprintf("discarded_%d_%d ", cycle, i), 1200),
				6000+i*100,
				20,
			)
			messages = append(messages, msg)
			cycleMsgs = append(cycleMsgs, msg)
		}
		messages = append(messages, schema.UserAgenticMessage("continue"))
		p.seedAnchorFromHistory(ctx, messages, nil)

		compacted, err := p.compact(ctx, messages, nil, false)
		if err != nil {
			t.Fatal(err)
		}
		if len(compacted) >= len(messages) {
			t.Fatal("fixture did not compact")
		}
		keptSet := map[*schema.AgenticMessage]bool{}
		for _, m := range compacted {
			keptSet[m] = true
		}
		for _, msg := range cycleMsgs {
			if !keptSet[msg] {
				if retainsAgenticMessagePointer(run, msg) || retainsAgenticMessagePointer(&p.fallbackUsage, msg) {
					t.Fatalf("cycle %d: Run or ContextPipeline retained discarded *schema.AgenticMessage pointer", cycle)
				}
				weakDiscarded = append(weakDiscarded, weak.Make(msg))
			}
		}
		if len(run.usageState.invalidatedAnchors) > 6 {
			t.Fatalf("cycle %d: invalidatedAnchors accumulated unbounded entries across compactions: %d", cycle, len(run.usageState.invalidatedAnchors))
		}
		// Verify neither the compacted slice nor a re-seed resurrects any old anchor.
		p.seedAnchorFromHistory(ctx, compacted, nil)
		afterMeasure, err := p.measureMainContext(ctx, compacted, nil)
		if err != nil || afterMeasure.CounterMode != "local" || afterMeasure.AnchorPromptTokens != 0 {
			t.Fatalf("cycle %d: compacted history resurrected a discarded usage anchor: %+v (%v)", cycle, afterMeasure, err)
		}
		// Even if pre-compaction messages are passed back to seedAnchorFromHistory in the same turn,
		// the cleared Extra and invalidated hash prevent resurrecting the old anchor.
		p.seedAnchorFromHistory(ctx, messages, nil)
		if run.getUsageAnchor() != nil {
			t.Fatalf("cycle %d: pre-compaction messages resurrected an invalidated anchor", cycle)
		}
	}

	if len(weakDiscarded) == 0 {
		t.Fatal("no assistant messages were discarded across compaction cycles")
	}
	// Clear the test fixture model's recorded inputs slice so only Run and ContextPipeline remain live.
	summary.mu.Lock()
	summary.inputs = nil
	summary.mu.Unlock()
	for i := 0; i < 4; i++ {
		runtime.GC()
	}
	for i, wp := range weakDiscarded {
		if wp.Value() != nil {
			t.Fatalf("discarded message %d remained reachable after GC", i)
		}
	}
	runtime.KeepAlive(p)
	runtime.KeepAlive(run)
}

func TestGatewayModelConfigIDBindsAndInvalidatesPromptTokensAnchorOnModelSwitch(t *testing.T) {
	s, lease, run := contextModelRun(t)
	run.ContextBudget, _ = NewContextBudgetSnapshot(16384)
	if _, err := s.DB.Exec(context.Background(), `UPDATE runtime_runs SET context_budget=$2 WHERE id=$1`, run.ID, run.ContextBudget); err != nil {
		t.Fatal(err)
	}
	budget, err := ParseContextBudgetSnapshot(run.ContextBudget)
	if err != nil {
		t.Fatal(err)
	}

	configA := modeltransport.CountingConfigID("https://provider-a.example/v1", "model-a")
	configBModel := modeltransport.CountingConfigID("https://provider-a.example/v1", "model-b")
	configBURL := modeltransport.CountingConfigID("https://provider-b.example/v1", "model-b")
	if configA == "" || configA == configBModel || configBModel == configBURL {
		t.Fatalf("expected distinct opaque config IDs: %q %q %q", configA, configBModel, configBURL)
	}

	var activeConfigID atomic.Value
	activeConfigID.Store(configA)
	var activePromptTokens atomic.Int64
	activePromptTokens.Store(6000)

	gatewayServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cfgID, _ := activeConfigID.Load().(string)
		if r.Method == http.MethodGet {
			configured := cfgID != ""
			win := int64(16384)
			var idPtr *string
			if cfgID != "" {
				idPtr = &cfgID
			}
			_ = json.NewEncoder(w).Encode(&wire.PingResponse{
				Ready:               configured,
				Configured:          &configured,
				Enabled:             &configured,
				ContextWindowTokens: &win,
				ModelConfigId:       idPtr,
			})
			return
		}
		prompt := int(activePromptTokens.Load())
		resp := assistantTextWithUsage("网关模型回复", prompt, 40)
		raw, _ := (&schema.HumanReadableSerializer{}).Marshal(resp)
		text := string(raw)
		if cfgID != "" {
			w.Header().Set(modeltransport.ModelConfigIDHeader, cfgID)
		}
		_ = json.NewEncoder(w).Encode(&wire.GatewayChatResponse{Ok: true, ResponseJson: &text})
	}))
	defer gatewayServer.Close()

	httpClient, err := newGatewayHTTPClient(nil, gatewayServer.URL+modeltransport.StreamPath)
	if err != nil {
		t.Fatal(err)
	}
	gwModel := &GatewayModel{
		Store:     s,
		Lease:     lease,
		AgentID:   lease.AgentID,
		Client:    httpClient,
		StreamURL: gatewayServer.URL + modeltransport.StreamPath,
		ModelConfigID: func() string {
			cfgID, _ := activeConfigID.Load().(string)
			return cfgID
		},
	}

	// Step 1 on Model A: local prefix is ~9,900 tokens (39,600 UTF-8 bytes), real PromptTokens is 6,000.
	sys := schema.SystemAgenticMessage("Trusted system instructions.")
	user1 := schema.UserAgenticMessage(strings.Repeat("甲乙丙丁", 3300))
	step1Prefix := []*schema.AgenticMessage{sys, user1}
	ctx1 := WithRun(context.Background(), run)
	respA, err := gwModel.Generate(ctx1, step1Prefix)
	if err != nil {
		t.Fatal(err)
	}
	savedAnchor, ok := extractUsageAnchor(respA)
	if !ok || savedAnchor.ConfigID != configA || savedAnchor.PromptTokens != 6000 {
		t.Fatalf("Model A response did not attach ConfigID-bound usage anchor: %+v (ok=%v)", savedAnchor, ok)
	}

	// Follow-up input (~1,200 local tokens) -> uncalibrated local total ~11,100 > TriggerTokens (10,828),
	// calibrated effective total ~7,200 <= TriggerTokens (10,828).
	inputs := []Input{{EventID: "turn-2", ConversationID: "conv", MessageID: "msg-2", SenderID: "user", SenderKind: "human", Body: strings.Repeat("继续第二阶段补充说明", 160)}}
	historyWithRespA := append([]*schema.AgenticMessage{sys, user1, respA}, inputMessages(inputs)...)

	// 1. Same-model restart recovery: fresh Run + fresh ContextPipeline with Gateway still on Model A (configA)
	// must continue to hit usage_delta and skip premature compaction!
	archiveRestart := &pipelineArchiveFixture{}
	pRestartSameModel, err := NewContextPipeline(context.Background(), ContextPipelineConfig{
		Model: gwModel, Budget: budget, Backend: archiveRestart, ArchivePath: archiveRestart.path, ArchiveTranscript: archiveRestart.transcript,
	})
	if err != nil {
		t.Fatal(err)
	}
	runRestartSame := &Run{ID: run.ID, Attempt: run.Attempt, AgentID: run.AgentID, Inputs: inputs, ContextProfile: contextProfileV1, ContextBudget: run.ContextBudget, Front: run.Front}
	ctxRestartSame := WithRun(context.Background(), runRestartSame)
	pRestartSameModel.seedAnchorFromHistory(ctxRestartSame, historyWithRespA, nil)
	mSame, err := pRestartSameModel.measureMainContext(ctxRestartSame, historyWithRespA, nil)
	if err != nil || mSame.CounterMode != "usage_delta" || mSame.AnchorPromptTokens != 6000 || mSame.EstimatedTokens > budget.TriggerTokens {
		t.Fatalf("same-model restart failed to restore usage_delta anchor: %+v (%v)", mSame, err)
	}

	// Also verify checkContextRequest accepts a candidate (~12,200 local tokens > InputTokenLimit 12,032, ~8,250 effective tokens) on Model A.
	user2Large := schema.UserAgenticMessage(strings.Repeat("补充材料", 750))
	largeCandidate := []*schema.AgenticMessage{sys, user1, respA, user2Large}
	maxOut := budget.initialOutputTokens()
	rawLargeReq, err := agenticRequestForRun(runRestartSame, largeCandidate, &model.Options{MaxTokens: &maxOut}, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := gwModel.checkContextRequest(ctxRestartSame, runRestartSame, 2, modelRequestHash(rawLargeReq), rawLargeReq, false); err != nil {
		t.Fatalf("same-model checkContextRequest rejected request fitting under Model A calibration: %v", err)
	}

	// 2. Same contextWindowTokens (16384), switch Gateway model from "model-a" to "model-b" (configBModel):
	// Both existing Run/ContextPipeline (at unrecorded Step 2) and a freshly restarted Run/ContextPipeline
	// must reject Model A's anchor and fall back to "local" estimation!
	activeConfigID.Store(configBModel)
	activePromptTokens.Store(7100)

	mSwitchedLive, err := pRestartSameModel.measureMainContext(ctx1, historyWithRespA, nil)
	if err != nil || mSwitchedLive.CounterMode != "local" || mSwitchedLive.AnchorPromptTokens != 0 || mSwitchedLive.EstimatedTokens <= budget.TriggerTokens {
		t.Fatalf("live ContextPipeline continued using Model A anchor after gateway switched to Model B: %+v (%v)", mSwitchedLive, err)
	}
	if _, err := gwModel.checkContextRequest(ctx1, run, 99, modelRequestHash(rawLargeReq), rawLargeReq, false); err == nil {
		t.Fatal("checkContextRequest continued using Model A anchor after gateway switched to Model B")
	}

	runRestartSwitched := &Run{ID: run.ID, Attempt: run.Attempt, AgentID: run.AgentID, Inputs: inputs, ContextProfile: contextProfileV1, ContextBudget: run.ContextBudget, Front: run.Front}
	ctxRestartSwitched := WithRun(context.Background(), runRestartSwitched)

	// When Step 1 is replayed from runtime_model_calls after the gateway switched to Model B,
	// Step 1 replays with Model A's ConfigID, and unrecorded Step 2 must NOT enable usage_delta under Model B!
	replayedRespA, err := gwModel.Generate(ctxRestartSwitched, step1Prefix)
	if err != nil {
		t.Fatal(err)
	}
	replayedAnchorA, ok := extractUsageAnchor(replayedRespA)
	if !ok || replayedAnchorA.ConfigID != configA {
		t.Fatalf("replayed Step 1 receipt did not preserve original Model A ConfigID: %+v (ok=%v)", replayedAnchorA, ok)
	}
	pRestartSameModel.seedAnchorFromHistory(ctxRestartSwitched, historyWithRespA, nil)
	mAfterReplayMismatch, err := pRestartSameModel.measureMainContext(ctxRestartSwitched, historyWithRespA, nil)
	if err != nil || mAfterReplayMismatch.CounterMode != "local" || mAfterReplayMismatch.AnchorPromptTokens != 0 {
		t.Fatalf("replayed Model A receipt enabled usage_delta after gateway switched to Model B: %+v (%v)", mAfterReplayMismatch, err)
	}

	// Once Model B completes a real call (Step 2) and returns its receipt, a new anchor bound to configBModel is established!
	respB, err := gwModel.Generate(ctxRestartSwitched, historyWithRespA)
	if err != nil {
		t.Fatal(err)
	}
	anchorB, ok := extractUsageAnchor(respB)
	if !ok || anchorB.ConfigID != configBModel || anchorB.PromptTokens != 7100 {
		t.Fatalf("Model B response did not establish new ConfigID anchor: %+v (ok=%v)", anchorB, ok)
	}
	followUpInputs := []Input{{EventID: "turn-3", ConversationID: "conv", MessageID: "msg-3", SenderID: "user", SenderKind: "human", Body: "继续第三阶段"}}
	historyWithRespB := append(append([]*schema.AgenticMessage{}, historyWithRespA...), respB)
	historyWithRespB = append(historyWithRespB, inputMessages(followUpInputs)...)
	mAfterB, err := pRestartSameModel.measureMainContext(ctxRestartSwitched, historyWithRespB, nil)
	if err != nil || mAfterB.CounterMode != "usage_delta" || mAfterB.AnchorPromptTokens != 7100 {
		t.Fatalf("Model B anchor did not activate usage_delta on subsequent turn: %+v (%v)", mAfterB, err)
	}

	// 3. Same contextWindowTokens (16384), switch Gateway baseUrl (configBURL):
	// Model B's old baseUrl anchor must immediately mismatch and fall back to "local"!
	activeConfigID.Store(configBURL)
	pRestartSameModel.seedAnchorFromHistory(ctxRestartSwitched, historyWithRespB, nil)
	mURLSwitched, err := pRestartSameModel.measureMainContext(ctxRestartSwitched, historyWithRespB, nil)
	if err != nil || mURLSwitched.CounterMode != "local" || mURLSwitched.AnchorPromptTokens != 0 {
		t.Fatalf("baseUrl switch under same window did not invalidate old usage anchor: %+v (%v)", mURLSwitched, err)
	}

	// 4. Unprovable model config (empty configID): must also fall back to "local".
	activeConfigID.Store("")
	pRestartSameModel.seedAnchorFromHistory(ctxRestartSwitched, historyWithRespB, nil)
	mUnprovable, err := pRestartSameModel.measureMainContext(ctxRestartSwitched, historyWithRespB, nil)
	if err != nil || mUnprovable.CounterMode != "local" || mUnprovable.AnchorPromptTokens != 0 {
		t.Fatalf("unprovable gateway config did not fall back to local estimation: %+v (%v)", mUnprovable, err)
	}
}

func TestReviewPR53ModelConfigSwitchDoesNotChangeRecordedCompactionBranch(t *testing.T) {
	for _, switchModel := range []bool{false, true} {
		name := "same_config"
		if switchModel {
			name = "changed_config"
		}
		t.Run(name, func(t *testing.T) {
			s, lease, run := contextModelRun(t)
			budget, err := ParseContextBudgetSnapshot(run.ContextBudget)
			if err != nil {
				t.Fatal(err)
			}
			configA := modeltransport.CountingConfigID("https://provider.example/v1", "model-a")
			configB := modeltransport.CountingConfigID("https://provider.example/v1", "model-b")
			var activeConfig atomic.Value
			activeConfig.Store(configA)
			var upstreamCalls atomic.Int32
			toolRound := contextToolRound("first", "small successful tool result")
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				call := upstreamCalls.Add(1)
				configID := activeConfig.Load().(string)
				msg := assistantTextWithUsage("Done.", 7300, 40)
				if call == 1 {
					msg = toolRound[0]
					msg.ContentBlocks = append([]*schema.ContentBlock{
						schema.NewContentBlock(&schema.AssistantGenText{Text: strings.Repeat("阶段成果", 400)}),
					}, msg.ContentBlocks...)
					msg.ResponseMeta = &schema.AgenticResponseMeta{TokenUsage: &schema.TokenUsage{PromptTokens: 6000, CompletionTokens: 1300, TotalTokens: 7300}}
				}
				raw, _ := (&schema.HumanReadableSerializer{}).Marshal(msg)
				text := string(raw)
				w.Header().Set(modeltransport.ModelConfigIDHeader, configID)
				_ = json.NewEncoder(w).Encode(&wire.GatewayChatResponse{Ok: true, ResponseJson: &text})
			}))
			defer server.Close()
			client, err := newGatewayHTTPClient(nil, server.URL+modeltransport.StreamPath)
			if err != nil {
				t.Fatal(err)
			}
			gateway := &GatewayModel{
				Store: s, Lease: lease, AgentID: lease.AgentID,
				Client: client, StreamURL: server.URL + modeltransport.StreamPath,
				ModelConfigID: func() string { return activeConfig.Load().(string) },
			}
			newPipeline := func() *ContextPipeline {
				archive := &pipelineArchiveFixture{}
				p, err := NewContextPipeline(context.Background(), ContextPipelineConfig{
					Model: gateway, Budget: budget, Backend: archive,
					ArchivePath: archive.path, ArchiveTranscript: archive.transcript,
				})
				if err != nil {
					t.Fatal(err)
				}
				return p
			}
			tools := []*schema.ToolInfo{{Name: "lookup", Desc: "Return a short lookup result."}}
			prefix := append([]*schema.AgenticMessage{
				schema.SystemAgenticMessage("Trusted system instructions."),
				schema.UserAgenticMessage(strings.Repeat("甲乙丙丁", 3250)),
				assistantText("Earlier task completed."),
			}, inputMessages(run.Inputs)...)
			ctx := WithRun(context.Background(), run)
			p := newPipeline()
			_, initial, err := p.BeforeModelRewriteState(ctx, &adk.TypedChatModelAgentState[*schema.AgenticMessage]{Messages: prefix, ToolInfos: tools}, nil)
			if err != nil || run.Step.Load() != 0 {
				t.Fatalf("setup unexpectedly compacted first request: step=%d err=%v", run.Step.Load(), err)
			}
			first, err := gateway.Generate(ctx, initial.Messages, model.WithTools(tools))
			if err != nil {
				t.Fatal(err)
			}
			next := append(append([]*schema.AgenticMessage{}, initial.Messages...), first)
			next = append(next, toolRound[1:]...)
			_, original, err := p.BeforeModelRewriteState(ctx, &adk.TypedChatModelAgentState[*schema.AgenticMessage]{Messages: next, ToolInfos: tools}, nil)
			if err != nil {
				t.Fatal(err)
			}
			measured, err := p.measureMainContext(ctx, original.Messages, tools)
			if err != nil || measured.CounterMode != "usage_delta" || measured.LocalTokens <= budget.TriggerTokens || measured.EstimatedTokens >= budget.TriggerTokens || run.Step.Load() != 1 {
				t.Fatalf("setup must select uncompressed calibrated branch: %+v threshold=%d step=%d err=%v", measured, budget.TriggerTokens, run.Step.Load(), err)
			}
			t.Logf("original branch: mode=%s local=%d calibrated=%d trigger=%d", measured.CounterMode, measured.LocalTokens, measured.EstimatedTokens, budget.TriggerTokens)
			if _, err = gateway.Generate(ctx, original.Messages, model.WithTools(tools)); err != nil {
				t.Fatal(err)
			}
			if upstreamCalls.Load() != 2 || run.Step.Load() != 2 {
				t.Fatal("setup did not persist two main-model receipts")
			}
			// Restart the same durable Run before these two accepted calls. Both
			// recorded calls should replay without contacting any provider.
			if switchModel {
				activeConfig.Store(configB)
			}
			restarted := &Run{ID: run.ID, Attempt: run.Attempt, AgentID: run.AgentID, Inputs: run.Inputs, ContextProfile: run.ContextProfile, ContextBudget: run.ContextBudget, Front: run.Front}
			replayCtx := WithRun(context.Background(), restarted)
			replayPipeline := newPipeline()
			_, replayInitial, err := replayPipeline.BeforeModelRewriteState(replayCtx, &adk.TypedChatModelAgentState[*schema.AgenticMessage]{Messages: prefix, ToolInfos: tools}, nil)
			if err != nil {
				t.Fatal(err)
			}
			replayedFirst, err := gateway.Generate(replayCtx, replayInitial.Messages, model.WithTools(tools))
			if err != nil {
				t.Fatalf("first accepted call failed replay: %v", err)
			}
			replayedNext := append(append([]*schema.AgenticMessage{}, replayInitial.Messages...), replayedFirst)
			replayedNext = append(replayedNext, toolRound[1:]...)
			replayMeasure, err := replayPipeline.measureMainContext(replayCtx, replayedNext, tools)
			if err != nil {
				t.Fatal(err)
			}
			t.Logf("replayed branch: mode=%s local=%d calibrated=%d trigger=%d", replayMeasure.CounterMode, replayMeasure.LocalTokens, replayMeasure.EstimatedTokens, budget.TriggerTokens)
			_, replaySecond, err := replayPipeline.BeforeModelRewriteState(replayCtx, &adk.TypedChatModelAgentState[*schema.AgenticMessage]{Messages: replayedNext, ToolInfos: tools}, nil)
			if err != nil {
				t.Fatalf("recorded second main-model call cannot replay after config change: step=%d upstream=%d err=%v", restarted.Step.Load(), upstreamCalls.Load(), err)
			}
			replayedSecondResp, err := gateway.Generate(replayCtx, replaySecond.Messages, model.WithTools(tools))
			if err != nil {
				t.Fatalf("second accepted call failed replay: %v", err)
			}
			if upstreamCalls.Load() != 2 {
				t.Fatalf("replay repeated provider calls: %d", upstreamCalls.Load())
			}
			// After all recorded steps have replayed (step=2, nextStep=3 is unrecorded),
			// a switched gateway config must invalidate the old anchor for any new live step!
			if switchModel {
				postReplayMessages := append(append([]*schema.AgenticMessage{}, replaySecond.Messages...), replayedSecondResp, schema.UserAgenticMessage("new live step after replay"))
				replayPipeline.seedAnchorFromHistory(replayCtx, postReplayMessages, tools)
				liveMeasure, err := replayPipeline.measureMainContext(replayCtx, postReplayMessages, tools)
				if err != nil || liveMeasure.CounterMode != "local" || liveMeasure.AnchorPromptTokens != 0 {
					t.Fatalf("expected unrecorded step 3 after model switch to fall back to local, got %+v (err=%v)", liveMeasure, err)
				}
			}
		})
	}
}

func TestAdmissionConfigBindingAndRejectingConfigRecoveryPersistence(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		mode := "unary"
		if streaming {
			mode = "stream"
		}
		t.Run(mode, func(t *testing.T) {
			s, lease, run := contextModelRun(t)
			budget, err := ParseContextBudgetSnapshot(run.ContextBudget)
			if err != nil {
				t.Fatal(err)
			}
			configA := modeltransport.CountingConfigID("https://provider.example/v1", "model-a")
			configB := modeltransport.CountingConfigID("https://provider.example/v1", "model-b")
			var pingConfig atomic.Value
			pingConfig.Store(configA)
			var serverConfig atomic.Value
			serverConfig.Store(configA)
			var rejectOverflow atomic.Bool
			var lastRequestHeader atomic.Value

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				reqCfg := strings.TrimSpace(r.Header.Get(modeltransport.ModelConfigIDHeader))
				lastRequestHeader.Store(reqCfg)
				actualCfg := serverConfig.Load().(string)
				w.Header().Set(modeltransport.ModelConfigIDHeader, actualCfg)
				// Rotate pingConfig right when the server handles the request so any post-failure Ping
				// would observe configB instead of actualCfg (configA).
				if rejectOverflow.Load() {
					pingConfig.Store(configB)
					code := "context_overflow"
					if streaming {
						_ = json.NewEncoder(w).Encode(map[string]any{"type": "error", "errorCode": code, "statusCode": 400, "modelConfigId": actualCfg})
					} else {
						status := int32(400)
						_ = json.NewEncoder(w).Encode(&wire.GatewayChatResponse{Ok: false, ErrorCode: &code, StatusCode: &status})
					}
					return
				}
				if reqCfg != "" && reqCfg != actualCfg {
					code := "gateway_unavailable"
					if streaming {
						_ = json.NewEncoder(w).Encode(map[string]any{"type": "error", "errorCode": code, "statusCode": 409, "modelConfigId": actualCfg})
					} else {
						status := int32(409)
						_ = json.NewEncoder(w).Encode(&wire.GatewayChatResponse{Ok: false, ErrorCode: &code, StatusCode: &status})
					}
					return
				}
				msg := assistantTextWithUsage("ok", 500, 10)
				raw, _ := (&schema.HumanReadableSerializer{}).Marshal(msg)
				text := string(raw)
				if streaming {
					_ = json.NewEncoder(w).Encode(map[string]string{"type": "chunk", "responseJson": text})
					_ = json.NewEncoder(w).Encode(map[string]string{"type": "done", "modelConfigId": actualCfg})
				} else {
					_ = json.NewEncoder(w).Encode(&wire.GatewayChatResponse{Ok: true, ResponseJson: &text})
				}
			}))
			defer server.Close()

			client, err := newGatewayHTTPClient(nil, server.URL+modeltransport.StreamPath)
			if err != nil {
				t.Fatal(err)
			}
			gateway := &GatewayModel{
				Store:         s,
				Lease:         lease,
				AgentID:       lease.AgentID,
				Client:        client,
				StreamURL:     server.URL + modeltransport.StreamPath,
				ModelConfigID: func() string { return pingConfig.Load().(string) },
			}
			archive := &pipelineArchiveFixture{}
			p, err := NewContextPipeline(context.Background(), ContextPipelineConfig{
				Model: gateway, Budget: budget, Backend: archive, ArchivePath: archive.path, ArchiveTranscript: archive.transcript,
			})
			if err != nil {
				t.Fatal(err)
			}

			callModel := func(ctx context.Context, msgs []*schema.AgenticMessage) error {
				if streaming {
					sr, err := gateway.Stream(ctx, msgs)
					if err != nil {
						return err
					}
					defer sr.Close()
					for {
						if _, err := sr.Recv(); err != nil {
							if err == io.EOF {
								return nil
							}
							return err
						}
					}
				}
				_, err := gateway.Generate(ctx, msgs)
				return err
			}

			// 1. TOCTOU between BeforeModelRewriteState (admitted under configA) and generation (gateway rotated to configB):
			// Generation request must carry X-Teamie-Model-Config-Id: configA and fail with retryable gateway_unavailable.
			msgs := []*schema.AgenticMessage{schema.SystemAgenticMessage("System"), schema.UserAgenticMessage("Hello")}
			ctx := WithRun(context.Background(), run)
			_, rewritten, err := p.BeforeModelRewriteState(ctx, &adk.TypedChatModelAgentState[*schema.AgenticMessage]{Messages: msgs}, nil)
			if err != nil {
				t.Fatal(err)
			}
			serverConfig.Store(configB)
			err = callModel(ctx, rewritten.Messages)
			var modelErr *ModelError
			if !errors.As(err, &modelErr) || modelErr.Code != "gateway_unavailable" || !modelErr.Retry {
				t.Fatalf("expected retryable gateway_unavailable on config rotation race, got %v", err)
			}
			if got, _ := lastRequestHeader.Load().(string); got != configA {
				t.Fatalf("generation request did not carry admission config ID %q: got %q", configA, got)
			}

			// 2. When the gateway rejects a request with context_overflow under configA and an admin rotates
			// pingConfig to configB before gatewayFailure finishes, runtime_context_recoveries must persist configA.
			serverConfig.Store(configA)
			pingConfig.Store(configA)
			rejectOverflow.Store(true)
			_, rewritten2, err := p.BeforeModelRewriteState(ctx, &adk.TypedChatModelAgentState[*schema.AgenticMessage]{Messages: msgs}, nil)
			if err != nil {
				t.Fatal(err)
			}
			err = callModel(ctx, rewritten2.Messages)
			if !errors.As(err, &modelErr) || modelErr.Code != "context_overflow" {
				t.Fatalf("expected context_overflow, got %v", err)
			}
			recordedCfg, recorded := s.RecordedModelStepConfigID(ctx, run, int(run.Step.Load()))
			if !recorded || recordedCfg != configA {
				t.Fatalf("recovery receipt recorded wrong config ID after post-failure rotation: got %q (recorded=%v), want %q", recordedCfg, recorded, configA)
			}
		})
	}
}

func TestUnknownAdmissionReplayPreservesLocalDecisionAndFailsClosed(t *testing.T) {
	for _, failPreflight := range []bool{false, true} {
		name := "known_config_control"
		if failPreflight {
			name = "one_failed_config_lookup"
		}
		t.Run(name, func(t *testing.T) {
			s, lease, run := contextModelRun(t)
			budget, _ := ParseContextBudgetSnapshot(run.ContextBudget)
			configA := modeltransport.CountingConfigID("https://provider.example/v1", "a")
			var failOnce atomic.Bool
			var mainCalls, summaryCalls atomic.Int32
			round := contextToolRound("first", "short successful tool result")
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var call modeltransport.CallRequest
				if err := json.NewDecoder(r.Body).Decode(&call); err != nil {
					t.Error(err)
					return
				}
				if expected := r.Header.Get(modeltransport.ModelConfigIDHeader); expected != "" && expected != configA {
					t.Error("wrong expected configuration")
					return
				}
				var result *schema.AgenticMessage
				if isSummaryRequest([]byte(call.RequestJSON)) {
					summaryCalls.Add(1)
					result = assistantTextWithUsage("Earlier completed work summarized.", 400, 30)
				} else if mainCalls.Add(1) == 1 {
					result = round[0]
					result.ContentBlocks = append([]*schema.ContentBlock{schema.NewContentBlock(&schema.AssistantGenText{Text: strings.Repeat("阶段成果", 400)})}, result.ContentBlocks...)
					result.ResponseMeta = &schema.AgenticResponseMeta{TokenUsage: &schema.TokenUsage{PromptTokens: 6000, CompletionTokens: 1300, TotalTokens: 7300}}
				} else {
					result = assistantTextWithUsage("Done.", 7300, 40)
				}
				raw, _ := (&schema.HumanReadableSerializer{}).Marshal(result)
				text := string(raw)
				w.Header().Set(modeltransport.ModelConfigIDHeader, configA)
				_ = json.NewEncoder(w).Encode(&wire.GatewayChatResponse{Ok: true, ResponseJson: &text})
			}))
			defer server.Close()
			client, err := newGatewayHTTPClient(nil, server.URL+modeltransport.StreamPath)
			if err != nil {
				t.Fatal(err)
			}
			gateway := &GatewayModel{
				Store: s, Lease: lease, AgentID: lease.AgentID, Client: client, StreamURL: server.URL + modeltransport.StreamPath,
				ModelConfigID: func() string {
					if failOnce.CompareAndSwap(true, false) {
						return ""
					}
					return configA
				},
			}
			newPipeline := func() *ContextPipeline {
				a := &pipelineArchiveFixture{}
				p, err := NewContextPipeline(context.Background(), ContextPipelineConfig{Model: gateway, Budget: budget, Backend: a, ArchivePath: a.path, ArchiveTranscript: a.transcript})
				if err != nil {
					t.Fatal(err)
				}
				return p
			}
			tools := []*schema.ToolInfo{{Name: "lookup", Desc: "Return a short result."}}
			prefix := append([]*schema.AgenticMessage{schema.SystemAgenticMessage("Trusted policy."), schema.UserAgenticMessage(strings.Repeat("甲乙丙丁", 3250)), assistantText("Previous task done.")}, inputMessages(run.Inputs)...)
			ctx := WithRun(context.Background(), run)
			p := newPipeline()
			_, initial, err := p.BeforeModelRewriteState(ctx, &adk.TypedChatModelAgentState[*schema.AgenticMessage]{Messages: prefix, ToolInfos: tools}, nil)
			if err != nil {
				t.Fatal(err)
			}
			first, err := gateway.Generate(ctx, initial.Messages, model.WithTools(tools))
			if err != nil {
				t.Fatal(err)
			}
			next := append(append([]*schema.AgenticMessage{}, initial.Messages...), first)
			next = append(next, round[1:]...)
			before, err := p.measureMainContext(ctx, next, tools)
			if err != nil || before.CounterMode != "usage_delta" || before.LocalTokens <= budget.TriggerTokens || before.EstimatedTokens >= budget.TriggerTokens {
				t.Fatalf("bad threshold fixture: %+v err=%v", before, err)
			}
			failOnce.Store(failPreflight)
			_, original, err := p.BeforeModelRewriteState(ctx, &adk.TypedChatModelAgentState[*schema.AgenticMessage]{Messages: next, ToolInfos: tools}, nil)
			if err != nil {
				t.Fatal(err)
			}
			if failPreflight && summaryCalls.Load() == 0 {
				t.Fatal("failed lookup did not choose local compaction")
			}
			if !failPreflight && summaryCalls.Load() != 0 {
				t.Fatal("control unexpectedly compacted")
			}
			if _, err = gateway.Generate(ctx, original.Messages, model.WithTools(tools)); err != nil {
				t.Fatal(err)
			}
			// Restart before the committed steps, without changing model configuration.
			restarted := &Run{ID: run.ID, Attempt: run.Attempt, AgentID: run.AgentID, Inputs: run.Inputs, ContextProfile: run.ContextProfile, ContextBudget: run.ContextBudget, Front: run.Front}
			rctx := WithRun(context.Background(), restarted)
			rp := newPipeline()
			_, rinitial, err := rp.BeforeModelRewriteState(rctx, &adk.TypedChatModelAgentState[*schema.AgenticMessage]{Messages: prefix, ToolInfos: tools}, nil)
			if err != nil {
				t.Fatal(err)
			}
			rfirst, err := gateway.Generate(rctx, rinitial.Messages, model.WithTools(tools))
			if err != nil {
				t.Fatal(err)
			}
			rnext := append(append([]*schema.AgenticMessage{}, rinitial.Messages...), rfirst)
			rnext = append(rnext, round[1:]...)
			_, rsecond, err := rp.BeforeModelRewriteState(rctx, &adk.TypedChatModelAgentState[*schema.AgenticMessage]{Messages: rnext, ToolInfos: tools}, nil)
			if err != nil {
				t.Fatalf("cannot replay compaction: %v", err)
			}
			if _, err = gateway.Generate(rctx, rsecond.Messages, model.WithTools(tools)); err != nil {
				t.Fatalf("replay changed original local admission branch: step=%d err=%v", restarted.Step.Load(), err)
			}
			// If the database lookup fails during replay admission, BeforeModelRewriteState must fail closed
			// instead of falling back to the live gateway.
			failedReplay := &Run{ID: run.ID, Attempt: run.Attempt, AgentID: run.AgentID, Inputs: run.Inputs, ContextProfile: run.ContextProfile, ContextBudget: run.ContextBudget, Front: run.Front}
			canceledBase, cancelBase := context.WithCancel(context.Background())
			cancelBase()
			fctx := WithRun(canceledBase, failedReplay)
			if _, _, err := newPipeline().BeforeModelRewriteState(fctx, &adk.TypedChatModelAgentState[*schema.AgenticMessage]{Messages: prefix, ToolInfos: tools}, nil); err == nil {
				t.Fatal("expected BeforeModelRewriteState to fail closed when replay admission lookup fails")
			}
		})
	}
}
