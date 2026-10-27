package eino

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"time"
)

const (
	readReceiptCoalesceInterval = 2 * time.Second
	readReceiptRequestTimeout   = 2 * time.Second
	readReceiptRecoveryPageSize = 256
	readReceiptMaxBackoff       = 30 * time.Second
)

func (imc *IM) MarkRead(ctx context.Context, conversationID string, seq int64) error {
	if !validUUID(conversationID) || seq <= 0 {
		return nil
	}
	return imc.Call(ctx, http.MethodPost, "/api/v1/conversations/"+conversationID+"/read", map[string]int64{"seq": seq}, nil)
}

type readReceiptEntry struct {
	pendingSeq    int64
	ackedSeq      int64
	suppressedSeq int64
	nextAttempt   time.Time
	failures      int
	active        bool
}

type readReceiptTarget struct {
	conversationID string
	seq            int64
}

// A sender belongs to exactly one worker lease. It only acknowledges ordinary
// IM cursors; it never writes the native Session or starts another TurnLoop.
type readReceiptSender struct {
	mu       sync.Mutex
	entries  map[string]*readReceiptEntry
	wake     chan struct{}
	interval time.Duration
	timeout  time.Duration
	now      func() time.Time
}

func newReadReceiptSender() *readReceiptSender {
	return &readReceiptSender{
		entries:  make(map[string]*readReceiptEntry),
		wake:     make(chan struct{}, 1),
		interval: readReceiptCoalesceInterval,
		timeout:  readReceiptRequestTimeout,
		now:      time.Now,
	}
}

// recordInputs is only called after the inbox transaction commits. The wakeup
// may coalesce; the maximum pending sequence itself cannot be lost.
func (s *readReceiptSender) recordInputs(inputs []Input) {
	s.mu.Lock()
	changed := false
	for _, in := range inputs {
		if in.Kind != "" || !validUUID(in.ConversationID) || in.Seq <= 0 {
			continue
		}
		entry := s.entries[in.ConversationID]
		if entry == nil {
			entry = &readReceiptEntry{nextAttempt: s.now().Add(s.interval)}
			s.entries[in.ConversationID] = entry
		}
		if in.Seq > entry.pendingSeq {
			entry.pendingSeq = in.Seq
			changed = true
		}
	}
	s.mu.Unlock()
	if changed {
		select {
		case s.wake <- struct{}{}:
		default:
		}
	}
}

func (s *readReceiptSender) take(now time.Time) (*readReceiptTarget, time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var id string
	var due time.Time
	for conversationID, entry := range s.entries {
		if entry.active || entry.pendingSeq <= max(entry.ackedSeq, entry.suppressedSeq) {
			continue
		}
		if id == "" || entry.nextAttempt.Before(due) || entry.nextAttempt.Equal(due) && conversationID < id {
			id, due = conversationID, entry.nextAttempt
		}
	}
	if id == "" {
		return nil, -1
	}
	if due.After(now) {
		return nil, due.Sub(now)
	}
	entry := s.entries[id]
	entry.active = true
	return &readReceiptTarget{conversationID: id, seq: entry.pendingSeq}, 0
}

func (s *readReceiptSender) backoff(failures int) time.Duration {
	delay := s.interval
	for i := 1; i < failures && delay < readReceiptMaxBackoff; i++ {
		delay *= 2
	}
	return min(delay, readReceiptMaxBackoff)
}

func (s *readReceiptSender) complete(target *readReceiptTarget, err error, suppress bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry := s.entries[target.conversationID]
	entry.active = false
	delay := s.interval
	switch {
	case err == nil:
		entry.ackedSeq = max(entry.ackedSeq, target.seq)
		entry.failures = 0
	case suppress:
		// A denied cursor is not an acknowledgement. Suppression is confined to
		// this lease and never consumes a higher sequence recorded in flight.
		entry.suppressedSeq = max(entry.suppressedSeq, target.seq)
		entry.failures = 0
	default:
		entry.failures++
		delay = s.backoff(entry.failures)
	}
	entry.nextAttempt = s.now().Add(delay)
}

func stopReadReceipts(err error) bool {
	if errors.Is(err, ErrLeaseLost) {
		return true
	}
	var model *ModelError
	var api *APIError
	code := ""
	if errors.As(err, &model) {
		code = model.Code
	} else if errors.As(err, &api) {
		code = api.Code
	}
	return code == "principal_disabled" || code == "principal_deleted" || code == "runtime_disabled"
}

// start returns a cancel-and-join function. The caller must join before releasing
// its lease; a later worker always constructs a fresh sender and recovery scan.
func (s *readReceiptSender) start(parent context.Context, store *Store, lease *Lease, authorize func(context.Context) error, markRead func(context.Context, string, int64) error) func() {
	ctx, cancel := context.WithCancel(parent)
	done := make(chan struct{})
	ownedLease := *lease
	go func() {
		defer close(done)
		s.run(ctx, store, &ownedLease, authorize, markRead)
	}()
	return func() {
		cancel()
		<-done
	}
}

func (s *readReceiptSender) run(ctx context.Context, store *Store, lease *Lease, authorize func(context.Context) error, markRead func(context.Context, string, int64) error) {
	after, through := "0", ""
	captured, recovered := false, false
	recoveryAt := s.now()
	recoveryFailures := 0
	for ctx.Err() == nil {
		if !recovered && !s.now().Before(recoveryAt) {
			rc, cancel := context.WithTimeout(ctx, s.timeout)
			err := store.CheckLease(rc, lease)
			if err == nil && !captured {
				through, err = store.Cursor(rc, lease)
				captured = err == nil
			}
			if err == nil {
				page, pageErr := store.ReadReceiptInputs(rc, lease, after, through, readReceiptRecoveryPageSize)
				err = pageErr
				if err == nil {
					if page.HasMore && page.NextEventID == after {
						err = errors.New("read receipt recovery did not advance")
					} else {
						inputs := make([]Input, 0, len(page.Items))
						for _, position := range page.Items {
							inputs = append(inputs, Input{ConversationID: position.ConversationID, Seq: position.Seq})
						}
						s.recordInputs(inputs)
						after, recovered = page.NextEventID, !page.HasMore
					}
				}
			}
			cancel()
			if ctx.Err() != nil || stopReadReceipts(err) {
				return
			}
			if err != nil {
				recoveryFailures++
				recoveryAt = s.now().Add(s.backoff(recoveryFailures))
			} else {
				recoveryFailures = 0
				recoveryAt = s.now()
			}
		}
		if ctx.Err() != nil {
			return
		}
		target, delay := s.take(s.now())
		if target != nil {
			rc, cancel := context.WithTimeout(ctx, s.timeout)
			err := authorize(rc)
			if err == nil {
				err = store.CheckLease(rc, lease)
			}
			suppress := false
			if err == nil && rc.Err() == nil {
				err = markRead(rc, target.conversationID, target.seq)
				var api *APIError
				suppress = errors.As(err, &api) && (api.Status == 400 || api.Status == 403 || api.Status == 404)
			} else if err == nil {
				err = rc.Err()
			}
			cancel()
			if ctx.Err() != nil || stopReadReceipts(err) {
				return
			}
			s.complete(target, err, suppress)
			continue
		}
		if !recovered {
			waitRecovery := recoveryAt.Sub(s.now())
			if waitRecovery <= 0 {
				continue
			}
			if delay < 0 || waitRecovery < delay {
				delay = waitRecovery
			}
		}
		var timer *time.Timer
		var tick <-chan time.Time
		if delay >= 0 {
			timer = time.NewTimer(delay)
			tick = timer.C
		}
		select {
		case <-ctx.Done():
		case <-s.wake:
		case <-tick:
		}
		if timer != nil {
			timer.Stop()
		}
	}
}
