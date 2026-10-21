package engine

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"
)

func notificationBatchFixture(t *testing.T, human bool) (*ContextPipeline, *pipelineArchiveFixture, context.Context, []*schema.AgenticMessage) {
	t.Helper()
	pipeline, archive, _ := testPipeline(t, 16384)
	sources := []InputSource{}
	for i := 0; i < 9; i++ {
		sources = append(sources, InputSource{EventID: fmt.Sprint(100 + i), WorkspaceID: "11111111-1111-4111-8111-111111111111", ConversationID: "22222222-2222-4222-8222-222222222222", MessageID: fmt.Sprintf("33333333-3333-4333-8333-%012d", i), SenderID: "44444444-4444-4444-8444-444444444444", SenderKind: "human"})
	}
	inputs := []Input{}
	for i := 0; i < 20; i++ {
		inputs = append(inputs, Input{Kind: "task.notification", EventID: fmt.Sprint(200 + i), TaskID: fmt.Sprintf("shell-completed-%02d", i), NotificationID: fmt.Sprintf("notification-%02d", i), ParentRunID: "55555555-5555-4555-8555-555555555555", Status: "completed", SourceRefs: sources})
	}
	if human {
		inputs = append(inputs, Input{EventID: "300", WorkspaceID: sources[0].WorkspaceID, ConversationID: sources[0].ConversationID, MessageID: "66666666-6666-4666-8666-666666666666", SenderID: sources[0].SenderID, SenderKind: "human", Body: strings.Repeat("Complete the current file delivery, preserving this exact new request. ", 15)})
	}
	ctx := WithRun(context.Background(), &Run{Inputs: inputs})
	messages := append([]*schema.AgenticMessage{schema.SystemAgenticMessage("trusted policy")}, inputMessages(inputs)...)
	return pipeline, archive, ctx, messages
}

func TestCompletedNotificationBatchCannotPinTheEntireModelWindow(t *testing.T) {
	for _, human := range []bool{false, true} {
		t.Run(fmt.Sprint(human), func(t *testing.T) {
			p, archive, ctx, messages := notificationBatchFixture(t, human)
			tools := []*schema.ToolInfo{{Name: "fixture_manifest", Desc: strings.Repeat("manifest ", 2400)}}
			out, err := p.compact(ctx, messages, tools, false)
			if err != nil {
				t.Fatal("completed machine notifications blocked the current turn", err)
			}
			m, err := measureContext(out, tools, p.cfg.Budget.OutputReserveTokens, false)
			if err != nil || !p.cfg.Budget.fits(m) {
				t.Fatal("compacted request exceeds the real capacity", m, err)
			}
			if human {
				current := agenticText(messages[len(messages)-1])
				found := false
				for _, message := range out {
					found = found || agenticText(message) == current
				}
				if !found {
					t.Fatal("lost the complete current human body")
				}
			}
			retained := ""
			for _, message := range out {
				retained += agenticText(message)
			}
			for _, text := range archive.content {
				retained += text
			}
			for i := 0; i < 20; i++ {
				if !strings.Contains(retained, fmt.Sprintf("notification-%02d", i)) {
					t.Fatal("lost durable notification provenance", i)
				}
			}
		})
	}
}

func TestNativeModelResumesWithBatchedTaskNotifications(t *testing.T) {
	p, _, ctx, messages := notificationBatchFixture(t, true)
	current := agenticText(messages[len(messages)-1])
	agent, err := adk.NewTypedChatModelAgent(ctx, &adk.TypedChatModelAgentConfig[*schema.AgenticMessage]{Name: "notification_batch", Model: p.cfg.Model, MaxIterations: 1, Handlers: []adk.TypedChatModelAgentMiddleware[*schema.AgenticMessage]{p}, ModelRetryConfig: p.RetryConfig()})
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
	}
	model := p.cfg.Model.(*pipelineModelFixture)
	model.mu.Lock()
	defer model.mu.Unlock()
	if len(model.inputs) < 2 {
		t.Fatal("native turn did not exercise actual compaction and main calls")
	}
	found := false
	for _, message := range model.inputs[len(model.inputs)-1] {
		found = found || agenticText(message) == current
	}
	if !found {
		t.Fatal("native model did not receive the exact latest human request")
	}
	for _, measurement := range model.budgets {
		if !p.cfg.Budget.fits(measurement) {
			t.Fatal("native encoded request exceeded the frozen window", measurement)
		}
	}
}

func TestFrozenV4RetainsNotificationPinning(t *testing.T) {
	p, _, ctx, messages := notificationBatchFixture(t, true)
	budget, err := makeContextBudgetSnapshotVersion(16384, 4)
	if err != nil {
		t.Fatal(err)
	}
	p.cfg.Budget = budget
	if _, err = p.compact(ctx, messages, []*schema.ToolInfo{{Name: "fixture_manifest", Desc: strings.Repeat("manifest ", 2400)}}, false); err == nil {
		t.Fatal("new admission changed the frozen v4 transformation")
	}
}
