package engine

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"unicode/utf8"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

const contextBudgetVersion = 1
const contextCounterVersion = "teamie-wire-estimate-v1"
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
}

func makeContextBudgetSnapshot(window int) (ContextBudgetSnapshot, error) {
	if window < 4096 || window > 2_000_000 {
		return ContextBudgetSnapshot{}, errors.New("invalid context window")
	}
	reserve := min(2048, window/4)
	input := window - reserve - 256
	return ContextBudgetSnapshot{
		Version: contextBudgetVersion, CounterVersion: contextCounterVersion,
		ContextWindowTokens: window, InputTokenLimit: input, TriggerTokens: input * 3 / 4,
		OutputReserveTokens: reserve, SummaryOutputTokens: reserve,
		TransportLimitBytes: contextTransportLimit, TransportTargetBytes: contextTransportLimit * 3 / 4,
		TailTokenLimit: min(6000, input/3), ToolResultByteLimit: min(16384, max(1024, input/4)),
	}, nil
}

func NewContextBudgetSnapshot(window int) (json.RawMessage, error) {
	budget, err := makeContextBudgetSnapshot(window)
	if err != nil {
		return nil, err
	}
	return json.Marshal(budget)
}

func ParseContextBudgetSnapshot(raw json.RawMessage) (ContextBudgetSnapshot, error) {
	var budget ContextBudgetSnapshot
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&budget) != nil || decoder.Decode(new(any)) != io.EOF {
		return budget, errors.New("invalid context budget snapshot")
	}
	expected, err := makeContextBudgetSnapshot(budget.ContextWindowTokens)
	if err != nil || budget != expected {
		return budget, errors.New("unsupported context budget snapshot")
	}
	return budget, nil
}

type ContextMeasurement struct {
	EstimatedTokens int `json:"estimatedTokens"`
	TransportBytes  int `json:"transportBytes"`
}

// Do not reuse a previous assistant's TotalTokens after rewriting its input.
// Count the complete current wire (including tools and escapes) consistently.
// This is a conservative, versioned estimate, not a provider tokenizer claim.
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

func measureContext(messages []*schema.AgenticMessage, tools []*schema.ToolInfo, output int, streaming bool) (ContextMeasurement, error) {
	opts := &model.Options{MaxTokens: &output, Tools: tools, AgenticToolChoice: &schema.AgenticToolChoice{Type: schema.ToolChoiceAllowed}}
	request, err := agenticRequest(messages, opts, streaming)
	if err != nil {
		return ContextMeasurement{}, err
	}
	return ContextMeasurement{EstimatedTokens: estimateContextTokens(request), TransportBytes: len(request)}, nil
}

func (b ContextBudgetSnapshot) fits(measurement ContextMeasurement) bool {
	return measurement.EstimatedTokens <= b.InputTokenLimit && measurement.TransportBytes <= b.TransportLimitBytes
}

func (b ContextBudgetSnapshot) shouldCompact(measurement ContextMeasurement) bool {
	return measurement.EstimatedTokens > b.TriggerTokens || measurement.TransportBytes > b.TransportTargetBytes
}
