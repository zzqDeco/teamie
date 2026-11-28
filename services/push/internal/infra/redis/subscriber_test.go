package redisinfra

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"teamie/services/push/internal/domain"
)

type memClaimer struct {
	mu      sync.Mutex
	claimed map[string]bool
}

func (c *memClaimer) ClaimMessage(_ context.Context, messageID string, _ time.Duration) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.claimed[messageID] {
		return false, nil
	}
	c.claimed[messageID] = true
	return true, nil
}

type countingHandler struct {
	count atomic.Int32
}

func (h *countingHandler) HandleMessageHint(_ context.Context, _ domain.PushMessageHint) error {
	h.count.Add(1)
	return nil
}

func TestDispatchClaimedHint_SingleActiveWinnerAcrossConcurrentSlots(t *testing.T) {
	claimer := &memClaimer{claimed: map[string]bool{}}
	handler := &countingHandler{}
	hint := domain.PushMessageHint{
		Type:           "push.message",
		WorkspaceID:    "w1",
		ConversationID: "c1",
		MessageID:      "m-overlap-1",
		SenderID:       "uSender",
		UserIDs:        []string{"u1", "u2"},
	}

	const slots = 16
	var wg sync.WaitGroup
	var winners atomic.Int32
	for i := 0; i < slots; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			claimed, err := DispatchClaimedHint(context.Background(), claimer, handler, hint)
			if err != nil {
				t.Errorf("unexpected error: %v", err)
			}
			if claimed {
				winners.Add(1)
			}
		}()
	}
	wg.Wait()

	if winners.Load() != 1 || handler.count.Load() != 1 {
		t.Fatalf("expected exactly 1 claimed delivery across %d overlapping slots, got winners=%d deliveries=%d",
			slots, winners.Load(), handler.count.Load())
	}

	// Distinct message ID is claimed independently.
	hint2 := hint
	hint2.MessageID = "m-overlap-2"
	claimed, err := DispatchClaimedHint(context.Background(), claimer, handler, hint2)
	if err != nil || !claimed || handler.count.Load() != 2 {
		t.Fatalf("expected second distinct message claimed, got claimed=%v count=%d err=%v", claimed, handler.count.Load(), err)
	}
}
