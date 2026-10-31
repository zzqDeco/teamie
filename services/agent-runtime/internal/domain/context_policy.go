package domain

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"unicode/utf8"
)

// Context policy and budget constants.
const (
	ContextProfileLegacy    = "teamie-context-v037"
	ContextProfileV1        = "teamie-context-v038"
	ContextBudgetVersion    = 7
	ContextCounterVersion   = "teamie-visible-estimate-v1"
	ContextCounterVersionV7 = "teamie-visible-bytes-div4-v1"
	ContextTransportLimit   = 262144

	MaxOutputTokens           = 131072
	MaxRequestBytes           = 16 << 20
	MaxResponseBytes          = 8 << 20
	MaxCalibratedPromptTokens = 100_000_000

	ContextArtifactMaximum = 4 << 20
	ContextReadDefault     = 4096
	ContextReadMaximum     = 8192
	ContextResponseMaximum = 16 << 10
	ContextSearchScan      = 64
)

// ContextBudgetSnapshot is immutable Run policy, not a wall-clock work budget.
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
	WebSearch            bool   `json:"webSearch,omitempty"`
	ScheduledWakeups     bool   `json:"scheduledWakeups,omitempty"`
	DirectMessages       bool   `json:"directMessages,omitempty"`
	ConversationalStyle  bool   `json:"conversationalStyle,omitempty"`
}

// MakeContextBudgetSnapshot creates a new ContextBudgetSnapshot for the current budget version.
func MakeContextBudgetSnapshot(window int) (ContextBudgetSnapshot, error) {
	return MakeContextBudgetSnapshotVersion(window, ContextBudgetVersion)
}

// MakeContextBudgetSnapshotVersion creates a ContextBudgetSnapshot for a specific version (1..7).
func MakeContextBudgetSnapshotVersion(window, version int) (ContextBudgetSnapshot, error) {
	if window < 4096 || window > 2_000_000 {
		return ContextBudgetSnapshot{}, errors.New("invalid context window")
	}
	reserve := min(2048, window/4)
	if version == 2 {
		reserve = min(8192, window/4)
	} else if version == 3 || version == 4 || version == 5 {
		reserve = min(4096, window/4)
	} else if version == 6 || version == 7 {
		reserve = min(MaxOutputTokens, window/4)
	} else if version != 1 {
		return ContextBudgetSnapshot{}, errors.New("unsupported context budget version")
	}
	input := window - reserve - 256
	budget := ContextBudgetSnapshot{
		Version:              version,
		CounterVersion:       ContextCounterVersion,
		ContextWindowTokens:  window,
		InputTokenLimit:      input,
		TriggerTokens:        input * 3 / 4,
		OutputReserveTokens:  reserve,
		SummaryOutputTokens:  min(2048, reserve),
		TransportLimitBytes:  ContextTransportLimit,
		TransportTargetBytes: ContextTransportLimit * 3 / 4,
		TailTokenLimit:       min(6000, input/3),
		ToolResultByteLimit:  min(16384, max(1024, input/4)),
	}
	if version >= 6 {
		budget.TransportLimitBytes = MaxRequestBytes
		budget.TransportTargetBytes = budget.TransportLimitBytes * 3 / 4
		budget.SummaryOutputTokens = min(reserve, min(8192, max(2048, input/32)))
		budget.TailTokenLimit = min(262144, input/3)
		budget.ToolResultByteLimit = min(262144, max(1024, input/4))
	}
	if version >= 7 {
		budget.CounterVersion = ContextCounterVersionV7
		budget.TriggerTokens = (input * 9) / 10
	}
	return budget, nil
}

// ParseContextBudgetSnapshot parses and validates a serialized ContextBudgetSnapshot.
func ParseContextBudgetSnapshot(raw json.RawMessage) (ContextBudgetSnapshot, error) {
	var budget ContextBudgetSnapshot
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&budget) != nil || decoder.Decode(new(any)) != io.EOF {
		return budget, errors.New("invalid context budget snapshot")
	}
	expected, err := MakeContextBudgetSnapshotVersion(budget.ContextWindowTokens, budget.Version)
	expected.FileDelivery = budget.FileDelivery
	expected.StructuredMentions = budget.StructuredMentions
	expected.EventDrivenInputs = budget.EventDrivenInputs
	expected.WebSearch = budget.WebSearch
	expected.ScheduledWakeups = budget.ScheduledWakeups
	expected.DirectMessages = budget.DirectMessages
	expected.ConversationalStyle = budget.ConversationalStyle
	if err != nil || budget != expected {
		return budget, errors.New("unsupported context budget snapshot")
	}
	return budget, nil
}

// CanonicalContextPolicy validates and normalizes a Run's context profile and budget JSON.
func CanonicalContextPolicy(profile string, raw json.RawMessage) (string, json.RawMessage, error) {
	if profile == "" {
		profile = ContextProfileLegacy
	}
	if profile != ContextProfileLegacy && profile != ContextProfileV1 {
		return "", nil, errors.New("unknown context policy")
	}
	if len(raw) == 0 {
		raw = json.RawMessage(`{}`)
	}
	if len(raw) > 4096 {
		return "", nil, errors.New("context policy exceeds limit")
	}
	var value map[string]any
	if json.Unmarshal(raw, &value) != nil || value == nil {
		return "", nil, errors.New("invalid context policy")
	}
	if profile == ContextProfileLegacy && len(value) != 0 {
		return "", nil, errors.New("legacy context policy has no budget snapshot")
	}
	if profile == ContextProfileV1 {
		if _, err := ParseContextBudgetSnapshot(raw); err != nil {
			return "", nil, err
		}
	}
	b, err := json.Marshal(value)
	return profile, b, err
}

// InitialOutputTokens returns the default initial output token cap for a model step.
func (b ContextBudgetSnapshot) InitialOutputTokens() int {
	if b.Version >= 6 {
		return min(b.OutputReserveTokens, max(2048, min(32768, b.OutputReserveTokens/2)))
	}
	return min(2048, b.OutputReserveTokens)
}

// Fits reports whether estimated tokens and transport bytes fit within hard budget ceilings.
func (b ContextBudgetSnapshot) Fits(estimatedTokens, transportBytes int) bool {
	return estimatedTokens <= b.InputTokenLimit && transportBytes <= b.TransportLimitBytes
}

// ShouldCompact reports whether estimated tokens or transport bytes exceed compaction triggers.
func (b ContextBudgetSnapshot) ShouldCompact(estimatedTokens, transportBytes int) bool {
	return estimatedTokens > b.TriggerTokens || transportBytes > b.TransportTargetBytes
}

// CompactionReason returns the diagnostic reason why compaction was triggered, or "".
func (b ContextBudgetSnapshot) CompactionReason(estimatedTokens, transportBytes int, forced bool) string {
	if forced {
		return "provider_context_overflow"
	}
	tokenTriggered := estimatedTokens > b.TriggerTokens
	byteTriggered := transportBytes > b.TransportTargetBytes
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

// ValidCalibratedTokenBounds reports whether provider prompt tokens and local token estimates
// are within positive, bounded calibration limits.
func ValidCalibratedTokenBounds(promptTokens, localTokens int) bool {
	return promptTokens > 0 && promptTokens <= MaxCalibratedPromptTokens &&
		localTokens > 0 && localTokens <= MaxCalibratedPromptTokens
}

// CalibratedEffectiveTokens computes the calibrated token estimate from an anchor's prompt/local tokens.
func CalibratedEffectiveTokens(promptTokens, localTokens, anchorLocalTokens int) (int, bool) {
	if !ValidCalibratedTokenBounds(promptTokens, anchorLocalTokens) || localTokens <= 0 || localTokens > MaxCalibratedPromptTokens {
		return 0, false
	}
	delta := int64(localTokens) - int64(anchorLocalTokens)
	effective := int64(promptTokens) + delta
	if effective < 1 {
		effective = 1
	}
	if effective > MaxCalibratedPromptTokens {
		return 0, false
	}
	return int(effective), true
}

// EstimateContextTokens computes the conservative v1-v6 token estimate over raw text bytes.
func EstimateContextTokens(text []byte) int {
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

// EstimateVisibleTokensV7 computes the v7 token estimate from provider-visible bytes.
func EstimateVisibleTokensV7(visibleBytes int) int {
	if visibleBytes <= 0 {
		return 0
	}
	return (visibleBytes+3)/4 + 64
}

// SummaryTrigger returns the conservative legacy summarization token threshold for window.
func SummaryTrigger(window int) int {
	if window < 4096 {
		window = 16384
	}
	trigger := window * 3 / 4
	if trigger > 12000 {
		trigger = 12000
	}
	return trigger
}

// IsMachineNotificationKind reports whether an input kind is a machine observation
// (background task notification, scheduled wakeup, marketplace app notification, or unread digest) rather than a new IM message.
func IsMachineNotificationKind(kind string) bool {
	return kind == "task.notification" || kind == "schedule.wakeup" || kind == "app.notification" || kind == "im.unread_digest"
}

// ShouldPinCurrentInput reports whether an input of the given kind should be pinned
// against history reduction/coalescing under the given budget version.
func ShouldPinCurrentInput(budgetVersion int, kind string) bool {
	return !(budgetVersion >= 5 && IsMachineNotificationKind(kind))
}

// ValidContextArtifactKind reports whether kind is a valid private context archive artifact kind.
func ValidContextArtifactKind(kind string) bool {
	switch kind {
	case "tool_trunc", "tool_clear", "transcript", "message":
		return true
	}
	return false
}

// FormatContextRef constructs a deterministic context:v1 reference string.
func FormatContextRef(runID, kind, key string) string {
	return "context:v1:" + runID + ":" + kind + ":" + Digest([]byte(key))
}

// ParseContextRef validates and decomposes a context:v1 reference string.
func ParseContextRef(ref string) (runID, kind, key string, err error) {
	parts := strings.Split(ref, ":")
	if len(parts) != 5 || parts[0] != "context" || parts[1] != "v1" || !ValidUUID(parts[2]) || !ValidContextArtifactKind(parts[3]) || len(parts[4]) != 64 {
		return "", "", "", ErrContextArguments
	}
	for _, c := range parts[4] {
		if !strings.ContainsRune("0123456789abcdef", c) {
			return "", "", "", ErrContextArguments
		}
	}
	return parts[2], parts[3], parts[3] + ":" + parts[4], nil
}

// UTF8Prefix returns the longest valid UTF-8 prefix of text of at most n bytes.
func UTF8Prefix(text string, n int) string {
	if len(text) <= n {
		return text
	}
	for n > 0 && !utf8.RuneStart(text[n]) {
		n--
	}
	return text[:n]
}
