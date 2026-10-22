package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/adk/middlewares/reduction"
	"github.com/cloudwego/eino/schema"
	"github.com/jackc/pgx/v5"
)

const contextArchiveRefKey = "teamie_context_archive_ref"

func contextArgumentHashes(name, argument string) []string {
	hashes := []string{digest([]byte(argument))}
	// IM receipts hash their parsed typed arguments, while native receipts hash
	// the original wire. Reproduce only these known schemas for source lookup;
	// no receipt or request fingerprint is changed.
	var parsers []any
	switch name {
	case "im_list_conversations":
		parsers = []any{&listConversationsArgs{}}
	case "im_list_messages":
		parsers = []any{&listMessagesArgs{}}
	case "im_get_message":
		parsers = []any{&getMessageArgs{}}
	case "im_send_message":
		parsers = []any{&sendArgs{}, &fileMessageArgs{}, &mentionedSendArgs{}}
	case "im_get_attachment":
		parsers = []any{&getAttachmentArgs{}}
	case "im_upload_file":
		parsers = []any{&uploadFileArgs{}}
	case "web_search":
		parsers = []any{&webSearchArgs{}}
	case "web_fetch":
		parsers = []any{&webFetchArgs{}}
	case "runtime_clock":
		parsers = []any{&runtimeClockArgs{}}
	case "schedule_create":
		parsers = []any{&scheduleCreateArgs{}}
	case "schedule_list":
		parsers = []any{&scheduleListArgs{}}
	case "schedule_cancel":
		parsers = []any{&scheduleCancelArgs{}}
	}
	for _, parsed := range parsers {
		if json.Unmarshal([]byte(argument), parsed) == nil {
			canonical, _ := json.Marshal(parsed)
			hashes = append(hashes, digest(canonical))
		}
	}
	return hashes
}

func canonicalContextToolText(text string) string {
	decoder := json.NewDecoder(strings.NewReader(text))
	decoder.UseNumber()
	var value any
	if decoder.Decode(&value) == nil {
		var extra any
		if decoder.Decode(&extra) == io.EOF {
			raw, _ := json.Marshal(value)
			return string(raw)
		}
	}
	return text
}

func contextReceiptMatchesTool(raw []byte, name string, detail *reduction.ToolDetail) bool {
	if detail.ToolResult == nil {
		return false
	}
	wanted := []string{}
	for _, part := range detail.ToolResult.Parts {
		if part.Type != schema.ToolPartTypeText {
			return false
		}
		wanted = append(wanted, canonicalContextToolText(part.Text))
	}
	actual := []string{}
	if strings.HasPrefix(name, "native:") {
		var text string
		if json.Unmarshal(raw, &text) == nil {
			actual = append(actual, canonicalContextToolText(text))
		} else {
			var result schema.ToolResult
			if json.Unmarshal(raw, &result) != nil {
				return false
			}
			for _, part := range result.Parts {
				if part.Type != schema.ToolPartTypeText {
					return false
				}
				actual = append(actual, canonicalContextToolText(part.Text))
			}
		}
	} else {
		actual = append(actual, canonicalContextToolText(string(raw)))
	}
	x, _ := json.Marshal(wanted)
	y, _ := json.Marshal(actual)
	return bytes.Equal(x, y)
}

func (a *ContextArchive) toolSources(ctx context.Context, r *Run, detail *reduction.ToolDetail, currentOnly bool) ([]InputSource, error) {
	if detail == nil || detail.ToolContext == nil || detail.ToolContext.Name == "" || detail.ToolContext.CallID == "" || detail.ToolArgument == nil {
		return nil, errContextUnavailable
	}
	name, call := detail.ToolContext.Name, detail.ToolContext.CallID
	hashes := contextArgumentHashes(name, detail.ToolArgument.Text)
	rows, err := a.Store.DB.Query(ctx, `SELECT t.run_id::text,t.name,t.result FROM runtime_tool_calls t JOIN runtime_runs r ON r.id=t.run_id WHERE r.agent_id=$1 AND t.call_id=$2 AND t.name=ANY($3::text[]) AND t.request_hash=ANY($4::text[]) AND t.result IS NOT NULL AND (r.id=$5 OR EXISTS(SELECT 1 FROM runtime_session_acceptances x WHERE x.agent_id=r.agent_id AND x.run_id=r.id AND x.seq<=$6)) AND (NOT $7::boolean OR r.id=$5)`, r.AgentID, call, []string{name, "native:" + name}, hashes, r.ID, r.AcceptanceHead, currentOnly)
	if err != nil {
		return nil, err
	}
	type candidate struct {
		id, name string
		raw      []byte
	}
	matches := []candidate{}
	for rows.Next() {
		var id, receiptName string
		var result []byte
		if err = rows.Scan(&id, &receiptName, &result); err != nil {
			rows.Close()
			return nil, err
		}
		if contextReceiptMatchesTool(result, receiptName, detail) {
			matches = append(matches, candidate{id, receiptName, result})
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	if len(matches) == 0 {
		return nil, errContextUnavailable
	}
	refs := []InputSource{}
	for _, match := range matches {
		sources, e := a.runSources(ctx, match.id)
		if e != nil {
			return nil, e
		}
		refs = append(refs, sources...)
		if contextIMTool(name) {
			// IM owns its typed receipt. A generic native envelope alone cannot
			// supply authenticated message provenance.
			if match.name != name {
				return nil, errContextUnavailable
			}
			actual, err := a.imReceiptSources(ctx, match.id, call, name, match.raw)
			if err != nil {
				return nil, err
			}
			refs = append(refs, actual...)
		} else if workspaceTool(name) {
			actual, err := a.workspaceToolSources(ctx, r, match.id, call, match.name, []byte(detail.ToolArgument.Text))
			if err != nil {
				return nil, err
			}
			refs = append(refs, actual...)
		}
	}
	return canonicalContextSources(refs)
}

// ArchiveToolPath binds only actual receipt candidates for this native tool
// identity. Reused provider call IDs with different arguments do not acquire an
// unrelated Run's sources. Ambiguous matching receipts retain every candidate.
func (a *ContextArchive) ArchiveToolPath(ctx context.Context, phase string, detail *reduction.ToolDetail, content string) (string, error) {
	r, err := a.scope(ctx)
	if err != nil {
		return "", err
	}
	kind := phase
	if phase == "trunc" || phase == "clear" {
		kind = "tool_" + phase
	}
	if kind != "tool_trunc" && kind != "tool_clear" {
		return "", errContextArguments
	}
	refs, err := a.toolSources(ctx, r, detail, kind == "tool_trunc")
	if err != nil {
		return "", err
	}
	key := "sources-v1/" + detail.ToolContext.Name + "/" + detail.ToolContext.CallID + "/" + digest([]byte(detail.ToolArgument.Text)) + "/" + digest([]byte(content))
	return a.put(ctx, r, contextArtifact{RunID: r.ID, Attempt: r.Attempt, Key: key, Kind: kind, ToolName: detail.ToolContext.Name, ToolCallID: detail.ToolContext.CallID, Text: content, Sources: refs})
}

func contextNativeMessageID(message *schema.AgenticMessage) string {
	if message == nil {
		return ""
	}
	id, _ := message.Extra["_eino_msg_id"].(string)
	return id
}

func contextProjectionBytes(message *schema.AgenticMessage) []byte {
	projection := contextTranscriptProjection(message)
	if projection == nil {
		return nil
	}
	raw, _ := json.Marshal(projection)
	return raw
}

type contextSourceTarget struct {
	id, key, hash string
	text          []byte
	message       *schema.AgenticMessage
	refs          []InputSource
	updateRefs    []InputSource
	parents       []string
	found         bool
}

func contextSourceTargetKey(id, hash string) string { return "origin/sources-v1/" + id + "/" + hash }

func (a *ContextArchive) inputMessageSources(ctx context.Context, r *Run, message *schema.AgenticMessage) ([]InputSource, bool, error) {
	if message.Role != schema.AgenticRoleTypeUser || hasToolResults(message) {
		return nil, false, nil
	}
	text := agenticText(message)
	prefix := "TEAMIE_NEW_MESSAGE "
	if strings.HasPrefix(text, "TEAMIE_TASK_NOTIFICATION ") {
		prefix = "TEAMIE_TASK_NOTIFICATION "
	} else if strings.HasPrefix(text, "TEAMIE_SCHEDULE_WAKEUP ") {
		prefix = "TEAMIE_SCHEDULE_WAKEUP "
	}
	if !strings.HasPrefix(text, prefix) {
		return nil, false, nil
	}
	var input Input
	if json.Unmarshal([]byte(strings.TrimPrefix(text, prefix)), &input) != nil || len(input.EventID) == 0 || len(input.EventID) > 30 {
		return nil, false, nil
	}
	for _, c := range input.EventID {
		if c < '0' || c > '9' {
			return nil, false, nil
		}
	}
	var raw []byte
	var runID string
	err := a.Store.DB.QueryRow(ctx, `SELECT i.payload,i.run_id::text FROM runtime_inputs i JOIN runtime_runs owner ON owner.id=i.run_id WHERE i.agent_id=$1 AND i.event_id=$2 AND (owner.id=$3 OR EXISTS(SELECT 1 FROM runtime_session_acceptances x WHERE x.agent_id=i.agent_id AND x.run_id=owner.id AND x.seq<=$4))`, r.AgentID, input.EventID, r.ID, r.AcceptanceHead).Scan(&raw, &runID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	var actual Input
	if json.Unmarshal(raw, &actual) != nil {
		return nil, false, ErrReplayConflict
	}
	x, _ := json.Marshal(actual)
	y, _ := json.Marshal(input)
	if !bytes.Equal(x, y) {
		return nil, false, nil
	}
	refs, overflow := inputSources([]Input{actual})
	if overflow || actual.Kind == "task.notification" || actual.Kind == "schedule.wakeup" {
		refs, err = a.runSources(ctx, runID)
	} else {
		refs, err = canonicalContextSources(refs)
	}
	if err == nil && len(refs) == 0 {
		err = errContextUnavailable
	}
	return refs, true, err
}

// Retrieval text must not be archived again under the later Run's audience.
// Only an exact native result linked to its immutable server observation can
// add parent refs; neither model text nor a copied ref string grants scope.
func (a *ContextArchive) transcriptRetrievalProvenance(ctx context.Context, r *Run, messages []*schema.AgenticMessage) ([]InputSource, []string, error) {
	parents := []string{}
	sources := []InputSource{}
	budget, budgetErr := ParseContextBudgetSnapshot(r.ContextBudget)
	bindEmptyResults := budgetErr == nil && budget.Version >= 4
	for _, message := range messages {
		if message == nil {
			continue
		}
		for _, block := range message.ContentBlocks {
			if block == nil || block.FunctionToolResult == nil {
				continue
			}
			result := block.FunctionToolResult
			if result.Name != "context_read" && result.Name != "context_search" {
				continue
			}
			if result.CallID == "" || len(result.Content) != 1 || result.Content[0] == nil || result.Content[0].Type != schema.FunctionToolResultContentBlockTypeText || result.Content[0].Text == nil {
				return nil, nil, errContextUnavailable
			}
			wire := canonicalContextToolText(result.Content[0].Text.Text)
			rows, err := a.Store.DB.Query(ctx, `SELECT t.run_id::text,t.result,o.response FROM runtime_tool_calls t JOIN runtime_runs owner ON owner.id=t.run_id JOIN runtime_tool_observations o ON o.run_id=t.run_id AND o.call_id=t.call_id AND o.observation_key='context-observation-v1' WHERE owner.agent_id=$1 AND t.call_id=$2 AND t.name=$3 AND t.result IS NOT NULL AND (owner.id=$4 OR EXISTS(SELECT 1 FROM runtime_session_acceptances x WHERE x.agent_id=owner.agent_id AND x.run_id=owner.id AND x.seq<=$5))`, r.AgentID, result.CallID, "native:"+result.Name, r.ID, r.AcceptanceHead)
			if err != nil {
				return nil, nil, err
			}
			matched := false
			emptyOrigins := map[string]bool{}
			for rows.Next() {
				var receipt, raw []byte
				var origin string
				if err = rows.Scan(&origin, &receipt, &raw); err != nil {
					rows.Close()
					return nil, nil, err
				}
				var observation contextToolObservation
				if json.Unmarshal(raw, &observation) != nil || len(observation.Wire) > contextResponseMaximum || !json.Valid([]byte(observation.Wire)) {
					rows.Close()
					return nil, nil, ErrReplayConflict
				}
				expected, _ := json.Marshal(observation.Wire)
				if !bytes.Equal(receipt, expected) {
					rows.Close()
					return nil, nil, ErrReplayConflict
				}
				if canonicalContextToolText(observation.Wire) == wire {
					matched = true
					parents = append(parents, observation.Refs...)
					if bindEmptyResults && len(observation.Refs) == 0 {
						emptyOrigins[origin] = true
					}
				}
			}
			err = rows.Err()
			rows.Close()
			if err != nil {
				return nil, nil, err
			}
			if !matched {
				return nil, nil, errContextUnavailable
			}
			// A committed empty/error retrieval contains no quoted data parent.
			// Bind it to the authenticated receipt's actual request sources so
			// it remains compactable without granting an unobserved data source.
			for origin := range emptyOrigins {
				refs, err := a.runSources(ctx, origin)
				if err != nil {
					return nil, nil, err
				}
				sources = append(sources, refs...)
			}
		}
	}
	return sources, parents, nil
}

func (a *ContextArchive) transcriptSources(ctx context.Context, r *Run, messages []*schema.AgenticMessage, parents *[]string, refs []InputSource) ([]InputSource, error) {
	targets := map[string]*contextSourceTarget{}
	for _, message := range messages {
		projection := contextProjectionBytes(message)
		if projection == nil {
			continue
		}
		// Only application-created user metadata carries a source closure. Model
		// output, source bodies and copied textual markers cannot create this.
		if message.Role == schema.AgenticRoleTypeUser {
			if ref, ok := message.Extra[contextArchiveRefKey].(string); ok {
				_, err := a.load(ctx, r, ref)
				if err != nil {
					return nil, err
				}
				*parents = append(*parents, ref)
				continue
			}
		}
		if inputRefs, found, err := a.inputMessageSources(ctx, r, message); err != nil {
			return nil, err
		} else if found {
			refs = append(refs, inputRefs...)
			continue
		}
		if actual, found, err := a.toolMessageSources(ctx, r, message, ""); err != nil {
			return nil, err
		} else if found {
			refs = append(refs, actual...)
			continue
		}
		hash, id := digest(projection), contextNativeMessageID(message)
		key := contextSourceTargetKey(id, hash)
		targets[key] = &contextSourceTarget{id: id, key: key, hash: hash, text: projection, message: message}
	}
	if len(targets) == 0 {
		if len(refs) == 0 && len(*parents) == 0 {
			return nil, errContextUnavailable
		}
		return canonicalContextSources(refs)
	}
	// Fast immutable source index: messages retained by a prior compaction do
	// not cause their history/source set to be scanned and copied again.
	keys := []string{}
	for _, target := range targets {
		keys = append(keys, "message:"+digest([]byte(target.key)))
	}
	rows, err := a.Store.DB.Query(ctx, `SELECT a.artifact_key,a.sources,a.source_artifact_refs FROM runtime_context_artifacts a JOIN runtime_runs r ON r.id=a.run_id WHERE a.agent_id=$1 AND `+contextArtifactVisibility+` AND a.kind='message' AND a.artifact_key=ANY($6::text[])`, r.AgentID, r.Attempt, r.ID, r.ContextHead, r.AcceptanceHead, keys)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var key string
		var sources, inherited []byte
		if err = rows.Scan(&key, &sources, &inherited); err != nil {
			rows.Close()
			return nil, err
		}
		for _, target := range targets {
			if key == "message:"+digest([]byte(target.key)) {
				var found []InputSource
				var parentRefs []string
				if json.Unmarshal(sources, &found) != nil || json.Unmarshal(inherited, &parentRefs) != nil {
					rows.Close()
					return nil, ErrReplayConflict
				}
				target.refs, target.found = append(target.refs, found...), true
				target.parents = append(target.parents, parentRefs...)
			}
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	for _, target := range targets {
		if !target.found {
			continue
		}
		// Storage can split this index's direct sources into provenance-only
		// chunks. Recover the same logical source list for archive identity;
		// retain real transcript/retrieval parent refs as DAG edges.
		inherited := []string{}
		for _, ref := range target.parents {
			parent, err := a.load(ctx, r, ref)
			if err != nil {
				return nil, err
			}
			raw, _ := json.Marshal(parent.Sources)
			if parent.Kind == "message" && parent.ToolName == "context_source_index" && parent.Text == "" && len(parent.ArtifactRefs) == 0 && ref == contextRef(parent.RunID, "message", "provenance/"+digest(raw)) {
				target.refs = append(target.refs, parent.Sources...)
			} else {
				inherited = append(inherited, ref)
			}
		}
		target.parents = inherited
	}
	ids := []string{}
	needsLegacy := false
	for _, target := range targets {
		if !target.found {
			if target.id == "" {
				needsLegacy = true
			} else {
				ids = append(ids, target.id)
			}
		}
	}
	if len(ids) > 0 || needsLegacy {
		var after int64
		for {
			rows, err = a.Store.DB.Query(ctx, `SELECT e.seq,e.run_id::text,e.attempt_id::text,e.event_id,e.payload FROM runtime_session_events e JOIN runtime_runs r ON r.id=e.run_id WHERE e.session_id=$1 AND `+contextEventVisibility+` AND ($6 OR EXISTS(SELECT 1 FROM unnest($7::text[]) wanted(id) WHERE position(wanted.id in convert_from(e.payload,'UTF8'))>0)) AND e.seq>$8 ORDER BY e.seq LIMIT 64`, "agent:"+r.AgentID, r.Attempt, r.ID, r.ContextHead, r.AcceptanceHead, needsLegacy, ids, after)
			if err != nil {
				return nil, err
			}
			items := []contextArchiveEvent{}
			for rows.Next() {
				var item contextArchiveEvent
				var payload []byte
				if err = rows.Scan(&item.seq, &item.runID, &item.attempt, &item.id, &payload); err != nil {
					rows.Close()
					return nil, err
				}
				item.event = new(adk.SessionEvent[*schema.AgenticMessage])
				if err = decodeSessionPayload(payload, item.event); err != nil {
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
			for _, item := range items {
				candidates := []*schema.AgenticMessage{item.event.Message}
				if item.event.MessageInserted != nil {
					candidates = append(candidates, item.event.MessageInserted.Message)
				}
				if item.event.MessageUpdated != nil {
					candidates = append(candidates, item.event.MessageUpdated.Message)
				}
				if item.event.MessagesReplaced != nil {
					// Only a legacy generated summary needs this fallback. Ordinary
					// retained messages keep the origin of their original event.
					for _, candidate := range *item.event.MessagesReplaced {
						if candidate != nil && candidate.Role == schema.AgenticRoleTypeUser && strings.HasPrefix(agenticText(candidate), "TEAMIE_SESSION_SUMMARY ") {
							candidates = append(candidates, candidate)
						}
					}
				}
				for _, candidate := range candidates {
					projection := contextProjectionBytes(candidate)
					if projection == nil {
						continue
					}
					for _, target := range targets {
						if target.found || target.hash != digest(projection) || target.id != "" && target.id != contextNativeMessageID(candidate) {
							continue
						}
						var found []InputSource
						updated := item.event.MessageUpdated != nil
						if updated {
							found, err = a.reductionUpdateSources(ctx, r, item, candidate)
							if err != nil {
								continue
							}
						} else if candidate.Role == schema.AgenticRoleTypeUser && strings.HasPrefix(agenticText(candidate), "TEAMIE_SESSION_SUMMARY ") {
							var inherited []string
							found, inherited, err = a.sourcesThrough(ctx, r, item.seq)
							target.parents = append(target.parents, inherited...)
						} else {
							var inherited []string
							found, inherited, err = a.causalMessageSources(ctx, r, item, candidate)
							target.parents = append(target.parents, inherited...)
						}
						if err != nil {
							return nil, err
						}
						if updated {
							target.updateRefs = append(target.updateRefs, found...)
						} else {
							target.refs = append(target.refs, found...)
						}
					}
				}
			}
			if len(items) < contextSearchScan {
				break
			}
			after = items[len(items)-1].seq
		}
	}
	for _, target := range targets {
		if len(target.refs) == 0 {
			target.refs = target.updateRefs
		}
		if len(target.refs) == 0 && len(target.parents) == 0 {
			return nil, errContextUnavailable
		}
		sources, err := canonicalContextSources(target.refs)
		if err != nil {
			return nil, err
		}
		if !target.found {
			if _, err = a.put(ctx, r, contextArtifact{RunID: r.ID, Attempt: r.Attempt, Key: target.key, Kind: "message", ToolName: "context_source_index", Text: string(target.text), Sources: sources, ArtifactRefs: target.parents}); err != nil {
				return nil, err
			}
		}
		refs = append(refs, sources...)
		*parents = append(*parents, target.parents...)
	}
	if len(refs) == 0 && len(*parents) == 0 {
		return nil, errors.New("context source closure is empty")
	}
	return canonicalContextSources(refs)
}

// Ordinary tool adapters may drop ToolOutputPart.Extra. A native persisted
// message_updated event can still be correlated to the immutable reduction
// artifact by its outer call/name, owning Run and exact generated ref notice.
// Text without that server-side event/artifact linkage grants no source scope.
func (a *ContextArchive) reductionUpdateSources(ctx context.Context, r *Run, item contextArchiveEvent, message *schema.AgenticMessage) ([]InputSource, error) {
	refs := []InputSource{}
	for _, block := range message.ContentBlocks {
		if block == nil || block.FunctionToolResult == nil {
			continue
		}
		result := block.FunctionToolResult
		rows, err := a.Store.DB.Query(ctx, `SELECT artifact_key FROM runtime_context_artifacts WHERE agent_id=$1 AND run_id=$2 AND tool_call_id=$3 AND tool_name=$4 AND kind IN ('tool_clear','tool_trunc')`, r.AgentID, item.runID, result.CallID, result.Name)
		if err != nil {
			return nil, err
		}
		keys := []string{}
		for rows.Next() {
			var key string
			if err = rows.Scan(&key); err != nil {
				rows.Close()
				return nil, err
			}
			keys = append(keys, key)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
		for _, key := range keys {
			ref := "context:v1:" + item.runID + ":" + key
			linked := false
			for _, part := range result.Content {
				if part != nil && part.Text != nil && strings.HasPrefix(part.Text.Text, "Archived private context: "+ref) {
					linked = true
				}
			}
			if linked {
				artifact, err := a.load(ctx, r, ref)
				if err != nil {
					return nil, err
				}
				refs = append(refs, artifact.Sources...)
			}
		}
	}
	if len(refs) == 0 {
		return nil, errContextUnavailable
	}
	return canonicalContextSources(refs)
}
