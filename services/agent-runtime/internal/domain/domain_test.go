package domain

import (
	"errors"
	"testing"
	"time"
)

func TestLeaseAndRevisionInvariants(t *testing.T) {
	agentID := "10000000-0000-4000-8000-000000000001"
	lease := Lease{AgentID: agentID, Owner: "worker-1", Fence: 3}
	if !lease.Valid() {
		t.Fatal("expected valid lease")
	}
	if (Lease{AgentID: "invalid", Owner: "worker-1", Fence: 1}).Valid() {
		t.Fatal("expected invalid UUID to fail lease validation")
	}
	if (Lease{AgentID: agentID, Owner: "worker-1", Fence: 0}).Valid() {
		t.Fatal("expected non-positive fence to fail lease validation")
	}
	if err := lease.ValidateSessionScope("agent:" + agentID); err != nil {
		t.Fatalf("expected valid session scope, got %v", err)
	}
	if !errors.Is(lease.ValidateSessionScope("agent:other"), ErrSessionScopeMismatch) {
		t.Fatal("expected ErrSessionScopeMismatch for mismatched session ID")
	}
	if err := lease.ValidateCheckpointScope("turnloop:" + agentID); err != nil {
		t.Fatalf("expected valid checkpoint scope, got %v", err)
	}
	if !errors.Is(lease.ValidateCheckpointScope("turnloop:other"), ErrCheckpointScopeMismatch) {
		t.Fatal("expected ErrCheckpointScopeMismatch for mismatched checkpoint ID")
	}

	if ShouldPreemptWorker(true, false, 5, 5) {
		t.Fatal("matching revision on enabled agent must not preempt")
	}
	if !ShouldPreemptWorker(false, false, 5, 5) {
		t.Fatal("disabled agent must preempt worker")
	}
	if !ShouldPreemptWorker(true, true, 5, 5) {
		t.Fatal("deleted agent must preempt worker")
	}
	if !ShouldPreemptWorker(true, false, 6, 5) {
		t.Fatal("advanced revision must preempt worker")
	}
}

func TestProvenanceDeduplicationAndCanonicalization(t *testing.T) {
	wsID := "20000000-0000-4000-8000-000000000001"
	convID := "30000000-0000-4000-8000-000000000001"
	msgID := "40000000-0000-4000-8000-000000000001"
	senderID := "50000000-0000-4000-8000-000000000001"
	att1 := "60000000-0000-4000-8000-000000000001"
	att2 := "60000000-0000-4000-8000-000000000002"

	inputs := []Input{
		{
			EventID:        "10",
			WorkspaceID:    wsID,
			ConversationID: convID,
			MessageID:      msgID,
			SenderID:       senderID,
			SenderKind:     "human",
			Attachments:    []Attachment{{ID: att2}, {ID: att1}},
		},
		{
			EventID:        "10",
			WorkspaceID:    wsID,
			ConversationID: convID,
			MessageID:      msgID,
			SenderID:       senderID,
			SenderKind:     "human",
		},
		{
			EventID:        "11",
			Kind:           "schedule.wakeup",
			ScheduleID:     "sched-1",
			ConversationID: convID,
			MessageID:      msgID,
			SourceRefs: []InputSource{
				{EventID: "10", WorkspaceID: wsID, ConversationID: convID, MessageID: msgID},
			},
		},
	}

	extracted, overflow := ExtractInputSources(inputs)
	if overflow {
		t.Fatal("unexpected source overflow")
	}
	if len(extracted) != 1 {
		t.Fatalf("expected 1 deduplicated input source, got %d", len(extracted))
	}

	merged, err := CanonicalContextSources([]InputSource{
		{EventID: "10", ConversationID: convID, MessageID: msgID, AttachmentIDs: []string{att2}},
		{EventID: "10", WorkspaceID: wsID, ConversationID: convID, MessageID: msgID, SenderID: senderID, AttachmentIDs: []string{att1, att2}},
	})
	if err != nil {
		t.Fatalf("CanonicalContextSources failed: %v", err)
	}
	if len(merged) != 1 || merged[0].WorkspaceID != wsID || merged[0].SenderID != senderID || len(merged[0].AttachmentIDs) != 2 {
		t.Fatalf("unexpected merged canonical source: %+v", merged)
	}

	_, err = CanonicalContextSources([]InputSource{
		{EventID: "10", WorkspaceID: wsID, ConversationID: convID, MessageID: msgID},
		{EventID: "10", WorkspaceID: "20000000-0000-4000-8000-000000000099", ConversationID: convID, MessageID: msgID},
	})
	if !errors.Is(err, ErrReplayConflict) {
		t.Fatalf("expected ErrReplayConflict on conflicting workspace IDs, got %v", err)
	}
}

func TestScheduleValidationAndDST(t *testing.T) {
	now := time.Date(2026, time.October, 23, 12, 0, 0, 0, time.UTC)

	validated, code := ValidateScheduleSpec(ScheduleSpec{
		Kind:         "once",
		DelaySeconds: 120,
		Prompt:       "Check deployment status",
	}, now)
	if code != "" {
		t.Fatalf("expected valid once schedule, got error code %q", code)
	}
	if !validated.NextRunAt.Equal(now.Add(120 * time.Second)) {
		t.Fatalf("unexpected nextRunAt: %v", validated.NextRunAt)
	}

	if _, code := ValidateScheduleSpec(ScheduleSpec{
		Kind:         "once",
		DelaySeconds: 60,
		RunAt:        now.Add(time.Hour).Format(time.RFC3339),
		Prompt:       "Mutually exclusive",
	}, now); code != "invalid_arguments" {
		t.Fatalf("expected invalid_arguments when both delaySeconds and runAt are set, got %q", code)
	}

	// Verify DST fall-back protection in America/New_York (2026-11-01 01:30 EDT -> 01:00 EST).
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatalf("LoadLocation failed: %v", err)
	}
	beforeFallback := time.Date(2026, time.November, 1, 1, 30, 0, 0, loc).UTC()
	nextUTC, err := NextCronTime("0 9 2 11 *", "America/New_York", beforeFallback)
	if err != nil {
		t.Fatalf("NextCronTime across DST fall-back failed: %v", err)
	}
	expectedLocal := time.Date(2026, time.November, 2, 9, 0, 0, 0, loc)
	if !nextUTC.Equal(expectedLocal.UTC()) {
		t.Fatalf("expected %v, got %v", expectedLocal.UTC(), nextUTC)
	}
}
