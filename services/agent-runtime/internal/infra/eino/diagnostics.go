package eino

import (
	"context"
	"errors"
	"github.com/cloudwego/eino/schema"
	"strings"

	"go.uber.org/zap"
	"teamie/pkg/observability"
)

func runFields(run *Run) []zap.Field {
	if run == nil {
		return nil
	}
	return []zap.Field{zap.String("custom.agent.id", run.AgentID), zap.String("custom.runtime.session_id", "agent:"+run.AgentID), zap.String("custom.runtime.run_id", run.ID), zap.String("custom.runtime.attempt_id", run.Attempt)}
}

func modelDiagnostic(stage, reason string, cause error) *ModelError {
	return &ModelError{Code: "invalid_upstream_response", Retry: true, Stage: stage, Reason: reason, Cause: cause}
}

// Eino's concat errors can embed model-authored names/IDs/values. Classify the
// actual native cause without emitting those values, arguments or reasoning.
func concatDiagnostic(stage string, err error) *ModelError {
	var existing *ModelError
	if errors.As(err, &existing) && existing.Stage != "" {
		return existing
	}
	reason := "native_concat_failed"
	for _, test := range []struct{ match, reason string }{
		{"expected call ID", "tool_call_id_conflict"}, {"expected tool name", "tool_name_conflict"},
		{"different roles", "message_role_conflict"}, {"non-streaming block after streaming", "mixed_streaming_blocks"},
		{"streaming block after non-streaming", "mixed_streaming_blocks"}, {"inconsistent extension types", "response_extension_type_conflict"},
		{"failed to concat extensions", "response_extension_concat_failed"}, {"different types", "block_or_metadata_type_conflict"},
		{"is nil", "nil_stream_chunk"}, {"content block type", "content_block_type_conflict"},
	} {
		if strings.Contains(err.Error(), test.match) {
			reason = test.reason
			break
		}
	}
	return modelDiagnostic(stage, reason, err)
}

func modelCompletion(op *observability.Operation, err error, stage string, run *Run, step int, fields ...zap.Field) {
	fields = append(fields, zap.String("custom.model.failure_stage", stage), zap.Int("custom.model.step", step), zap.String("custom.duration_scope", "model-slot-wait-through-durable-receipt"), zap.Bool("custom.model.receipt_replay", stage == "receipt-replay"))
	if err != nil {
		fields = append(fields, zap.String("custom.error.code", SafeError(err)))
		var model *ModelError
		if errors.As(err, &model) && model.Stage != "" {
			fields = append(fields, zap.String("custom.model.failure_reason", model.Reason), zap.String("custom.model.error_stage", model.Stage))
			if model.Reason == "block_index_count_mismatch" {
				fields = append(fields, zap.Int("custom.model.expected_block_count", model.ExpectedBlocks), zap.Int("custom.model.actual_block_count", model.ActualBlocks))
			}
		}
	}
	op.End(err, fields...)
}

func toolOperation(ctx context.Context, name, callID string) (context.Context, *observability.Operation) {
	ctx, op := observability.Begin(ctx, "agent-tool", zap.String("custom.tool.name", observability.ID(name)), zap.String("custom.tool.call_id", observability.ID(callID)))
	observability.L(ctx).Info("Agent tool started", zap.String("event.action", "agent-tool-started"))
	return ctx, op
}

func failureCode(err error) string {
	if err == nil {
		return ""
	}
	return SafeError(err)
}

// Summarize structure only, once at completion. Never retain chunk text, tool
// arguments, function names or model reasoning in diagnostic state.
type streamShape struct {
	reasoning, text, tool, unsupported int
	indices                            map[int]bool
	promptTokens, completionTokens     int
}

func (s *streamShape) observe(message *schema.AgenticMessage) {
	if s.indices == nil {
		s.indices = map[int]bool{}
	}
	for _, block := range message.ContentBlocks {
		if block == nil {
			s.unsupported++
			continue
		}
		switch block.Type {
		case schema.ContentBlockTypeReasoning:
			s.reasoning++
		case schema.ContentBlockTypeAssistantGenText:
			s.text++
		case schema.ContentBlockTypeFunctionToolCall:
			s.tool++
		default:
			s.unsupported++
		}
		if block.StreamingMeta != nil && len(s.indices) < 256 {
			s.indices[block.StreamingMeta.Index] = true
		}
	}
	if message.ResponseMeta != nil && message.ResponseMeta.TokenUsage != nil {
		s.promptTokens = message.ResponseMeta.TokenUsage.PromptTokens
		s.completionTokens = message.ResponseMeta.TokenUsage.CompletionTokens
	}
}
func (s *streamShape) fields() []zap.Field {
	return []zap.Field{zap.Int("custom.model.stream.reasoning_fragments", s.reasoning), zap.Int("custom.model.stream.text_fragments", s.text), zap.Int("custom.model.stream.tool_fragments", s.tool), zap.Int("custom.model.stream.unsupported_fragments", s.unsupported), zap.Int("custom.model.stream.distinct_indices_capped", len(s.indices)), zap.Int("custom.model.prompt_tokens", s.promptTokens), zap.Int("custom.model.completion_tokens", s.completionTokens)}
}
