package eino

import (
	"context"
	"encoding/json"
	"time"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"
	"teamie/services/agent-runtime/internal/domain"
)

const nativeRuntimeVersion = domain.NativeRuntimeVersion

type checkpointMetadata = domain.CheckpointMetadata

func (s *Store) ResumeMetadata(ctx context.Context, id string) (*checkpointMetadata, error) {
	b, err := s.DB.GetCheckpointMetadata(ctx, id)
	if err != nil {
		return nil, err
	}
	v := new(checkpointMetadata)
	if len(b) == 0 {
		return v, nil
	}
	err = json.Unmarshal(b, v)
	return v, err
}
func (s *SessionStore[M]) bind(run *Run, targets []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.run = run
	s.targets = append([]string{}, targets...)
}
func (s *SessionStore[M]) metadata() []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	m := checkpointMetadata{Version: nativeRuntimeVersion, Revision: s.Revision, Targets: append([]string{}, s.targets...)}
	if s.run != nil {
		m.Presence = s.run.checkpointPresence()
		m.ToolProfile = s.run.toolProfile
		if contextProfile(s.run) == contextProfileV1 {
			m.ContextProfile = s.run.ContextProfile
			_, budget, err := canonicalContextPolicy(s.run.ContextProfile, s.run.ContextBudget)
			if err == nil {
				m.ContextBudgetHash = digest(budget)
			}
		}
		m.RunID = s.run.ID
		m.Attempt = s.run.Attempt
		m.ModelStep = s.run.Step.Load()
	}
	b, _ := json.Marshal(m)
	return b
}
func (s *Store) configureRun(ctx context.Context, l *Lease, r *Run, revision, generation int64, resume *checkpointMetadata) error {
	r.resume = resume
	r.Revision = revision
	r.Generation = generation
	resumeAttempt := ""
	hasResume := resume != nil
	if resume != nil {
		r.Step.Store(resume.ModelStep)
		resumeAttempt = resume.Attempt
	}
	return s.DB.ConfigureRun(ctx, l, r.ID, revision, generation, resumeAttempt, hasResume)
}

// FreshPending never lets processing rows occupy the delivery page. It is the
// push path while a native turn is active; recovery still uses Pending.
func (s *Store) FreshPending(ctx context.Context, l *Lease) ([]Input, error) {
	raws, err := s.DB.ListFreshPendingInputs(ctx, l.AgentID)
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
func (s *Store) preemptRun(ctx context.Context, l *Lease, r *Run) error {
	return s.DB.PreemptRun(ctx, l, r.ID)
}
func (s *Store) runCanceled(ctx context.Context, id string) bool {
	return s.DB.IsRunCanceled(ctx, id)
}
func (s *Store) checkpointExists(ctx context.Context, id string) bool {
	return s.DB.CheckpointExists(ctx, id)
}
func targetsFromError(err error) []string {
	return TargetsFromCancelError(err)
}
func inputMessages(inputs []Input) []*schema.AgenticMessage {
	return BuildInputMessages(inputs, func(in Input) string { return in.Kind })
}
func relevantInput(in Input, tc *adk.TurnContext[Input, *schema.AgenticMessage], presence *conversationPresenceState) bool {
	if tc == nil {
		return false
	}
	var active *domain.InputSource
	if presence != nil {
		ws, conv, _ := presence.Active()
		active = &domain.InputSource{WorkspaceID: ws, ConversationID: conv}
	}
	return domain.RelevantPreemptInput(toDomainInput(in), toDomainInputs(tc.Consumed), active)
}
func resumeCompatible(m *checkpointMetadata, revision int64, runID string) bool {
	return domain.ResumeCompatible(m, revision, runID)
}
func detachedPersistence(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
}

func (s *Store) readObservation(ctx context.Context, run *Run, call, key string) ([]byte, error) {
	return s.DB.GetObservation(ctx, run.ID, call, key)
}
func (s *Store) saveObservation(ctx context.Context, l *Lease, run *Run, call, key string, b []byte) error {
	return s.DB.SaveObservation(ctx, l, run.ID, call, key, b)
}

func (s *Store) eligibleInputs(ctx context.Context, l *Lease, inputs []Input) ([]Input, error) {
	ids := make([]string, 0, len(inputs))
	for _, in := range inputs {
		ids = append(ids, in.EventID)
	}
	eligible, err := s.DB.FilterEligibleInputIDs(ctx, l.AgentID, ids)
	if err != nil {
		return nil, err
	}
	out := []Input{}
	for _, in := range inputs {
		if eligible[in.EventID] {
			out = append(out, in)
		}
	}
	return out, nil
}
