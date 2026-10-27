package domain

import (
	"sort"
	"sync"
	"time"
)

const (
	PresenceIdleTimeout      = 5 * time.Minute
	PresenceOfflineGrace     = 30 * time.Second
	PresenceThrottleInterval = 30 * time.Second
)

type connPresence struct {
	lastActive time.Time
	idle       bool
}

type userPresence struct {
	conns          map[Client]*connPresence
	disconnectedAt time.Time
	lastPublished  string
	graceGen       uint64
}

// PresenceTracker tracks per-user and per-connection human presence state
// (online, away, offline) with idle timeout and disconnect grace period.
type PresenceTracker struct {
	IdleTimeout  time.Duration
	OfflineGrace time.Duration
	Now          func() time.Time

	mu    sync.Mutex
	users map[string]*userPresence
}

func (p *PresenceTracker) now() time.Time {
	if p != nil && p.Now != nil {
		return p.Now()
	}
	return time.Now()
}

func (p *PresenceTracker) idleTimeout() time.Duration {
	if p != nil && p.IdleTimeout > 0 {
		return p.IdleTimeout
	}
	return PresenceIdleTimeout
}

func (p *PresenceTracker) offlineGrace() time.Duration {
	if p != nil && p.OfflineGrace > 0 {
		return p.OfflineGrace
	}
	return PresenceOfflineGrace
}

func (p *PresenceTracker) ensureUsers() {
	if p.users == nil {
		p.users = make(map[string]*userPresence)
	}
}

func computeConnectedState(up *userPresence, now time.Time, idleTimeout time.Duration) string {
	for _, cp := range up.conns {
		if !cp.idle && now.Sub(cp.lastActive) < idleTimeout {
			return "online"
		}
	}
	return "away"
}

// Connect registers a WebSocket connection for presence tracking. Only human
// participants are tracked; Agent connections are ignored.
func (p *PresenceTracker) Connect(c Client, kind string) (changed bool, state string) {
	if p == nil || c == nil || kind != "human" {
		return false, ""
	}
	now := p.now()
	idleTimeout := p.idleTimeout()
	p.mu.Lock()
	defer p.mu.Unlock()
	p.ensureUsers()
	up := p.users[c.UserID()]
	if up == nil {
		up = &userPresence{conns: make(map[Client]*connPresence)}
		p.users[c.UserID()] = up
	}
	up.conns[c] = &connPresence{lastActive: now, idle: false}
	up.disconnectedAt = time.Time{}
	up.graceGen++
	state = computeConnectedState(up, now, idleTimeout)
	if up.lastPublished != state {
		up.lastPublished = state
		return true, state
	}
	return false, state
}

// Report records a client presence frame ("active" or "idle") for a connection.
func (p *PresenceTracker) Report(c Client, kind, clientState string) (changed bool, state string, err error) {
	if clientState != "active" && clientState != "idle" {
		return false, "", ErrInvalidRequest("state must be active or idle")
	}
	if p == nil || c == nil || kind != "human" {
		return false, "", nil
	}
	now := p.now()
	idleTimeout := p.idleTimeout()
	p.mu.Lock()
	defer p.mu.Unlock()
	p.ensureUsers()
	up := p.users[c.UserID()]
	if up == nil {
		up = &userPresence{conns: make(map[Client]*connPresence)}
		p.users[c.UserID()] = up
	}
	cp := up.conns[c]
	if cp == nil {
		cp = &connPresence{lastActive: now}
		up.conns[c] = cp
	}
	if clientState == "active" {
		cp.idle = false
		cp.lastActive = now
	} else {
		cp.idle = true
	}
	up.disconnectedAt = time.Time{}
	state = computeConnectedState(up, now, idleTimeout)
	if up.lastPublished != state {
		up.lastPublished = state
		return true, state, nil
	}
	return false, state, nil
}

// Disconnect removes a closed WebSocket connection and starts the 30-second
// offline grace timer if it was the user's last connection.
func (p *PresenceTracker) Disconnect(c Client, onTransition func(userID, state string)) {
	if p == nil || c == nil {
		return
	}
	now := p.now()
	idleTimeout := p.idleTimeout()
	grace := p.offlineGrace()
	userID := c.UserID()

	var immediateState string
	var scheduleGrace bool
	var gen uint64

	p.mu.Lock()
	up := p.users[userID]
	if up == nil {
		p.mu.Unlock()
		return
	}
	delete(up.conns, c)
	if len(up.conns) > 0 {
		state := computeConnectedState(up, now, idleTimeout)
		if up.lastPublished != state {
			up.lastPublished = state
			immediateState = state
		}
	} else {
		up.disconnectedAt = now
		up.graceGen++
		gen = up.graceGen
		scheduleGrace = true
	}
	p.mu.Unlock()

	if immediateState != "" && onTransition != nil {
		onTransition(userID, immediateState)
	}
	if scheduleGrace {
		time.AfterFunc(grace, func() {
			p.mu.Lock()
			cur := p.users[userID]
			if cur == nil || len(cur.conns) > 0 || cur.graceGen != gen {
				p.mu.Unlock()
				return
			}
			delete(p.users, userID)
			p.mu.Unlock()
			if onTransition != nil {
				onTransition(userID, "offline")
			}
		})
	}
}

// Sweep evaluates idle timeouts and offline grace expirations against p.Now().
func (p *PresenceTracker) Sweep(onTransition func(userID, state string)) {
	if p == nil {
		return
	}
	now := p.now()
	idleTimeout := p.idleTimeout()
	grace := p.offlineGrace()

	type transition struct {
		userID string
		state  string
	}
	var transitions []transition

	p.mu.Lock()
	for uid, up := range p.users {
		if len(up.conns) > 0 {
			state := computeConnectedState(up, now, idleTimeout)
			if up.lastPublished != state {
				up.lastPublished = state
				transitions = append(transitions, transition{userID: uid, state: state})
			}
		} else if !up.disconnectedAt.IsZero() && now.Sub(up.disconnectedAt) >= grace {
			delete(p.users, uid)
			transitions = append(transitions, transition{userID: uid, state: "offline"})
		}
	}
	p.mu.Unlock()

	if onTransition != nil {
		for _, tr := range transitions {
			onTransition(tr.userID, tr.state)
		}
	}
}

// Snapshot returns current non-offline presence items for the given userIDs.
func (p *PresenceTracker) Snapshot(userIDs []string) []PresenceItem {
	if p == nil || len(userIDs) == 0 {
		return []PresenceItem{}
	}
	now := p.now()
	idleTimeout := p.idleTimeout()
	grace := p.offlineGrace()

	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]PresenceItem, 0, len(userIDs))
	seen := make(map[string]bool, len(userIDs))
	for _, uid := range userIDs {
		if seen[uid] {
			continue
		}
		seen[uid] = true
		up := p.users[uid]
		if up == nil {
			continue
		}
		if len(up.conns) > 0 {
			state := computeConnectedState(up, now, idleTimeout)
			out = append(out, PresenceItem{UserID: uid, State: state})
		} else if !up.disconnectedAt.IsZero() && now.Sub(up.disconnectedAt) < grace && (up.lastPublished == "online" || up.lastPublished == "away") {
			out = append(out, PresenceItem{UserID: uid, State: up.lastPublished})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].UserID < out[j].UserID
	})
	return out
}
