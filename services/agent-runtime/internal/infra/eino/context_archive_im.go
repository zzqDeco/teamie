package eino

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/adk/middlewares/reduction"
	"github.com/cloudwego/eino/schema"
	"teamie/pkg/postgres"
	"teamie/services/agent-runtime/internal/domain"
)

const contextIMArgumentsKey = "context-im-arguments-v1"

func contextIMTool(name string) bool {
	switch name {
	case "im_get_message", "im_list_messages", "im_list_conversations", "im_send_message", "im_get_attachment", "im_upload_file", "schedule_create", "schedule_list", "schedule_cancel":
		return true
	}
	return false
}

// ToolIntent has already checked the name and canonical argument hash before
// this hook runs. Preserve the actual typed request; a later model marker or
// an inferred destination can never replace it.
func (t *Tools) recordContextIMArguments(ctx context.Context, run *Run, call, name string, payload []byte) error {
	if contextProfile(run) != contextProfileV1 || !contextIMTool(name) {
		return nil
	}
	raw, err := json.Marshal(struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}{name, payload})
	if err != nil {
		return err
	}
	a := &ContextArchive{Store: t.Store, Lease: t.Lease}
	return a.saveObservation(ctx, run, call, contextIMArgumentsKey, raw)
}

func contextMessageSource(message Message) InputSource {
	ref := InputSource{ConversationID: message.ConversationID, MessageID: message.ID, SenderID: message.SenderID, SenderKind: message.SenderKind, ReplyToID: message.ReplyToID}
	for _, attachment := range message.Attachments {
		ref.AttachmentIDs = append(ref.AttachmentIDs, attachment.ID)
	}
	return ref
}

func (a *ContextArchive) imArguments(ctx context.Context, runID, call, name string) (json.RawMessage, error) {
	hash, actualName, raw, err := a.Store.DB.GetToolCallObservationWithMeta(ctx, runID, call, contextIMArgumentsKey)
	if err != nil {
		return nil, err
	}
	var saved struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if json.Unmarshal(raw, &saved) != nil || saved.Name != name || actualName != name || digest(saved.Arguments) != hash {
		return nil, ErrReplayConflict
	}
	return saved.Arguments, nil
}

// Parse only the typed output persisted by the authenticated IM tool. Source
// fields embedded in a message body, native argument text or a model response
// are never inspected here.
func (a *ContextArchive) imReceiptSources(ctx context.Context, runID, call, name string, raw []byte) ([]InputSource, error) {
	if !contextIMTool(name) {
		return nil, nil
	}
	var observation struct {
		OK   bool            `json:"ok"`
		Data json.RawMessage `json:"data"`
	}
	if json.Unmarshal(raw, &observation) != nil {
		return nil, ErrReplayConflict
	}
	if !observation.OK {
		return nil, nil
	}
	refs := []InputSource{}
	switch name {
	case "im_get_message", "im_send_message":
		var message Message
		if json.Unmarshal(observation.Data, &message) != nil || !validUUID(message.ID) {
			return nil, errContextUnavailable
		}
		refs = append(refs, contextMessageSource(message))
	case "im_list_messages":
		var page struct {
			Messages []Message `json:"messages"`
		}
		if json.Unmarshal(observation.Data, &page) != nil || page.Messages == nil {
			return nil, errContextUnavailable
		}
		for _, message := range page.Messages {
			if !validUUID(message.ID) {
				return nil, errContextUnavailable
			}
			refs = append(refs, contextMessageSource(message))
		}
		// A successful empty page still contains private cursor/coverage
		// metadata. Its authenticated request supplies the conversation scope.
		if len(page.Messages) == 0 {
			args, err := a.imArguments(ctx, runID, call, name)
			if postgres.IsNoRows(err) {
				return nil, errContextUnavailable
			}
			if err != nil {
				return nil, err
			}
			var request listMessagesArgs
			if json.Unmarshal(args, &request) != nil {
				return nil, errContextUnavailable
			}
			refs = append(refs, InputSource{ConversationID: request.ConversationID})
		}
	case "im_list_conversations":
		var conversations []Conversation
		if json.Unmarshal(observation.Data, &conversations) != nil || conversations == nil {
			return nil, errContextUnavailable
		}
		for _, conversation := range conversations {
			refs = append(refs, InputSource{ConversationID: conversation.ID, WorkspaceID: conversation.WorkspaceID})
		}
	case "im_get_attachment":
		args, err := a.imArguments(ctx, runID, call, name)
		if postgres.IsNoRows(err) {
			return nil, errContextUnavailable
		}
		if err != nil {
			return nil, err
		}
		var source getAttachmentArgs
		if json.Unmarshal(args, &source) != nil || !validUUID(source.MessageID) || !validUUID(source.AttachmentID) {
			return nil, errContextUnavailable
		}
		refs = append(refs, InputSource{ConversationID: source.ConversationID, MessageID: source.MessageID, AttachmentIDs: []string{source.AttachmentID}})
	case "im_upload_file":
		actual, err := a.uploadedFileSources(ctx, runID, call)
		if err != nil {
			return nil, err
		}
		refs = append(refs, actual...)
	case "schedule_create":
		var created struct {
			Schedule scheduleView `json:"schedule"`
		}
		if json.Unmarshal(observation.Data, &created) != nil || created.Schedule.ScheduleID == "" {
			return nil, errContextUnavailable
		}
		agentID := ""
		if a.Lease != nil {
			agentID = a.Lease.AgentID
		}
		actual, err := a.scheduleSourcesByID(ctx, agentID, created.Schedule.ScheduleID)
		if err != nil {
			return nil, err
		}
		refs = append(refs, actual...)
	case "schedule_list":
		var page struct {
			Schedules []scheduleView `json:"schedules"`
		}
		if json.Unmarshal(observation.Data, &page) != nil || page.Schedules == nil {
			return nil, errContextUnavailable
		}
		agentID := ""
		if a.Lease != nil {
			agentID = a.Lease.AgentID
		}
		for _, item := range page.Schedules {
			actual, err := a.scheduleSourcesByID(ctx, agentID, item.ScheduleID)
			if err != nil {
				return nil, err
			}
			refs = append(refs, actual...)
		}
	case "schedule_cancel":
		var canceled struct {
			ScheduleID string `json:"scheduleId"`
		}
		if json.Unmarshal(observation.Data, &canceled) != nil || canceled.ScheduleID == "" {
			return nil, errContextUnavailable
		}
		agentID := ""
		if a.Lease != nil {
			agentID = a.Lease.AgentID
		}
		actual, err := a.scheduleSourcesByID(ctx, agentID, canceled.ScheduleID)
		if err != nil {
			return nil, err
		}
		refs = append(refs, actual...)
	}
	return canonicalContextSources(refs)
}

// An original native result can be archived without ever being truncated.
// Match its outer identity and exact committed output, then retain the real
// returned-message scope alongside the Run's original input scope.
func (a *ContextArchive) toolMessageSources(ctx context.Context, r *Run, message *schema.AgenticMessage, originRun string) ([]InputSource, bool, error) {
	refs, found := []InputSource{}, false
	for _, block := range message.ContentBlocks {
		if block == nil || block.FunctionToolResult == nil || !contextIMTool(block.FunctionToolResult.Name) && !workspaceTool(block.FunctionToolResult.Name) {
			continue
		}
		found = true
		result := block.FunctionToolResult
		parts := []schema.ToolOutputPart{}
		for _, part := range result.Content {
			if part == nil || part.Type != schema.FunctionToolResultContentBlockTypeText || part.Text == nil {
				return nil, true, errContextUnavailable
			}
			parts = append(parts, schema.ToolOutputPart{Type: schema.ToolPartTypeText, Text: part.Text.Text})
		}
		detail := &reduction.ToolDetail{ToolResult: &schema.ToolResult{Parts: parts}}
		receiptName := result.Name
		if !contextIMTool(receiptName) {
			receiptName = "native:" + receiptName
		}
		names := []string{receiptName}
		if result.Name == "workspace_execute" {
			names = append(names, "workspace_execute")
		}
		candidates, err := a.Store.DB.ListAcceptedToolCallCandidates(ctx, r.AgentID, result.CallID, names, r.ID, r.AcceptanceHead, originRun)
		if err != nil {
			return nil, true, err
		}
		matches := []domain.AcceptedToolCallCandidate{}
		for _, item := range candidates {
			if contextReceiptMatchesTool(item.Raw, item.Name, detail) {
				matches = append(matches, item)
			}
		}
		if len(matches) == 0 {
			parents, err := a.toolReducedParents(ctx, r, []*schema.AgenticMessage{{Role: message.Role, ContentBlocks: []*schema.ContentBlock{block}}}, originRun)
			if err != nil {
				return nil, true, err
			}
			if len(parents) == 0 {
				return nil, true, errContextUnavailable
			}
		}
		for _, match := range matches {
			inputs, err := a.runSources(ctx, match.RunID)
			if err != nil {
				return nil, true, err
			}
			var actual []InputSource
			if contextIMTool(result.Name) {
				actual, err = a.imReceiptSources(ctx, match.RunID, result.CallID, result.Name, match.Raw)
			} else {
				actual, err = a.workspaceToolSources(ctx, r, match.RunID, result.CallID, match.Name, nil)
			}
			if err != nil {
				return nil, true, err
			}
			refs = append(refs, inputs...)
			refs = append(refs, actual...)
		}
	}
	refs, err := canonicalContextSources(refs)
	return refs, found, err
}

// The native string-tool truncation hook returns its notice before the original
// Session result is persisted. Match that exact generated view to a real
// immutable offload AND its original committed tool receipt. A copied textual
// context ref alone cannot acquire a source closure.
func (a *ContextArchive) toolReducedParents(ctx context.Context, r *Run, messages []*schema.AgenticMessage, originRun string) ([]string, error) {
	parents := []string{}
	for _, message := range messages {
		if message == nil {
			continue
		}
		for _, block := range message.ContentBlocks {
			if block == nil || block.FunctionToolResult == nil || !contextIMTool(block.FunctionToolResult.Name) && !workspaceTool(block.FunctionToolResult.Name) {
				continue
			}
			result := block.FunctionToolResult
			if len(result.Content) != 1 || result.Content[0] == nil || result.Content[0].Type != schema.FunctionToolResultContentBlockTypeText || result.Content[0].Text == nil {
				continue
			}
			text := result.Content[0].Text.Text
			if !strings.HasPrefix(text, "Archived private context: context:v1:") {
				continue
			}
			// Before workspace results had the exact-notice recognizer, native
			// reduction updates were persisted as views. Preserve those immutable
			// views only with an exact server event plus owning artifact identity.
			if !contextIMTool(result.Name) {
				legacy, err := a.workspaceUpdatedParents(ctx, r, message, result, originRun)
				if err != nil {
					return nil, err
				}
				if len(legacy) > 0 {
					parents = append(parents, legacy...)
					continue
				}
			}
			candidates, err := a.Store.DB.ListVisibleToolReductionRefs(ctx, r.AgentID, r.Attempt, r.ID, r.ContextHead, r.AcceptanceHead, result.Name, result.CallID, originRun)
			if err != nil {
				return nil, err
			}
			matched := false
			for _, ref := range candidates {
				if !strings.HasPrefix(text, "Archived private context: "+ref+".") {
					continue
				}
				artifact, err := a.load(ctx, r, ref)
				if err != nil {
					return nil, err
				}
				var original struct {
					Name     string               `json:"name"`
					CallID   string               `json:"callId"`
					Argument *schema.ToolArgument `json:"argument"`
					Result   *schema.ToolResult   `json:"result"`
				}
				if json.Unmarshal([]byte(artifact.Text), &original) != nil || original.Name != result.Name || original.CallID != result.CallID || original.Argument == nil || original.Result == nil {
					return nil, errContextUnavailable
				}
				expected := contextReadNotice(ref)
				if artifact.Kind == "tool_trunc" {
					raw, err := a.Store.DB.GetRunContextBudget(ctx, artifact.RunID)
					if err != nil {
						return nil, err
					}
					budget, err := ParseContextBudgetSnapshot(raw)
					if err != nil {
						return nil, err
					}
					expected += "\nPreview (quoted tool data):\n" + utf8Prefix(toolResultText(original.Result), min(1024, budget.ToolResultByteLimit/4))
				}
				if text != expected {
					return nil, errContextUnavailable
				}
				if _, err = a.toolSources(ctx, r, &reduction.ToolDetail{ToolContext: &adk.ToolContext{Name: original.Name, CallID: original.CallID}, ToolArgument: original.Argument, ToolResult: original.Result}, false); err != nil {
					return nil, err
				}
				matched = true
				parents = append(parents, ref)
			}
			if !matched {
				return nil, errContextUnavailable
			}
		}
	}
	return parents, nil
}

// Generated text can quote earlier reads from the same Run. Its provenance is
// the bounded causal prefix of that Run, never later receipts or the lifetime
// Session. Retrieval pages contribute immutable DAG edges, not copied bodies.
func (a *ContextArchive) causalMessageSources(ctx context.Context, r *Run, item contextArchiveEvent, message *schema.AgenticMessage) ([]InputSource, []string, error) {
	refs, err := a.runSources(ctx, item.runID)
	if err != nil {
		return nil, nil, err
	}
	if message.Role == schema.AgenticRoleTypeUser && !hasToolResults(message) {
		if parent, ok := message.Extra[contextArchiveRefKey].(string); ok {
			// Only the exact persisted application summary event can supply
			// this metadata link. A model-authored body marker grants nothing.
			linked := false
			messages := []*schema.AgenticMessage{item.event.Message}
			if item.event.MessageInserted != nil {
				messages = append(messages, item.event.MessageInserted.Message)
			}
			if item.event.MessagesReplaced != nil {
				messages = append(messages, (*item.event.MessagesReplaced)...)
			}
			for _, actual := range messages {
				if actual != nil && actual.Extra[contextArchiveRefKey] == parent && bytes.Equal(contextProjectionBytes(actual), contextProjectionBytes(message)) {
					linked = true
				}
			}
			if !linked {
				return nil, nil, errContextUnavailable
			}
			if _, err = a.load(ctx, r, parent); err != nil {
				return nil, nil, err
			}
			return refs, []string{parent}, nil
		}
		if strings.HasPrefix(agenticText(message), "TEAMIE_SESSION_SUMMARY ") {
			return nil, nil, errContextUnavailable
		}
	}
	if actual, found, err := a.toolMessageSources(ctx, r, message, item.runID); err != nil {
		return nil, nil, err
	} else if found {
		refs = append(refs, actual...)
	}
	parents, err := a.toolReducedParents(ctx, r, []*schema.AgenticMessage{message}, item.runID)
	if err != nil {
		return nil, nil, err
	}
	if message.Role != schema.AgenticRoleTypeAssistant {
		refs, err = canonicalContextSources(refs)
		return refs, parents, err
	}
	actual, inherited, err := a.causalRunReadSources(ctx, r, item.runID, item.seq)
	if err != nil {
		return nil, nil, err
	}
	refs = append(refs, actual...)
	parents = append(parents, inherited...)
	refs, err = canonicalContextSources(refs)
	return refs, parents, err
}

func (a *ContextArchive) causalRunReadSources(ctx context.Context, r *Run, runID string, eventHead int64) ([]InputSource, []string, error) {
	refs, parents := []InputSource{}, []string{}
	var after int64
	for {
		items, err := a.Store.DB.ListVisibleRunEventPayloads(ctx, "agent:"+r.AgentID, r.Attempt, r.ID, r.ContextHead, r.AcceptanceHead, runID, after, eventHead)
		if err != nil {
			return nil, nil, err
		}
		messages := []*schema.AgenticMessage{}
		for _, item := range items {
			after = item.Seq
			var event adk.SessionEvent[*schema.AgenticMessage]
			if err = decodeSessionPayload(item.Payload, &event); err != nil {
				return nil, nil, err
			}
			if event.Message != nil && hasToolResults(event.Message) {
				messages = append(messages, event.Message)
			}
			if event.MessageInserted != nil && event.MessageInserted.Message != nil && hasToolResults(event.MessageInserted.Message) {
				messages = append(messages, event.MessageInserted.Message)
			}
		}
		for _, previous := range messages {
			actual, _, err := a.toolMessageSources(ctx, r, previous, runID)
			if err != nil {
				return nil, nil, err
			}
			refs = append(refs, actual...)
		}
		retrievalSources, inherited, err := a.transcriptRetrievalProvenance(ctx, r, messages)
		if err != nil {
			return nil, nil, err
		}
		refs = append(refs, retrievalSources...)
		parents = append(parents, inherited...)
		reduced, err := a.toolReducedParents(ctx, r, messages, runID)
		if err != nil {
			return nil, nil, err
		}
		parents = append(parents, reduced...)
		if len(items) < 64 {
			break
		}
	}
	refs, err := canonicalContextSources(refs)
	return refs, parents, err
}
