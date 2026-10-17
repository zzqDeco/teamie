package engine

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"unicode/utf8"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/adk/middlewares/reduction"
	"github.com/cloudwego/eino/schema"
)

func utf8Prefix(text string, n int) string {
	if len(text) <= n {
		return text
	}
	for n > 0 && !utf8.RuneStart(text[n]) {
		n--
	}
	return text[:n]
}

func contextReadNotice(ref string) string {
	return "Archived private context: " + ref + ". Read with context_read({\"ref\":\"" + ref + "\",\"offset\":0,\"limit\":4096}); offset and nextOffset are UTF-8 byte offsets. Current source access is checked on every read."
}

func toolResultText(result *schema.ToolResult) string {
	if result == nil {
		return ""
	}
	var b strings.Builder
	for _, part := range result.Parts {
		if part.Type == schema.ToolPartTypeText {
			b.WriteString(part.Text)
		}
	}
	return b.String()
}

func textToolResult(text string) *schema.ToolResult {
	return &schema.ToolResult{Parts: []schema.ToolOutputPart{{Type: schema.ToolPartTypeText, Text: text}}}
}

func (p *ContextPipeline) archiveTool(ctx context.Context, kind string, detail *reduction.ToolDetail, result *schema.ToolResult) (string, string, error) {
	if detail.ToolContext == nil || detail.ToolContext.CallID == "" || detail.ToolContext.Name == "" {
		return "", "", errors.New("tool archive has no identity")
	}
	raw, err := json.Marshal(struct {
		Name     string               `json:"name"`
		CallID   string               `json:"callId"`
		Argument *schema.ToolArgument `json:"argument"`
		Result   *schema.ToolResult   `json:"result"`
	}{detail.ToolContext.Name, detail.ToolContext.CallID, detail.ToolArgument, result})
	if err != nil {
		return "", "", err
	}
	key := detail.ToolContext.Name + ":" + detail.ToolContext.CallID + ":" + digest(raw)
	if p.cfg.ArchiveToolPath != nil {
		ref, err := p.cfg.ArchiveToolPath(ctx, kind, detail, string(raw))
		return ref, string(raw), err
	}
	ref, err := p.cfg.ArchivePath(ctx, kind, key, string(raw))
	return ref, string(raw), err
}

func (p *ContextPipeline) truncateTool(ctx context.Context, detail *reduction.ToolDetail) (*reduction.TruncResult, error) {
	result := detail.ToolResult
	streaming := detail.StreamToolResult != nil
	if streaming {
		defer detail.StreamToolResult.Close()
		chunks := []*schema.ToolResult{}
		total := 0
		for {
			chunk, err := detail.StreamToolResult.Recv()
			if err == io.EOF {
				break
			}
			if err != nil {
				return nil, err
			}
			if chunk == nil {
				continue
			}
			raw, err := json.Marshal(chunk)
			if err != nil {
				return nil, err
			}
			total += len(raw)
			if total > 2<<20 {
				return nil, &ModelError{Code: "tool_response_limit"}
			}
			chunks = append(chunks, chunk)
		}
		var err error
		if len(chunks) > 0 {
			result, err = schema.ConcatToolResults(chunks)
			if err != nil {
				return nil, err
			}
		}
	}
	raw, err := json.Marshal(result)
	if err != nil {
		return nil, err
	}
	if len(raw) <= p.cfg.Budget.ToolResultByteLimit {
		return &reduction.TruncResult{}, nil
	}
	ref, content, err := p.archiveTool(ctx, "tool_trunc", detail, result)
	if err != nil {
		return nil, err
	}
	preview := utf8Prefix(toolResultText(result), min(1024, p.cfg.Budget.ToolResultByteLimit/4))
	reduced := textToolResult(contextReadNotice(ref) + "\nPreview (quoted tool data):\n" + preview)
	reduced.Parts[0].Extra = map[string]any{"teamie_context_archive_ref": ref}
	out := &reduction.TruncResult{NeedTrunc: true, ToolResult: reduced, NeedOffload: true, OffloadFilePath: ref, OffloadContent: content}
	if streaming {
		out.StreamToolResult = schema.StreamReaderFromArray([]*schema.ToolResult{reduced})
	}
	return out, nil
}

func (p *ContextPipeline) clearTool(ctx context.Context, detail *reduction.ToolDetail) (*reduction.ClearResult, error) {
	// An already reduced result still points to its complete immutable receipt.
	// Do not create a chain of archives containing only another archive notice.
	if strings.HasPrefix(toolResultText(detail.ToolResult), "Archived private context: context:v1:") {
		return &reduction.ClearResult{}, nil
	}
	raw, err := json.Marshal(detail.ToolResult)
	if err != nil {
		return nil, err
	}
	if len(raw) < 768 {
		return &reduction.ClearResult{}, nil
	}
	ref, content, err := p.archiveTool(ctx, "tool_clear", detail, detail.ToolResult)
	if err != nil {
		return nil, err
	}
	reduced := textToolResult(contextReadNotice(ref))
	reduced.Parts[0].Extra = map[string]any{"teamie_context_archive_ref": ref}
	return &reduction.ClearResult{NeedClear: true, ToolArgument: detail.ToolArgument, ToolResult: reduced, NeedOffload: true, OffloadFilePath: ref, OffloadContent: content}, nil
}

// .35 exposes custom reduction handlers per tool, so construct the native
// reducer with the exact names at each wrapper/model hook. No mutable name map
// is shared between parallel SDK tool wrapper construction.
func (p *ContextPipeline) reducer(ctx context.Context, names []string) (adk.TypedChatModelAgentMiddleware[*schema.AgenticMessage], error) {
	config := map[string]*reduction.ToolReductionConfig{}
	for _, name := range names {
		entry := &reduction.ToolReductionConfig{Backend: p.cfg.Backend, TruncHandler: p.truncateTool, ClearHandler: p.clearTool}
		if name == "context_read" || name == "context_search" {
			entry.SkipClear = true
			entry.ClearHandler = nil
		}
		config[name] = entry
	}
	return reduction.NewTyped(ctx, &reduction.TypedConfig[*schema.AgenticMessage]{Backend: p.cfg.Backend, ReadFileToolName: "context_read", TruncExcludeTools: []string{"context_read", "context_search", "workspace_read_page"}, MaxLengthForTrunc: p.cfg.Budget.ToolResultByteLimit, MaxTokensForClear: int64(p.cfg.Budget.TriggerTokens), ClearRetentionSuffixLimit: 2, ClearAtLeastTokens: 1, ToolConfig: config, TokenCounter: func(_ context.Context, msgs []*schema.AgenticMessage, tools []*schema.ToolInfo) (int64, error) {
		m, err := measureContext(msgs, tools, p.cfg.Budget.OutputReserveTokens, false)
		if m.TransportBytes > p.cfg.Budget.TransportTargetBytes {
			return int64(max(m.EstimatedTokens, p.cfg.Budget.TriggerTokens)), err
		}
		return int64(m.EstimatedTokens), err
	}})
}

func (p *ContextPipeline) WrapInvokableToolCall(ctx context.Context, endpoint adk.InvokableToolCallEndpoint, tc *adk.ToolContext) (adk.InvokableToolCallEndpoint, error) {
	r, err := p.reducer(ctx, []string{tc.Name})
	if err != nil {
		return nil, err
	}
	return r.WrapInvokableToolCall(ctx, endpoint, tc)
}
func (p *ContextPipeline) WrapStreamableToolCall(ctx context.Context, endpoint adk.StreamableToolCallEndpoint, tc *adk.ToolContext) (adk.StreamableToolCallEndpoint, error) {
	r, err := p.reducer(ctx, []string{tc.Name})
	if err != nil {
		return nil, err
	}
	return r.WrapStreamableToolCall(ctx, endpoint, tc)
}
func (p *ContextPipeline) WrapEnhancedInvokableToolCall(ctx context.Context, endpoint adk.EnhancedInvokableToolCallEndpoint, tc *adk.ToolContext) (adk.EnhancedInvokableToolCallEndpoint, error) {
	r, err := p.reducer(ctx, []string{tc.Name})
	if err != nil {
		return nil, err
	}
	return r.WrapEnhancedInvokableToolCall(ctx, endpoint, tc)
}
func (p *ContextPipeline) WrapEnhancedStreamableToolCall(ctx context.Context, endpoint adk.EnhancedStreamableToolCallEndpoint, tc *adk.ToolContext) (adk.EnhancedStreamableToolCallEndpoint, error) {
	r, err := p.reducer(ctx, []string{tc.Name})
	if err != nil {
		return nil, err
	}
	return r.WrapEnhancedStreamableToolCall(ctx, endpoint, tc)
}
