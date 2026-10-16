package engine

import (
	"context"
	"sync"
)

// The host has one foreground slot. Awaiting Sandbox/background work releases
// it; the next model operation must acquire it again. Agent Session writers
// remain independently fenced and serial throughout a wait.
type frontSlot struct {
	mu    sync.Mutex
	slots chan struct{}
	held  bool
}

func (f *frontSlot) acquire(ctx context.Context) error {
	if f == nil {
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
	if f == nil {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.held {
		<-f.slots
		f.held = false
	}
}
func (r *Run) releaseFront() {
	if r != nil {
		r.Front.release()
	}
}
