package eino

import (
	"bytes"
	"context"
	"encoding/json"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/adk/middlewares/reduction"
	"github.com/cloudwego/eino/schema"
)

const contextSourceVersionKey = "context-source-version-v1"

func contextSourceVersionBytes(content, sources, parents []byte) []byte {
	raw, _ := json.Marshal(struct {
		Version     int    `json:"version"`
		ContentHash string `json:"contentHash"`
		SourcesHash string `json:"sourcesHash"`
		ParentsHash string `json:"parentsHash"`
	}{1, digest(content), digest(sources), digest(parents)})
	return raw
}

// Archives produced before workspace source binding cannot acquire a trusted
// version merely by being read. Reconstruct a tool offload only from its exact
// original arguments/result and committed receipt. Old derived summaries have
// no such proof; keep them unavailable when their fixed view included workspace
// observations. Their original native history remains independently indexable.
func (a *ContextArchive) legacyWorkspaceArchiveSources(ctx context.Context, r *Run, artifact *contextArtifact) ([]InputSource, error) {
	// Provenance-only DAG nodes contain no copied observation or summary body.
	if artifact.Text == "" {
		return nil, nil
	}
	if workspaceTool(artifact.ToolName) && !contextIMTool(artifact.ToolName) {
		var original struct {
			Name     string               `json:"name"`
			CallID   string               `json:"callId"`
			Argument *schema.ToolArgument `json:"argument"`
			Result   *schema.ToolResult   `json:"result"`
		}
		if json.Unmarshal([]byte(artifact.Text), &original) != nil || original.Name != artifact.ToolName || original.CallID != artifact.ToolCallID || original.Argument == nil || original.Result == nil {
			return nil, errContextUnavailable
		}
		return a.toolSources(ctx, r, &reduction.ToolDetail{ToolContext: &adk.ToolContext{Name: original.Name, CallID: original.CallID}, ToolArgument: original.Argument, ToolResult: original.Result}, false)
	}
	if artifact.EventSeq > 0 {
		raw, err := a.Store.DB.GetSessionEventPayloadBySeq(ctx, "agent:"+r.AgentID, artifact.RunID, artifact.EventSeq)
		if err != nil {
			return nil, err
		}
		var event adk.SessionEvent[*schema.AgenticMessage]
		if err := decodeSessionPayload(raw, &event); err != nil {
			return nil, err
		}
		message := event.Message
		if event.MessageInserted != nil {
			message = event.MessageInserted.Message
		}
		if !bytes.Equal(contextProjectionBytes(message), []byte(artifact.Text)) {
			return nil, errContextUnavailable
		}
		refs, parents, err := a.causalMessageSources(ctx, r, contextArchiveEvent{seq: artifact.EventSeq, runID: artifact.RunID, attempt: artifact.Attempt, event: &event}, message)
		if err != nil {
			return nil, err
		}
		if err = a.validateRefs(ctx, r, parents); err != nil {
			return nil, err
		}
		inherited, err := a.workspaceProofSources(ctx, r.AgentID, parents)
		return append(refs, inherited...), err
	}
	// These artifacts never contain workspace-derived bytes: typed IM results
	// bind their own returned origins, while source-only nodes carry no body.
	if contextIMTool(artifact.ToolName) {
		return nil, nil
	}
	touched, err := a.Store.DB.HasLegacyWorkspaceToolActivity(ctx, r.AgentID, artifact.RunID, artifact.CreatedAt, []string{"native:read_file", "native:ls", "native:glob", "native:grep", "native:workspace_read_page", "native:workspace_execute", "native:task_output", "native:task_stop", "native:TaskCreate", "native:TaskUpdate", "native:TaskGet", "native:TaskList", "workspace_execute"}, "agent:"+r.AgentID)
	if err != nil {
		return nil, err
	}
	if touched {
		return nil, errContextUnavailable
	}
	return nil, nil
}
