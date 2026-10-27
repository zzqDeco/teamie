package realtime

import (
	"context"
	"testing"
	"time"
)

func TestReceiptTrailingUsesLiveClientsAfterSourceContextCancelled(t *testing.T) {
	access := &workspacePresenceAccess{convAllowed: map[string]bool{"alice": true}}
	old := &peer{user: "alice", queue: make(chan Hint, 8), done: make(chan struct{})}
	h := &Hub{Chat: access, peers: map[*peer]struct{}{old: {}}}
	h.policy.ReceiptInterval = 40 * time.Millisecond
	t.Cleanup(h.policy.CloseReceiptHints)
	hint := Hint{Type: "receipts.changed", ConversationID: "conv-1", UserIDs: []string{"alice"}}
	h.deliver(context.Background(), hint)
	<-old.queue
	ctx, cancel := context.WithCancel(context.Background())
	h.deliver(ctx, hint)
	cancel()
	fresh := &peer{user: "alice", queue: make(chan Hint, 8), done: make(chan struct{})}
	h.mu.Lock()
	delete(h.peers, old)
	h.peers[fresh] = struct{}{}
	h.mu.Unlock()
	select {
	case ev := <-fresh.queue:
		if ev.Type != "receipts.changed" {
			t.Fatalf("unexpected event %+v", ev)
		}
	case <-time.After(400 * time.Millisecond):
		t.Fatal("trailing receipt did not reach the newly connected client")
	}
}

func TestReceiptTrailingRevalidatesRevokedAccess(t *testing.T) {
	access := &workspacePresenceAccess{convAllowed: map[string]bool{"alice": true}}
	alice := &peer{user: "alice", queue: make(chan Hint, 8), done: make(chan struct{})}
	h := &Hub{Chat: access, peers: map[*peer]struct{}{alice: {}}}
	h.policy.ReceiptInterval = 40 * time.Millisecond
	t.Cleanup(h.policy.CloseReceiptHints)
	hint := Hint{Type: "receipts.changed", ConversationID: "conv-1", UserIDs: []string{"alice"}}
	h.deliver(context.Background(), hint)
	<-alice.queue
	h.deliver(context.Background(), hint)
	access.mu.Lock()
	access.convAllowed["alice"] = false
	access.mu.Unlock()
	select {
	case ev := <-alice.queue:
		t.Fatalf("revoked recipient received trailing event %+v", ev)
	case <-time.After(150 * time.Millisecond):
	}
}
