package domain

import (
	"context"
	"sync"
	"time"
)

const (
	TypingMinimumInterval = 300 * time.Millisecond
	TypingTTL             = 5 * time.Second
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
// serialization, positive start coalescing, immediate unthrottled stop, and idle gate expiration.
type TypingTracker struct {
	Interval time.Duration
	mu       sync.Mutex
	gates    map[string]*typingGate
}

// TypingPolicy is an alias for TypingTracker.
type TypingPolicy = TypingTracker

func (p *TypingTracker) interval() time.Duration {
	if p.Interval > 0 {
		return p.Interval
	}
	return TypingMinimumInterval
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
