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

type receiptGate struct {
	lastSent time.Time
	timer    *time.Timer
}

// TypingTracker (aliased as TypingPolicy) enforces per-(user, conversation) signal
// serialization, positive start coalescing, immediate unthrottled stop, idle gate expiration,
// and per-(conversation, recipient) receipt-hint coalescing with trailing delivery.
type TypingTracker struct {
	Interval        time.Duration
	ReceiptInterval time.Duration
	mu              sync.Mutex
	gates           map[string]*typingGate
	receiptGates    map[string]*receiptGate
	receiptContext  context.Context
	receiptCancel   context.CancelFunc
	receiptClosed   bool
	receiptWait     sync.WaitGroup
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

// ScheduleReceiptHints evaluates per-(conversationID, userID) coalescing for receipts.changed hints.
// Recipients outside the throttle window are returned for immediate delivery; throttled recipients
// are scheduled for a single trailing invalidation via onTrailing when their window expires.
func (p *TypingTracker) ScheduleReceiptHints(conversationID string, userIDs []string, onTrailing func(context.Context, string)) []string {
	if p == nil || conversationID == "" || len(userIDs) == 0 {
		return userIDs
	}
	minInterval := p.receiptInterval()
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.receiptClosed {
		return nil
	}
	if p.receiptGates == nil {
		p.receiptGates = make(map[string]*receiptGate)
		p.receiptContext, p.receiptCancel = context.WithCancel(context.Background())
	}
	now := time.Now()
	for k, gate := range p.receiptGates {
		if gate.timer == nil && now.Sub(gate.lastSent) >= minInterval {
			delete(p.receiptGates, k)
		}
	}
	seen := make(map[string]bool, len(userIDs))
	immediate := make([]string, 0, len(userIDs))
	for _, uid := range userIDs {
		if uid == "" || seen[uid] {
			continue
		}
		seen[uid] = true
		key := conversationID + "/" + uid
		gate := p.receiptGates[key]
		if gate == nil {
			gate = &receiptGate{lastSent: now}
			p.receiptGates[key] = gate
			immediate = append(immediate, uid)
			continue
		}
		elapsed := now.Sub(gate.lastSent)
		if elapsed >= minInterval {
			if gate.timer != nil {
				if gate.timer.Stop() {
					p.receiptWait.Done()
				}
				gate.timer = nil
			}
			gate.lastSent = now
			immediate = append(immediate, uid)
			continue
		}
		if gate.timer == nil && onTrailing != nil {
			delay := minInterval - elapsed
			u := uid
			k := key
			g := gate
			p.receiptWait.Add(1)
			var scheduled *time.Timer
			scheduled = time.AfterFunc(delay, func() {
				defer p.receiptWait.Done()
				p.mu.Lock()
				if p.receiptClosed || p.receiptGates[k] != g || g.timer != scheduled {
					p.mu.Unlock()
					return
				}
				g.lastSent = time.Now()
				g.timer = nil
				p.mu.Unlock()
				onTrailing(p.receiptContext, u)
			})
			gate.timer = scheduled
		}
	}
	return immediate
}

// CloseReceiptHints cancels queued and in-flight hints and joins every scheduled
// callback. No receipt work is admitted after shutdown; typing is unaffected.
func (p *TypingTracker) CloseReceiptHints() {
	if p == nil {
		return
	}
	p.mu.Lock()
	p.receiptClosed = true
	if p.receiptCancel != nil {
		p.receiptCancel()
	}
	for _, gate := range p.receiptGates {
		if gate.timer != nil && gate.timer.Stop() {
			p.receiptWait.Done()
		}
		gate.timer = nil
	}
	p.receiptGates = nil
	p.mu.Unlock()
	p.receiptWait.Wait()
}

// AdmitReceiptHint reports whether a receipts.changed hint for conversationID should be delivered
// immediately when no recipient list is supplied.
func (p *TypingTracker) AdmitReceiptHint(conversationID string) bool {
	if p == nil || conversationID == "" {
		return true
	}
	admitted := p.ScheduleReceiptHints(conversationID, []string{"*"}, nil)
	return len(admitted) > 0
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
