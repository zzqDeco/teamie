package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
	"github.com/jackc/pgx/v5"
)

const workspaceSourcesKey = "context-workspace-sources-v1"
const workspaceImportKey = "context-workspace-import-v1"

type workspaceSourceSnapshot struct {
	Version     int    `json:"version"`
	Name        string `json:"name"`
	RequestHash string `json:"requestHash"`
	Ref         string `json:"ref"`
}

func workspaceWriter(name string) bool {
	switch strings.TrimPrefix(name, "native:") {
	case "write_file", "edit_file", "workspace_execute", "TaskCreate", "TaskUpdate", "im_get_attachment":
		return true
	}
	return false
}

func workspaceTool(name string) bool {
	if workspaceWriter(name) {
		return true
	}
	switch strings.TrimPrefix(name, "native:") {
	case "read_file", "ls", "glob", "grep", "workspace_read_page", "task_output", "task_stop", "TaskGet", "TaskList", "im_upload_file", "im_send_message":
		return true
	}
	return false
}

// This is conservative provenance for one shared Agent workspace, not a file
// taint ledger. Shell commands can rename, link or rewrite arbitrary confined
// files, so paths and model-authored source markers cannot establish origin.
// Deletion does not prove a new workspace generation and cannot erase origins.
func (a *ContextArchive) captureWorkspaceSources(ctx context.Context, r *Run, call, name string, args []byte) error {
	if contextProfile(r) != contextProfileV1 || !workspaceTool(name) {
		return nil
	}
	refs, err := a.workspaceToolSources(ctx, r, r.ID, call, name, args)
	if err != nil {
		return err
	}
	return a.authorizeSources(ctx, refs)
}

func (a *ContextArchive) workspaceToolSources(ctx context.Context, r *Run, runID, call, name string, args []byte) ([]InputSource, error) {
	var hash, actualName string
	var result []byte
	var cutoff time.Time
	if err := a.Store.DB.QueryRow(ctx, `SELECT t.request_hash,t.name,t.created_at,t.result FROM runtime_tool_calls t JOIN runtime_runs owner ON owner.id=t.run_id WHERE owner.agent_id=$1 AND t.run_id=$2 AND t.call_id=$3`, r.AgentID, runID, call).Scan(&hash, &actualName, &cutoff, &result); err != nil {
		return nil, err
	}
	if actualName != name || len(args) > 0 && digest(args) != hash {
		return nil, ErrReplayConflict
	}
	if name == "workspace_execute" {
		if err := a.legacyWorkspaceWriterView(ctx, r.AgentID, runID); err != nil {
			return nil, err
		}
	}
	raw, err := a.Store.readObservation(ctx, &Run{ID: runID}, call, workspaceSourcesKey)
	if err == nil {
		var saved workspaceSourceSnapshot
		if json.Unmarshal(raw, &saved) != nil || saved.Version != 1 || saved.Name != name || saved.RequestHash != hash {
			return nil, ErrReplayConflict
		}
		return a.workspaceSnapshotSources(ctx, r.AgentID, saved.Ref)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	refs, err := a.runSources(ctx, runID)
	if err != nil {
		return nil, err
	}
	if workspaceWriter(name) {
		causal, err := a.workspaceCausalSources(ctx, r.AgentID, runID, cutoff)
		if err != nil {
			return nil, err
		}
		refs = append(refs, causal...)
		if name == "im_get_attachment" {
			actual, err := a.workspaceImportSources(ctx, runID, call, hash)
			if errors.Is(err, pgx.ErrNoRows) && len(result) > 0 {
				actual, err = a.imReceiptSources(ctx, runID, call, name, result)
			}
			if err != nil {
				return nil, err
			}
			refs = append(refs, actual...)
		}
	}
	// Intents precede effects. Include pending/failed writers too: a missing
	// terminal receipt cannot prove that a persistent file was not changed.
	rows, err := a.Store.DB.Query(ctx, `SELECT t.run_id::text,t.call_id,t.name,t.request_hash,t.result,t.created_at,o.response FROM runtime_tool_calls t JOIN runtime_runs owner ON owner.id=t.run_id LEFT JOIN runtime_tool_observations o ON o.run_id=t.run_id AND o.call_id=t.call_id AND o.observation_key=$6 WHERE owner.agent_id=$1 AND t.created_at<=$2 AND t.name=ANY($3::text[]) AND (t.run_id<>$4 OR t.call_id<>$5) ORDER BY t.created_at,t.run_id,t.call_id`, r.AgentID, cutoff, []string{"native:write_file", "native:edit_file", "native:workspace_execute", "native:TaskCreate", "native:TaskUpdate", "im_get_attachment", "workspace_execute"}, runID, call, workspaceSourcesKey)
	if err != nil {
		return nil, err
	}
	type writer struct {
		run, call, name, hash string
		result, snapshot      []byte
		at                    time.Time
	}
	writers := []writer{}
	for rows.Next() {
		var w writer
		if err = rows.Scan(&w.run, &w.call, &w.name, &w.hash, &w.result, &w.at, &w.snapshot); err != nil {
			rows.Close()
			return nil, err
		}
		writers = append(writers, w)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	for _, w := range writers {
		if w.name == "workspace_execute" {
			if err := a.legacyWorkspaceWriterView(ctx, r.AgentID, w.run); err != nil {
				return nil, err
			}
		}
		if len(w.snapshot) > 0 {
			var saved workspaceSourceSnapshot
			if json.Unmarshal(w.snapshot, &saved) != nil || saved.Version != 1 || saved.Name != w.name || saved.RequestHash != w.hash {
				return nil, ErrReplayConflict
			}
			actual, err := a.workspaceSnapshotSources(ctx, r.AgentID, saved.Ref)
			if err != nil {
				return nil, err
			}
			refs = append(refs, actual...)
			continue
		}
		if w.name == "im_get_attachment" {
			// A verified pre-import proof survives a crash after ImportFile but
			// before the terminal tool receipt. Legacy unknown imports without
			// either proof or a successful typed receipt cannot be relabelled.
			actual, proofErr := a.workspaceImportSources(ctx, w.run, w.call, w.hash)
			if proofErr == nil {
				refs = append(refs, actual...)
			} else if !errors.Is(proofErr, pgx.ErrNoRows) {
				return nil, proofErr
			} else {
				var output Observation
				if len(w.result) == 0 || json.Unmarshal(w.result, &output) != nil {
					return nil, errContextUnavailable
				}
				if !output.OK {
					continue
				}
				actual, err := a.imReceiptSources(ctx, w.run, w.call, w.name, w.result)
				if err != nil {
					return nil, err
				}
				refs = append(refs, actual...)
			}
		}
		causal, err := a.workspaceCausalSources(ctx, r.AgentID, w.run, w.at)
		if err != nil {
			return nil, err
		}
		refs = append(refs, causal...)
	}
	var request struct {
		Operation string `json:"operation"`
		TaskID    string `json:"task_id"`
		PageTask  string `json:"taskId"`
	}
	if len(args) > 0 && json.Unmarshal(args, &request) != nil {
		return nil, errContextArguments
	}
	if name == "native:task_output" || name == "native:task_stop" || name == "native:workspace_read_page" && request.Operation == "output" {
		if name == "native:workspace_read_page" {
			// Match the actual tool schema. An ignored task_id extra field must
			// not relabel the taskId that the workspace endpoint will read.
			request.TaskID = request.PageTask
		}
		var origin string
		var created time.Time
		if request.TaskID == "" || a.Store.DB.QueryRow(ctx, `SELECT run_id::text,created_at FROM runtime_tasks WHERE agent_id=$1 AND id=$2 AND run_id IS NOT NULL AND created_at<=$3`, r.AgentID, request.TaskID, cutoff).Scan(&origin, &created) != nil {
			return nil, errContextUnavailable
		}
		actual, err := a.workspaceCausalSources(ctx, r.AgentID, origin, created)
		if err != nil {
			return nil, err
		}
		refs = append(refs, actual...)
	}
	refs, err = canonicalContextSources(refs)
	if err != nil {
		return nil, err
	}
	if runID == r.ID && contextProfile(r) == contextProfileV1 {
		identity, _ := json.Marshal(refs)
		ref, err := a.put(ctx, r, contextArtifact{RunID: r.ID, Attempt: r.Attempt, Key: "workspace/" + digest(identity), Kind: "message", ToolName: "workspace_source_snapshot", Sources: refs})
		if err != nil {
			return nil, err
		}
		saved, _ := json.Marshal(workspaceSourceSnapshot{Version: 1, Name: name, RequestHash: hash, Ref: ref})
		if err = a.saveObservation(ctx, r, call, workspaceSourcesKey, saved); err != nil {
			return nil, err
		}
	}
	return refs, nil
}

// A writer can use facts from its fixed pre-Run Session (including summaries),
// not just its trigger input. Only server events/receipts in that causal view
// contribute; later unrelated conversations and model text do not add scope.
func (a *ContextArchive) workspaceCausalSources(ctx context.Context, agent, runID string, cutoff time.Time) ([]InputSource, error) {
	var run Run
	run.ID, run.AgentID = runID, agent
	if err := a.Store.DB.QueryRow(ctx, `SELECT attempt_id::text,context_head,acceptance_head FROM runtime_runs WHERE id=$1 AND agent_id=$2`, runID, agent).Scan(&run.Attempt, &run.ContextHead, &run.AcceptanceHead); err != nil {
		return nil, err
	}
	runs := map[string]bool{runID: true}
	parents := []string{}
	var after int64
	for {
		items, err := a.eventPage(ctx, &run, after, run.ContextHead, contextSearchScan)
		if err != nil {
			return nil, err
		}
		for _, item := range items {
			after = item.seq
			runs[item.runID] = true
			messages := []*schema.AgenticMessage{item.event.Message}
			if item.event.MessagesReplaced != nil {
				messages = append(messages, (*item.event.MessagesReplaced)...)
			}
			for _, message := range messages {
				if message != nil && message.Role == schema.AgenticRoleTypeUser {
					if ref, ok := message.Extra[contextArchiveRefKey].(string); ok {
						parents = append(parents, ref)
					}
				}
			}
		}
		if len(items) < contextSearchScan {
			break
		}
	}
	ids := make([]string, 0, len(runs))
	for id := range runs {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	refs := []InputSource{}
	for _, id := range ids {
		inputs, err := a.runSources(ctx, id)
		if err != nil {
			return nil, err
		}
		refs = append(refs, inputs...)
		rows, err := a.Store.DB.Query(ctx, `SELECT call_id,name,result FROM runtime_tool_calls WHERE run_id=$1 AND created_at<=$2 AND result IS NOT NULL ORDER BY created_at,call_id`, id, cutoff)
		if err != nil {
			return nil, err
		}
		type receipt struct {
			call, name string
			raw        []byte
		}
		receipts := []receipt{}
		for rows.Next() {
			var item receipt
			if err = rows.Scan(&item.call, &item.name, &item.raw); err != nil {
				rows.Close()
				return nil, err
			}
			receipts = append(receipts, item)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
		for _, item := range receipts {
			if contextIMTool(item.name) {
				actual, err := a.imReceiptSources(ctx, id, item.call, item.name, item.raw)
				if err != nil {
					return nil, err
				}
				refs = append(refs, actual...)
			} else if item.name == "native:context_read" || item.name == "native:context_search" {
				raw, err := a.Store.readObservation(ctx, &Run{ID: id}, item.call, "context-observation-v1")
				var observation contextToolObservation
				if err != nil || json.Unmarshal(raw, &observation) != nil {
					return nil, errContextUnavailable
				}
				expected, _ := json.Marshal(observation.Wire)
				if !bytes.Equal(expected, item.raw) {
					return nil, ErrReplayConflict
				}
				parents = append(parents, observation.Refs...)
			}
		}
	}
	inherited, err := a.workspaceProofSources(ctx, agent, parents)
	if err != nil {
		return nil, err
	}
	return canonicalContextSources(append(refs, inherited...))
}

// Workspace effects can survive a failed Session attempt. Traverse only the
// provenance metadata of authenticated dependencies, never their private text;
// Session acceptance is therefore not a substitute for source authorization.
func (a *ContextArchive) workspaceProofSources(ctx context.Context, agent string, parents []string) ([]InputSource, error) {
	seen := map[string]bool{}
	refs := []InputSource{}
	for len(parents) > 0 {
		ref := parents[len(parents)-1]
		parents = parents[:len(parents)-1]
		if seen[ref] {
			continue
		}
		seen[ref] = true
		run, _, key, err := parseContextRef(ref)
		if err != nil {
			return nil, err
		}
		var sources, inherited []byte
		if err = a.Store.DB.QueryRow(ctx, `SELECT sources,source_artifact_refs FROM runtime_context_artifacts WHERE agent_id=$1 AND run_id=$2 AND artifact_key=$3`, agent, run, key).Scan(&sources, &inherited); err != nil {
			return nil, err
		}
		var actual []InputSource
		var more []string
		if json.Unmarshal(sources, &actual) != nil || json.Unmarshal(inherited, &more) != nil {
			return nil, ErrReplayConflict
		}
		refs = append(refs, actual...)
		parents = append(parents, more...)
	}
	return canonicalContextSources(refs)
}

func (a *ContextArchive) workspaceSnapshotSources(ctx context.Context, agent, ref string) ([]InputSource, error) {
	run, _, key, err := parseContextRef(ref)
	if err != nil {
		return nil, err
	}
	var valid bool
	if err = a.Store.DB.QueryRow(ctx, `SELECT kind='message' AND tool_name='workspace_source_snapshot' AND octet_length(content)=0 FROM runtime_context_artifacts WHERE agent_id=$1 AND run_id=$2 AND artifact_key=$3`, agent, run, key).Scan(&valid); err != nil {
		return nil, err
	}
	if !valid {
		return nil, ErrReplayConflict
	}
	refs, err := a.workspaceProofSources(ctx, agent, []string{ref})
	raw, _ := json.Marshal(refs)
	if err == nil && ref != contextRef(run, "message", "workspace/"+digest(raw)) {
		return nil, ErrReplayConflict
	}
	return refs, err
}

// v0.2's synchronous tool used this unprefixed receipt name and the same
// persistent Agent workspace. Its schema did not freeze a Session head. A
// genesis view is provable; an older Session with no saved head is not.
func (a *ContextArchive) legacyWorkspaceWriterView(ctx context.Context, agent, run string) error {
	var unknown bool
	err := a.Store.DB.QueryRow(ctx, `SELECT owner.context_head=0 AND EXISTS(SELECT 1 FROM runtime_session_events e JOIN runtime_runs prior ON prior.id=e.run_id WHERE prior.agent_id=$1 AND prior.id<>owner.id AND prior.started_at<=owner.started_at) FROM runtime_runs owner WHERE owner.id=$2 AND owner.agent_id=$1`, agent, run).Scan(&unknown)
	if err != nil {
		return err
	}
	if unknown {
		return errContextUnavailable
	}
	return nil
}

func (a *ContextArchive) workspaceImportSources(ctx context.Context, run, call, hash string) ([]InputSource, error) {
	raw, err := a.Store.readObservation(ctx, &Run{ID: run}, call, workspaceImportKey)
	if err != nil {
		return nil, err
	}
	var proof struct {
		RequestHash string      `json:"requestHash"`
		Source      InputSource `json:"source"`
	}
	if json.Unmarshal(raw, &proof) != nil || proof.RequestHash != hash {
		return nil, ErrReplayConflict
	}
	return canonicalContextSources([]InputSource{proof.Source})
}

// Persist only after current IM ACL, descriptor ownership, size and checksum
// have been verified, and before the first possible persistent Sandbox effect.
// A lost RPC response must retain its authenticated origin without requiring a
// successful final receipt or trusting a model-supplied filename/source marker.
func (t *Tools) recordWorkspaceImport(ctx context.Context, args *getAttachmentArgs, message *Message) error {
	run, err := CurrentRun(ctx)
	if err != nil || contextProfile(run) != contextProfileV1 {
		return err
	}
	call := compose.GetToolCallID(ctx)
	payload, _ := json.Marshal(args)
	proof, _ := json.Marshal(struct {
		RequestHash string      `json:"requestHash"`
		Source      InputSource `json:"source"`
	}{digest(payload), InputSource{ConversationID: message.ConversationID, MessageID: message.ID, SenderID: message.SenderID, SenderKind: message.SenderKind, ReplyToID: message.ReplyToID, AttachmentIDs: []string{args.AttachmentID}}})
	a := &ContextArchive{Store: t.Store, Lease: t.Lease, IM: t.IM, Authorize: t.Authorize}
	if err = a.saveObservation(ctx, run, call, workspaceImportKey, proof); err != nil {
		return err
	}
	return a.captureWorkspaceSources(ctx, run, call, "im_get_attachment", payload)
}

func (a *ContextArchive) workspaceUpdatedParents(ctx context.Context, r *Run, message *schema.AgenticMessage, result *schema.FunctionToolResult, originRun string) ([]string, error) {
	rows, err := a.Store.DB.Query(ctx, `SELECT e.run_id::text,e.payload FROM runtime_session_events e JOIN runtime_runs r ON r.id=e.run_id WHERE e.session_id=$1 AND `+contextEventVisibility+` AND e.kind='message_updated' AND ($6='' OR e.run_id::text=$6) AND position($7 in convert_from(e.payload,'UTF8'))>0`, "agent:"+r.AgentID, r.Attempt, r.ID, r.ContextHead, r.AcceptanceHead, originRun, result.CallID)
	if err != nil {
		return nil, err
	}
	type update struct {
		run string
		raw []byte
	}
	items := []update{}
	for rows.Next() {
		var item update
		if err = rows.Scan(&item.run, &item.raw); err != nil {
			rows.Close()
			return nil, err
		}
		items = append(items, item)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	parents := []string{}
	for _, item := range items {
		var event adk.SessionEvent[*schema.AgenticMessage]
		if err = decodeSessionPayload(item.raw, &event); err != nil {
			return nil, err
		}
		if event.MessageUpdated == nil || !bytes.Equal(contextProjectionBytes(event.MessageUpdated.Message), contextProjectionBytes(message)) {
			continue
		}
		text := result.Content[0].Text.Text
		fields := strings.Fields(strings.TrimPrefix(text, "Archived private context: "))
		if len(fields) == 0 {
			continue
		}
		ref := strings.TrimSuffix(fields[0], ".")
		owner, _, _, parseErr := parseContextRef(ref)
		if parseErr != nil || owner != item.run {
			continue
		}
		artifact, err := a.load(ctx, r, ref)
		if err != nil {
			return nil, err
		}
		if artifact.ToolName == result.Name && artifact.ToolCallID == result.CallID && (artifact.Kind == "tool_clear" || artifact.Kind == "tool_trunc") {
			parents = append(parents, ref)
		}
	}
	return parents, nil
}
