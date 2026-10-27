package app

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"teamie/services/agent-runtime/internal/domain"
)

type fakeTx struct {
	txCalls int
}

func (f *fakeTx) WithinTx(ctx context.Context, fn func(context.Context) error) error {
	f.txCalls++
	return fn(ctx)
}

func (f *fakeTx) Ping(context.Context) error { return nil }

type fakeSessions struct {
	leaseErr error
}

func (f *fakeSessions) AcquireLease(context.Context, string, string) (*domain.Lease, error) {
	return nil, nil
}
func (f *fakeSessions) RenewLease(context.Context, *domain.Lease) error { return f.leaseErr }
func (f *fakeSessions) ReleaseLease(context.Context, *domain.Lease)     {}
func (f *fakeSessions) CheckLease(context.Context, *domain.Lease) error { return f.leaseErr }
func (f *fakeSessions) CheckLeaseForUpdate(context.Context, *domain.Lease) error {
	return f.leaseErr
}
func (f *fakeSessions) GetCursor(context.Context, *domain.Lease) (string, error) {
	return "", nil
}
func (f *fakeSessions) ListReadReceiptInputs(context.Context, *domain.Lease, string, string, int) (domain.ReadReceiptInputPage, error) {
	return domain.ReadReceiptInputPage{}, nil
}
func (f *fakeSessions) IngestInputs(context.Context, *domain.Lease, string, []domain.Input) error {
	return nil
}
func (f *fakeSessions) SkipInput(context.Context, *domain.Lease, string) error {
	return nil
}

type fakeSchedules struct {
	now         time.Time
	activeCount int
	created     *domain.ScheduleItem
	canceledIDs []string
	sources     []domain.InputSource
	updateCount int
}

func (f *fakeSchedules) ClockNow(context.Context) (time.Time, error) {
	return f.now, nil
}
func (f *fakeSchedules) FindScheduleByID(_ context.Context, _, schedID string) (*domain.ScheduleItem, bool, error) {
	if f.created != nil && f.created.ScheduleID == schedID {
		return f.created, true, nil
	}
	return nil, false, nil
}
func (f *fakeSchedules) CountActiveSchedules(context.Context, string) (int, error) {
	return f.activeCount, nil
}
func (f *fakeSchedules) CreateSchedule(_ context.Context, _, _, schedID string, spec domain.ValidatedScheduleSpec, _ []domain.InputSource, _ bool, now time.Time) (*domain.ScheduleItem, error) {
	item := &domain.ScheduleItem{
		ScheduleID:      schedID,
		Prompt:          spec.Prompt,
		Kind:            spec.Kind,
		IntervalSeconds: spec.IntervalSeconds,
		MaxRuns:         spec.MaxRuns,
		Status:          "active",
		Timezone:        spec.Timezone,
		CronExpr:        spec.CronExpr,
		NextRunAt:       spec.NextRunAt.UTC().Format(time.RFC3339Nano),
		CreatedAt:       now.UTC().Format(time.RFC3339Nano),
	}
	f.created = item
	return item, nil
}
func (f *fakeSchedules) ListActiveScheduleCandidates(context.Context, string, int) ([]domain.ScheduleCandidate, error) {
	return nil, nil
}
func (f *fakeSchedules) GetScheduleStatusAndParent(context.Context, string, string) (string, string, bool, error) {
	return "active", "", true, nil
}
func (f *fakeSchedules) GetScheduleSourcesMeta(context.Context, string, string) (string, []byte, bool, bool, error) {
	b, _ := json.Marshal(f.sources)
	return "", b, false, len(f.sources) > 0, nil
}
func (f *fakeSchedules) CancelActiveSchedule(_ context.Context, _, scheduleID string) error {
	f.canceledIDs = append(f.canceledIDs, scheduleID)
	return nil
}
func (f *fakeSchedules) CancelScheduleWithLease(context.Context, *domain.Lease, string) (string, bool, error) {
	return "canceled", true, nil
}

type fakeReceipts struct {
	saved map[string][]byte
}

func (f *fakeReceipts) ReadObservation(_ context.Context, runID, callID, name string) ([]byte, bool, error) {
	if f.saved == nil {
		return nil, false, nil
	}
	b, ok := f.saved[runID+":"+callID+":"+name]
	return b, ok, nil
}

func (f *fakeReceipts) SaveObservation(_ context.Context, _ *domain.Lease, runID, callID, name string, payload []byte) error {
	if f.saved == nil {
		f.saved = map[string][]byte{}
	}
	f.saved[runID+":"+callID+":"+name] = payload
	return nil
}

type fakeIM struct {
	authErr error
}

func (f *fakeIM) AuthorizeSources(context.Context, []domain.InputSource) error {
	return f.authErr
}

func TestAppServiceScheduleCreationAndAuthorization(t *testing.T) {
	ctx := context.Background()
	tx := &fakeTx{}
	sessions := &fakeSessions{}
	now := time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)
	schedules := &fakeSchedules{now: now}
	receipts := &fakeReceipts{}
	im := &fakeIM{}

	svc := &Service{
		Tx:           tx,
		Sessions:     sessions,
		Schedules:    schedules,
		ToolReceipts: receipts,
		IM:           im,
	}

	lease := &domain.Lease{
		AgentID: "11111111-1111-1111-1111-111111111111",
		Owner:   "worker-1",
		Fence:   1,
	}
	runID := "22222222-2222-2222-2222-222222222222"
	callID := "call-1"
	inputs := []domain.Input{{
		WorkspaceID:    "33333333-3333-3333-3333-333333333333",
		ConversationID: "44444444-4444-4444-4444-444444444444",
		MessageID:      "55555555-5555-5555-5555-555555555555",
	}}
	spec := domain.ScheduleSpec{
		Prompt:   "Check deployment health",
		Kind:     "cron",
		CronExpr: "0 9 * * *",
		Timezone: "UTC",
	}

	item, errCode, err := svc.EvaluateScheduleCreation(ctx, lease, runID, callID, inputs, spec)
	if err != nil || errCode != "" || item == nil {
		t.Fatalf("expected schedule creation to succeed, got item=%v code=%q err=%v", item, errCode, err)
	}
	if tx.txCalls != 1 {
		t.Fatalf("expected 1 WithinTx call, got %d", tx.txCalls)
	}

	// Second call with the same (runID, callID) recovers idempotently from ToolReceipts.
	recovered, errCode, err := svc.EvaluateScheduleCreation(ctx, lease, runID, callID, inputs, spec)
	if err != nil || errCode != "" || recovered == nil || recovered.ScheduleID != item.ScheduleID {
		t.Fatalf("expected idempotent recovery, got %v %q %v", recovered, errCode, err)
	}

	// FilterAuthorizedSchedules cancels schedules whose source ACL is revoked.
	srcJSON, _ := json.Marshal([]domain.InputSource{{
		WorkspaceID:    "33333333-3333-3333-3333-333333333333",
		ConversationID: "44444444-4444-4444-4444-444444444444",
		MessageID:      "55555555-5555-5555-5555-555555555555",
	}})
	im.authErr = domain.ErrContextUnavailable
	filtered, err := svc.FilterAuthorizedSchedules(ctx, lease.AgentID, []domain.ScheduleCandidate{{
		View:           *item,
		CreatedRunID:   runID,
		SourceRefsJSON: srcJSON,
	}})
	if err != nil || len(filtered) != 0 || len(schedules.canceledIDs) != 1 {
		t.Fatalf("expected revoked schedule to be filtered and canceled, got filtered=%v canceled=%v err=%v", filtered, schedules.canceledIDs, err)
	}
}

func (f *fakeSchedules) UpdateSchedule(ctx context.Context, agentID, scheduleID string, spec domain.ValidatedScheduleSpec, refs []domain.InputSource, overflow bool, now time.Time) (*domain.ScheduleItem, error) {
	f.updateCount++
	return f.CreateSchedule(ctx, agentID, "", scheduleID, spec, refs, overflow, now)
}
func (f *fakeSchedules) EnsureDefaultHeartbeatSchedule(context.Context, *domain.Lease, string, int64, string, []domain.InputSource) (bool, error) {
	return false, nil
}

func TestScheduleUpdateFailsClosedOnIncompleteSourcesAndReplayRevocation(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC()
	iv := int64(3600)
	workspace := "33333333-3333-3333-3333-333333333333"
	agent := "11111111-1111-1111-1111-111111111111"
	run := "22222222-2222-2222-2222-222222222222"
	id := domain.DefaultHeartbeatScheduleID(agent)
	schedules := &fakeSchedules{now: now, sources: []domain.InputSource{{WorkspaceID: workspace}}, created: &domain.ScheduleItem{ScheduleID: id, Kind: "interval", IntervalSeconds: &iv, Prompt: "old", Timezone: "UTC", Status: "active", NextRunAt: now.Add(time.Hour).Format(time.RFC3339)}}
	im := &fakeIM{}
	receipts := &fakeReceipts{}
	svc := &Service{Tx: &fakeTx{}, Sessions: &fakeSessions{}, Schedules: schedules, IM: im, ToolReceipts: receipts}
	lease := &domain.Lease{AgentID: agent}
	patch := domain.ScheduleSpec{Prompt: "new"}
	input := domain.Input{Kind: "schedule.wakeup", SourceOverflow: true, SourceRefs: schedules.sources}
	if _, _, err := svc.EvaluateScheduleUpdate(ctx, lease, run, "call", id, []domain.Input{input}, patch); err != domain.ErrContextUnavailable || schedules.updateCount != 0 {
		t.Fatalf("incomplete source set allowed update: %v", err)
	}
	input.SourceOverflow = false
	if _, code, err := svc.EvaluateScheduleUpdate(ctx, lease, run, "call", id, []domain.Input{input}, patch); err != nil || code != "" || schedules.updateCount != 1 {
		t.Fatalf("valid update failed: %s %v", code, err)
	}
	if _, code, err := svc.EvaluateScheduleUpdate(ctx, lease, run, "call", id, []domain.Input{input}, patch); err != nil || code != "" || schedules.updateCount != 1 {
		t.Fatalf("replay updated twice: %s %v", code, err)
	}
	im.authErr = domain.ErrContextUnavailable
	if item, code, err := svc.EvaluateScheduleUpdate(ctx, lease, run, "call", id, []domain.Input{input}, patch); err != nil || code != "not_found" || item != nil || schedules.updateCount != 1 {
		t.Fatalf("revoked replay exposed schedule: %s %v", code, err)
	}
}
