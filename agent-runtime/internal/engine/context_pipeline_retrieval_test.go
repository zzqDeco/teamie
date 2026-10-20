package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

func TestContextBoundedRetrievalWrapsKeep4096And8192BytePages(t *testing.T) {
	for _, name := range []string{"context_read", "context_search", "workspace_read_page"} {
		for _, size := range []int{4096, 8192} {
			t.Run(fmt.Sprintf("%s/%d", name, size), func(t *testing.T) {
				pipeline, archive, _ := testPipeline(t, 16384)
				archive.fail = true
				ctx := context.Background()
				tc := &adk.ToolContext{Name: name, CallID: "bounded-retrieval"}
				body := "界" + strings.Repeat("p", size-len("界"))
				raw, _ := json.Marshal(map[string]any{"text": body, "nextOffset": size, "hasMore": true})
				page := string(raw)
				if len(raw) <= pipeline.cfg.Budget.ToolResultByteLimit {
					t.Fatal("fixture does not cross ordinary result reduction limit")
				}
				wrapped, err := pipeline.WrapInvokableToolCall(ctx, func(context.Context, string, ...tool.Option) (string, error) { return page, nil }, tc)
				if err != nil {
					t.Fatal(err)
				}
				result, err := wrapped(ctx, `{"offset":0}`)
				if err != nil || result != page {
					t.Fatal("bounded invokable page was rearchived/truncated", err)
				}
				enhanced, err := pipeline.WrapEnhancedInvokableToolCall(ctx, func(context.Context, *schema.ToolArgument, ...tool.Option) (*schema.ToolResult, error) {
					return textToolResult(page), nil
				}, tc)
				if err != nil {
					t.Fatal(err)
				}
				structured, err := enhanced(ctx, &schema.ToolArgument{Text: `{"offset":0}`})
				if err != nil || toolResultText(structured) != page {
					t.Fatal("bounded enhanced page was rearchived/truncated", err)
				}
				first := utf8Prefix(page, len(page)/2)
				last := page[len(first):]
				stream, err := pipeline.WrapStreamableToolCall(ctx, func(context.Context, string, ...tool.Option) (*schema.StreamReader[string], error) {
					return schema.StreamReaderFromArray([]string{first, last}), nil
				}, tc)
				if err != nil {
					t.Fatal(err)
				}
				reader, err := stream(ctx, `{"offset":0}`)
				if err != nil {
					t.Fatal(err)
				}
				var output strings.Builder
				for {
					part, recvErr := reader.Recv()
					if recvErr == io.EOF {
						break
					}
					if recvErr != nil {
						t.Fatal(recvErr)
					}
					output.WriteString(part)
				}
				reader.Close()
				if output.String() != page {
					t.Fatal("bounded stream page changed")
				}
				enhancedStream, err := pipeline.WrapEnhancedStreamableToolCall(ctx, func(context.Context, *schema.ToolArgument, ...tool.Option) (*schema.StreamReader[*schema.ToolResult], error) {
					return schema.StreamReaderFromArray([]*schema.ToolResult{textToolResult(first), textToolResult(last)}), nil
				}, tc)
				if err != nil {
					t.Fatal(err)
				}
				structuredReader, err := enhancedStream(ctx, &schema.ToolArgument{Text: `{"offset":0}`})
				if err != nil {
					t.Fatal(err)
				}
				output.Reset()
				for {
					part, recvErr := structuredReader.Recv()
					if recvErr == io.EOF {
						break
					}
					if recvErr != nil {
						t.Fatal(recvErr)
					}
					output.WriteString(toolResultText(part))
				}
				structuredReader.Close()
				if output.String() != page {
					t.Fatal("bounded enhanced stream page changed")
				}
				if len(archive.content) != 0 {
					t.Fatal("retrieval created a recursive archive")
				}
			})
		}
	}
}

func TestContextBoundedRetrievalClearKeepsPrivateArchiveObservations(t *testing.T) {
	pipeline, archive, _ := testPipeline(t, 16384)
	archive.fail = true
	ctx := context.Background()
	page := strings.Repeat("page data ", 1000)
	state := &adk.TypedChatModelAgentState[*schema.AgenticMessage]{Messages: []*schema.AgenticMessage{schema.SystemAgenticMessage(strings.Repeat("policy ", 6000))}}
	for i, name := range []string{"context_read", "context_search", "lookup", "lookup"} {
		round := contextToolRound(fmt.Sprint(i), page)
		for _, msg := range round {
			for _, block := range msg.ContentBlocks {
				if block.FunctionToolCall != nil {
					block.FunctionToolCall.Name = name
				}
				if block.FunctionToolResult != nil {
					block.FunctionToolResult.Name = name
				}
			}
		}
		state.Messages = append(state.Messages, round...)
	}
	reducer, err := pipeline.reducer(ctx, []string{"context_read", "context_search", "lookup"})
	if err != nil {
		t.Fatal(err)
	}
	_, after, err := reducer.BeforeModelRewriteState(ctx, state, nil)
	if err != nil {
		t.Fatal("private retrieval was cleared into another source artifact", err)
	}
	for _, msg := range after.Messages {
		for _, block := range msg.ContentBlocks {
			result := block.FunctionToolResult
			if result != nil && (result.Name == "context_read" || result.Name == "context_search") && result.Content[0].Text.Text != page {
				t.Fatal("private archive observation was cleared")
			}
		}
	}
	if len(archive.content) != 0 {
		t.Fatal("clear created a recursive private archive")
	}
}

func TestContextBoundedRetrievalStillHitsModelCapacityHardCheck(t *testing.T) {
	s, lease, run := contextModelRun(t)
	fixture := &overflowGatewayFixture{code: "context_overflow"}
	gateway := &GatewayModel{Store: s, Lease: lease, AgentID: lease.AgentID, Client: fixture}
	input := []*schema.AgenticMessage{schema.SystemAgenticMessage("policy")}
	for i := 0; i < 3; i++ {
		round := contextToolRound(fmt.Sprint(i), strings.Repeat("界", 8192/3))
		for _, msg := range round {
			for _, block := range msg.ContentBlocks {
				if block.FunctionToolCall != nil {
					block.FunctionToolCall.Name = "context_read"
				}
				if block.FunctionToolResult != nil {
					block.FunctionToolResult.Name = "context_read"
				}
			}
		}
		input = append(input, round...)
	}
	_, err := gateway.Generate(WithRun(context.Background(), run), input)
	var failure *ModelError
	if !errors.As(err, &failure) || failure.Code != "request_too_large" || fixture.calls.Load() != 0 || count(t, s, `SELECT count(*) FROM runtime_context_recoveries`) != 1 {
		t.Fatal("retrieval exclusion bypassed complete input capacity check", err)
	}
}
