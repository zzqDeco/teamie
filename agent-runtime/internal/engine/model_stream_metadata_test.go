package engine

import (
	"strings"
	"testing"

	"github.com/cloudwego/eino/schema"
)

func TestLiveStreamKeepsConstantRequestIDAndNativeTextDeltas(t *testing.T) {
	const requestID = "chatcmpl-production-header"
	var previous string
	chunks := make([]*schema.AgenticMessage, 512)
	for i := range chunks {
		original := &schema.AgenticMessage{
			Role: schema.AgenticRoleTypeAssistant,
			ContentBlocks: []*schema.ContentBlock{schema.NewContentBlockChunk(
				&schema.AssistantGenText{Text: "测"}, &schema.StreamingMeta{Index: 0},
			)},
			Extra: map[string]any{"openai-request-id": requestID, "other-delta": "."},
		}
		chunks[i] = streamRequestIDDelta(original, &previous)
		if original.Extra["openai-request-id"] != requestID {
			t.Fatal("adapter mutated the decoded transport message")
		}
	}
	result, err := schema.ConcatAgenticMessages(chunks)
	if err != nil {
		t.Fatal(err)
	}
	if result.Extra["openai-request-id"] != requestID {
		t.Fatalf("constant response header grew with chunks: %v", result.Extra["openai-request-id"])
	}
	if result.Extra["other-delta"] != strings.Repeat(".", len(chunks)) || result.ContentBlocks[0].AssistantGenText.Text != strings.Repeat("测", len(chunks)) {
		t.Fatal("normalizing the response header changed native text or other metadata deltas")
	}
}

func TestLiveStreamRetainsChangedAndAlreadyAggregatedRequestIDs(t *testing.T) {
	var previous string
	first := "opaque-headeropaque-header"
	values := []string{first, first, "another-header", "another-header"}
	chunks := make([]*schema.AgenticMessage, len(values))
	for i, value := range values {
		chunks[i] = streamRequestIDDelta(&schema.AgenticMessage{Role: schema.AgenticRoleTypeAssistant, Extra: map[string]any{"openai-request-id": value}}, &previous)
	}
	result, err := schema.ConcatAgenticMessages(chunks)
	if err != nil {
		t.Fatal(err)
	}
	if result.Extra["openai-request-id"] != first+"another-header" {
		t.Fatal("adapter rewrote an opaque header value or discarded a changed header")
	}
}
