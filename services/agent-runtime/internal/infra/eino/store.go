package eino

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"teamie/pkg/observability"
	"time"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	"github.com/google/uuid"
	"teamie/contracts/gen/agent/modeltransport"
	"teamie/services/agent-runtime/internal/domain"
	"teamie/services/agent-runtime/internal/infra/pg"
	"teamie/pkg/imtext"
)

var ErrLeaseLost = domain.ErrLeaseLost
var ErrReplayConflict = domain.ErrReplayConflict

type Store struct {
	DB          *pg.Repository
	Coordinator func(im domain.IMGateway) domain.ScheduleCoordinator
}
type Lease = domain.Lease
type Input struct {
	EventID        string           `json:"eventId"`
	WorkspaceID    string           `json:"workspaceId"`
	ConversationID string           `json:"conversationId"`
	MessageID      string           `json:"messageId"`
	SenderID       string           `json:"senderId"`
	SenderName     string           `json:"senderName"`
	SenderKind     string           `json:"senderKind"`
	Body           string           `json:"body"`
	Attachments    []Attachment     `json:"attachments,omitempty"`
	Mentions       []imtext.Mention `json:"mentions,omitempty"`
	ReplyToID      string           `json:"replyToId,omitempty"`
	Seq            int64            `json:"seq"`
	CreatedAt      string           `json:"createdAt"`
	Kind           string           `json:"kind,omitempty"`
	TaskID         string           `json:"taskId,omitempty"`
	ScheduleID     string           `json:"scheduleId,omitempty"`
	NotificationID string           `json:"notificationId,omitempty"`
	Status         string           `json:"status,omitempty"`
	ParentRunID    string           `json:"parentRunId,omitempty"`
	SourceRefs     []InputSource    `json:"sourceRefs,omitempty"`
	SourceOverflow bool             `json:"sourceOverflow,omitempty"`
}
type Run struct {
	diagnostic                  *observability.Operation
	toolProfile                 string
	ContextProfile              string
	ContextBudget               json.RawMessage
	ID, Attempt, AgentID        string
	Inputs                      []Input
	Step                        atomic.Int64
	Revision, Generation        int64
	ContextHead, AcceptanceHead int64
	Front                       *frontSlot
	usageState                  usageAnchorState
}

func (r *Run) getUsageAnchor() *contextUsageAnchor {
	if r == nil {
		return nil
	}
	return r.usageState.get()
}

func (r *Run) getUsageAnchorForConfig(configID string) *contextUsageAnchor {
	if r == nil {
		return nil
	}
	return r.usageState.getForConfig(configID)
}

func (r *Run) setUsageAnchor(anchor *contextUsageAnchor, onlyIfValid bool) {
	if r == nil {
		return
	}
	r.usageState.set(anchor, onlyIfValid)
}

func (r *Run) clearUsageAnchor() {
	if r == nil {
		return
	}
	r.usageState.clear()
}

func (r *Run) setAdmissionConfig(configID, counterMode string) {
	if r == nil {
		return
	}
	r.usageState.setAdmissionConfig(configID, counterMode)
}

func (r *Run) clearAdmissionConfig() {
	if r == nil {
		return
	}
	r.usageState.clearAdmissionConfig()
}

func (r *Run) consumeAdmissionConfig(preserve bool) (string, string, bool) {
	if r == nil {
		return "", "", false
	}
	return r.usageState.consumeAdmissionConfig(preserve)
}

func (r *Run) invalidateUsageAnchor(messages []*schema.AgenticMessage) {
	if r == nil {
		return
	}
	r.usageState.invalidate(messages)
}

func (r *Run) computeRequestUsageAnchor(step int, request []byte, result *schema.AgenticMessage, configID string) *contextUsageAnchor {
	if r == nil || contextProfile(r) != contextProfileV1 || result == nil || result.ResponseMeta == nil || result.ResponseMeta.TokenUsage == nil || result.ResponseMeta.TokenUsage.PromptTokens <= 0 {
		return nil
	}
	budget, err := ParseContextBudgetSnapshot(r.ContextBudget)
	if err != nil || budget.Version < 7 {
		return nil
	}
	configID = strings.TrimSpace(configID)
	if configID == "" {
		if saved, ok := extractUsageAnchor(result); ok {
			configID = saved.ConfigID
		}
	}
	if configID == "" {
		return nil
	}
	var envelope modeltransport.Request
	var messages []*schema.AgenticMessage
	var opts model.Options
	if json.Unmarshal(request, &envelope) != nil || (&schema.HumanReadableSerializer{}).Unmarshal(envelope.Messages, &messages) != nil || json.Unmarshal(envelope.Options, &opts) != nil {
		return nil
	}
	if isSummaryModelOptions(&opts) || len(messages) == 0 {
		return nil
	}
	visibleBytes, prefixHash, err := visibleContextBytes(messages, opts.Tools, opts.DeferredTools, opts.Stop, opts.AgenticToolChoice)
	if err != nil {
		return nil
	}
	localTokens := estimateVisibleTokensV7(visibleBytes)
	promptTokens := result.ResponseMeta.TokenUsage.PromptTokens
	if !validCalibratedTokenBounds(promptTokens, localTokens) {
		return nil
	}
	return &contextUsageAnchor{
		Step:         step,
		ConfigID:     configID,
		MessageCount: len(messages),
		PrefixHash:   prefixHash,
		LocalTokens:  localTokens,
		PromptTokens: promptTokens,
	}
}

func (r *Run) recordModelUsageAnchor(step int, request []byte, result *schema.AgenticMessage, configID string, admission ...requestAdmissionState) {
	if r == nil || result == nil {
		return
	}
	if configID == "" {
		if savedID, ok := extractRecordedModelConfigID(result); ok {
			configID = savedID
		}
	}
	var adm requestAdmissionState
	if len(admission) > 0 {
		adm = admission[0]
	} else if savedAdm, ok := extractRecordedAdmissionMetadata(result); ok {
		adm = savedAdm
	} else {
		adm = requestAdmissionState{ConfigID: strings.TrimSpace(configID), Known: strings.TrimSpace(configID) != ""}
	}
	anchor := r.computeRequestUsageAnchor(step, request, result, configID)
	if anchor == nil {
		if contextProfile(r) == contextProfileV1 {
			if budget, err := ParseContextBudgetSnapshot(r.ContextBudget); err == nil && budget.Version >= 7 {
				attachRecordedStepMetadata(result, step, configID, adm)
			}
		}
		return
	}
	anchor.AdmissionConfigID = strings.TrimSpace(adm.ConfigID)
	anchor.CounterMode = strings.TrimSpace(adm.CounterMode)
	attachUsageAnchor(result, anchor)
	r.setUsageAnchor(anchor, false)
}

func executionID(run *Run) string { return run.ID + ":" + run.Attempt }

type runKey struct{}

func WithRun(ctx context.Context, run *Run) context.Context {
	return context.WithValue(observability.WithFields(ctx, runFields(run)...), runKey{}, run)
}
func CurrentRun(ctx context.Context) (*Run, error) {
	r, ok := ctx.Value(runKey{}).(*Run)
	if !ok {
		return nil, errors.New("missing run context")
	}
	return r, nil
}
func digest(b []byte) string { return domain.Digest(b) }

func toDomainInput(in Input) domain.Input {
	var mentions []domain.MentionSpan
	if len(in.Mentions) > 0 {
		mentions = make([]domain.MentionSpan, len(in.Mentions))
		for i, m := range in.Mentions {
			mentions[i] = domain.MentionSpan(m)
		}
	}
	return domain.Input{
		EventID:        in.EventID,
		WorkspaceID:    in.WorkspaceID,
		ConversationID: in.ConversationID,
		MessageID:      in.MessageID,
		SenderID:       in.SenderID,
		SenderName:     in.SenderName,
		SenderKind:     in.SenderKind,
		Body:           in.Body,
		Attachments:    in.Attachments,
		Mentions:       mentions,
		ReplyToID:      in.ReplyToID,
		Seq:            in.Seq,
		CreatedAt:      in.CreatedAt,
		Kind:           in.Kind,
		TaskID:         in.TaskID,
		ScheduleID:     in.ScheduleID,
		NotificationID: in.NotificationID,
		Status:         in.Status,
		ParentRunID:    in.ParentRunID,
		SourceRefs:     in.SourceRefs,
		SourceOverflow: in.SourceOverflow,
	}
}

func toDomainInputs(inputs []Input) []domain.Input {
	out := make([]domain.Input, len(inputs))
	for i, in := range inputs {
		out[i] = toDomainInput(in)
	}
	return out
}

func (s *Store) Repo() *pg.Repository {
	if s == nil {
		return nil
	}
	return s.DB
}

func (s *Store) ScheduleCoordinator(im domain.IMGateway) domain.ScheduleCoordinator {
	if s != nil && s.Coordinator != nil {
		return s.Coordinator(im)
	}
	repo := s.Repo()
	if domain.DefaultScheduleCoordinator != nil {
		return domain.DefaultScheduleCoordinator(repo, repo, repo, repo, repo, im)
	}
	return nil
}

func Open(ctx context.Context, url string) (*Store, error) {
	db, err := pg.OpenRepository(ctx, url)
	if err != nil {
		return nil, err
	}
	return &Store{DB: db}, nil
}
func (s *Store) Acquire(ctx context.Context, agentID, owner string) (*Lease, error) {
	return s.Repo().AcquireLease(ctx, agentID, owner)
}
func (s *Store) Renew(ctx context.Context, l *Lease) error {
	return s.Repo().RenewLease(ctx, l)
}
func (s *Store) Release(ctx context.Context, l *Lease) {
	s.Repo().ReleaseLease(ctx, l)
}
func (s *Store) CheckLease(ctx context.Context, l *Lease) error {
	return s.Repo().CheckLease(ctx, l)
}
func (s *Store) Cursor(ctx context.Context, l *Lease) (string, error) {
	return s.Repo().GetCursor(ctx, l)
}
func (s *Store) Ingest(ctx context.Context, l *Lease, cursor string, inputs []Input) error {
	return s.Repo().IngestInputs(ctx, l, cursor, toDomainInputs(inputs))
}
func (s *Store) Pending(ctx context.Context, l *Lease) ([]Input, error) {
	raws, err := s.DB.ListPendingInputs(ctx, l.AgentID)
	if err != nil {
		return nil, err
	}
	out := make([]Input, 0, len(raws))
	for _, b := range raws {
		var in Input
		if err = json.Unmarshal(b, &in); err != nil {
			return nil, err
		}
		out = append(out, in)
	}
	return out, nil
}
func (s *Store) Skip(ctx context.Context, l *Lease, id string) error {
	return s.Repo().SkipInput(ctx, l, id)
}
func (s *Store) StartRun(ctx context.Context, l *Lease, inputs []Input) (*Run, error) {
	return s.StartRunWithContextPolicy(ctx, l, inputs, contextProfileLegacy, json.RawMessage(`{}`))
}

// Admission chooses a context policy only for a new logical Run. Recovery
// always restores the persisted policy and budget, regardless of live flags.
func (s *Store) StartRunWithContextPolicy(ctx context.Context, l *Lease, inputs []Input, profile string, budget json.RawMessage) (*Run, error) {
	if len(inputs) == 0 {
		return nil, errors.New("empty run")
	}
	r := &Run{ID: uuid.NewString(), Attempt: uuid.NewString(), AgentID: l.AgentID, Inputs: inputs}
	runID, inputsJSON, contextHead, acceptanceHead, contextProfile, contextBudget, resumed, err := s.DB.StartOrResumeRun(
		ctx,
		l,
		inputs[0].EventID,
		r.ID,
		r.Attempt,
		func() (string, []byte, string, json.RawMessage, []string, error) {
			canonProfile, canonBudget, err := canonicalContextPolicy(profile, budget)
			if err != nil {
				return "", nil, "", nil, nil, err
			}
			b, err := json.Marshal(inputs)
			if err != nil {
				return "", nil, "", nil, nil, err
			}
			ids := make([]string, len(inputs))
			for i, in := range inputs {
				ids[i] = in.EventID
			}
			return strings.Join(ids, ","), b, canonProfile, canonBudget, ids, nil
		},
	)
	if err != nil {
		return nil, err
	}
	r.ID = runID
	r.ContextHead = contextHead
	r.AcceptanceHead = acceptanceHead
	r.ContextProfile = contextProfile
	r.ContextBudget = contextBudget
	if resumed {
		if err = json.Unmarshal(inputsJSON, &r.Inputs); err != nil {
			return nil, err
		}
	}
	return r, nil
}
func (s *Store) FinishRun(ctx context.Context, l *Lease, r *Run, state, safeError string, retry bool) error {
	return s.DB.FinishRun(ctx, l, r.ID, state, safeError, retry)
}

type SessionStore[M adk.MessageType] struct {
	Store    *Store
	Lease    *Lease
	mu       sync.Mutex
	run      *Run
	targets  []string
	Revision int64
}

func (s *SessionStore[M]) AppendEvents(ctx context.Context, sessionID string, events []*adk.SessionEvent[M]) error {
	// Native Eino flushes terminal events/checkpoints after cancellation. Keep
	// values and lease fencing while bounding the independent persistence work.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()

	r, e := CurrentRun(ctx)
	if e != nil {
		return e
	}
	if sessionID != "agent:"+s.Lease.AgentID {
		return errors.New("session scope mismatch")
	}
	ser := &schema.HumanReadableSerializer{}
	items := make([]domain.SessionEventWrite, 0, len(events))
	for _, event := range events {
		if event == nil || event.EventID == "" {
			return adk.ErrInvalidEventID
		}
		if e = adk.NormalizeSessionEventKind(event); e != nil {
			return e
		}
		b, err := ser.Marshal(event)
		if err != nil {
			return err
		}
		items = append(items, domain.SessionEventWrite{
			EventID: event.EventID,
			Kind:    string(event.Kind),
			Payload: b,
			Hash:    digest(b),
		})
	}
	duplicate, err := s.Store.DB.AppendSessionEvents(ctx, s.Lease, sessionID, r.ID, r.Attempt, items)
	if duplicate {
		return adk.ErrDuplicateEventID
	}
	return err
}
func (s *SessionStore[M]) LoadEvents(ctx context.Context, sessionID string, req *adk.LoadSessionEventsRequest) (*adk.LoadSessionEventsResult[M], error) {
	r, e := CurrentRun(ctx)
	if e != nil {
		return nil, e
	}
	if sessionID != "agent:"+s.Lease.AgentID {
		return nil, errors.New("session scope mismatch")
	}
	if req == nil {
		req = &adk.LoadSessionEventsRequest{}
	}
	kinds := make([]string, 0, len(req.Kinds))
	for _, k := range req.Kinds {
		kinds = append(kinds, string(k))
	}
	payloads, outOfRange, err := s.Store.DB.LoadSessionEventPayloads(ctx, sessionID, r.ID, r.Attempt, r.ContextHead, r.AcceptanceHead, req.After, kinds, req.Limit, req.Reverse)
	if outOfRange {
		return nil, adk.ErrEventIDOutOfRange
	}
	if err != nil {
		return nil, err
	}
	out := &adk.LoadSessionEventsResult[M]{Events: make([]*adk.SessionEvent[M], 0, len(payloads))}
	for _, b := range payloads {
		event := new(adk.SessionEvent[M])
		if e = decodeSessionPayload(b, event); e != nil {
			return nil, e
		}
		out.Events = append(out.Events, event)
		out.Next = event.EventID
	}
	return out, nil
}
func (s *SessionStore[M]) Get(ctx context.Context, id string) ([]byte, bool, error) {
	if !strings.Contains(id, s.Lease.AgentID) {
		return nil, false, errors.New("checkpoint scope mismatch")
	}
	b, metadata, ok, e := s.Store.DB.GetCheckpoint(ctx, id)
	if !ok || e != nil {
		return nil, false, e
	}
	if strings.HasPrefix(id, "turnloop:") {
		var m checkpointMetadata
		if json.Unmarshal(metadata, &m) != nil || m.Version != nativeRuntimeVersion {
			return nil, false, nil
		}
	}
	return b, true, nil
}
func (s *SessionStore[M]) Set(ctx context.Context, id string, b []byte) error {
	// Native Eino flushes terminal events/checkpoints after cancellation. Keep
	// values and lease fencing while bounding the independent persistence work.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()

	if !strings.Contains(id, s.Lease.AgentID) {
		return errors.New("checkpoint scope mismatch")
	}
	return s.Store.DB.SetCheckpoint(ctx, s.Lease, id, b, s.metadata())
}
func (s *SessionStore[M]) Delete(ctx context.Context, id string) error {
	// Native Eino flushes terminal events/checkpoints after cancellation. Keep
	// values and lease fencing while bounding the independent persistence work.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()

	if !strings.Contains(id, s.Lease.AgentID) {
		return errors.New("checkpoint scope mismatch")
	}
	return s.Store.DB.DeleteCheckpoint(ctx, s.Lease, id)
}

func (s *Store) CachedModel(ctx context.Context, r *Run, step int, hash string) ([]byte, error) {
	return s.DB.GetCachedModelCall(ctx, r.ID, step, hash)
}

func (s *Store) RecordedModelStepAdmission(ctx context.Context, r *Run, step int) (string, string, bool, error) {
	if s == nil || s.DB == nil || r == nil || r.ID == "" || step < 1 {
		return "", "", false, nil
	}
	raw, decisionRaw, err := s.DB.GetModelStepAndRecovery(ctx, r.ID, step)
	if err != nil {
		return "", "", false, err
	}
	if len(raw) == 0 && len(decisionRaw) == 0 {
		return "", "", false, nil
	}
	if len(raw) > 0 {
		msg, decodeErr := decodeAgenticMessage(raw)
		if decodeErr != nil {
			return "", "", false, decodeErr
		}
		if adm, ok := extractRecordedAdmissionMetadata(msg); ok {
			return adm.ConfigID, adm.CounterMode, true, nil
		}
	}
	if len(decisionRaw) > 0 {
		var decision contextRecoveryDecision
		if unmarshalErr := json.Unmarshal(decisionRaw, &decision); unmarshalErr != nil {
			return "", "", false, unmarshalErr
		}
		if decision.CounterMode != "" || decision.AdmissionConfigID != "" {
			return strings.TrimSpace(decision.AdmissionConfigID), strings.TrimSpace(decision.CounterMode), true, nil
		}
		if id := strings.TrimSpace(decision.ConfigID); id != "" {
			return id, "", true, nil
		}
	}
	return "", "", true, nil
}

func (s *Store) RecordedModelStepConfigID(ctx context.Context, r *Run, step int) (string, bool) {
	if s == nil || s.DB == nil || r == nil || r.ID == "" || step < 1 {
		return "", false
	}
	raw, decisionRaw, err := s.DB.GetModelStepAndRecovery(ctx, r.ID, step)
	if err != nil || (len(raw) == 0 && len(decisionRaw) == 0) {
		return "", false
	}
	if len(raw) > 0 {
		if msg, decodeErr := decodeAgenticMessage(raw); decodeErr == nil {
			if id, ok := extractRecordedModelConfigID(msg); ok {
				return id, true
			}
			if adm, ok := extractRecordedAdmissionMetadata(msg); ok && adm.ConfigID != "" {
				return adm.ConfigID, true
			}
		}
	}
	if len(decisionRaw) > 0 {
		var decision contextRecoveryDecision
		if json.Unmarshal(decisionRaw, &decision) == nil {
			if id := strings.TrimSpace(decision.ConfigID); id != "" {
				return id, true
			}
			if id := strings.TrimSpace(decision.AdmissionConfigID); id != "" {
				return id, true
			}
		}
	}
	return "", true
}
func (s *Store) SaveModel(ctx context.Context, l *Lease, r *Run, step int, hash string, b []byte) error {
	return s.DB.SaveModelCall(ctx, l, r.ID, step, hash, b)
}
func (s *Store) ToolIntent(ctx context.Context, l *Lease, r *Run, id, name string, payload []byte) ([]byte, error) {
	if id == "" {
		return nil, errors.New("tool call has no identity")
	}
	return s.DB.RecordToolIntent(ctx, l, r.ID, id, name, digest(payload))
}
func (s *Store) ToolResult(ctx context.Context, l *Lease, r *Run, id string, b []byte) error {
	return s.DB.RecordToolResult(ctx, l, r.ID, id, b)
}

// InvalidateRun abandons a replay whose source was revoked or changed. Unrelated
// inputs get a fresh run; they must not inherit the abandoned model/tool tape.
func (s *Store) InvalidateRun(ctx context.Context, l *Lease, r *Run, invalid []string) error {
	return s.DB.InvalidateRun(ctx, l, r.ID, invalid)
}
