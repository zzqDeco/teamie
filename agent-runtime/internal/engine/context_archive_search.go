package engine

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
	"github.com/jackc/pgx/v5"
)

type contextTranscriptBlock struct {
	Kind      string `json:"kind"`
	Text      string `json:"text,omitempty"`
	Name      string `json:"name,omitempty"`
	CallID    string `json:"callId,omitempty"`
	Arguments string `json:"arguments,omitempty"`
}

type contextTranscriptMessage struct {
	Role   string                   `json:"role"`
	Blocks []contextTranscriptBlock `json:"blocks"`
}

// Project only model-visible text and function calls/results. Native reasoning,
// signatures, provider metadata, binary contents and the platform instruction
// stay in the existing private journal and never enter searchable artifacts.
func contextTranscriptProjection(message *schema.AgenticMessage) *contextTranscriptMessage {
	if message == nil || message.Role == schema.AgenticRoleTypeSystem {
		return nil
	}
	out := &contextTranscriptMessage{Role: string(message.Role), Blocks: []contextTranscriptBlock{}}
	for _, block := range message.ContentBlocks {
		if block == nil {
			continue
		}
		switch {
		case block.UserInputText != nil:
			out.Blocks = append(out.Blocks, contextTranscriptBlock{Kind: "text", Text: block.UserInputText.Text})
		case block.AssistantGenText != nil:
			out.Blocks = append(out.Blocks, contextTranscriptBlock{Kind: "text", Text: block.AssistantGenText.Text})
		case block.FunctionToolCall != nil:
			call := block.FunctionToolCall
			if !strings.HasPrefix(call.Name, "context_") {
				out.Blocks = append(out.Blocks, contextTranscriptBlock{Kind: "tool_call", Name: call.Name, CallID: call.CallID, Arguments: call.Arguments})
			}
		case block.FunctionToolResult != nil:
			result := block.FunctionToolResult
			if strings.HasPrefix(result.Name, "context_") {
				continue
			}
			for _, part := range result.Content {
				if part != nil && part.Type == schema.FunctionToolResultContentBlockTypeText && part.Text != nil {
					out.Blocks = append(out.Blocks, contextTranscriptBlock{Kind: "tool_result", Name: result.Name, CallID: result.CallID, Text: part.Text.Text})
				}
			}
		}
	}
	if len(out.Blocks) == 0 {
		return nil
	}
	return out
}

func (a *ContextArchive) ArchiveTranscript(ctx context.Context, messages []*schema.AgenticMessage) (string, error) {
	r, err := a.scope(ctx)
	if err != nil {
		return "", err
	}
	projection := []*contextTranscriptMessage{}
	for _, message := range messages {
		if item := contextTranscriptProjection(message); item != nil {
			projection = append(projection, item)
		}
	}
	content, err := json.Marshal(projection)
	if err != nil {
		return "", err
	}
	// Retrieval results are deliberately absent from the text projection. Their
	// server-owned observations still carry the source closure of any private
	// data used to produce the next summary.
	parents, err := a.transcriptRetrievalParents(ctx, r, messages)
	if err != nil {
		return "", err
	}
	refs, err := a.transcriptSources(ctx, r, messages, &parents)
	if err != nil {
		return "", err
	}
	sort.Strings(parents)
	parents = slices.Compact(parents)
	identity, _ := json.Marshal(struct {
		ContentHash string        `json:"contentHash"`
		Sources     []InputSource `json:"sources"`
		Parents     []string      `json:"parents"`
	}{digest(content), refs, parents})
	return a.put(ctx, r, contextArtifact{RunID: r.ID, Attempt: r.Attempt, Key: "prefix/" + digest(identity), Kind: "transcript", Text: string(content), Sources: refs, ArtifactRefs: parents})
}

type contextArchiveEvent struct {
	seq                int64
	runID, attempt, id string
	event              *adk.SessionEvent[*schema.AgenticMessage]
}

func (a *ContextArchive) eventPage(ctx context.Context, r *Run, after, head int64, limit int) ([]contextArchiveEvent, error) {
	rows, err := a.Store.DB.Query(ctx, `SELECT e.seq,e.run_id::text,e.attempt_id::text,e.event_id,e.payload FROM runtime_session_events e JOIN runtime_runs r ON r.id=e.run_id WHERE e.session_id=$1 AND `+contextEventVisibility+` AND e.seq>$6 AND e.seq<=$7 ORDER BY e.seq LIMIT $8`, "agent:"+r.AgentID, r.Attempt, r.ID, r.ContextHead, r.AcceptanceHead, after, head, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []contextArchiveEvent{}
	for rows.Next() {
		var item contextArchiveEvent
		var payload []byte
		if err = rows.Scan(&item.seq, &item.runID, &item.attempt, &item.id, &payload); err != nil {
			return nil, err
		}
		item.event = new(adk.SessionEvent[*schema.AgenticMessage])
		if err = decodeSessionPayload(payload, item.event); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (a *ContextArchive) eventArtifact(ctx context.Context, item contextArchiveEvent) (*contextArtifact, error) {
	// Original message events remain durable across messages_replaced. Re-index
	// those originals rather than copying every old message on each compaction.
	message := item.event.Message
	if item.event.MessageInserted != nil {
		message = item.event.MessageInserted.Message
	}
	// Reduction replacements are views of an existing tool observation. Its
	// original Message event and separately scoped offload artifact remain the
	// history sources; a later compactor Run must not become the observation's
	// source merely because it emitted message_updated.
	projection := contextTranscriptProjection(message)
	if projection == nil {
		return nil, nil
	}
	refs, err := a.runSources(ctx, item.runID)
	if err != nil {
		return nil, err
	}
	// A real outer input envelope can narrow a multi-input Run's candidate set.
	// Match it to persisted input provenance; model-created text cannot add refs.
	if message != nil && message.Role == schema.AgenticRoleTypeUser && !hasToolResults(message) && strings.HasPrefix(agenticText(message), "TEAMIE_NEW_MESSAGE ") {
		var input Input
		if json.Unmarshal([]byte(strings.TrimPrefix(agenticText(message), "TEAMIE_NEW_MESSAGE ")), &input) == nil {
			for _, ref := range refs {
				if ref.EventID == input.EventID && ref.ConversationID == input.ConversationID && ref.MessageID == input.MessageID {
					refs = []InputSource{ref}
					break
				}
			}
		}
	}
	text, err := json.Marshal(projection)
	if err != nil {
		return nil, err
	}
	return &contextArtifact{RunID: item.runID, Attempt: item.attempt, Key: "event/" + item.id, Kind: "transcript", Text: string(text), Sources: refs, EventSeq: item.seq}, nil
}

func contextExcerpt(text, query string) string {
	lowerIndex := strings.Index(strings.ToLower(text), strings.ToLower(query))
	start, normalizedOffset := 0, 0
	if lowerIndex >= 0 {
		for index, r := range text {
			start = index
			if normalizedOffset >= lowerIndex {
				break
			}
			normalizedOffset += utf8.RuneLen(unicode.ToLower(r))
		}
	}
	if start > 64 {
		start -= 64
	} else {
		start = 0
	}
	for start > 0 && !utf8.RuneStart(text[start]) {
		start--
	}
	end := start + 256
	if end > len(text) {
		end = len(text)
	}
	for end > start && end < len(text) && !utf8.RuneStart(text[end]) {
		end--
	}
	return text[start:end]
}

func (a *ContextArchive) Search(ctx context.Context, query, rawCursor string, limit int) (*ContextSearchPage, error) {
	r, err := a.scope(ctx)
	if err != nil {
		return nil, err
	}
	if limit == 0 {
		limit = 4
	}
	if !utf8.ValidString(query) || len(query) < 2 || len(query) > 256 || strings.TrimSpace(query) == "" || limit < 1 || limit > 8 {
		return nil, errContextArguments
	}
	var cursor contextSearchCursor
	const viewKey = "context-search-view-v1"
	if rawCursor == "" {
		cursor = contextSearchCursor{RunID: r.ID, ViewID: contextSearchViewID(compose.GetToolCallID(ctx)), QueryHash: digest([]byte(query)), ContextHead: r.ContextHead, AcceptanceHead: r.AcceptanceHead, Phase: "artifacts"}
		prior, e := a.Store.readObservation(ctx, r, "context-search/"+cursor.ViewID, viewKey)
		if errors.Is(e, pgx.ErrNoRows) {
			err = a.Store.DB.QueryRow(ctx, `SELECT COALESCE(max(seq),0) FROM runtime_context_artifacts WHERE agent_id=$1`, r.AgentID).Scan(&cursor.ArtifactHead)
			if err == nil {
				err = a.Store.DB.QueryRow(ctx, `SELECT COALESCE(max(e.seq),0) FROM runtime_session_events e JOIN runtime_runs r ON r.id=e.run_id WHERE e.session_id=$1 AND `+contextEventVisibility, "agent:"+r.AgentID, r.Attempt, r.ID, r.ContextHead, r.AcceptanceHead).Scan(&cursor.EventHead)
			}
			if err == nil {
				snapshot, _ := json.Marshal(cursor)
				err = a.saveObservation(ctx, r, "context-search/"+cursor.ViewID, viewKey, snapshot)
			}
		} else if e != nil {
			err = e
		} else if json.Unmarshal(prior, &cursor) != nil || cursor.RunID != r.ID || cursor.QueryHash != digest([]byte(query)) || cursor.ContextHead != r.ContextHead || cursor.AcceptanceHead != r.AcceptanceHead {
			err = ErrReplayConflict
		}
	} else {
		cursor, err = decodeContextCursor(rawCursor, r)
		if err == nil {
			var snapshot contextSearchCursor
			prior, e := a.Store.readObservation(ctx, r, "context-search/"+cursor.ViewID, viewKey)
			if e != nil || json.Unmarshal(prior, &snapshot) != nil || snapshot.QueryHash != digest([]byte(query)) || snapshot.RunID != cursor.RunID || snapshot.QueryHash != cursor.QueryHash || snapshot.ContextHead != cursor.ContextHead || snapshot.AcceptanceHead != cursor.AcceptanceHead || snapshot.ArtifactHead != cursor.ArtifactHead || snapshot.EventHead != cursor.EventHead {
				err = errContextArguments
			}
		}
	}
	if err != nil {
		return nil, err
	}
	page := &ContextSearchPage{Hits: []ContextSearchHit{}}
	scanned := 0
	if cursor.Phase == "artifacts" {
		rows, e := a.Store.DB.Query(ctx, `SELECT a.seq,a.run_id::text,a.kind,a.artifact_key FROM runtime_context_artifacts a JOIN runtime_runs r ON r.id=a.run_id WHERE a.agent_id=$1 AND `+contextArtifactVisibility+` AND a.source_event_seq=0 AND a.tool_name<>'context_source_index' AND a.seq>$6 AND a.seq<=$7 ORDER BY a.seq LIMIT $8`, r.AgentID, r.Attempt, r.ID, r.ContextHead, r.AcceptanceHead, cursor.After, cursor.ArtifactHead, contextSearchScan)
		if e != nil {
			return nil, e
		}
		type candidate struct {
			seq              int64
			runID, kind, key string
		}
		candidates := []candidate{}
		for rows.Next() {
			var item candidate
			if e = rows.Scan(&item.seq, &item.runID, &item.kind, &item.key); e != nil {
				rows.Close()
				return nil, e
			}
			candidates = append(candidates, item)
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return nil, e
		}
		for _, item := range candidates {
			cursor.After = item.seq
			scanned++
			ref := "context:v1:" + item.runID + ":" + item.key
			artifact, e := a.load(ctx, r, ref)
			if e != nil {
				return nil, e
			}
			if strings.Contains(strings.ToLower(artifact.Text), strings.ToLower(query)) {
				if e = a.validateRefs(ctx, r, []string{ref}); e != nil && !errors.Is(e, errContextUnavailable) {
					return nil, e
				}
				if e == nil {
					page.Hits = append(page.Hits, ContextSearchHit{Ref: ref, Kind: artifact.Kind, Excerpt: contextExcerpt(artifact.Text, query)})
				}
			}
			if len(page.Hits) == limit {
				break
			}
		}
		if cursor.After >= cursor.ArtifactHead || len(candidates) < contextSearchScan && len(page.Hits) < limit {
			cursor.Phase, cursor.After = "events", 0
		}
	}
	if cursor.Phase == "events" && len(page.Hits) < limit && scanned < contextSearchScan {
		maximum := contextSearchScan - scanned
		events, e := a.eventPage(ctx, r, cursor.After, cursor.EventHead, maximum)
		if e != nil {
			return nil, e
		}
		for _, item := range events {
			cursor.After = item.seq
			artifact, e := a.eventArtifact(ctx, item)
			if e != nil {
				return nil, e
			}
			if artifact == nil || !strings.Contains(strings.ToLower(artifact.Text), strings.ToLower(query)) {
				continue
			}
			if e = a.authorizeSources(ctx, artifact.Sources); e != nil {
				if errors.Is(e, errContextUnavailable) {
					continue
				}
				return nil, e
			}
			ref, e := a.put(ctx, r, *artifact)
			if e != nil {
				return nil, e
			}
			page.Hits = append(page.Hits, ContextSearchHit{Ref: ref, Kind: artifact.Kind, Excerpt: contextExcerpt(artifact.Text, query)})
			if len(page.Hits) == limit {
				break
			}
		}
		if cursor.After >= cursor.EventHead || len(events) < maximum && len(page.Hits) < limit {
			cursor.Phase = "done"
		}
	}
	page.HasMore = cursor.Phase != "done"
	if page.HasMore {
		page.Cursor = encodeContextCursor(cursor)
	}
	wire, _ := json.Marshal(contextToolResponse{OK: true, Data: page})
	if len(wire) > contextResponseMaximum {
		return nil, errContextUnavailable
	}
	return page, nil
}
