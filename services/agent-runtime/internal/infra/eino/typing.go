package eino

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"time"
)

// Browser typing expires five seconds after its last hint. Refresh often enough
// to cover normal scheduling/network jitter without creating durable activity.
const typingRefreshInterval = 1500 * time.Millisecond
const typingRequestTimeout = time.Second
const readReceiptCoalesceInterval = 2 * time.Second

func (imc *IM) Typing(ctx context.Context, conversationID string, active bool) error {
	if !validUUID(conversationID) {
		return &APIError{Status: 400, Code: "invalid_id"}
	}
	return imc.callAt(ctx, imc.RealtimeURL, http.MethodPost, "/api/v1/conversations/"+conversationID+"/typing", map[string]bool{"isTyping": active}, nil)
}

func (imc *IM) MarkRead(ctx context.Context, conversationID string, seq int64) error {
	if !validUUID(conversationID) || seq <= 0 {
		return nil
	}
	return imc.Call(ctx, http.MethodPost, "/api/v1/conversations/"+conversationID+"/read", map[string]int64{"seq": seq}, nil)
}

type readReceiptEntry struct {
	conversationID string
	pendingSeq     int64
	ackedSeq       int64
	lastSentAt     time.Time
}

type readReceiptFlusher struct {
	mu      sync.Mutex
	entries map[string]*readReceiptEntry
	now     func() time.Time
}

func (f *readReceiptFlusher) recordInputs(agentID string, inputs []Input) {
	if f == nil || agentID == "" {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.entries == nil {
		f.entries = make(map[string]*readReceiptEntry)
	}
	for _, in := range inputs {
		if in.Kind != "" || !validUUID(in.ConversationID) || in.Seq <= 0 {
			continue
		}
		key := agentID + "/" + in.ConversationID
		ent := f.entries[key]
		if ent == nil {
			ent = &readReceiptEntry{conversationID: in.ConversationID}
			f.entries[key] = ent
		}
		if in.Seq > ent.pendingSeq {
			ent.pendingSeq = in.Seq
		}
	}
}

func (f *readReceiptFlusher) flush(ctx context.Context, agentID string, markRead func(context.Context, string, int64) error) {
	if f == nil || agentID == "" || markRead == nil {
		return
	}
	nowFn := time.Now
	if f.now != nil {
		nowFn = f.now
	}
	now := nowFn()

	type target struct {
		key    string
		convID string
		seq    int64
	}
	var due []target
	prefix := agentID + "/"

	f.mu.Lock()
	for key, ent := range f.entries {
		if len(key) <= len(prefix) || key[:len(prefix)] != prefix {
			continue
		}
		if ent.pendingSeq > ent.ackedSeq && (ent.lastSentAt.IsZero() || now.Sub(ent.lastSentAt) >= readReceiptCoalesceInterval) {
			due = append(due, target{key: key, convID: ent.conversationID, seq: ent.pendingSeq})
		}
	}
	f.mu.Unlock()

	for _, t := range due {
		if ctx.Err() != nil {
			return
		}
		rc, cancel := context.WithTimeout(ctx, 2*time.Second)
		err := markRead(rc, t.convID, t.seq)
		cancel()
		var api *APIError
		terminalDrop := errors.As(err, &api) && (api.Status == 400 || api.Status == 403 || api.Status == 404)
		if err == nil || terminalDrop {
			f.mu.Lock()
			if ent := f.entries[t.key]; ent != nil {
				if t.seq > ent.ackedSeq {
					ent.ackedSeq = t.seq
				}
				if err == nil {
					ent.lastSentAt = nowFn()
				}
			}
			f.mu.Unlock()
		}
	}
}

func typingConversations(inputs []Input) []string {
	seen := map[string]bool{}
	conversations := []string{}
	for _, input := range inputs {
		// Native background notifications carry no new IM participation. A
		// global Session's history is deliberately not a destination list.
		if input.Kind != "" || !validUUID(input.ConversationID) || seen[input.ConversationID] {
			continue
		}
		seen[input.ConversationID] = true
		conversations = append(conversations, input.ConversationID)
	}
	return conversations
}

type typingPublish func(context.Context, string, bool) error

// One goroutine serializes all positive hints and their final clears. Stop
// cancels any in-flight refresh and joins it before the next turn may start;
// neither a late tick nor a failed request can resurrect a finished turn.
// ticks is injectable to exercise refresh/cancellation without wall-clock waits.
func beginTyping(ctx context.Context, inputs []Input, publish typingPublish, authorize func(context.Context) error, ticks <-chan time.Time) func() {
	conversations := typingConversations(inputs)
	if len(conversations) == 0 {
		return func() {}
	}
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		var ticker *time.Ticker
		if ticks == nil {
			ticker = time.NewTicker(typingRefreshInterval)
			defer ticker.Stop()
			ticks = ticker.C
		}
		attempted := map[string]bool{}
		defer func() {
			// Cleanup intentionally survives run cancellation but is bounded as
			// a whole. Revoked credentials/transport failures fall back to TTL.
			clear, finish := context.WithTimeout(context.WithoutCancel(ctx), typingRequestTimeout)
			defer finish()
			for _, conversation := range conversations {
				if attempted[conversation] && clear.Err() == nil {
					_ = publish(clear, conversation, false)
				}
			}
		}()
		refresh := func() bool {
			pc, finish := context.WithTimeout(ctx, typingRequestTimeout)
			defer finish()
			if pc.Err() != nil || authorize(pc) != nil || pc.Err() != nil {
				return false
			}
			for _, conversation := range conversations {
				if pc.Err() != nil {
					break
				}
				// Even a lost HTTP response may already have published the hint.
				attempted[conversation] = true
				_ = publish(pc, conversation, true)
			}
			return ctx.Err() == nil
		}
		if !refresh() {
			return
		}
		for {
			select {
			case <-ctx.Done():
				return
			case _, ok := <-ticks:
				if !ok || !refresh() {
					return
				}
			}
		}
	}()
	return func() { cancel(); <-done }
}
