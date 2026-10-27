package domain

import (
	"context"
	"sync"
	"time"
)

const (
	TypingMinimumInterval  = 300 * time.Millisecond
	TypingTTL              = 5 * time.Second
	ReceiptMinimumInterval = time.Second
)

// TypingKey uniquely identifies a participant's typing stream in a conversation.
type TypingKey struct {
	UserID         string
	ConversationID string
}

func (k TypingKey) String() string {
	return k.UserID + "/" + k.ConversationID
}

type typingGate struct {
	active       chan struct{}
	refs         int
	lastPositive time.Time
}

// TypingTracker (aliased as TypingPolicy) enforces per-(user, conversation) signal
// serialization, positive start coalescing, immediate unthrottled stop, idle gate expiration,
// and per-conversation receipt-hint coalescing.
type TypingTracker struct {
	Interval        time.Duration
	ReceiptInterval time.Duration
	mu              sync.Mutex
	gates           map[string]*typingGate
	receiptGates    map[string]time.Time
}

// TypingPolicy is an alias for TypingTracker.
type TypingPolicy = TypingTracker

func (p *TypingTracker) interval() time.Duration {
	if p.Interval > 0 {
		return p.Interval
	}
	return TypingMinimumInterval
}

func (p *TypingTracker) receiptInterval() time.Duration {
	if p != nil && p.ReceiptInterval > 0 {
		return p.ReceiptInterval
	}
	return ReceiptMinimumInterval
}

// AdmitReceiptHint reports whether a receipts.changed hint for conversationID should be delivered
// or coalesced (at most one per conversation per second).
func (p *TypingTracker) AdmitReceiptHint(conversationID string) bool {
	if p == nil || conversationID == "" {
		return true
	}
	minInterval := p.receiptInterval()
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.receiptGates == nil {
		p.receiptGates = make(map[string]time.Time)
	}
	now := time.Now()
	for id, last := range p.receiptGates {
		if now.Sub(last) >= minInterval {
			delete(p.receiptGates, id)
		}
	}
	if last, ok := p.receiptGates[conversationID]; ok && now.Sub(last) < minInterval {
		return false
	}
	p.receiptGates[conversationID] = now
	return true
}

// Lock acquires the per-(user, conversation) serialization gate.
func (p *TypingTracker) Lock(ctx context.Context, key TypingKey) (func(), error) {
	k := key.String()
	minInterval := p.interval()
	p.mu.Lock()
	if p.gates == nil {
		p.gates = map[string]*typingGate{}
	}
	now := time.Now()
	for id, gate := range p.gates {
		if gate.refs == 0 && now.Sub(gate.lastPositive) >= minInterval {
			delete(p.gates, id)
		}
	}
	gate := p.gates[k]
	if gate == nil {
		gate = &typingGate{active: make(chan struct{}, 1)}
		p.gates[k] = gate
	}
	gate.refs++
	p.mu.Unlock()

	forget := func() {
		p.mu.Lock()
		gate.refs--
		if gate.refs == 0 && time.Since(gate.lastPositive) >= minInterval {
			delete(p.gates, k)
		}
		p.mu.Unlock()
	}

	if err := ctx.Err(); err != nil {
		forget()
		return nil, err
	}
	select {
	case gate.active <- struct{}{}:
		return func() { <-gate.active; forget() }, nil
	case <-ctx.Done():
		forget()
		return nil, ctx.Err()
	}
}

// AdmitPositive reports whether a positive typing hint should be published or coalesced.
func (p *TypingTracker) AdmitPositive(key TypingKey) bool {
	k := key.String()
	minInterval := p.interval()
	p.mu.Lock()
	defer p.mu.Unlock()
	gate := p.gates[k]
	if gate == nil {
		gate = &typingGate{active: make(chan struct{}, 1)}
		p.gates[k] = gate
	}
	now := time.Now()
	if now.Sub(gate.lastPositive) < minInterval {
		return false
	}
	gate.lastPositive = now
	return true
}

// ResetPositive clears the positive-hint throttle after an explicit stop signal.
func (p *TypingTracker) ResetPositive(key TypingKey) {
	k := key.String()
	p.mu.Lock()
	if gate := p.gates[k]; gate != nil {
		gate.lastPositive = time.Time{}
	}
	p.mu.Unlock()
}
