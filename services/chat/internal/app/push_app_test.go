package app_test

import (
	"context"
	"errors"
	"slices"
	"testing"

	"teamie/services/chat/internal/app"
	"teamie/services/chat/internal/domain"
)

type pushTestFlags map[string]bool

func (f pushTestFlags) Enabled(key string) bool               { return f[key] }
func (f pushTestFlags) Snapshot() (string, map[string]bool)   { return "test", f }

type pushTestMessages struct {
	domain.MessageRepository
	wsID          string
	eligible      []string
	lastConvID    string
	lastMsgID     string
	lastCands     []string
}

func (m *pushTestMessages) EvaluatePushRecipients(_ context.Context, convID, msgID string, candidates []string) (string, []string, error) {
	m.lastConvID = convID
	m.lastMsgID = msgID
	m.lastCands = slices.Clone(candidates)
	return m.wsID, slices.Clone(m.eligible), nil
}

type pushTestPresence struct {
	states map[string]string
	err    error
}

func (p pushTestPresence) Presence(context.Context, []string) (map[string]string, error) {
	return p.states, p.err
}

func TestEvaluatePushRecipients_FlagPresenceAndDeduplication(t *testing.T) {
	msgRepo := &pushTestMessages{
		wsID:     "w1",
		eligible: []string{"uAway", "uOffline", "uOnline"},
	}
	flags := pushTestFlags{"im.push.web": false}
	svc := &app.Service{
		Messages: msgRepo,
		Flags:    flags,
		Presence: pushTestPresence{
			states: map[string]string{
				"uOnline": "online",
				"uAway":   "away",
			},
		},
	}

	// 1. Flag disabled returns empty without hitting DB.
	res, err := svc.EvaluatePushRecipients(context.Background(), "c1", "m1", []string{"uOnline", "uAway"})
	if err != nil || len(res.RecipientUserIDs) != 0 || msgRepo.lastMsgID != "" {
		t.Fatalf("expected empty evaluation when flag off, got %+v err=%v", res, err)
	}

	// 2. Flag enabled deduplicates candidates and suppresses "online" users while keeping "away" and "offline".
	flags["im.push.web"] = true
	res, err = svc.EvaluatePushRecipients(context.Background(), "c1", "m1", []string{"uOnline", "uAway", "uOnline", "", "uOffline"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.WorkspaceID != "w1" || !slices.Equal(res.RecipientUserIDs, []string{"uAway", "uOffline"}) {
		t.Fatalf("expected [uAway uOffline], got %+v", res)
	}
	if !slices.Equal(msgRepo.lastCands, []string{"uAway", "uOffline", "uOnline"}) {
		t.Fatalf("expected sorted deduplicated candidates, got %v", msgRepo.lastCands)
	}

	// 3. Transient Realtime presence error fails open to eligible recipients rather than failing the push.
	svc.Presence = pushTestPresence{err: errors.New("realtime restarting")}
	res, err = svc.EvaluatePushRecipients(context.Background(), "c1", "m1", []string{"uAway", "uOnline"})
	if err != nil || len(res.RecipientUserIDs) != 3 {
		t.Fatalf("expected fallback to DB-eligible users on transient presence error, got %+v err=%v", res, err)
	}
}
