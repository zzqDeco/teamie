package eino

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"sync"

	"github.com/cloudwego/eino/schema"
	"go.uber.org/zap"
	"teamie/pkg/observability"
)

type contextUsageAnchor struct {
	Step              int    `json:"step"`
	ConfigID          string `json:"configId,omitempty"`
	AdmissionConfigID string `json:"admissionConfigId,omitempty"`
	CounterMode       string `json:"counterMode,omitempty"`
	MessageCount      int    `json:"messageCount"`
	PrefixHash        string `json:"prefixHash"`
	LocalTokens       int    `json:"localTokens"`
	PromptTokens      int    `json:"promptTokens"`
}

type requestAdmissionState struct {
	ConfigID    string
	CounterMode string
	Known       bool
}

type usageAnchorState struct {
	mu                 sync.Mutex
	anchor             *contextUsageAnchor
	admissionConfigID  string
	admissionCounter   string
	hasAdmissionConfig bool
	invalidatedAnchors map[string]bool
}

func (s *usageAnchorState) setAdmissionConfig(configID, counterMode string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.admissionConfigID = strings.TrimSpace(configID)
	s.admissionCounter = strings.TrimSpace(counterMode)
	s.hasAdmissionConfig = true
}

func (s *usageAnchorState) clearAdmissionConfig() {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.admissionConfigID = ""
	s.admissionCounter = ""
	s.hasAdmissionConfig = false
}

func (s *usageAnchorState) hasAdmission() bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.hasAdmissionConfig
}

func (s *usageAnchorState) consumeAdmissionConfig(preserve bool) (string, string, bool) {
	if s == nil {
		return "", "", false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	id, mode, ok := s.admissionConfigID, s.admissionCounter, s.hasAdmissionConfig
	if !preserve {
		s.admissionConfigID = ""
		s.admissionCounter = ""
		s.hasAdmissionConfig = false
	}
	return id, mode, ok
}

func (s *usageAnchorState) get() *contextUsageAnchor {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.anchor == nil {
		return nil
	}
	copy := *s.anchor
	return &copy
}

func (s *usageAnchorState) getForConfig(configID string) *contextUsageAnchor {
	anchor := s.get()
	if anchor == nil || anchor.ConfigID == "" || configID == "" || anchor.ConfigID != configID {
		return nil
	}
	return anchor
}

func (s *usageAnchorState) set(anchor *contextUsageAnchor, onlyIfValid bool) {
	if s == nil || anchor == nil || !validCalibratedTokenBounds(anchor.PromptTokens, anchor.LocalTokens) || anchor.MessageCount <= 0 || anchor.PrefixHash == "" || anchor.ConfigID == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if onlyIfValid {
		if s.invalidatedAnchors[anchor.PrefixHash] {
			return
		}
		if s.anchor != nil && s.anchor.PrefixHash == anchor.PrefixHash && s.anchor.ConfigID == anchor.ConfigID {
			return
		}
	}
	copy := *anchor
	s.anchor = &copy
}

func (s *usageAnchorState) clear() {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.anchor = nil
}

func (s *usageAnchorState) isInvalidated(prefixHash string) bool {
	if s == nil || prefixHash == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.invalidatedAnchors[prefixHash]
}

func (s *usageAnchorState) invalidate(messages []*schema.AgenticMessage) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	next := map[string]bool{}
	if s.anchor != nil && s.anchor.PrefixHash != "" {
		next[s.anchor.PrefixHash] = true
	}
	s.anchor = nil
	for _, msg := range messages {
		if msg != nil && msg.Role == schema.AgenticRoleTypeAssistant {
			if saved, ok := extractUsageAnchor(msg); ok && saved.PrefixHash != "" {
				next[saved.PrefixHash] = true
			}
			clearMessageUsageAnchor(msg)
		}
	}
	if len(next) > 0 {
		s.invalidatedAnchors = next
	} else {
		s.invalidatedAnchors = nil
	}
}

func (s *usageAnchorState) pruneAfterCompaction(retained []*schema.AgenticMessage) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.anchor = nil
	for _, msg := range retained {
		if msg != nil && msg.Role == schema.AgenticRoleTypeAssistant {
			clearMessageUsageAnchor(msg)
		}
	}
}

func setMessageExtraString(msg *schema.AgenticMessage, key, value string) {
	if msg == nil {
		return
	}
	cloned := make(map[string]any, len(msg.Extra)+1)
	for k, v := range msg.Extra {
		cloned[k] = v
	}
	cloned[key] = value
	msg.Extra = cloned
}

func attachUsageAnchor(msg *schema.AgenticMessage, anchor *contextUsageAnchor) {
	if msg == nil || anchor == nil || !validCalibratedTokenBounds(anchor.PromptTokens, anchor.LocalTokens) || anchor.MessageCount <= 0 || anchor.PrefixHash == "" || anchor.ConfigID == "" {
		return
	}
	raw, err := json.Marshal(anchor)
	if err != nil {
		return
	}
	setMessageExtraString(msg, contextUsageAnchorExtraKey, string(raw))
}

func attachRecordedStepMetadata(msg *schema.AgenticMessage, step int, configID string, adm requestAdmissionState) {
	configID = strings.TrimSpace(configID)
	admID := strings.TrimSpace(adm.ConfigID)
	mode := strings.TrimSpace(adm.CounterMode)
	if msg == nil || (configID == "" && admID == "" && mode == "") {
		return
	}
	if existing, ok := extractUsageAnchor(msg); ok {
		existing.AdmissionConfigID = admID
		existing.CounterMode = mode
		attachUsageAnchor(msg, existing)
		return
	}
	raw, err := json.Marshal(contextUsageAnchor{
		Step:              step,
		ConfigID:          configID,
		AdmissionConfigID: admID,
		CounterMode:       mode,
	})
	if err != nil {
		return
	}
	setMessageExtraString(msg, contextUsageAnchorExtraKey, string(raw))
}

func extractRawUsageAnchor(msg *schema.AgenticMessage) (contextUsageAnchor, bool) {
	if msg == nil || len(msg.Extra) == 0 {
		return contextUsageAnchor{}, false
	}
	val, ok := msg.Extra[contextUsageAnchorExtraKey]
	if !ok || val == nil {
		return contextUsageAnchor{}, false
	}
	var raw []byte
	switch v := val.(type) {
	case string:
		raw = []byte(v)
	default:
		var err error
		raw, err = json.Marshal(v)
		if err != nil {
			return contextUsageAnchor{}, false
		}
	}
	var anchor contextUsageAnchor
	if json.Unmarshal(raw, &anchor) != nil {
		return contextUsageAnchor{}, false
	}
	return anchor, true
}

func extractRecordedModelConfigID(msg *schema.AgenticMessage) (string, bool) {
	anchor, ok := extractRawUsageAnchor(msg)
	if !ok {
		return "", false
	}
	id := strings.TrimSpace(anchor.ConfigID)
	if id == "" {
		return "", false
	}
	return id, true
}

func extractRecordedAdmissionMetadata(msg *schema.AgenticMessage) (requestAdmissionState, bool) {
	anchor, ok := extractRawUsageAnchor(msg)
	if !ok {
		return requestAdmissionState{}, false
	}
	if anchor.CounterMode != "" || anchor.AdmissionConfigID != "" {
		id := strings.TrimSpace(anchor.AdmissionConfigID)
		return requestAdmissionState{
			ConfigID:    id,
			CounterMode: strings.TrimSpace(anchor.CounterMode),
			Known:       id != "",
		}, true
	}
	if id := strings.TrimSpace(anchor.ConfigID); id != "" {
		return requestAdmissionState{
			ConfigID: id,
			Known:    true,
		}, true
	}
	return requestAdmissionState{}, false
}

func extractUsageAnchor(msg *schema.AgenticMessage) (*contextUsageAnchor, bool) {
	anchor, ok := extractRawUsageAnchor(msg)
	if !ok {
		return nil, false
	}
	if !validCalibratedTokenBounds(anchor.PromptTokens, anchor.LocalTokens) || anchor.MessageCount <= 0 || anchor.PrefixHash == "" || anchor.ConfigID == "" {
		return nil, false
	}
	return &anchor, true
}

func stripRetainedUsageAnchors(messages []*schema.AgenticMessage) []*schema.AgenticMessage {
	if len(messages) == 0 {
		return messages
	}
	var out []*schema.AgenticMessage
	for i, msg := range messages {
		if msg == nil || msg.Role != schema.AgenticRoleTypeAssistant || len(msg.Extra) == 0 {
			continue
		}
		if _, exists := msg.Extra[contextUsageAnchorExtraKey]; !exists {
			continue
		}
		if out == nil {
			out = append([]*schema.AgenticMessage(nil), messages...)
		}
		cloned := *msg
		clearMessageUsageAnchor(&cloned)
		out[i] = &cloned
	}
	if out != nil {
		return out
	}
	return messages
}

func clearMessageUsageAnchor(msg *schema.AgenticMessage) {
	if msg == nil || len(msg.Extra) == 0 {
		return
	}
	if _, exists := msg.Extra[contextUsageAnchorExtraKey]; !exists {
		return
	}
	if len(msg.Extra) == 1 {
		msg.Extra = nil
		return
	}
	cloned := make(map[string]any, len(msg.Extra)-1)
	for k, v := range msg.Extra {
		if k != contextUsageAnchorExtraKey {
			cloned[k] = v
		}
	}
	msg.Extra = cloned
}

func (p *ContextPipeline) commitCompaction(ctx context.Context, original, compacted []*schema.AgenticMessage) {
	if !slices.Equal(compacted, original) {
		p.invalidateAnchor(ctx, original)
		p.usageState(ctx).pruneAfterCompaction(compacted)
	}
}

func (p *ContextPipeline) compact(ctx context.Context, messages []*schema.AgenticMessage, tools []*schema.ToolInfo, force bool, deferred ...[]*schema.ToolInfo) ([]*schema.AgenticMessage, error) {
	result, err := p.compactStage(ctx, messages, tools, force, deferred...)
	if err == nil {
		p.commitCompaction(ctx, messages, result)
	}
	return result, err
}

func (p *ContextPipeline) compactStage(ctx context.Context, messages []*schema.AgenticMessage, tools []*schema.ToolInfo, force bool, deferred ...[]*schema.ToolInfo) (result []*schema.AgenticMessage, failure error) {
	allDeferred := flattenToolInfos(deferred)
	initial, err := p.measureMainContext(ctx, messages, tools, allDeferred)
	if err != nil {
		return nil, err
	}
	compactionFields := append([]zap.Field{zap.Int("custom.context.messages_before", len(messages)), zap.Bool("custom.context.forced", force)}, p.cfg.Budget.measurementLogFields(initial, p.cfg.Budget.compactionReason(initial, force))...)
	ctx, operation := observability.Begin(ctx, "context-compaction", compactionFields...)
	observability.L(ctx).Info("Context compaction started", zap.String("event.action", "context-compaction-started"))
	defer func() {
		if failure == nil && !slices.Equal(result, messages) {
			result = stripRetainedUsageAnchors(result)
		}
		operation.End(failure, zap.Int("custom.context.messages_after", len(result)))
	}()
	target := p.cfg.Budget.InputTokenLimit
	shrinkLocal := force || (p.cfg.Budget.Version >= 7 && initial.CounterMode == "usage_delta" && initial.EstimatedTokens > initial.LocalTokens)
	if shrinkLocal {
		measurement, err := p.cfg.Budget.measureContext(messages, tools, p.cfg.Budget.OutputReserveTokens, false, allDeferred)
		if err != nil {
			return nil, err
		}
		target = min(target, measurement.EstimatedTokens*3/4)
	}
	systems, prefix, tail := p.partition(messages, shrinkLocal)
	if p.cfg.Budget.Version >= 3 {
		pinned, err := p.currentInputSet(ctx, messages)
		if err != nil {
			return nil, err
		}
		kept, older := []*schema.AgenticMessage{}, []*schema.AgenticMessage{}
		for _, message := range prefix {
			if pinned[message] {
				kept = append(kept, message)
			} else {
				older = append(older, message)
			}
		}
		prefix, tail = older, append(kept, tail...)
	}
	if len(prefix) == 0 {
		return p.fitTailTo(ctx, systems, tail, tools, target, allDeferred)
	}
	ref, err := p.cfg.ArchiveTranscript(ctx, prefix)
	if err != nil {
		return nil, err
	}
	text, err := p.summarizePrefix(ctx, prefix)
	if err != nil {
		return nil, err
	}
	sources := contextSources(prefix)
	sourceJSON, _ := json.Marshal(sources)
	summaryText := "TEAMIE_SESSION_SUMMARY (source-scoped memory; verify current access using tools):\n" + text + "\n" + contextReadNotice(ref)
	if p.cfg.Budget.Version < 3 {
		summaryText += "\nTEAMIE_SOURCE_REFERENCES " + string(sourceJSON)
	}
	summary := schema.UserAgenticMessage(summaryText)
	summary.Extra = map[string]any{"teamie_context_summary": contextProfileV1, "teamie_context_sources": contextSourceValue(sources), "teamie_context_archive_ref": ref}
	return p.fitTailTo(ctx, append(append([]*schema.AgenticMessage{}, systems...), summary), tail, tools, target, allDeferred)
}
