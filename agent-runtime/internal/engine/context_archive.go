package engine

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/cloudwego/eino/compose"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/cloudwego/eino/adk/filesystem"
	"github.com/cloudwego/eino/adk/middlewares/reduction"
	"github.com/cloudwego/eino/schema"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// These are private Runtime records. Neither Chat nor the browser reads this table.
const contextArchiveSchemaSQL = `
CREATE TABLE IF NOT EXISTS runtime_context_artifacts(
 seq bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 agent_id uuid NOT NULL,
 run_id uuid NOT NULL REFERENCES runtime_runs(id),
 attempt_id uuid NOT NULL,
 artifact_key text NOT NULL,
 kind text NOT NULL,
 tool_call_id text NOT NULL DEFAULT '',
 tool_name text NOT NULL DEFAULT '',
 content bytea NOT NULL,
 content_hash text NOT NULL,
 sources jsonb NOT NULL,
 source_artifact_refs jsonb NOT NULL DEFAULT '[]',
 source_event_seq bigint NOT NULL DEFAULT 0,
 created_at timestamptz NOT NULL DEFAULT now(),
 UNIQUE(agent_id,run_id,artifact_key)
);
ALTER TABLE runtime_context_artifacts ADD COLUMN IF NOT EXISTS source_artifact_refs jsonb NOT NULL DEFAULT '[]';
CREATE INDEX IF NOT EXISTS runtime_context_artifacts_agent_order ON runtime_context_artifacts(agent_id,seq);
CREATE INDEX IF NOT EXISTS runtime_context_artifacts_agent_key ON runtime_context_artifacts(agent_id,artifact_key);
`

const (
	contextArtifactMaximum = 4 << 20
	contextReadDefault     = 4096
	contextReadMaximum     = 8192
	contextResponseMaximum = 16 << 10
	contextSearchScan      = 64
)

var errContextUnavailable = errors.New("context_unavailable")
var errContextArguments = errors.New("invalid_context_arguments")

// ContextArchive stores immutable text, outside the mutable Sandbox workspace.
// Authorize checks the current Agent configuration; IM checks source membership.
type ContextArchive struct {
	Store     *Store
	Lease     *Lease
	IM        *IM
	Authorize func(context.Context) error
}

type contextArtifact struct {
	RunID, Attempt, Key, Kind, ToolCallID, ToolName string
	Text                                            string
	Sources                                         []InputSource
	ArtifactRefs                                    []string
	EventSeq                                        int64
	CreatedAt                                       time.Time
	SourceVersion                                   int
}

type ContextReadPage struct {
	Ref        string `json:"ref"`
	Kind       string `json:"kind"`
	Text       string `json:"text"`
	Offset     int    `json:"offset"`
	NextOffset int    `json:"nextOffset"`
	HasMore    bool   `json:"hasMore"`
}

type ContextSearchHit struct {
	Ref     string `json:"ref"`
	Kind    string `json:"kind"`
	Excerpt string `json:"excerpt"`
}

type ContextSearchPage struct {
	Hits    []ContextSearchHit `json:"hits"`
	Cursor  string             `json:"cursor,omitempty"`
	HasMore bool               `json:"hasMore"`
}

func (a *ContextArchive) scope(ctx context.Context) (*Run, error) {
	if a == nil || a.Store == nil || a.Lease == nil {
		return nil, errContextUnavailable
	}
	r, err := CurrentRun(ctx)
	if err != nil || r == nil || r.AgentID != a.Lease.AgentID || !validUUID(r.ID) || !validUUID(r.Attempt) {
		return nil, errContextUnavailable
	}
	if a.Authorize != nil {
		if err = a.Authorize(ctx); err != nil {
			return nil, err
		}
	}
	if err = a.Store.CheckLease(ctx, a.Lease); err != nil {
		return nil, err
	}
	var current bool
	err = a.Store.DB.QueryRow(ctx, `SELECT agent_id=$2 AND attempt_id=$3 AND state='running' FROM runtime_runs WHERE id=$1`, r.ID, r.AgentID, r.Attempt).Scan(&current)
	if err != nil {
		return nil, err
	}
	if !current {
		return nil, ErrLeaseLost
	}
	return r, nil
}

func contextKind(kind string) bool {
	switch kind {
	case "tool_trunc", "tool_clear", "transcript", "message":
		return true
	}
	return false
}

func contextRef(runID, kind, key string) string {
	return "context:v1:" + runID + ":" + kind + ":" + digest([]byte(key))
}

func parseContextRef(ref string) (runID, kind, key string, err error) {
	parts := strings.Split(ref, ":")
	if len(parts) != 5 || parts[0] != "context" || parts[1] != "v1" || !validUUID(parts[2]) || !contextKind(parts[3]) || len(parts[4]) != 64 {
		return "", "", "", errContextArguments
	}
	for _, c := range parts[4] {
		if !strings.ContainsRune("0123456789abcdef", c) {
			return "", "", "", errContextArguments
		}
	}
	return parts[2], parts[3], parts[3] + ":" + parts[4], nil
}

func canonicalContextSources(refs []InputSource) ([]InputSource, error) {
	byKey := make(map[string]InputSource, len(refs))
	for _, ref := range refs {
		if !validUUID(ref.ConversationID) || !validUUID(ref.MessageID) || ref.WorkspaceID != "" && !validUUID(ref.WorkspaceID) || ref.SenderID != "" && !validUUID(ref.SenderID) {
			// A conversation-only source represents metadata from a committed
			// im_list_conversations response, never a fabricated message ID.
			if !validUUID(ref.ConversationID) || ref.MessageID != "" || ref.EventID != "" || ref.ReplyToID != "" || ref.SenderID != "" || ref.SenderKind != "" || len(ref.AttachmentIDs) != 0 || ref.WorkspaceID != "" && !validUUID(ref.WorkspaceID) {
				return nil, errContextUnavailable
			}
		}
		ref.AttachmentIDs = append([]string(nil), ref.AttachmentIDs...)
		for _, id := range ref.AttachmentIDs {
			if !validUUID(id) {
				return nil, errContextUnavailable
			}
		}
		sort.Strings(ref.AttachmentIDs)
		ref.AttachmentIDs = slices.Compact(ref.AttachmentIDs)
		key := ref.EventID + "/" + ref.ConversationID + "/" + ref.MessageID
		if old, exists := byKey[key]; exists {
			// Different authenticated tools can know different parts of the
			// same source. Fill missing fields without accepting two conflicting
			// known identities, and retain every observed attachment reference.
			for _, pair := range []struct {
				old *string
				new string
			}{{&old.WorkspaceID, ref.WorkspaceID}, {&old.SenderID, ref.SenderID}, {&old.SenderKind, ref.SenderKind}, {&old.ReplyToID, ref.ReplyToID}} {
				if pair.new != "" {
					if *pair.old != "" && *pair.old != pair.new {
						return nil, ErrReplayConflict
					}
					*pair.old = pair.new
				}
			}
			old.AttachmentIDs = append(old.AttachmentIDs, ref.AttachmentIDs...)
			sort.Strings(old.AttachmentIDs)
			old.AttachmentIDs = slices.Compact(old.AttachmentIDs)
			ref = old
		}
		byKey[key] = ref
	}
	keys := make([]string, 0, len(byKey))
	for key := range byKey {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := make([]InputSource, 0, len(keys))
	for _, key := range keys {
		out = append(out, byKey[key])
	}
	return out, nil
}

// runSources follows durable parent references, never routing text in a model
// message. Overflow is resolved against the complete parent Run, not silently
// treated as a unique destination.
func (a *ContextArchive) runSources(ctx context.Context, runID string) ([]InputSource, error) {
	visited := map[string]bool{}
	active := map[string]bool{}
	refs := []InputSource{}
	var visit func(string) error
	visit = func(id string) error {
		if active[id] {
			return errContextUnavailable
		}
		if visited[id] {
			return nil
		}
		active[id] = true
		defer delete(active, id)
		visited[id] = true
		var raw []byte
		if err := a.Store.DB.QueryRow(ctx, `SELECT input_json FROM runtime_runs WHERE id=$1 AND agent_id=$2`, id, a.Lease.AgentID).Scan(&raw); err != nil {
			return err
		}
		var inputs []Input
		if json.Unmarshal(raw, &inputs) != nil {
			return errContextUnavailable
		}
		for _, in := range inputs {
			if in.Kind != "task.notification" && in.ConversationID != "" && in.MessageID != "" {
				ref := InputSource{EventID: in.EventID, WorkspaceID: in.WorkspaceID, ConversationID: in.ConversationID, MessageID: in.MessageID, ReplyToID: in.ReplyToID, SenderID: in.SenderID, SenderKind: in.SenderKind}
				for _, attachment := range in.Attachments {
					ref.AttachmentIDs = append(ref.AttachmentIDs, attachment.ID)
				}
				refs = append(refs, ref)
			}
			refs = append(refs, in.SourceRefs...)
			parent := in.ParentRunID
			if parent == "" && in.Kind == "task.notification" && len(in.SourceRefs) == 0 {
				if in.TaskID == "" {
					return errContextUnavailable
				}
				var storedParent *string
				if err := a.Store.DB.QueryRow(ctx, `SELECT run_id::text FROM runtime_tasks WHERE id=$1 AND agent_id=$2`, in.TaskID, a.Lease.AgentID).Scan(&storedParent); err != nil || storedParent == nil {
					return errContextUnavailable
				}
				parent = *storedParent
			}
			if parent != "" {
				if !validUUID(parent) {
					return errContextUnavailable
				}
				if err := visit(parent); err != nil {
					return err
				}
			} else if in.SourceOverflow {
				return errContextUnavailable
			}
		}
		return nil
	}
	if err := visit(runID); err != nil {
		return nil, err
	}
	refs, err := canonicalContextSources(refs)
	if err == nil && len(refs) == 0 {
		err = errContextUnavailable
	}
	return refs, err
}

func (a *ContextArchive) sourcesThrough(ctx context.Context, r *Run, eventHead int64) ([]InputSource, []string, error) {
	rows, err := a.Store.DB.Query(ctx, `SELECT DISTINCT e.run_id::text FROM runtime_session_events e JOIN runtime_runs r ON r.id=e.run_id WHERE e.session_id=$1 AND `+contextEventVisibility+` AND e.seq<=$6`, "agent:"+r.AgentID, r.Attempt, r.ID, r.ContextHead, r.AcceptanceHead, eventHead)
	if err != nil {
		return nil, nil, err
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return nil, nil, err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, nil, err
	}
	refs, parents := []InputSource{}, []string{}
	for _, id := range ids {
		sources, e := a.runSources(ctx, id)
		if e != nil {
			return nil, nil, e
		}
		refs = append(refs, sources...)
		// Legacy summaries have no immutable archive marker. Resolve their
		// actual fixed prefix once, including explicit reads, then cache it.
		actual, inherited, e := a.causalRunReadSources(ctx, r, id, eventHead)
		if e != nil {
			return nil, nil, e
		}
		refs = append(refs, actual...)
		parents = append(parents, inherited...)
	}
	refs, err = canonicalContextSources(refs)
	return refs, parents, err
}

// Path commits before returning a reference. Reduction subsequently calls Write,
// which verifies the same bytes; a failed write can never publish a dangling ref.
// Generic callers are scoped to their actual Run. Historical tool reduction and
// transcript spans must use ArchiveToolPath and ArchiveTranscript respectively.
func (a *ContextArchive) Path(ctx context.Context, kind, key, content string) (string, error) {
	r, err := a.scope(ctx)
	if err != nil {
		return "", err
	}
	if kind == "tool_clear" || kind == "transcript" {
		return "", errContextUnavailable
	}
	refs, err := a.runSources(ctx, r.ID)
	if err != nil {
		return "", err
	}
	return a.PathWithSources(ctx, kind, key, content, refs)
}

func (a *ContextArchive) PathWithSources(ctx context.Context, kind, key, content string, refs []InputSource) (string, error) {
	r, err := a.scope(ctx)
	if err != nil {
		return "", err
	}
	if len(refs) == 0 {
		refs, err = a.runSources(ctx, r.ID)
		if err != nil {
			return "", err
		}
	}
	return a.put(ctx, r, contextArtifact{RunID: r.ID, Attempt: r.Attempt, Key: key, Kind: kind, Text: content, Sources: refs})
}

func (a *ContextArchive) put(ctx context.Context, r *Run, artifact contextArtifact) (string, error) {
	if !contextKind(artifact.Kind) || artifact.Key == "" || len(artifact.Key) > 4096 || len(artifact.Text) > contextArtifactMaximum || !utf8.ValidString(artifact.Text) {
		return "", errContextArguments
	}
	refs, err := canonicalContextSources(artifact.Sources)
	if err != nil {
		return "", err
	}
	parents := append([]string{}, artifact.ArtifactRefs...)
	// Keep each row's direct provenance small without imposing a lifetime or
	// per-Run source limit. Chunks contain no copied tool/transcript text, and
	// their identity depends only on canonical sources and the logical Run.
	if len(refs) > 128 {
		for start := 0; start < len(refs); start += 128 {
			chunk := refs[start:min(start+128, len(refs))]
			raw, _ := json.Marshal(chunk)
			parent, err := a.put(ctx, r, contextArtifact{RunID: artifact.RunID, Attempt: artifact.Attempt, Key: "provenance/" + digest(raw), Kind: "message", ToolName: "context_source_index", Sources: chunk})
			if err != nil {
				return "", err
			}
			parents = append(parents, parent)
		}
		refs = []InputSource{}
	}
	sourceJSON, _ := json.Marshal(refs)
	sort.Strings(parents)
	parents = slices.Compact(parents)
	parentJSON, _ := json.Marshal(parents)
	ref := contextRef(artifact.RunID, artifact.Kind, artifact.Key)
	for _, parent := range parents {
		if parent == ref {
			return "", ErrReplayConflict
		}
		if _, err = a.load(ctx, r, parent); err != nil {
			return "", err
		}
	}
	if len(refs) == 0 && len(parents) == 0 {
		return "", errContextUnavailable
	}
	_, _, key, _ := parseContextRef(ref)
	tx, err := a.Store.DB.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback(ctx)
	if err = checkLease(ctx, tx, a.Lease); err != nil {
		return "", err
	}
	var current bool
	if err = tx.QueryRow(ctx, `SELECT agent_id=$2 AND attempt_id=$3 AND state='running' FROM runtime_runs WHERE id=$1 FOR SHARE`, r.ID, r.AgentID, r.Attempt).Scan(&current); err != nil || !current {
		if err == nil {
			err = ErrLeaseLost
		}
		return "", err
	}
	_, err = tx.Exec(ctx, `INSERT INTO runtime_context_artifacts(agent_id,run_id,attempt_id,artifact_key,kind,tool_call_id,tool_name,content,content_hash,sources,source_artifact_refs,source_event_seq) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12) ON CONFLICT DO NOTHING`, r.AgentID, artifact.RunID, artifact.Attempt, key, artifact.Kind, artifact.ToolCallID, artifact.ToolName, []byte(artifact.Text), digest([]byte(artifact.Text)), sourceJSON, parentJSON, artifact.EventSeq)
	if err != nil {
		return "", err
	}
	var hash, kind, call, name string
	var oldSources, oldParents []byte
	var seq int64
	err = tx.QueryRow(ctx, `SELECT content_hash,kind,tool_call_id,tool_name,sources,source_artifact_refs,source_event_seq FROM runtime_context_artifacts WHERE agent_id=$1 AND run_id=$2 AND artifact_key=$3`, r.AgentID, artifact.RunID, key).Scan(&hash, &kind, &call, &name, &oldSources, &oldParents, &seq)
	if err != nil {
		return "", err
	}
	var oldRefs []InputSource
	if json.Unmarshal(oldSources, &oldRefs) != nil {
		return "", ErrReplayConflict
	}
	canonicalOld, e := canonicalContextSources(oldRefs)
	oldJSON, _ := json.Marshal(canonicalOld)
	var previousParents []string
	if json.Unmarshal(oldParents, &previousParents) != nil {
		return "", ErrReplayConflict
	}
	previousJSON, _ := json.Marshal(previousParents)
	if e != nil || hash != digest([]byte(artifact.Text)) || kind != artifact.Kind || call != artifact.ToolCallID || name != artifact.ToolName || seq != artifact.EventSeq || !bytes.Equal(oldJSON, sourceJSON) || !bytes.Equal(previousJSON, parentJSON) {
		return "", fmt.Errorf("%w: context artifact changed", ErrReplayConflict)
	}
	// Version the provenance in the existing private observation journal. Keep
	// immutable artifact/tool/model bytes unchanged and commit proof atomically.
	version := contextSourceVersionBytes([]byte(artifact.Text), sourceJSON, parentJSON)
	_, err = tx.Exec(ctx, `INSERT INTO runtime_tool_observations(run_id,call_id,observation_key,response) VALUES($1,$2,$3,$4) ON CONFLICT DO NOTHING`, artifact.RunID, "context-artifact:"+key, contextSourceVersionKey, version)
	if err != nil {
		return "", err
	}
	var savedVersion []byte
	if err = tx.QueryRow(ctx, `SELECT response FROM runtime_tool_observations WHERE run_id=$1 AND call_id=$2 AND observation_key=$3`, artifact.RunID, "context-artifact:"+key, contextSourceVersionKey).Scan(&savedVersion); err != nil {
		return "", err
	}
	if !bytes.Equal(version, savedVersion) {
		return "", ErrReplayConflict
	}
	if err = tx.Commit(ctx); err != nil {
		return "", err
	}
	return ref, nil
}

// Write implements the native reduction.Backend. Only references created by
// this archive are accepted; Sandbox paths and arbitrary new refs are rejected.
func (a *ContextArchive) Write(ctx context.Context, request *filesystem.WriteRequest) error {
	if request == nil {
		return errContextArguments
	}
	r, err := a.scope(ctx)
	if err != nil {
		return err
	}
	artifact, err := a.load(ctx, r, request.FilePath)
	if err != nil {
		return err
	}
	if artifact.RunID != r.ID || artifact.Text != request.Content {
		return ErrReplayConflict
	}
	return nil
}

// OffloadPath is a convenience for text-only callers. Custom reduction handlers
// that choose another serialization should call Path with their final content.
func (a *ContextArchive) OffloadPath(ctx context.Context, phase string, detail *reduction.ToolDetail) (string, error) {
	if detail == nil || detail.ToolContext == nil || detail.ToolResult == nil || detail.StreamToolResult != nil {
		return "", errContextArguments
	}
	kind := "tool_" + phase
	parts := []string{}
	for _, part := range detail.ToolResult.Parts {
		if part.Type != schema.ToolPartTypeText {
			return "", errContextArguments
		}
		parts = append(parts, part.Text)
	}
	content := strings.Join(parts, "\n")
	return a.ArchiveToolPath(ctx, kind, detail, content)
}

const contextEventVisibility = `(e.run_id=$3 OR e.seq<=$4) AND (e.attempt_id=$2 OR (e.run_id=$3 AND e.attempt_id=ANY(r.context_attempts)) OR EXISTS(SELECT 1 FROM runtime_session_acceptances x WHERE x.agent_id=r.agent_id AND x.attempt_id=e.attempt_id AND x.seq<=$5))`

const contextArtifactVisibility = `((a.source_event_seq=0 AND (a.run_id=$3 OR EXISTS(SELECT 1 FROM runtime_session_acceptances x WHERE x.agent_id=a.agent_id AND x.run_id=a.run_id AND x.seq<=$5))) OR (a.source_event_seq>0 AND (a.run_id=$3 OR a.source_event_seq<=$4) AND (a.attempt_id=$2 OR (a.run_id=$3 AND a.attempt_id=ANY(r.context_attempts)) OR EXISTS(SELECT 1 FROM runtime_session_acceptances x WHERE x.agent_id=a.agent_id AND x.attempt_id=a.attempt_id AND x.seq<=$5))))`

func (a *ContextArchive) load(ctx context.Context, r *Run, ref string) (*contextArtifact, error) {
	runID, _, key, err := parseContextRef(ref)
	if err != nil {
		return nil, err
	}
	out := &contextArtifact{RunID: runID}
	var raw, sources, parents, version []byte
	var hash string
	err = a.Store.DB.QueryRow(ctx, `SELECT a.attempt_id::text,a.artifact_key,a.kind,a.tool_call_id,a.tool_name,a.content,a.content_hash,a.sources,a.source_artifact_refs,a.source_event_seq,a.created_at,o.response FROM runtime_context_artifacts a JOIN runtime_runs r ON r.id=a.run_id LEFT JOIN runtime_tool_observations o ON o.run_id=a.run_id AND o.call_id='context-artifact:'||a.artifact_key AND o.observation_key='context-source-version-v1' WHERE a.agent_id=$1 AND `+contextArtifactVisibility+` AND a.run_id=$6 AND a.artifact_key=$7`, r.AgentID, r.Attempt, r.ID, r.ContextHead, r.AcceptanceHead, runID, key).Scan(&out.Attempt, &out.Key, &out.Kind, &out.ToolCallID, &out.ToolName, &raw, &hash, &sources, &parents, &out.EventSeq, &out.CreatedAt, &version)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, errContextUnavailable
	}
	if err != nil {
		return nil, err
	}
	if hash != digest(raw) || len(raw) > contextArtifactMaximum || !utf8.Valid(raw) || json.Unmarshal(sources, &out.Sources) != nil || json.Unmarshal(parents, &out.ArtifactRefs) != nil {
		return nil, ErrReplayConflict
	}
	out.Text = string(raw)
	if len(version) > 0 {
		canonical, err := canonicalContextSources(out.Sources)
		if err != nil {
			return nil, err
		}
		sourceJSON, _ := json.Marshal(canonical)
		parentJSON, _ := json.Marshal(out.ArtifactRefs)
		if !bytes.Equal(version, contextSourceVersionBytes(raw, sourceJSON, parentJSON)) {
			return nil, ErrReplayConflict
		}
		out.SourceVersion = 1
	}
	return out, nil
}

func (a *ContextArchive) authorizeSources(ctx context.Context, refs []InputSource) error {
	if len(refs) == 0 {
		return nil
	}
	if a.IM == nil {
		return errContextUnavailable
	}
	var conversations map[string]Conversation
	for _, ref := range refs {
		if ref.MessageID == "" {
			if conversations == nil {
				bootstrap, err := a.IM.Bootstrap(ctx)
				if err != nil {
					var api *APIError
					if errors.As(err, &api) && api.Status >= 400 && api.Status < 500 {
						return errContextUnavailable
					}
					return err
				}
				conversations = map[string]Conversation{}
				for _, conversation := range bootstrap.Conversations {
					conversations[conversation.ID] = conversation
				}
			}
			current, exists := conversations[ref.ConversationID]
			if !exists || ref.WorkspaceID != "" && current.WorkspaceID != ref.WorkspaceID {
				return errContextUnavailable
			}
			continue
		}
		message, err := a.IM.Message(ctx, ref.ConversationID, ref.MessageID)
		if err != nil {
			var api *APIError
			if errors.As(err, &api) && api.Status >= 400 && api.Status < 500 {
				return errContextUnavailable
			}
			return err
		}
		if message == nil || message.ID != ref.MessageID || message.ConversationID != ref.ConversationID || message.RetractedAt != nil || ref.SenderID != "" && message.SenderID != ref.SenderID {
			return errContextUnavailable
		}
		for _, id := range ref.AttachmentIDs {
			linked := false
			for _, attachment := range message.Attachments {
				if attachment.ID == id {
					linked = true
					break
				}
			}
			if !linked {
				return errContextUnavailable
			}
		}
	}
	return nil
}

func (a *ContextArchive) validateRefs(ctx context.Context, r *Run, refs []string) error {
	pending := append([]string{}, refs...)
	seen := map[string]bool{}
	sources := []InputSource{}
	for len(pending) > 0 {
		ref := pending[len(pending)-1]
		pending = pending[:len(pending)-1]
		if seen[ref] {
			continue
		}
		seen[ref] = true
		artifact, err := a.load(ctx, r, ref)
		if err != nil {
			return err
		}
		sources = append(sources, artifact.Sources...)
		pending = append(pending, artifact.ArtifactRefs...)
		if artifact.SourceVersion == 0 {
			inherited, err := a.legacyWorkspaceArchiveSources(ctx, r, artifact)
			if err != nil {
				return err
			}
			sources = append(sources, inherited...)
		}
	}
	canonical, err := canonicalContextSources(sources)
	if err != nil {
		return err
	}
	if len(canonical) == 0 && len(refs) > 0 {
		return errContextUnavailable
	}
	return a.authorizeSources(ctx, canonical)
}

func (a *ContextArchive) Read(ctx context.Context, ref string, offset, limit int) (_ *ContextReadPage, failure error) {
	ctx, operation := toolOperation(ctx, "context_read", compose.GetToolCallID(ctx))
	defer func() { operation.End(failure) }()
	r, err := a.scope(ctx)
	if err != nil {
		return nil, err
	}
	if limit == 0 {
		limit = contextReadDefault
	}
	if offset < 0 || limit < 1 || limit > contextReadMaximum {
		return nil, errContextArguments
	}
	artifact, err := a.load(ctx, r, ref)
	if err != nil {
		return nil, err
	}
	if err = a.validateRefs(ctx, r, []string{ref}); err != nil {
		return nil, err
	}
	if offset > len(artifact.Text) || offset < len(artifact.Text) && !utf8.RuneStart(artifact.Text[offset]) {
		return nil, errContextArguments
	}
	end := offset + limit
	if end > len(artifact.Text) {
		end = len(artifact.Text)
	}
	for end > offset && end < len(artifact.Text) && !utf8.RuneStart(artifact.Text[end]) {
		end--
	}
	if end == offset && offset < len(artifact.Text) {
		return nil, errContextArguments
	}
	for {
		page := &ContextReadPage{Ref: ref, Kind: artifact.Kind, Text: artifact.Text[offset:end], Offset: offset, NextOffset: end, HasMore: end < len(artifact.Text)}
		wire, _ := json.Marshal(contextToolResponse{OK: true, Data: page})
		if len(wire) <= contextResponseMaximum {
			return page, nil
		}
		end = offset + (end-offset)/2
		for end > offset && !utf8.RuneStart(artifact.Text[end]) {
			end--
		}
		if end == offset {
			return nil, errContextArguments
		}
	}
}

type contextSearchCursor struct {
	RunID, ViewID, QueryHash                             string
	ContextHead, AcceptanceHead, ArtifactHead, EventHead int64
	Phase                                                string
	After                                                int64
}

func encodeContextCursor(cursor contextSearchCursor) string {
	b, _ := json.Marshal(cursor)
	return base64.RawURLEncoding.EncodeToString(b)
}

func decodeContextCursor(raw string, r *Run) (contextSearchCursor, error) {
	var cursor contextSearchCursor
	b, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil || len(b) > 1024 || json.Unmarshal(b, &cursor) != nil || cursor.RunID != r.ID || !validUUID(cursor.ViewID) || cursor.ContextHead != r.ContextHead || cursor.AcceptanceHead != r.AcceptanceHead || cursor.After < 0 || cursor.ArtifactHead < 0 || cursor.EventHead < 0 || cursor.Phase != "artifacts" && cursor.Phase != "events" && cursor.Phase != "done" {
		return cursor, errContextArguments
	}
	return cursor, nil
}

func contextSearchViewID(callID string) string {
	if callID == "" {
		return uuid.NewString()
	}
	return uuid.NewSHA1(uuid.NameSpaceOID, []byte("context-search/"+callID)).String()
}
