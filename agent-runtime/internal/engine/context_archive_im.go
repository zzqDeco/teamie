package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/adk/middlewares/reduction"
	"github.com/cloudwego/eino/schema"
	"github.com/jackc/pgx/v5"
)

const contextIMArgumentsKey = "context-im-arguments-v1"

var errContextSourceOverflow = fmt.Errorf("%w: context source overflow", errContextUnavailable)

func contextIMTool(name string) bool {
	switch name {
	case "im_get_message", "im_list_messages", "im_list_conversations", "im_send_message", "im_get_attachment":
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

func boundedContextSources(refs []InputSource) ([]InputSource, error) {
	refs, err := canonicalContextSources(refs)
	if err == nil && len(refs) > 128 {
		return nil, errContextSourceOverflow
	}
	return refs, err
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
	}
	return boundedContextSources(refs)
}

// An original native IM result can be archived without ever being truncated.
// Match its outer identity and exact committed output, then retain the real
// returned-message scope alongside the Run's original input scope.
func (a *ContextArchive) imMessageSources(ctx context.Context, r *Run, message *schema.AgenticMessage, originRun string) ([]InputSource, bool, error) {
	refs, found := []InputSource{}, false
	for _, block := range message.ContentBlocks {
		if block == nil || block.FunctionToolResult == nil || !contextIMTool(block.FunctionToolResult.Name) {
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
		rows, err := a.Store.DB.Query(ctx, `SELECT t.run_id::text,t.result FROM runtime_tool_calls t JOIN runtime_runs owner ON owner.id=t.run_id WHERE owner.agent_id=$1 AND t.call_id=$2 AND t.name=$3 AND t.result IS NOT NULL AND ($6='' OR owner.id::text=$6) AND (owner.id=$4 OR EXISTS(SELECT 1 FROM runtime_session_acceptances x WHERE x.agent_id=owner.agent_id AND x.run_id=owner.id AND x.seq<=$5))`, r.AgentID, result.CallID, result.Name, r.ID, r.AcceptanceHead, originRun)
		if err != nil {
			return nil, true, err
		}
		type candidate struct {
			runID string
			raw   []byte
		}
		matches := []candidate{}
		for rows.Next() {
			var item candidate
			if err = rows.Scan(&item.runID, &item.raw); err != nil {
				rows.Close()
				return nil, true, err
			}
			if contextReceiptMatchesTool(item.raw, result.Name, detail) {
				matches = append(matches, item)
			}
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, true, err
		}
		if len(matches) == 0 {
			return nil, true, errContextUnavailable
		}
		for _, match := range matches {
			inputs, err := a.runSources(ctx, match.runID)
			if err != nil {
				return nil, true, err
			}
			actual, err := a.imReceiptSources(ctx, match.runID, result.CallID, result.Name, match.raw)
			if err != nil {
				return nil, true, err
			}
			refs = append(refs, inputs...)
			refs = append(refs, actual...)
		}
	}
	refs, err := boundedContextSources(refs)
	return refs, found, err
}

// Generated text can quote earlier reads from the same Run. Its provenance is
// the bounded causal prefix of that Run, never later receipts or the lifetime
// Session. Retrieval pages contribute immutable DAG edges, not copied bodies.
func (a *ContextArchive) causalMessageSources(ctx context.Context, r *Run, item contextArchiveEvent, message *schema.AgenticMessage) ([]InputSource, []string, error) {
	refs, err := a.runSources(ctx, item.runID)
	if err != nil {
		return nil, nil, err
	}
	if actual, found, err := a.imMessageSources(ctx, r, message, item.runID); err != nil {
		return nil, nil, err
	} else if found {
		refs = append(refs, actual...)
	}
	parents := []string{}
	if message.Role != schema.AgenticRoleTypeAssistant {
		refs, err = boundedContextSources(refs)
		return refs, parents, err
	}
	actual, parents, err := a.causalRunReadSources(ctx, r, item.runID, item.seq)
	if err != nil {
		return nil, nil, err
	}
	refs = append(refs, actual...)
	refs, err = boundedContextSources(refs)
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
			actual, _, err := a.imMessageSources(ctx, r, previous, runID)
			if err != nil {
				return nil, nil, err
			}
			refs = append(refs, actual...)
			refs, err = boundedContextSources(refs)
			if err != nil {
				return nil, nil, err
			}
		}
		inherited, err := a.transcriptRetrievalParents(ctx, r, messages)
		if err != nil {
			return nil, nil, err
		}
		parents = append(parents, inherited...)
		if count < 64 {
			break
		}
	}
	refs, err := boundedContextSources(refs)
	return refs, parents, err
}
