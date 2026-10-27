package eino

import (
	"context"
	"sync"
)

// Each Agent Session writer remains independently fenced and serial while other
// Sessions make progress concurrently unless a bounded slots channel is set.
type frontSlot struct {
	mu    sync.Mutex
	slots chan struct{}
	held  bool
}

func (f *frontSlot) acquire(ctx context.Context) error {
	if f == nil || f.slots == nil {
		if ctx != nil {
			return ctx.Err()
		}
		return nil
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.held {
		return nil
	}
	select {
	case f.slots <- struct{}{}:
		f.held = true
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (f *frontSlot) release() {
	if f == nil || f.slots == nil {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.held {
		<-f.slots
		f.held = false
	}
}

// Transfer the initial worker reservation, or acquire a new call-owned permit.
// A stream may expose its terminal error before its producer has cleaned up.
// Its release must therefore never act on a later call's shared held flag.
func (f *frontSlot) modelPermit(ctx context.Context) (func(), error) {
	if f == nil || f.slots == nil {
		if ctx != nil {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}
		return func() {}, nil
	}
	f.mu.Lock()
	reserved := f.held
	if reserved {
		f.held = false
	}
	f.mu.Unlock()
	if !reserved {
		select {
		case f.slots <- struct{}{}:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	var once sync.Once
	return func() { once.Do(func() { <-f.slots }) }, nil
}

func (r *Run) releaseFront() {
	if r != nil {
		r.Front.release()
	}
}
