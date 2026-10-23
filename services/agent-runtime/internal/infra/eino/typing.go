package eino

import (
	"context"
	"net/http"
	"time"
)

// Browser typing expires five seconds after its last hint. Refresh often enough
// to cover normal scheduling/network jitter without creating durable activity.
const typingRefreshInterval = 1500 * time.Millisecond
const typingRequestTimeout = time.Second

func (imc *IM) Typing(ctx context.Context, conversationID string, active bool) error {
	if !validUUID(conversationID) {
		return &APIError{Status: 400, Code: "invalid_id"}
	}
	return imc.callAt(ctx, imc.RealtimeURL, http.MethodPost, "/api/v1/conversations/"+conversationID+"/typing", map[string]bool{"isTyping": active}, nil)
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
