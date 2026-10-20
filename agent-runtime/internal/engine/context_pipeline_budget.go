package engine

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"unicode/utf8"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	"go.uber.org/zap"
	"teamie/agent-protocol/modeltransport"
)

const contextBudgetVersion = 7
const contextCounterVersion = "teamie-visible-estimate-v1"
const contextCounterVersionV7 = "teamie-visible-bytes-div4-v1"
const contextTransportLimit = 262144

// ContextBudgetSnapshot is immutable Run policy, not a wall-clock work budget.
// Keeping its complete values prevents a gateway configuration change from
// moving a replay's summary/model steps or changing its output options.
type ContextBudgetSnapshot struct {
	Version              int    `json:"version"`
	CounterVersion       string `json:"counterVersion"`
	ContextWindowTokens  int    `json:"contextWindowTokens"`
	InputTokenLimit      int    `json:"inputTokenLimit"`
	TriggerTokens        int    `json:"triggerTokens"`
	OutputReserveTokens  int    `json:"outputReserveTokens"`
	SummaryOutputTokens  int    `json:"summaryOutputTokens"`
	TransportLimitBytes  int    `json:"transportLimitBytes"`
	TransportTargetBytes int    `json:"transportTargetBytes"`
	TailTokenLimit       int    `json:"tailTokenLimit"`
	ToolResultByteLimit  int    `json:"toolResultByteLimit"`
	FileDelivery         bool   `json:"fileDelivery,omitempty"`
	StructuredMentions   bool   `json:"structuredMentions,omitempty"`
	EventDrivenInputs    bool   `json:"eventDrivenInputs,omitempty"`
}

func makeContextBudgetSnapshot(window int) (ContextBudgetSnapshot, error) {
	return makeContextBudgetSnapshotVersion(window, contextBudgetVersion)
}

func makeContextBudgetSnapshotVersion(window, version int) (ContextBudgetSnapshot, error) {
	if window < 4096 || window > 2_000_000 {
		return ContextBudgetSnapshot{}, errors.New("invalid context window")
	}
	reserve := min(2048, window/4)
	if version == 2 {
		reserve = min(8192, window/4)
	} else if version == 3 || version == 4 || version == 5 {
		reserve = min(4096, window/4)
	} else if version == 6 || version == 7 {
		reserve = min(modeltransport.MaxOutputTokens, window/4)
	} else if version != 1 {
		return ContextBudgetSnapshot{}, errors.New("unsupported context budget version")
	}
	input := window - reserve - 256
	budget := ContextBudgetSnapshot{
		Version: version, CounterVersion: contextCounterVersion,
		ContextWindowTokens: window, InputTokenLimit: input, TriggerTokens: input * 3 / 4,
		OutputReserveTokens: reserve, SummaryOutputTokens: min(2048, reserve),
		TransportLimitBytes: contextTransportLimit, TransportTargetBytes: contextTransportLimit * 3 / 4,
		TailTokenLimit: min(6000, input/3), ToolResultByteLimit: min(16384, max(1024, input/4)),
	}
	if version >= 6 {
		budget.TransportLimitBytes = modeltransport.MaxRequestBytes
		budget.TransportTargetBytes = budget.TransportLimitBytes * 3 / 4
		budget.SummaryOutputTokens = min(reserve, min(8192, max(2048, input/32)))
		budget.TailTokenLimit = min(262144, input/3)
		budget.ToolResultByteLimit = min(262144, max(1024, input/4))
	}
	if version >= 7 {
		budget.CounterVersion = contextCounterVersionV7
		budget.TriggerTokens = (input * 9) / 10
	}
	return budget, nil
}

func NewContextBudgetSnapshot(window int) (json.RawMessage, error) {
	return newContextBudgetWithFiles(window, false)
}

func newContextBudgetWithFiles(window int, fileDelivery bool, mentions ...bool) (json.RawMessage, error) {
	budget, err := makeContextBudgetSnapshot(window)
	if err != nil {
		return nil, err
	}
	budget.FileDelivery = fileDelivery
	budget.StructuredMentions = len(mentions) > 0 && mentions[0]
	return json.Marshal(budget)
}

func ParseContextBudgetSnapshot(raw json.RawMessage) (ContextBudgetSnapshot, error) {
	var budget ContextBudgetSnapshot
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&budget) != nil || decoder.Decode(new(any)) != io.EOF {
		return budget, errors.New("invalid context budget snapshot")
	}
	expected, err := makeContextBudgetSnapshotVersion(budget.ContextWindowTokens, budget.Version)
	expected.FileDelivery = budget.FileDelivery
	expected.StructuredMentions = budget.StructuredMentions
	expected.EventDrivenInputs = budget.EventDrivenInputs
	if err != nil || budget != expected {
		return budget, errors.New("unsupported context budget snapshot")
	}
	return budget, nil
}

func (b ContextBudgetSnapshot) initialOutputTokens() int {
	if b.Version >= 6 {
		return min(b.OutputReserveTokens, max(2048, min(32768, b.OutputReserveTokens/2)))
	}
	return min(2048, b.OutputReserveTokens)
}

func (b ContextBudgetSnapshot) measureContext(messages []*schema.AgenticMessage, tools []*schema.ToolInfo, output int, streaming bool) (ContextMeasurement, error) {
	if b.Version >= 7 {
		return measureContext(messages, tools, output, streaming, modeltransport.MaxOutputTokens)
	}
	if b.Version == 6 {
		return measureLegacyContext(messages, tools, output, streaming, modeltransport.MaxOutputTokens)
	}
	if b.Version > 0 {
		return measureLegacyContext(messages, tools, output, streaming)
	}
	return measureContext(messages, tools, output, streaming)
}

func (b ContextBudgetSnapshot) measureSummaryContext(messages []*schema.AgenticMessage, output int, streaming bool) (ContextMeasurement, error) {
	if b.Version > 0 && b.Version < 7 {
		return b.measureContext(messages, nil, output, streaming)
	}
	opts := &model.Options{MaxTokens: &output, AgenticToolChoice: &schema.AgenticToolChoice{Type: schema.ToolChoiceForbidden}}
	request, err := agenticRequest(messages, opts, streaming, modeltransport.MaxOutputTokens)
	if err != nil {
		return ContextMeasurement{}, err
	}
	return measureContextRequest(request)
}

func (b ContextBudgetSnapshot) measureContextRequest(request []byte) (ContextMeasurement, error) {
	if b.Version > 0 && b.Version < 7 {
		return measureLegacyContextRequest(request)
	}
	return measureContextRequest(request)
}

type contextUsageAnchor struct {
	Step         int
	MessageCount int
	PrefixHash   string
	LocalTokens  int
	PromptTokens int
}

func isSummaryModelOptions(opts *model.Options) bool {
	return opts != nil && len(opts.Tools) == 0 && len(opts.DeferredTools) == 0 &&
		opts.AgenticToolChoice != nil && opts.AgenticToolChoice.Type == schema.ToolChoiceForbidden
}

func (b ContextBudgetSnapshot) measureCalibratedContext(anchor *contextUsageAnchor, messages []*schema.AgenticMessage, tools []*schema.ToolInfo, output int, streaming bool) (ContextMeasurement, error) {
	base, err := b.measureContext(messages, tools, output, streaming)
	if err != nil || b.Version < 7 || anchor == nil || anchor.PromptTokens <= 0 || anchor.MessageCount <= 0 || len(messages) < anchor.MessageCount {
		return base, err
	}
	_, prefixHash, err := visibleContextBytes(messages[:anchor.MessageCount], tools, nil, nil, &schema.AgenticToolChoice{Type: schema.ToolChoiceAllowed})
	if err != nil || prefixHash != anchor.PrefixHash {
		return base, nil
	}
	effective := max(1, anchor.PromptTokens+base.LocalTokens-anchor.LocalTokens)
	return ContextMeasurement{
		EstimatedTokens:    effective,
		LocalTokens:        base.LocalTokens,
		AnchorPromptTokens: anchor.PromptTokens,
		AnchorLocalTokens:  anchor.LocalTokens,
		AnchorStep:         anchor.Step,
		CounterMode:        "usage_delta",
		TransportBytes:     base.TransportBytes,
	}, nil
}

func (b ContextBudgetSnapshot) measureCalibratedRequest(anchor *contextUsageAnchor, request []byte) (ContextMeasurement, error) {
	base, err := b.measureContextRequest(request)
	if err != nil || b.Version < 7 || anchor == nil || anchor.PromptTokens <= 0 || anchor.MessageCount <= 0 {
		return base, err
	}
	var envelope modeltransport.Request
	var messages []*schema.AgenticMessage
	var opts model.Options
	if json.Unmarshal(request, &envelope) != nil || (&schema.HumanReadableSerializer{}).Unmarshal(envelope.Messages, &messages) != nil || json.Unmarshal(envelope.Options, &opts) != nil {
		return base, nil
	}
	if isSummaryModelOptions(&opts) || len(messages) < anchor.MessageCount {
		return base, nil
	}
	_, prefixHash, err := visibleContextBytes(messages[:anchor.MessageCount], opts.Tools, opts.DeferredTools, opts.Stop, opts.AgenticToolChoice)
	if err != nil || prefixHash != anchor.PrefixHash {
		return base, nil
	}
	effective := max(1, anchor.PromptTokens+base.LocalTokens-anchor.LocalTokens)
	return ContextMeasurement{
		EstimatedTokens:    effective,
		LocalTokens:        base.LocalTokens,
		AnchorPromptTokens: anchor.PromptTokens,
		AnchorLocalTokens:  anchor.LocalTokens,
		AnchorStep:         anchor.Step,
		CounterMode:        "usage_delta",
		TransportBytes:     base.TransportBytes,
	}, nil
}

type ContextMeasurement struct {
	EstimatedTokens    int    `json:"estimatedTokens"`
	LocalTokens        int    `json:"localTokens,omitempty"`
	AnchorPromptTokens int    `json:"anchorPromptTokens,omitempty"`
	AnchorLocalTokens  int    `json:"anchorLocalTokens,omitempty"`
	AnchorStep         int    `json:"anchorStep,omitempty"`
	CounterMode        string `json:"counterMode,omitempty"`
	TransportBytes     int    `json:"transportBytes"`
}

func (m ContextMeasurement) localTokens() int {
	if m.LocalTokens > 0 {
		return m.LocalTokens
	}
	return m.EstimatedTokens
}

func (m ContextMeasurement) counterMode() string {
	if m.CounterMode != "" {
		return m.CounterMode
	}
	return "legacy"
}

func (b ContextBudgetSnapshot) compactionReason(m ContextMeasurement, forced bool) string {
	if forced {
		return "provider_context_overflow"
	}
	tokenTriggered := m.EstimatedTokens > b.TriggerTokens
	byteTriggered := m.TransportBytes > b.TransportTargetBytes
	switch {
	case tokenTriggered && byteTriggered:
		return "both"
	case tokenTriggered:
		return "token_threshold"
	case byteTriggered:
		return "byte_threshold"
	default:
		return ""
	}
}

func (b ContextBudgetSnapshot) measurementLogFields(m ContextMeasurement, compactionReason string) []zap.Field {
	fields := []zap.Field{
		zap.String("custom.context.counter_version", b.CounterVersion),
		zap.String("custom.context.counter_mode", m.counterMode()),
		zap.Int("custom.context.local_tokens", m.localTokens()),
		zap.Int("custom.context.effective_tokens", m.EstimatedTokens),
		zap.Int("custom.context.anchor_prompt_tokens", m.AnchorPromptTokens),
		zap.Int("custom.context.anchor_local_tokens", m.AnchorLocalTokens),
		zap.Int("custom.context.anchor_step", m.AnchorStep),
		zap.Int("custom.context.trigger_tokens", b.TriggerTokens),
		zap.Int("custom.context.input_token_limit", b.InputTokenLimit),
		zap.Int("custom.context.transport_bytes", m.TransportBytes),
	}
	if compactionReason != "" {
		fields = append(fields, zap.String("custom.context.compaction_reason", compactionReason))
	}
	return fields
}

// This is a conservative, versioned text estimate for frozen v1-v6 Runs.
func estimateContextTokens(text []byte) int {
	ascii, nonASCII := 0, 0
	for len(text) > 0 {
		r, size := utf8.DecodeRune(text)
		if r < 128 {
			ascii++
		} else {
			nonASCII++
		}
		text = text[size:]
	}
	return (ascii+2)/3 + nonASCII*2
}

func estimateVisibleTokensV7(visibleBytes int) int {
	if visibleBytes <= 0 {
		return 0
	}
	return (visibleBytes + 3) / 4
}

func visibleContextBytes(messages []*schema.AgenticMessage, tools, deferred []*schema.ToolInfo, stop []string, choice *schema.AgenticToolChoice) (int, string, error) {
	hasher := sha256.New()
	writePart := func(tag string, value string) int {
		_, _ = io.WriteString(hasher, tag)
		_, _ = io.WriteString(hasher, ":")
		_, _ = io.WriteString(hasher, value)
		_, _ = io.WriteString(hasher, "\x00")
		return len(value)
	}
	total := 0
	for _, message := range messages {
		if message == nil {
			continue
		}
		total += 16 + writePart("role", string(message.Role))
		for _, block := range message.ContentBlocks {
			if block == nil {
				continue
			}
			_ = writePart("block", string(block.Type))
			if block.UserInputText != nil {
				total += writePart("user_text", block.UserInputText.Text)
			}
			if block.AssistantGenText != nil {
				total += writePart("assistant_text", block.AssistantGenText.Text)
			}
			if block.Reasoning != nil {
				total += writePart("reasoning", block.Reasoning.Text)
			}
			if block.FunctionToolCall != nil {
				total += 16
				total += writePart("call_id", block.FunctionToolCall.CallID)
				total += writePart("call_name", block.FunctionToolCall.Name)
				total += writePart("call_args", block.FunctionToolCall.Arguments)
			}
			if block.FunctionToolResult != nil {
				total += 16
				total += writePart("result_id", block.FunctionToolResult.CallID)
				total += writePart("result_name", block.FunctionToolResult.Name)
				for _, part := range block.FunctionToolResult.Content {
					if part == nil {
						continue
					}
					_ = writePart("part_type", string(part.Type))
					if part.Text != nil {
						total += writePart("result_text", part.Text.Text)
					}
				}
			}
		}
	}
	for _, info := range append(append([]*schema.ToolInfo{}, tools...), deferred...) {
		if info == nil {
			continue
		}
		total += 16
		total += writePart("tool_name", info.Name)
		total += writePart("tool_desc", info.Desc)
		if info.ParamsOneOf != nil {
			params, err := info.ParamsOneOf.ToJSONSchema()
			if err != nil {
				return 0, "", err
			}
			rawParams, err := json.Marshal(params)
			if err != nil {
				return 0, "", err
			}
			total += writePart("tool_params", string(rawParams))
		}
	}
	for _, s := range stop {
		total += 4 + writePart("stop", s)
	}
	if choice != nil {
		rawChoice, err := json.Marshal(choice)
		if err != nil {
			return 0, "", err
		}
		total += 8 + writePart("choice", string(rawChoice))
	}
	return total, hex.EncodeToString(hasher.Sum(nil)), nil
}

func measureLegacyContext(messages []*schema.AgenticMessage, tools []*schema.ToolInfo, output int, streaming bool, outputLimits ...int) (ContextMeasurement, error) {
	opts := &model.Options{MaxTokens: &output, Tools: tools, AgenticToolChoice: &schema.AgenticToolChoice{Type: schema.ToolChoiceAllowed}}
	request, err := agenticRequest(messages, opts, streaming, outputLimits...)
	if err != nil {
		return ContextMeasurement{}, err
	}
	return measureLegacyContextRequest(request)
}

func measureContext(messages []*schema.AgenticMessage, tools []*schema.ToolInfo, output int, streaming bool, outputLimits ...int) (ContextMeasurement, error) {
	opts := &model.Options{MaxTokens: &output, Tools: tools, AgenticToolChoice: &schema.AgenticToolChoice{Type: schema.ToolChoiceAllowed}}
	request, err := agenticRequest(messages, opts, streaming, outputLimits...)
	if err != nil {
		return ContextMeasurement{}, err
	}
	return measureContextRequest(request)
}

// Transport capacity counts the complete lossless native wire. Token capacity
// counts provider-visible contents and function schemas, excluding Session IDs,
// usage, archive/ACL metadata and serializer type annotations. Previous reported
// TotalTokens must never become a baseline after rewriting the input.
func measureContextRequest(request []byte) (ContextMeasurement, error) {
	var envelope modeltransport.Request
	var messages []*schema.AgenticMessage
	var opts model.Options
	if json.Unmarshal(request, &envelope) != nil || (&schema.HumanReadableSerializer{}).Unmarshal(envelope.Messages, &messages) != nil || json.Unmarshal(envelope.Options, &opts) != nil {
		return ContextMeasurement{}, errors.New("invalid context measurement input")
	}
	visibleBytes, _, err := visibleContextBytes(messages, opts.Tools, opts.DeferredTools, opts.Stop, opts.AgenticToolChoice)
	if err != nil {
		return ContextMeasurement{}, err
	}
	tokens := estimateVisibleTokensV7(visibleBytes)
	return ContextMeasurement{EstimatedTokens: tokens, LocalTokens: tokens, CounterMode: "local", TransportBytes: len(request)}, nil
}

func measureLegacyContextRequest(request []byte) (ContextMeasurement, error) {
	var envelope modeltransport.Request
	var messages []*schema.AgenticMessage
	var opts model.Options
	if json.Unmarshal(request, &envelope) != nil || (&schema.HumanReadableSerializer{}).Unmarshal(envelope.Messages, &messages) != nil || json.Unmarshal(envelope.Options, &opts) != nil {
		return ContextMeasurement{}, errors.New("invalid context measurement input")
	}
	visible := make([]*schema.AgenticMessage, 0, len(messages))
	for _, message := range messages {
		if message == nil {
			continue
		}
		copy := *message
		copy.Extra = nil
		copy.ResponseMeta = nil
		visible = append(visible, &copy)
	}
	tools := []map[string]any{}
	for _, info := range append(append([]*schema.ToolInfo{}, opts.Tools...), opts.DeferredTools...) {
		if info == nil {
			continue
		}
		entry := map[string]any{"name": info.Name, "description": info.Desc}
		if info.ParamsOneOf != nil {
			params, err := info.ParamsOneOf.ToJSONSchema()
			if err != nil {
				return ContextMeasurement{}, err
			}
			entry["parameters"] = params
		}
		tools = append(tools, entry)
	}
	raw, err := json.Marshal(struct {
		Messages []*schema.AgenticMessage  `json:"messages"`
		Tools    []map[string]any          `json:"tools"`
		Stop     []string                  `json:"stop,omitempty"`
		Choice   *schema.AgenticToolChoice `json:"toolChoice,omitempty"`
	}{visible, tools, opts.Stop, opts.AgenticToolChoice})
	if err != nil {
		return ContextMeasurement{}, err
	}
	return ContextMeasurement{EstimatedTokens: estimateContextTokens(raw), TransportBytes: len(request)}, nil
}

func (b ContextBudgetSnapshot) fits(measurement ContextMeasurement) bool {
	return measurement.EstimatedTokens <= b.InputTokenLimit && measurement.TransportBytes <= b.TransportLimitBytes
}

func (b ContextBudgetSnapshot) shouldCompact(measurement ContextMeasurement) bool {
	return measurement.EstimatedTokens > b.TriggerTokens || measurement.TransportBytes > b.TransportTargetBytes
}
