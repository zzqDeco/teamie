package engine

import (
	"fmt"
	"strings"
	"testing"

	"github.com/cloudwego/eino/schema"
)

func fragmentedHistoryFixture(t *testing.T) (*ContextPipeline, []*schema.AgenticMessage, []*schema.ToolInfo) {
	t.Helper()
	p, _, messages, tools := currentInputFixture(t)
	p.cfg.Budget, _ = makeContextBudgetSnapshotVersion(16384, 4)
	history := []*schema.AgenticMessage{messages[0]}
	for i := 0; i < 30; i++ {
		message := schema.UserAgenticMessage(fmt.Sprintf("older-message-%02d: %s", i, strings.Repeat("A small immutable observation. ", 18)))
		message.Extra = map[string]any{"teamie_context_data": contextProfileV1, "teamie_context_sources": contextSourceValue([]InputSource{{ConversationID: "older-conversation", MessageID: fmt.Sprintf("older-message-%02d", i), EventID: fmt.Sprint(i)}})}
		measurement, err := p.cfg.Budget.measureContext([]*schema.AgenticMessage{message}, nil, p.cfg.Budget.OutputReserveTokens, false)
		if err != nil || measurement.EstimatedTokens >= 384 {
			t.Fatal("fixture does not reproduce individually small history", measurement, err)
		}
		history = append(history, message)
	}
	history = append(history, messages[2])
	history = append(history, contextToolRound("recent-a", "Verified input file path and digest.")...)
	history = append(history, contextToolRound("recent-b", "Latest complete read: rows=60; compute with Python next.")...)
	return p, history, tools
}

func TestFragmentedHistoryFitsWithoutLosingCurrentWorkOrRecentToolResults(t *testing.T) {
	p, messages, tools := fragmentedHistoryFixture(t)
	_, ctx, _, _ := currentInputFixture(t)
	before, err := p.cfg.Budget.measureContext(messages, tools, p.cfg.Budget.OutputReserveTokens, false)
	if err != nil || p.cfg.Budget.fits(before) {
		t.Fatal("fixture must exceed model capacity", before, err)
	}
	out, err := p.fitTailTo(ctx, messages[:1], messages[1:], tools, p.cfg.Budget.InputTokenLimit)
	if err != nil {
		t.Fatal("small historical fragments stopped the continuous Session", err)
	}
	after, err := p.cfg.Budget.measureContext(out, tools, p.cfg.Budget.OutputReserveTokens, false)
	if err != nil || !p.cfg.Budget.fits(after) || after.EstimatedTokens >= before.EstimatedTokens {
		t.Fatal("history did not shrink to real capacity", before, after, err)
	}
	for _, protected := range messages[len(messages)-5:] {
		found := false
		for _, message := range out {
			found = found || message == protected
		}
		if !found {
			t.Fatal("coalescing lost the current request or a recent complete tool round")
		}
	}
	archive := p.cfg.Backend.(*pipelineArchiveFixture)
	found := false
	for _, content := range archive.content {
		if strings.Contains(content, "older-message-00") && strings.Contains(content, "older-message-29") {
			if strings.Contains(content, "CURRENT request") {
				t.Fatal("current input was archived with old fragments")
			}
			found = true
		}
	}
	if !found {
		t.Fatal("older observations are not recoverable from an immutable archive")
	}
	if len(contextSources(out)) != 31 {
		t.Fatal("coalescing lost historical source closure")
	}
}

func TestFragmentedHistoryTransportPressureIsAlsoRecoverable(t *testing.T) {
	p, messages, _ := fragmentedHistoryFixture(t)
	_, ctx, _, _ := currentInputFixture(t)
	for _, message := range messages[1:31] {
		message.Extra["receipt_metadata"] = strings.Repeat("opaque", 3000)
	}
	before, err := p.cfg.Budget.measureContext(messages, nil, p.cfg.Budget.OutputReserveTokens, false)
	if err != nil || before.EstimatedTokens > p.cfg.Budget.InputTokenLimit || before.TransportBytes <= p.cfg.Budget.TransportLimitBytes {
		t.Fatal("fixture must reproduce transport-only pressure", before, err)
	}
	out, err := p.fitTailTo(ctx, messages[:1], messages[1:], nil, p.cfg.Budget.InputTokenLimit)
	if err != nil {
		t.Fatal(err)
	}
	after, err := p.cfg.Budget.measureContext(out, nil, p.cfg.Budget.OutputReserveTokens, false)
	if err != nil || !p.cfg.Budget.fits(after) {
		t.Fatal("coalesced transport still exceeds capacity", after, err)
	}
}

func TestFragmentedHistoryArchiveFailureDoesNotDiscardWorkingContext(t *testing.T) {
	p, messages, tools := fragmentedHistoryFixture(t)
	_, ctx, _, _ := currentInputFixture(t)
	p.cfg.Backend.(*pipelineArchiveFixture).fail = true
	current := agenticText(messages[len(messages)-5])
	out, err := p.fitTailTo(ctx, messages[:1], messages[1:], tools, p.cfg.Budget.InputTokenLimit)
	if err == nil || out != nil || agenticText(messages[len(messages)-5]) != current {
		t.Fatal("archive failure discarded or mutated current work", err)
	}
}

func TestFrozenV3DoesNotChangeFragmentedHistoryReplay(t *testing.T) {
	p, messages, tools := fragmentedHistoryFixture(t)
	p.cfg.Budget.Version = 3
	_, ctx, _, _ := currentInputFixture(t)
	if _, err := p.fitTailTo(ctx, messages[:1], messages[1:], tools, p.cfg.Budget.InputTokenLimit); err == nil {
		t.Fatal("historical version 3 fitting behavior changed")
	}
}
