package eino

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
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
	"teamie/services/agent-runtime/internal/domain"
)

const (
	contextArtifactMaximum = domain.ContextArtifactMaximum
	contextReadDefault     = domain.ContextReadDefault
	contextReadMaximum     = domain.ContextReadMaximum
	contextResponseMaximum = domain.ContextResponseMaximum
	contextSearchScan      = domain.ContextSearchScan
)

var errContextUnavailable = domain.ErrContextUnavailable
var errContextArguments = domain.ErrContextArguments

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
	current, err := a.Store.DB.CheckRunAttemptRunning(ctx, r.ID, r.AgentID, r.Attempt)
	if err != nil {
		return nil, err
	}
	if !current {
		return nil, ErrLeaseLost
	}
	return r, nil
}

func contextKind(kind string) bool {
	return domain.ValidContextArtifactKind(kind)
}

func contextRef(runID, kind, key string) string {
	return domain.FormatContextRef(runID, kind, key)
}

func parseContextRef(ref string) (runID, kind, key string, err error) {
	return domain.ParseContextRef(ref)
}

func canonicalContextSources(refs []InputSource) ([]InputSource, error) {
	return domain.CanonicalContextSources(refs)
}

// runSources follows durable parent references, never routing text in a model
// message. Overflow is resolved against the complete parent Run, not silently
// treated as a unique destination.
func (a *ContextArchive) runSources(ctx context.Context, runID string) ([]InputSource, error) {
	if a == nil || a.Store == nil || a.Lease == nil {
		return nil, errContextUnavailable
	}
	return a.Store.Repo().ResolveRunSources(ctx, a.Lease.AgentID, runID)
}

// ResolveRunSources implements domain.ArchiveRepository.
func (a *ContextArchive) ResolveRunSources(ctx context.Context, agentID, runID string) ([]InputSource, error) {
	if a == nil || a.Store == nil {
		return nil, errContextUnavailable
	}
	return a.Store.Repo().ResolveRunSources(ctx, agentID, runID)
}

// AuthorizeSources implements domain.IMGateway.
func (a *ContextArchive) AuthorizeSources(ctx context.Context, refs []InputSource) error {
	return a.authorizeSources(ctx, refs)
}

func (a *ContextArchive) sourcesThrough(ctx context.Context, r *Run, eventHead int64) ([]InputSource, []string, error) {
	ids, err := a.Store.DB.ListVisibleSessionRunIDsThrough(ctx, "agent:"+r.AgentID, r.Attempt, r.ID, r.ContextHead, r.AcceptanceHead, eventHead)
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
	version := contextSourceVersionBytes([]byte(artifact.Text), sourceJSON, parentJSON)
	if err = a.Store.DB.PutContextArtifact(
		ctx,
		a.Lease,
		domain.ContextArtifactWrite{
			AgentID:       r.AgentID,
			ActiveRunID:   r.ID,
			ActiveAttempt: r.Attempt,
			RunID:         artifact.RunID,
			Attempt:       artifact.Attempt,
			Key:           key,
			Kind:          artifact.Kind,
			ToolCallID:    artifact.ToolCallID,
			ToolName:      artifact.ToolName,
			Content:       []byte(artifact.Text),
			ContentHash:   digest([]byte(artifact.Text)),
			SourceJSON:    sourceJSON,
			ParentJSON:    parentJSON,
			EventSeq:      artifact.EventSeq,
			VersionKey:    contextSourceVersionKey,
			VersionBytes:  version,
		},
		func(oldSources []byte) ([]byte, error) {
			var oldRefs []InputSource
			if json.Unmarshal(oldSources, &oldRefs) != nil {
				return nil, ErrReplayConflict
			}
			canonicalOld, err := canonicalContextSources(oldRefs)
			if err != nil {
				return nil, err
			}
			oldJSON, _ := json.Marshal(canonicalOld)
			return oldJSON, nil
		},
	); err != nil {
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

func (a *ContextArchive) load(ctx context.Context, r *Run, ref string) (*contextArtifact, error) {
	runID, _, key, err := parseContextRef(ref)
	if err != nil {
		return nil, err
	}
	row, found, err := a.Store.DB.LoadVisibleContextArtifact(ctx, r.AgentID, r.Attempt, r.ID, r.ContextHead, r.AcceptanceHead, runID, key)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, errContextUnavailable
	}
	out := &contextArtifact{
		RunID:      runID,
		Attempt:    row.Attempt,
		Key:        row.Key,
		Kind:       row.Kind,
		ToolCallID: row.ToolCallID,
		ToolName:   row.ToolName,
		EventSeq:   row.EventSeq,
		CreatedAt:  row.CreatedAt,
	}
	raw, hash, sources, parents, version := row.Raw, row.Hash, row.Sources, row.Parents, row.Version
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
	var conversations map[string]string
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
				conversations = map[string]string{}
				for _, conversation := range bootstrap.Conversations {
					conversations[conversation.ID] = conversation.WorkspaceID
				}
			}
			if err := domain.VerifyConversationSourceAccess(ref, conversations); err != nil {
				return err
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
		var snap *domain.SourceMessageSnapshot
		if message != nil {
			attIDs := make([]string, len(message.Attachments))
			for i, att := range message.Attachments {
				attIDs[i] = att.ID
			}
			snap = &domain.SourceMessageSnapshot{
				ID:             message.ID,
				ConversationID: message.ConversationID,
				SenderID:       message.SenderID,
				Retracted:      message.RetractedAt != nil,
				AttachmentIDs:  attIDs,
			}
		}
		if err := domain.VerifyMessageSourceAccess(ref, snap); err != nil {
			return err
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
