package domain

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

func TestReceiptShutdownCancelsPendingAndJoinsRunningDelivery(t *testing.T) {
	p := &TypingTracker{ReceiptInterval: 10 * time.Millisecond}
	t.Cleanup(p.CloseReceiptHints)
	started := make(chan struct{})
	finished := make(chan struct{})
	deliver := func(ctx context.Context, _ string) {
		close(started)
		<-ctx.Done()
		close(finished)
	}
	p.ScheduleReceiptHints("conversation", []string{"running"}, deliver)
	p.ScheduleReceiptHints("conversation", []string{"running"}, deliver)
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("trailing delivery did not start")
	}
	var pendingCalls atomic.Int32
	pending := func(context.Context, string) { pendingCalls.Add(1) }
	p.ReceiptInterval = time.Hour
	p.ScheduleReceiptHints("conversation", []string{"pending"}, pending)
	p.ScheduleReceiptHints("conversation", []string{"pending"}, pending)
	p.CloseReceiptHints()
	select {
	case <-finished:
	default:
		t.Fatal("shutdown returned before delivery observed cancellation")
	}
	if got := pendingCalls.Load(); got != 0 {
		t.Fatalf("shutdown delivered %d pending callbacks", got)
	}
	if got := p.ScheduleReceiptHints("conversation", []string{"late"}, pending); len(got) != 0 {
		t.Fatalf("closed tracker admitted recipients: %v", got)
	}
}
