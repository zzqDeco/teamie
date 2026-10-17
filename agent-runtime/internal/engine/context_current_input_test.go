package engine

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"
)

func currentInputFixture(t *testing.T) (*ContextPipeline, context.Context, []*schema.AgenticMessage, []*schema.ToolInfo) {
	t.Helper()
	p, _, _ := testPipeline(t, 16384)
	inputs := []Input{{EventID: "current-event", WorkspaceID: "11111111-1111-4111-8111-111111111111", ConversationID: "22222222-2222-4222-8222-222222222222", MessageID: "33333333-3333-4333-8333-333333333333", SenderID: "44444444-4444-4444-8444-444444444444", SenderKind: "human", Body: strings.Repeat("CURRENT request: retain my full task body. ", 35)}}
	ctx := WithRun(context.Background(), &Run{Inputs: inputs})
	current := inputMessages(inputs)
	tools := []*schema.ToolInfo{{Name: "large_manifest", Desc: strings.Repeat("manifest ", 2400)}}
	messages := append([]*schema.AgenticMessage{schema.SystemAgenticMessage("trusted policy"), schema.UserAgenticMessage("Earlier permission work is finished.")}, current...)
	messages = append(messages, contextToolRound("recent", strings.Repeat("old output ", 1600))...)
	return p, ctx, messages, tools
}

func TestNativeModelReceivesCurrentBodyAfterCapacityCompaction(t *testing.T) {
	p, ctx, messages, _ := currentInputFixture(t)
	current := agenticText(messages[2])
	messages = append(messages[:3], contextToolRound("large", strings.Repeat("earlier output ", 4000))...)
	agent, err := adk.NewTypedChatModelAgent(ctx, &adk.TypedChatModelAgentConfig[*schema.AgenticMessage]{Name: "current_input", Model: p.cfg.Model, MaxIterations: 1, Handlers: []adk.TypedChatModelAgentMiddleware[*schema.AgenticMessage]{p}, ModelRetryConfig: p.RetryConfig()})
	if err != nil {
		t.Fatal(err)
	}
	iter := agent.Run(ctx, &adk.TypedAgentInput[*schema.AgenticMessage]{Messages: messages})
	for {
		event, ok := iter.Next()
		if !ok {
			break
		}
		if event != nil && event.Err != nil && !modelRetryObservation(event.Err) {
			t.Fatal(event.Err)
		}
		if event != nil && event.Output != nil && event.Output.MessageOutput != nil {
			if _, err = event.Output.MessageOutput.GetMessage(); err != nil && !modelRetryObservation(err) {
				t.Fatal(err)
			}
		}
	}
	model := p.cfg.Model.(*pipelineModelFixture)
	model.mu.Lock()
	defer model.mu.Unlock()
	if len(model.inputs) < 2 {
		t.Fatal("fixture did not exercise a real summary then main call")
	}
	found := false
	for _, message := range model.inputs[len(model.inputs)-1] {
		found = found || agenticText(message) == current
	}
	if !found {
		t.Fatal("native main model received only a preview of the current request")
	}
	for _, summaryInput := range model.inputs[:len(model.inputs)-1] {
		for _, message := range summaryInput {
			if agenticText(message) == current {
				t.Fatal("current request was consumed by a historical summary call")
			}
		}
	}
}

func TestSoftReductionTargetCannotOverrideFullCurrentInput(t *testing.T) {
	p, ctx, messages, _ := currentInputFixture(t)
	out, err := p.fitTailTo(ctx, messages[:1], messages[1:3], nil, 100)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, message := range out {
		found = found || agenticText(message) == agenticText(messages[2])
	}
	if !found {
		t.Fatal("soft target removed the actual task")
	}
}

func TestCapacityPressureKeepsCompleteCurrentBodyBeforeOlderToolRound(t *testing.T) {
	p, ctx, messages, tools := currentInputFixture(t)
	current := agenticText(messages[2])
	if len(current) < 256 || strings.Contains(current[:256], "CURRENT request") {
		t.Fatal("fixture does not reproduce metadata-only archive previews")
	}
	out, err := p.fitTailTo(ctx, messages[:1], messages[1:], tools, p.cfg.Budget.InputTokenLimit)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, message := range out {
		found = found || agenticText(message) == current
	}
	if !found {
		t.Fatal("capacity fitting replaced the actual current body with an archive preview")
	}
	measurement, err := measureContext(out, tools, p.cfg.Budget.OutputReserveTokens, false)
	if err != nil || !p.cfg.Budget.fits(measurement) {
		t.Fatal("preserved input exceeds actual model capacity", measurement, err)
	}
}

func TestCurrentInputBatchStaysOutsideTheSummaryPrefix(t *testing.T) {
	p, _, _ := testPipeline(t, 16384)
	inputs := []Input{{EventID: "first", ConversationID: "dm", MessageID: "dm-request", Body: strings.Repeat("first actual request ", 70)}, {EventID: "second", ConversationID: "channel", MessageID: "channel-request", Body: strings.Repeat("second actual request ", 70)}}
	ctx := WithRun(context.Background(), &Run{Inputs: inputs})
	messages := []*schema.AgenticMessage{schema.SystemAgenticMessage("trusted policy"), schema.UserAgenticMessage("old resolved work")}
	messages = append(messages, inputMessages(inputs)...)
	for i := 0; i < 7; i++ {
		messages = append(messages, contextToolRound(string(rune('a'+i)), strings.Repeat("earlier tool result ", 250))...)
	}
	out, err := p.compact(ctx, messages, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	position := -1
	for _, expected := range inputMessages(inputs) {
		found := -1
		for index, message := range out {
			if agenticText(message) == agenticText(expected) {
				found = index
			}
		}
		if found <= position {
			t.Fatal("batched source body was summarized, dropped, or reordered")
		}
		position = found
	}
}

func TestCurrentInputOverCapacityFailsInsteadOfLosingItsBody(t *testing.T) {
	p, _, _ := testPipeline(t, 16384)
	inputs := []Input{{EventID: "huge", ConversationID: "source", MessageID: "huge-source", Body: strings.Repeat("不可丢失的新请求正文", 2000)}}
	ctx := WithRun(context.Background(), &Run{Inputs: inputs})
	_, err := p.fitTailTo(ctx, []*schema.AgenticMessage{schema.SystemAgenticMessage("policy")}, inputMessages(inputs), nil, p.cfg.Budget.InputTokenLimit)
	var failure *ModelError
	if !errors.As(err, &failure) || failure.Code != "context_unrecoverable" {
		t.Fatal("silently offloaded an oversized current request", err)
	}
}

func TestCurrentInputMissingOrOnlyRepresentedByPreviewFailsClosed(t *testing.T) {
	p, ctx, messages, _ := currentInputFixture(t)
	preview, err := p.archiveUnit(ctx, messages[2:3])
	if err != nil {
		t.Fatal(err)
	}
	_, err = p.currentInputSet(ctx, []*schema.AgenticMessage{messages[0], preview})
	var failure *ModelError
	if !errors.As(err, &failure) || failure.Code != "context_unrecoverable" {
		t.Fatal("accepted a metadata preview in place of the current request", err)
	}
}

func TestHistoricalBudgetVersionsKeepTheirCapacityAndSourceProjection(t *testing.T) {
	for _, version := range []int{1, 2, 3} {
		budget, err := makeContextBudgetSnapshotVersion(65536, version)
		if err != nil {
			t.Fatal(err)
		}
		want := map[int]int{1: 2048, 2: 8192, 3: 4096}[version]
		if budget.OutputReserveTokens != want {
			t.Fatal("changed historical frozen capacity", version, budget)
		}
		raw, _ := json.Marshal(budget)
		parsed, err := ParseContextBudgetSnapshot(raw)
		if err != nil || parsed != budget {
			t.Fatal("lost old snapshot reader support", err)
		}
	}
}
