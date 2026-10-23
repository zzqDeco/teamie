package pg

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"time"

	"teamie/pkg/postgres"
	"teamie/services/agent-runtime/internal/domain"
)

const contextEventVisibility = `(e.run_id=$3 OR e.seq<=$4) AND (e.attempt_id=$2 OR (e.run_id=$3 AND e.attempt_id=ANY(r.context_attempts)) OR EXISTS(SELECT 1 FROM runtime_session_acceptances x WHERE x.agent_id=r.agent_id AND x.attempt_id=e.attempt_id AND x.seq<=$5))`

const contextArtifactVisibility = `((a.source_event_seq=0 AND (a.run_id=$3 OR EXISTS(SELECT 1 FROM runtime_session_acceptances x WHERE x.agent_id=a.agent_id AND x.run_id=a.run_id AND x.seq<=$5))) OR (a.source_event_seq>0 AND (a.run_id=$3 OR a.source_event_seq<=$4) AND (a.attempt_id=$2 OR (a.run_id=$3 AND a.attempt_id=ANY(r.context_attempts)) OR EXISTS(SELECT 1 FROM runtime_session_acceptances x WHERE x.agent_id=a.agent_id AND x.attempt_id=a.attempt_id AND x.seq<=$5))))`

// ResolveRunSources recursively walks durable run/task/schedule parent provenance to compute
// the canonical InputSource closure for runID.
func (r *Repository) ResolveRunSources(ctx context.Context, agentID, runID string) ([]domain.InputSource, error) {
	visited := map[string]bool{}
	active := map[string]bool{}
	refs := []domain.InputSource{}
	conn := r.db.Conn(ctx)
	var visit func(string) error
	visit = func(id string) error {
		if active[id] {
			return domain.ErrContextUnavailable
		}
		if visited[id] {
			return nil
		}
		active[id] = true
		defer delete(active, id)
		visited[id] = true
		var raw []byte
		if err := conn.QueryRow(ctx, `SELECT input_json FROM runtime_runs WHERE id=$1 AND agent_id=$2`, id, agentID).Scan(&raw); err != nil {
			return err
		}
		var inputs []domain.Input
		if json.Unmarshal(raw, &inputs) != nil {
			return domain.ErrContextUnavailable
		}
		for _, in := range inputs {
			if in.Kind != "task.notification" && in.Kind != "schedule.wakeup" && in.ConversationID != "" && in.MessageID != "" {
				ref := domain.InputSource{
					EventID:        in.EventID,
					WorkspaceID:    in.WorkspaceID,
					ConversationID: in.ConversationID,
					MessageID:      in.MessageID,
					ReplyToID:      in.ReplyToID,
					SenderID:       in.SenderID,
					SenderKind:     in.SenderKind,
				}
				for _, attachment := range in.Attachments {
					ref.AttachmentIDs = append(ref.AttachmentIDs, attachment.ID)
				}
				refs = append(refs, ref)
			}
			refs = append(refs, in.SourceRefs...)
			parent := in.ParentRunID
			if parent == "" && in.Kind == "task.notification" && len(in.SourceRefs) == 0 {
				if in.TaskID == "" {
					return domain.ErrContextUnavailable
				}
				var storedParent *string
				if err := conn.QueryRow(ctx, `SELECT run_id::text FROM runtime_tasks WHERE id=$1 AND agent_id=$2`, in.TaskID, agentID).Scan(&storedParent); err != nil || storedParent == nil {
					return domain.ErrContextUnavailable
				}
				parent = *storedParent
			}
			if parent == "" && in.Kind == "schedule.wakeup" && len(in.SourceRefs) == 0 {
				if in.ScheduleID == "" {
					return domain.ErrContextUnavailable
				}
				var storedParent *string
				if err := conn.QueryRow(ctx, `SELECT created_run_id::text FROM runtime_schedules WHERE id=$1 AND agent_id=$2`, in.ScheduleID, agentID).Scan(&storedParent); err != nil || storedParent == nil {
					return domain.ErrContextUnavailable
				}
				parent = *storedParent
			}
			if parent != "" {
				if !domain.ValidUUID(parent) {
					return domain.ErrContextUnavailable
				}
				if err := visit(parent); err != nil {
					return err
				}
			} else if in.SourceOverflow {
				return domain.ErrContextUnavailable
			}
		}
		return nil
	}
	if err := visit(runID); err != nil {
		return nil, err
	}
	refs, err := domain.CanonicalContextSources(refs)
	if err == nil && len(refs) == 0 {
		err = domain.ErrContextUnavailable
	}
	return refs, err
}

// CheckRunAttemptRunning verifies that runID belongs to agentID, has attempt_id=attemptID, and is in state='running'.
func (r *Repository) CheckRunAttemptRunning(ctx context.Context, runID, agentID, attemptID string) (bool, error) {
	var current bool
	err := r.db.Conn(ctx).QueryRow(ctx, `SELECT agent_id=$2 AND attempt_id=$3 AND state='running' FROM runtime_runs WHERE id=$1`, runID, agentID, attemptID).Scan(&current)
	return current, err
}

// ListVisibleSessionRunIDsThrough lists distinct visible run_ids in session_id up to eventHead.
func (r *Repository) ListVisibleSessionRunIDsThrough(ctx context.Context, sessionID, attemptID, runID string, contextHead, acceptanceHead, eventHead int64) ([]string, error) {
	rows, err := r.db.Conn(ctx).Query(ctx, `SELECT DISTINCT e.run_id::text FROM runtime_session_events e JOIN runtime_runs r ON r.id=e.run_id WHERE e.session_id=$1 AND `+contextEventVisibility+` AND e.seq<=$6`, sessionID, attemptID, runID, contextHead, acceptanceHead, eventHead)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := []string{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// ContextArtifactWrite holds the parameters for persisting a context artifact and its source version proof.
type ContextArtifactWrite = domain.ContextArtifactWrite

// PutContextArtifact atomically inserts and verifies a context artifact and its provenance version observation.
func (r *Repository) PutContextArtifact(
	ctx context.Context,
	l *domain.Lease,
	w ContextArtifactWrite,
	canonicalizeSources func(raw []byte) ([]byte, error),
) error {
	return r.WithinTx(ctx, func(txCtx context.Context) error {
		if err := r.CheckLeaseForUpdate(txCtx, l); err != nil {
			return err
		}
		conn := r.db.Conn(txCtx)
		var current bool
		if err := conn.QueryRow(txCtx, `SELECT agent_id=$2 AND attempt_id=$3 AND state='running' FROM runtime_runs WHERE id=$1 FOR SHARE`, w.ActiveRunID, w.AgentID, w.ActiveAttempt).Scan(&current); err != nil || !current {
			if err == nil {
				err = domain.ErrLeaseLost
			}
			return err
		}
		if _, err := conn.Exec(txCtx, `INSERT INTO runtime_context_artifacts(agent_id,run_id,attempt_id,artifact_key,kind,tool_call_id,tool_name,content,content_hash,sources,source_artifact_refs,source_event_seq) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12) ON CONFLICT DO NOTHING`, w.AgentID, w.RunID, w.Attempt, w.Key, w.Kind, w.ToolCallID, w.ToolName, w.Content, w.ContentHash, w.SourceJSON, w.ParentJSON, w.EventSeq); err != nil {
			return err
		}
		var hash, kind, call, name string
		var oldSources, oldParents []byte
		var seq int64
		if err := conn.QueryRow(txCtx, `SELECT content_hash,kind,tool_call_id,tool_name,sources,source_artifact_refs,source_event_seq FROM runtime_context_artifacts WHERE agent_id=$1 AND run_id=$2 AND artifact_key=$3`, w.AgentID, w.RunID, w.Key).Scan(&hash, &kind, &call, &name, &oldSources, &oldParents, &seq); err != nil {
			return err
		}
		oldJSON, e := canonicalizeSources(oldSources)
		var previousParents []string
		if json.Unmarshal(oldParents, &previousParents) != nil {
			return domain.ErrReplayConflict
		}
		previousJSON, _ := json.Marshal(previousParents)
		if e != nil || hash != w.ContentHash || kind != w.Kind || call != w.ToolCallID || name != w.ToolName || seq != w.EventSeq || !bytes.Equal(oldJSON, w.SourceJSON) || !bytes.Equal(previousJSON, w.ParentJSON) {
			return fmt.Errorf("%w: context artifact changed", domain.ErrReplayConflict)
		}
		if _, err := conn.Exec(txCtx, `INSERT INTO runtime_tool_observations(run_id,call_id,observation_key,response) VALUES($1,$2,$3,$4) ON CONFLICT DO NOTHING`, w.RunID, "context-artifact:"+w.Key, w.VersionKey, w.VersionBytes); err != nil {
			return err
		}
		var savedVersion []byte
		if err := conn.QueryRow(txCtx, `SELECT response FROM runtime_tool_observations WHERE run_id=$1 AND call_id=$2 AND observation_key=$3`, w.RunID, "context-artifact:"+w.Key, w.VersionKey).Scan(&savedVersion); err != nil {
			return err
		}
		if !bytes.Equal(w.VersionBytes, savedVersion) {
			return domain.ErrReplayConflict
		}
		return nil
	})
}

// LoadedContextArtifactRow holds the raw columns of a loaded context artifact.
type LoadedContextArtifactRow struct {
	Attempt    string
	Key        string
	Kind       string
	ToolCallID string
	ToolName   string
	Raw        []byte
	Hash       string
	Sources    []byte
	Parents    []byte
	EventSeq   int64
	CreatedAt  time.Time
	Version    []byte
}

// LoadVisibleContextArtifact loads a visible context artifact and its optional source-version observation.
func (r *Repository) LoadVisibleContextArtifact(
	ctx context.Context,
	agentID, attemptID, activeRunID string,
	contextHead, acceptanceHead int64,
	targetRunID, key string,
) (*LoadedContextArtifactRow, bool, error) {
	var out LoadedContextArtifactRow
	err := r.db.Conn(ctx).QueryRow(ctx, `SELECT a.attempt_id::text,a.artifact_key,a.kind,a.tool_call_id,a.tool_name,a.content,a.content_hash,a.sources,a.source_artifact_refs,a.source_event_seq,a.created_at,o.response FROM runtime_context_artifacts a JOIN runtime_runs r ON r.id=a.run_id LEFT JOIN runtime_tool_observations o ON o.run_id=a.run_id AND o.call_id='context-artifact:'||a.artifact_key AND o.observation_key='context-source-version-v1' WHERE a.agent_id=$1 AND `+contextArtifactVisibility+` AND a.run_id=$6 AND a.artifact_key=$7`, agentID, attemptID, activeRunID, contextHead, acceptanceHead, targetRunID, key).Scan(&out.Attempt, &out.Key, &out.Kind, &out.ToolCallID, &out.ToolName, &out.Raw, &out.Hash, &out.Sources, &out.Parents, &out.EventSeq, &out.CreatedAt, &out.Version)
	if postgres.IsNoRows(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return &out, true, nil
}

// SaveRunningRunObservation persists a tool observation after verifying the active lease and running Run attempt.
func (r *Repository) SaveRunningRunObservation(ctx context.Context, l *domain.Lease, runID, agentID, attemptID, call, key string, response []byte) error {
	return r.WithinTx(ctx, func(txCtx context.Context) error {
		if err := r.CheckLeaseForUpdate(txCtx, l); err != nil {
			return err
		}
		conn := r.db.Conn(txCtx)
		var current bool
		if err := conn.QueryRow(txCtx, `SELECT agent_id=$2 AND attempt_id=$3 AND state='running' FROM runtime_runs WHERE id=$1 FOR SHARE`, runID, agentID, attemptID).Scan(&current); err != nil {
			return err
		}
		if !current {
			return domain.ErrLeaseLost
		}
		if _, err := conn.Exec(txCtx, `INSERT INTO runtime_tool_observations(run_id,call_id,observation_key,response) VALUES($1,$2,$3,$4) ON CONFLICT DO NOTHING`, runID, call, key, response); err != nil {
			return err
		}
		var old []byte
		if err := conn.QueryRow(txCtx, `SELECT response FROM runtime_tool_observations WHERE run_id=$1 AND call_id=$2 AND observation_key=$3`, runID, call, key).Scan(&old); err != nil {
			return err
		}
		if !bytes.Equal(old, response) {
			return domain.ErrReplayConflict
		}
		return nil
	})
}

// ArchiveSessionEventRow holds a raw session event row from runtime_session_events.
type ArchiveSessionEventRow struct {
	Seq     int64
	RunID   string
	Attempt string
	EventID string
	Payload []byte
}

// ListVisibleSessionEventPage returns a page of visible session events in (after, head].
func (r *Repository) ListVisibleSessionEventPage(ctx context.Context, sessionID, attemptID, runID string, contextHead, acceptanceHead, after, head int64, limit int) ([]ArchiveSessionEventRow, error) {
	rows, err := r.db.Conn(ctx).Query(ctx, `SELECT e.seq,e.run_id::text,e.attempt_id::text,e.event_id,e.payload FROM runtime_session_events e JOIN runtime_runs r ON r.id=e.run_id WHERE e.session_id=$1 AND `+contextEventVisibility+` AND e.seq>$6 AND e.seq<=$7 ORDER BY e.seq LIMIT $8`, sessionID, attemptID, runID, contextHead, acceptanceHead, after, head, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ArchiveSessionEventRow{}
	for rows.Next() {
		var item ArchiveSessionEventRow
		if err = rows.Scan(&item.Seq, &item.RunID, &item.Attempt, &item.EventID, &item.Payload); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

// GetContextSearchHeads returns the maximum artifact seq and visible session event seq for a search view.
func (r *Repository) GetContextSearchHeads(ctx context.Context, agentID, sessionID, attemptID, runID string, contextHead, acceptanceHead int64) (int64, int64, error) {
	var artifactHead, eventHead int64
	conn := r.db.Conn(ctx)
	err := conn.QueryRow(ctx, `SELECT COALESCE(max(seq),0) FROM runtime_context_artifacts WHERE agent_id=$1`, agentID).Scan(&artifactHead)
	if err == nil {
		err = conn.QueryRow(ctx, `SELECT COALESCE(max(e.seq),0) FROM runtime_session_events e JOIN runtime_runs r ON r.id=e.run_id WHERE e.session_id=$1 AND `+contextEventVisibility, sessionID, attemptID, runID, contextHead, acceptanceHead).Scan(&eventHead)
	}
	return artifactHead, eventHead, err
}

// SearchArtifactCandidate holds a candidate context artifact row for search scanning.
type SearchArtifactCandidate struct {
	Seq   int64
	RunID string
	Kind  string
	Key   string
}

// ListVisibleSearchArtifactCandidates returns a page of searchable context artifact candidates.
func (r *Repository) ListVisibleSearchArtifactCandidates(ctx context.Context, agentID, attemptID, runID string, contextHead, acceptanceHead, after, artifactHead int64, limit int) ([]SearchArtifactCandidate, error) {
	rows, err := r.db.Conn(ctx).Query(ctx, `SELECT a.seq,a.run_id::text,a.kind,a.artifact_key FROM runtime_context_artifacts a JOIN runtime_runs r ON r.id=a.run_id WHERE a.agent_id=$1 AND `+contextArtifactVisibility+` AND a.source_event_seq=0 AND a.tool_name<>'context_source_index' AND a.seq>$6 AND a.seq<=$7 ORDER BY a.seq LIMIT $8`, agentID, attemptID, runID, contextHead, acceptanceHead, after, artifactHead, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	candidates := []SearchArtifactCandidate{}
	for rows.Next() {
		var item SearchArtifactCandidate
		if err = rows.Scan(&item.Seq, &item.RunID, &item.Kind, &item.Key); err != nil {
			return nil, err
		}
		candidates = append(candidates, item)
	}
	return candidates, rows.Err()
}

// GetUploadedFileToolObservation queries the agent_id, request_hash, and observation response for an im_upload_file call.
func (r *Repository) GetUploadedFileToolObservation(ctx context.Context, runID, call, obsKey string) (string, string, []byte, bool, error) {
	var agentID, hash string
	var raw []byte
	err := r.db.Conn(ctx).QueryRow(ctx, `SELECT r.agent_id,t.request_hash,o.response FROM runtime_tool_calls t JOIN runtime_runs r ON r.id=t.run_id JOIN runtime_tool_observations o ON o.run_id=t.run_id AND o.call_id=t.call_id AND o.observation_key=$3 WHERE t.run_id=$1 AND t.call_id=$2 AND t.name='im_upload_file'`, runID, call, obsKey).Scan(&agentID, &hash, &raw)
	if postgres.IsNoRows(err) {
		return "", "", nil, false, nil
	}
	return agentID, hash, raw, err == nil, err
}

// GetToolCallObservationWithMeta queries request_hash, name, and observation response for (runID, call, obsKey).
func (r *Repository) GetToolCallObservationWithMeta(ctx context.Context, runID, call, obsKey string) (string, string, []byte, error) {
	var hash, actualName string
	var raw []byte
	err := r.db.Conn(ctx).QueryRow(ctx, `SELECT t.request_hash,t.name,o.response FROM runtime_tool_calls t JOIN runtime_tool_observations o ON o.run_id=t.run_id AND o.call_id=t.call_id AND o.observation_key=$3 WHERE t.run_id=$1 AND t.call_id=$2`, runID, call, obsKey).Scan(&hash, &actualName, &raw)
	return hash, actualName, raw, err
}

// AcceptedToolCallCandidate holds a committed tool call candidate from an accepted or current Run.
type AcceptedToolCallCandidate = domain.AcceptedToolCallCandidate

// ListAcceptedToolCallCandidates lists committed tool calls matching (agentID, callID, names) in current or accepted runs.
func (r *Repository) ListAcceptedToolCallCandidates(ctx context.Context, agentID, callID string, names []string, runID string, acceptanceHead int64, originRun string) ([]AcceptedToolCallCandidate, error) {
	rows, err := r.db.Conn(ctx).Query(ctx, `SELECT t.run_id::text,t.name,t.result FROM runtime_tool_calls t JOIN runtime_runs owner ON owner.id=t.run_id WHERE owner.agent_id=$1 AND t.call_id=$2 AND t.name=ANY($3::text[]) AND t.result IS NOT NULL AND ($6='' OR owner.id::text=$6) AND (owner.id=$4 OR EXISTS(SELECT 1 FROM runtime_session_acceptances x WHERE x.agent_id=owner.agent_id AND x.run_id=owner.id AND x.seq<=$5))`, agentID, callID, names, runID, acceptanceHead, originRun)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AcceptedToolCallCandidate
	for rows.Next() {
		var item AcceptedToolCallCandidate
		if err = rows.Scan(&item.RunID, &item.Name, &item.Raw); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

// ListVisibleToolReductionRefs lists visible tool_trunc/tool_clear context artifact refs for (toolName, callID).
func (r *Repository) ListVisibleToolReductionRefs(ctx context.Context, agentID, attemptID, runID string, contextHead, acceptanceHead int64, toolName, callID, originRun string) ([]string, error) {
	rows, err := r.db.Conn(ctx).Query(ctx, `SELECT a.run_id::text,a.artifact_key FROM runtime_context_artifacts a JOIN runtime_runs r ON r.id=a.run_id WHERE a.agent_id=$1 AND `+contextArtifactVisibility+` AND a.kind IN ('tool_trunc','tool_clear') AND a.tool_name=$6 AND a.tool_call_id=$7 AND ($8='' OR a.run_id::text=$8)`, agentID, attemptID, runID, contextHead, acceptanceHead, toolName, callID, originRun)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	candidates := []string{}
	for rows.Next() {
		var rID, key string
		if err = rows.Scan(&rID, &key); err != nil {
			return nil, err
		}
		candidates = append(candidates, "context:v1:"+rID+":"+key)
	}
	return candidates, rows.Err()
}

// GetRunContextBudget returns the raw context_budget JSON for runID.
func (r *Repository) GetRunContextBudget(ctx context.Context, runID string) ([]byte, error) {
	var raw []byte
	err := r.db.Conn(ctx).QueryRow(ctx, `SELECT context_budget FROM runtime_runs WHERE id=$1`, runID).Scan(&raw)
	return raw, err
}

// SeqPayload holds a sequence number and serialized event payload.
type SeqPayload struct {
	Seq     int64
	Payload []byte
}

// ListVisibleRunEventPayloads returns a page of visible session event payloads for targetRunID in (after, eventHead].
func (r *Repository) ListVisibleRunEventPayloads(ctx context.Context, sessionID, attemptID, activeRunID string, contextHead, acceptanceHead int64, targetRunID string, after, eventHead int64) ([]SeqPayload, error) {
	rows, err := r.db.Conn(ctx).Query(ctx, `SELECT e.seq,e.payload FROM runtime_session_events e JOIN runtime_runs r ON r.id=e.run_id WHERE e.session_id=$1 AND `+contextEventVisibility+` AND e.run_id=$6 AND e.seq>$7 AND e.seq<=$8 ORDER BY e.seq LIMIT 64`, sessionID, attemptID, activeRunID, contextHead, acceptanceHead, targetRunID, after, eventHead)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SeqPayload
	for rows.Next() {
		var item SeqPayload
		if err = rows.Scan(&item.Seq, &item.Payload); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

// ListMatchingToolCallReceipts queries tool call receipts matching (agentID, callID, names, hashes) in current or accepted runs.
func (r *Repository) ListMatchingToolCallReceipts(ctx context.Context, agentID, callID string, names, hashes []string, runID string, acceptanceHead int64, currentOnly bool) ([]AcceptedToolCallCandidate, error) {
	rows, err := r.db.Conn(ctx).Query(ctx, `SELECT t.run_id::text,t.name,t.result FROM runtime_tool_calls t JOIN runtime_runs r ON r.id=t.run_id WHERE r.agent_id=$1 AND t.call_id=$2 AND t.name=ANY($3::text[]) AND t.request_hash=ANY($4::text[]) AND t.result IS NOT NULL AND (r.id=$5 OR EXISTS(SELECT 1 FROM runtime_session_acceptances x WHERE x.agent_id=r.agent_id AND x.run_id=r.id AND x.seq<=$6)) AND (NOT $7::boolean OR r.id=$5)`, agentID, callID, names, hashes, runID, acceptanceHead, currentOnly)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AcceptedToolCallCandidate
	for rows.Next() {
		var item AcceptedToolCallCandidate
		if err = rows.Scan(&item.RunID, &item.Name, &item.Raw); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

// GetAcceptedInputPayload returns the raw input payload and owning run_id for (agentID, eventID) if visible under (runID, acceptanceHead).
func (r *Repository) GetAcceptedInputPayload(ctx context.Context, agentID, eventID, runID string, acceptanceHead int64) ([]byte, string, bool, error) {
	var raw []byte
	var ownerRunID string
	err := r.db.Conn(ctx).QueryRow(ctx, `SELECT i.payload,i.run_id::text FROM runtime_inputs i JOIN runtime_runs owner ON owner.id=i.run_id WHERE i.agent_id=$1 AND i.event_id=$2 AND (owner.id=$3 OR EXISTS(SELECT 1 FROM runtime_session_acceptances x WHERE x.agent_id=i.agent_id AND x.run_id=owner.id AND x.seq<=$4))`, agentID, eventID, runID, acceptanceHead).Scan(&raw, &ownerRunID)
	if postgres.IsNoRows(err) {
		return nil, "", false, nil
	}
	return raw, ownerRunID, err == nil, err
}

// RetrievalObservationRow holds a committed context retrieval tool call and its observation payload.
type RetrievalObservationRow struct {
	OriginRunID string
	Receipt     []byte
	Observation []byte
}

// ListAcceptedRetrievalObservations lists committed context_read/context_search tool calls and observations.
func (r *Repository) ListAcceptedRetrievalObservations(ctx context.Context, agentID, callID, toolName, runID string, acceptanceHead int64) ([]RetrievalObservationRow, error) {
	rows, err := r.db.Conn(ctx).Query(ctx, `SELECT t.run_id::text,t.result,o.response FROM runtime_tool_calls t JOIN runtime_runs owner ON owner.id=t.run_id JOIN runtime_tool_observations o ON o.run_id=t.run_id AND o.call_id=t.call_id AND o.observation_key='context-observation-v1' WHERE owner.agent_id=$1 AND t.call_id=$2 AND t.name=$3 AND t.result IS NOT NULL AND (owner.id=$4 OR EXISTS(SELECT 1 FROM runtime_session_acceptances x WHERE x.agent_id=owner.agent_id AND x.run_id=owner.id AND x.seq<=$5))`, agentID, callID, toolName, runID, acceptanceHead)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RetrievalObservationRow
	for rows.Next() {
		var item RetrievalObservationRow
		if err = rows.Scan(&item.OriginRunID, &item.Receipt, &item.Observation); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

// MessageSourceArtifactRow holds a message source index artifact row.
type MessageSourceArtifactRow struct {
	Key       string
	Sources   []byte
	Inherited []byte
}

// ListVisibleMessageSourceArtifacts queries visible kind='message' context artifacts matching keys.
func (r *Repository) ListVisibleMessageSourceArtifacts(ctx context.Context, agentID, attemptID, runID string, contextHead, acceptanceHead int64, keys []string) ([]MessageSourceArtifactRow, error) {
	rows, err := r.db.Conn(ctx).Query(ctx, `SELECT a.artifact_key,a.sources,a.source_artifact_refs FROM runtime_context_artifacts a JOIN runtime_runs r ON r.id=a.run_id WHERE a.agent_id=$1 AND `+contextArtifactVisibility+` AND a.kind='message' AND a.artifact_key=ANY($6::text[])`, agentID, attemptID, runID, contextHead, acceptanceHead, keys)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []MessageSourceArtifactRow
	for rows.Next() {
		var item MessageSourceArtifactRow
		if err = rows.Scan(&item.Key, &item.Sources, &item.Inherited); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

// ListVisibleWantedSessionEvents queries visible session events containing any wanted message ID (or all if needsLegacy).
func (r *Repository) ListVisibleWantedSessionEvents(ctx context.Context, sessionID, attemptID, runID string, contextHead, acceptanceHead int64, needsLegacy bool, ids []string, after int64) ([]ArchiveSessionEventRow, error) {
	rows, err := r.db.Conn(ctx).Query(ctx, `SELECT e.seq,e.run_id::text,e.attempt_id::text,e.event_id,e.payload FROM runtime_session_events e JOIN runtime_runs r ON r.id=e.run_id WHERE e.session_id=$1 AND `+contextEventVisibility+` AND ($6 OR EXISTS(SELECT 1 FROM unnest($7::text[]) wanted(id) WHERE position(wanted.id in convert_from(e.payload,'UTF8'))>0)) AND e.seq>$8 ORDER BY e.seq LIMIT 64`, sessionID, attemptID, runID, contextHead, acceptanceHead, needsLegacy, ids, after)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ArchiveSessionEventRow
	for rows.Next() {
		var item ArchiveSessionEventRow
		if err = rows.Scan(&item.Seq, &item.RunID, &item.Attempt, &item.EventID, &item.Payload); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

// ListRunToolReductionArtifactKeys lists artifact_keys for tool_clear/tool_trunc artifacts matching (agentID, runID, callID, toolName).
func (r *Repository) ListRunToolReductionArtifactKeys(ctx context.Context, agentID, runID, callID, toolName string) ([]string, error) {
	rows, err := r.db.Conn(ctx).Query(ctx, `SELECT artifact_key FROM runtime_context_artifacts WHERE agent_id=$1 AND run_id=$2 AND tool_call_id=$3 AND tool_name=$4 AND kind IN ('tool_clear','tool_trunc')`, agentID, runID, callID, toolName)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	keys := []string{}
	for rows.Next() {
		var key string
		if err = rows.Scan(&key); err != nil {
			return nil, err
		}
		keys = append(keys, key)
	}
	return keys, rows.Err()
}

// GetSessionEventPayloadBySeq returns the raw payload for a session event at (sessionID, runID, seq).
func (r *Repository) GetSessionEventPayloadBySeq(ctx context.Context, sessionID, runID string, seq int64) ([]byte, error) {
	var raw []byte
	err := r.db.Conn(ctx).QueryRow(ctx, `SELECT payload FROM runtime_session_events WHERE session_id=$1 AND run_id=$2 AND seq=$3`, sessionID, runID, seq).Scan(&raw)
	return raw, err
}

// HasLegacyWorkspaceToolActivity checks whether any workspace tool calls occurred in or before ownerRunID's visible history.
func (r *Repository) HasLegacyWorkspaceToolActivity(ctx context.Context, agentID, ownerRunID string, createdAt time.Time, names []string, sessionID string) (bool, error) {
	var touched bool
	err := r.db.Conn(ctx).QueryRow(ctx, `SELECT EXISTS(
 SELECT 1 FROM runtime_tool_calls t JOIN runtime_runs origin ON origin.id=t.run_id JOIN runtime_runs owner ON owner.id=$2
 WHERE origin.agent_id=$1 AND t.created_at<=$3 AND t.name=ANY($4::text[]) AND
 (origin.id=owner.id OR EXISTS(SELECT 1 FROM runtime_session_events e
 WHERE e.session_id=$5 AND e.run_id=origin.id AND e.seq<=owner.context_head
 AND EXISTS(SELECT 1 FROM runtime_session_acceptances x WHERE x.agent_id=$1 AND x.attempt_id=e.attempt_id AND x.seq<=owner.acceptance_head))))`, agentID, ownerRunID, createdAt, names, sessionID).Scan(&touched)
	return touched, err
}

// GetAgentToolCallMeta returns request_hash, name, created_at, and result for a tool call owned by agentID.
func (r *Repository) GetAgentToolCallMeta(ctx context.Context, agentID, runID, callID string) (string, string, time.Time, []byte, error) {
	var hash, actualName string
	var result []byte
	var cutoff time.Time
	err := r.db.Conn(ctx).QueryRow(ctx, `SELECT t.request_hash,t.name,t.created_at,t.result FROM runtime_tool_calls t JOIN runtime_runs owner ON owner.id=t.run_id WHERE owner.agent_id=$1 AND t.run_id=$2 AND t.call_id=$3`, agentID, runID, callID).Scan(&hash, &actualName, &cutoff, &result)
	return hash, actualName, cutoff, result, err
}

// WorkspaceWriterRow holds a prior workspace writer tool call and its optional source snapshot observation.
type WorkspaceWriterRow struct {
	Run      string
	Call     string
	Name     string
	Hash     string
	Result   []byte
	Snapshot []byte
	At       time.Time
}

// ListPriorWorkspaceWriters lists workspace writer tool calls for agentID created at or before cutoff.
func (r *Repository) ListPriorWorkspaceWriters(ctx context.Context, agentID string, cutoff time.Time, names []string, excludeRunID, excludeCallID, obsKey string) ([]WorkspaceWriterRow, error) {
	rows, err := r.db.Conn(ctx).Query(ctx, `SELECT t.run_id::text,t.call_id,t.name,t.request_hash,t.result,t.created_at,o.response FROM runtime_tool_calls t JOIN runtime_runs owner ON owner.id=t.run_id LEFT JOIN runtime_tool_observations o ON o.run_id=t.run_id AND o.call_id=t.call_id AND o.observation_key=$6 WHERE owner.agent_id=$1 AND t.created_at<=$2 AND t.name=ANY($3::text[]) AND (t.run_id<>$4 OR t.call_id<>$5) ORDER BY t.created_at,t.run_id,t.call_id`, agentID, cutoff, names, excludeRunID, excludeCallID, obsKey)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	writers := []WorkspaceWriterRow{}
	for rows.Next() {
		var w WorkspaceWriterRow
		if err = rows.Scan(&w.Run, &w.Call, &w.Name, &w.Hash, &w.Result, &w.At, &w.Snapshot); err != nil {
			return nil, err
		}
		writers = append(writers, w)
	}
	return writers, rows.Err()
}

// GetTaskOriginBeforeCutoff returns run_id and created_at for a task created at or before cutoff.
func (r *Repository) GetTaskOriginBeforeCutoff(ctx context.Context, agentID, taskID string, cutoff time.Time) (string, time.Time, error) {
	var origin string
	var created time.Time
	err := r.db.Conn(ctx).QueryRow(ctx, `SELECT run_id::text,created_at FROM runtime_tasks WHERE agent_id=$1 AND id=$2 AND run_id IS NOT NULL AND created_at<=$3`, agentID, taskID, cutoff).Scan(&origin, &created)
	return origin, created, err
}

// GetRunHeads returns attempt_id, context_head, and acceptance_head for (runID, agentID).
func (r *Repository) GetRunHeads(ctx context.Context, runID, agentID string) (string, int64, int64, error) {
	var attempt string
	var contextHead, acceptanceHead int64
	err := r.db.Conn(ctx).QueryRow(ctx, `SELECT attempt_id::text,context_head,acceptance_head FROM runtime_runs WHERE id=$1 AND agent_id=$2`, runID, agentID).Scan(&attempt, &contextHead, &acceptanceHead)
	return attempt, contextHead, acceptanceHead, err
}

// ListRunToolCallsBeforeCutoff returns committed tool calls for runID created at or before cutoff.
func (r *Repository) ListRunToolCallsBeforeCutoff(ctx context.Context, runID string, cutoff time.Time) ([]ToolCallResultRecord, error) {
	rows, err := r.db.Conn(ctx).Query(ctx, `SELECT call_id,name,result FROM runtime_tool_calls WHERE run_id=$1 AND created_at<=$2 AND result IS NOT NULL ORDER BY created_at,call_id`, runID, cutoff)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ToolCallResultRecord
	for rows.Next() {
		var item ToolCallResultRecord
		if err = rows.Scan(&item.CallID, &item.Name, &item.Result); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

// GetArtifactProvenanceRaw returns raw sources and source_artifact_refs JSON for (agentID, runID, key).
func (r *Repository) GetArtifactProvenanceRaw(ctx context.Context, agentID, runID, key string) ([]byte, []byte, error) {
	var sources, inherited []byte
	err := r.db.Conn(ctx).QueryRow(ctx, `SELECT sources,source_artifact_refs FROM runtime_context_artifacts WHERE agent_id=$1 AND run_id=$2 AND artifact_key=$3`, agentID, runID, key).Scan(&sources, &inherited)
	return sources, inherited, err
}

// IsValidWorkspaceSourceSnapshotArtifact checks whether (agentID, runID, key) is an empty workspace_source_snapshot message artifact.
func (r *Repository) IsValidWorkspaceSourceSnapshotArtifact(ctx context.Context, agentID, runID, key string) (bool, error) {
	var valid bool
	err := r.db.Conn(ctx).QueryRow(ctx, `SELECT kind='message' AND tool_name='workspace_source_snapshot' AND octet_length(content)=0 FROM runtime_context_artifacts WHERE agent_id=$1 AND run_id=$2 AND artifact_key=$3`, agentID, runID, key).Scan(&valid)
	return valid, err
}

// HasUnknownLegacyWorkspaceWriterView checks whether runID has context_head=0 despite prior session events.
func (r *Repository) HasUnknownLegacyWorkspaceWriterView(ctx context.Context, agentID, runID string) (bool, error) {
	var unknown bool
	err := r.db.Conn(ctx).QueryRow(ctx, `SELECT owner.context_head=0 AND EXISTS(SELECT 1 FROM runtime_session_events e JOIN runtime_runs prior ON prior.id=e.run_id WHERE prior.agent_id=$1 AND prior.id<>owner.id AND prior.started_at<=owner.started_at) FROM runtime_runs owner WHERE owner.id=$2 AND owner.agent_id=$1`, agentID, runID).Scan(&unknown)
	return unknown, err
}

// RunEventPayloadRow holds a run_id and serialized session event payload.
type RunEventPayloadRow struct {
	RunID   string
	Payload []byte
}

// ListVisibleMessageUpdatedEventsForCall lists visible message_updated session events containing callID.
func (r *Repository) ListVisibleMessageUpdatedEventsForCall(ctx context.Context, sessionID, attemptID, runID string, contextHead, acceptanceHead int64, originRun, callID string) ([]RunEventPayloadRow, error) {
	rows, err := r.db.Conn(ctx).Query(ctx, `SELECT e.run_id::text,e.payload FROM runtime_session_events e JOIN runtime_runs r ON r.id=e.run_id WHERE e.session_id=$1 AND `+contextEventVisibility+` AND e.kind='message_updated' AND ($6='' OR e.run_id::text=$6) AND position($7 in convert_from(e.payload,'UTF8'))>0`, sessionID, attemptID, runID, contextHead, acceptanceHead, originRun, callID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RunEventPayloadRow
	for rows.Next() {
		var item RunEventPayloadRow
		if err = rows.Scan(&item.RunID, &item.Payload); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}
