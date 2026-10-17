package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/adk/middlewares/reduction"
	"github.com/cloudwego/eino/schema"
	"github.com/jackc/pgx/v5"
)

const contextIMArgumentsKey = "context-im-arguments-v1"

func contextIMTool(name string) bool {
	switch name {
	case "im_get_message", "im_list_messages", "im_list_conversations", "im_send_message", "im_get_attachment", "im_upload_file":
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
	var hash, actualName string
	var raw []byte
	err := a.Store.DB.QueryRow(ctx, `SELECT t.request_hash,t.name,o.response FROM runtime_tool_calls t JOIN runtime_tool_observations o ON o.run_id=t.run_id AND o.call_id=t.call_id AND o.observation_key=$3 WHERE t.run_id=$1 AND t.call_id=$2`, runID, call, contextIMArgumentsKey).Scan(&hash, &actualName, &raw)
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
			if errors.Is(err, pgx.ErrNoRows) {
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
		if errors.Is(err, pgx.ErrNoRows) {
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
		rows, err := a.Store.DB.Query(ctx, `SELECT t.run_id::text,t.name,t.result FROM runtime_tool_calls t JOIN runtime_runs owner ON owner.id=t.run_id WHERE owner.agent_id=$1 AND t.call_id=$2 AND t.name=ANY($3::text[]) AND t.result IS NOT NULL AND ($6='' OR owner.id::text=$6) AND (owner.id=$4 OR EXISTS(SELECT 1 FROM runtime_session_acceptances x WHERE x.agent_id=owner.agent_id AND x.run_id=owner.id AND x.seq<=$5))`, r.AgentID, result.CallID, names, r.ID, r.AcceptanceHead, originRun)
		if err != nil {
			return nil, true, err
		}
		type candidate struct {
			runID string
			name  string
			raw   []byte
		}
		matches := []candidate{}
		for rows.Next() {
			var item candidate
			if err = rows.Scan(&item.runID, &item.name, &item.raw); err != nil {
				rows.Close()
				return nil, true, err
			}
			if contextReceiptMatchesTool(item.raw, item.name, detail) {
				matches = append(matches, item)
			}
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, true, err
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
			inputs, err := a.runSources(ctx, match.runID)
			if err != nil {
				return nil, true, err
			}
			var actual []InputSource
			if contextIMTool(result.Name) {
				actual, err = a.imReceiptSources(ctx, match.runID, result.CallID, result.Name, match.raw)
			} else {
				actual, err = a.workspaceToolSources(ctx, r, match.runID, result.CallID, match.name, nil)
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
			rows, err := a.Store.DB.Query(ctx, `SELECT a.run_id::text,a.artifact_key FROM runtime_context_artifacts a JOIN runtime_runs r ON r.id=a.run_id WHERE a.agent_id=$1 AND `+contextArtifactVisibility+` AND a.kind IN ('tool_trunc','tool_clear') AND a.tool_name=$6 AND a.tool_call_id=$7 AND ($8='' OR a.run_id::text=$8)`, r.AgentID, r.Attempt, r.ID, r.ContextHead, r.AcceptanceHead, result.Name, result.CallID, originRun)
			if err != nil {
				return nil, err
			}
			candidates := []string{}
			for rows.Next() {
				var runID, key string
				if err = rows.Scan(&runID, &key); err != nil {
					rows.Close()
					return nil, err
				}
				candidates = append(candidates, "context:v1:"+runID+":"+key)
			}
			err = rows.Err()
			rows.Close()
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
					var raw []byte
					if err = a.Store.DB.QueryRow(ctx, `SELECT context_budget FROM runtime_runs WHERE id=$1`, artifact.RunID).Scan(&raw); err != nil {
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
		rows, err := a.Store.DB.Query(ctx, `SELECT e.seq,e.payload FROM runtime_session_events e JOIN runtime_runs r ON r.id=e.run_id WHERE e.session_id=$1 AND `+contextEventVisibility+` AND e.run_id=$6 AND e.seq>$7 AND e.seq<=$8 ORDER BY e.seq LIMIT 64`, "agent:"+r.AgentID, r.Attempt, r.ID, r.ContextHead, r.AcceptanceHead, runID, after, eventHead)
		if err != nil {
			return nil, nil, err
		}
		messages := []*schema.AgenticMessage{}
		count := 0
		for rows.Next() {
			var raw []byte
			if err = rows.Scan(&after, &raw); err != nil {
				rows.Close()
				return nil, nil, err
			}
			var event adk.SessionEvent[*schema.AgenticMessage]
			if err = decodeSessionPayload(raw, &event); err != nil {
				rows.Close()
				return nil, nil, err
			}
			if event.Message != nil && hasToolResults(event.Message) {
				messages = append(messages, event.Message)
			}
			if event.MessageInserted != nil && event.MessageInserted.Message != nil && hasToolResults(event.MessageInserted.Message) {
				messages = append(messages, event.MessageInserted.Message)
			}
			count++
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, nil, err
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
		if count < 64 {
			break
		}
	}
	refs, err := canonicalContextSources(refs)
	return refs, parents, err
}
