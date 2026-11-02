package app

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"teamie/services/llm-gateway/internal/domain"
)

const (
	// DefaultProviderMaxWorkers bounds simultaneous in-flight upstream requests per
	// ProviderProfile so bursts on one provider queue behind its virtual workers
	// without exhausting global gateway slots or triggering upstream concurrency 429s.
	DefaultProviderMaxWorkers = 4
	// DefaultProviderMaxQueue bounds waiting callers queued behind a single provider's
	// virtual workers; excess burst callers fail fast with 429 gateway_busy.
	DefaultProviderMaxQueue = 128
	// DefaultRateLimitRetries bounds automatic gateway retries on upstream HTTP 429
	// before any streaming chunks have been emitted.
	DefaultRateLimitRetries = 2
	// DefaultRateLimitBaseBackoff is the initial exponential backoff window on 429.
	DefaultRateLimitBaseBackoff = 800 * time.Millisecond
	// DefaultRateLimitMaxJitter is the random jitter added to each 429 backoff window.
	DefaultRateLimitMaxJitter = 400 * time.Millisecond

	providerIdleTTL = 5 * time.Minute
)

type providerPoolConfig struct {
	workersConfigured bool
	maxWorkers        int
	maxQueue          int
	retriesConfigured bool
	maxRetries        int
	baseBackoff       time.Duration
	maxJitter         time.Duration
}

type providerWaiter struct {
	ready chan struct{}
}

type providerGate struct {
	active        int
	waiters       []*providerWaiter
	cooldownUntil time.Time
	lastActive    time.Time
}

// ParseProviderMaxWorkers parses GATEWAY_PROVIDER_MAX_WORKERS.
// An empty value returns DefaultProviderMaxWorkers; a non-empty value must be >= 1.
func ParseProviderMaxWorkers(raw string) (int, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return DefaultProviderMaxWorkers, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 {
		return 0, domain.Err(400, "invalid_provider_workers", "GATEWAY_PROVIDER_MAX_WORKERS must be an integer >= 1")
	}
	return n, nil
}

func defaultProviderMaxWorkers() int {
	if n, err := ParseProviderMaxWorkers(os.Getenv("GATEWAY_PROVIDER_MAX_WORKERS")); err == nil {
		return n
	}
	return DefaultProviderMaxWorkers
}

// WithProviderWorkers configures per-ProviderProfile virtual worker concurrency and wait queue bounds.
// Passing maxWorkers <= 0 disables the per-provider cap; maxQueue < 0 falls back to DefaultProviderMaxQueue.
func WithProviderWorkers(maxWorkers, maxQueue int) Option {
	return func(s *Service) {
		s.SetProviderWorkers(maxWorkers, maxQueue)
	}
}

// WithRateLimitRetry configures automatic retry and jittered exponential backoff for
// upstream 429 (upstream_rate_limited) responses before any output chunk is emitted.
func WithRateLimitRetry(maxRetries int, baseBackoff, maxJitter time.Duration) Option {
	return func(s *Service) {
		s.SetRateLimitRetry(maxRetries, baseBackoff, maxJitter)
	}
}

// SetProviderWorkers updates per-ProviderProfile virtual worker bounds at runtime or in tests.
// Passing maxWorkers <= 0 disables per-provider worker capping; maxQueue == 0 disables queueing
// (excess concurrent calls on the provider fail immediately with 429 gateway_busy).
func (s *Service) SetProviderWorkers(maxWorkers, maxQueue int) {
	if maxWorkers < 0 {
		maxWorkers = 0
	}
	if maxQueue < 0 {
		maxQueue = DefaultProviderMaxQueue
	}
	s.mu.Lock()
	s.pool.workersConfigured = true
	s.pool.maxWorkers = maxWorkers
	s.pool.maxQueue = maxQueue
	for _, g := range s.providers {
		s.wakeProviderWaitersLocked(g, maxWorkers)
	}
	s.mu.Unlock()
}

// ProviderMaxWorkers returns the effective per-ProviderProfile virtual worker limit.
func (s *Service) ProviderMaxWorkers() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	w, _ := s.effectiveProviderWorkersLocked()
	return w
}

// SetRateLimitRetry updates upstream 429 retry parameters at runtime or in tests.
func (s *Service) SetRateLimitRetry(maxRetries int, baseBackoff, maxJitter time.Duration) {
	if maxRetries < 0 {
		maxRetries = 0
	}
	if baseBackoff < 0 {
		baseBackoff = 0
	}
	if maxJitter < 0 {
		maxJitter = 0
	}
	s.mu.Lock()
	s.pool.retriesConfigured = true
	s.pool.maxRetries = maxRetries
	s.pool.baseBackoff = baseBackoff
	s.pool.maxJitter = maxJitter
	s.mu.Unlock()
}

func randomJitter(max time.Duration) time.Duration {
	if max <= 0 {
		return 0
	}
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return max / 2
	}
	return time.Duration(binary.LittleEndian.Uint64(b[:]) % uint64(max+1))
}

func providerWorkerKey(c domain.ResolvedModel) string {
	if id := strings.TrimSpace(c.ProviderID); id != "" {
		return "provider:" + id
	}
	return "url:" + strings.TrimSpace(c.BaseURL)
}

func (s *Service) effectiveProviderWorkersLocked() (maxWorkers, maxQueue int) {
	if s.pool.workersConfigured {
		return s.pool.maxWorkers, s.pool.maxQueue
	}
	return defaultProviderMaxWorkers(), DefaultProviderMaxQueue
}

func (s *Service) effectiveRateLimitRetryLocked() (maxRetries int, baseBackoff, maxJitter time.Duration) {
	if s.pool.retriesConfigured {
		return s.pool.maxRetries, s.pool.baseBackoff, s.pool.maxJitter
	}
	return DefaultRateLimitRetries, DefaultRateLimitBaseBackoff, DefaultRateLimitMaxJitter
}

func (s *Service) providerGateLocked(key string, now time.Time) *providerGate {
	if s.providers == nil {
		s.providers = make(map[string]*providerGate)
	}
	for k, gate := range s.providers {
		if k != key && gate.active == 0 && len(gate.waiters) == 0 && !gate.cooldownUntil.After(now) && now.Sub(gate.lastActive) > providerIdleTTL {
			delete(s.providers, k)
		}
	}
	gate := s.providers[key]
	if gate == nil {
		gate = &providerGate{lastActive: now}
		s.providers[key] = gate
	}
	return gate
}

func (s *Service) wakeProviderWaitersLocked(gate *providerGate, maxWorkers int) {
	if gate == nil {
		return
	}
	for (maxWorkers <= 0 || gate.active < maxWorkers) && len(gate.waiters) > 0 {
		w := gate.waiters[0]
		gate.waiters[0] = nil
		gate.waiters = gate.waiters[1:]
		if len(gate.waiters) == 0 {
			gate.waiters = nil
		}
		gate.active++
		gate.lastActive = time.Now()
		close(w.ready)
	}
}

func (s *Service) releaseProviderWorkerLocked(key string) {
	gate := s.providers[key]
	if gate == nil {
		return
	}
	if gate.active > 0 {
		gate.active--
	}
	gate.lastActive = time.Now()
	maxWorkers, _ := s.effectiveProviderWorkersLocked()
	s.wakeProviderWaitersLocked(gate, maxWorkers)
}

func (s *Service) makeProviderRelease(key string) func() {
	var once sync.Once
	return func() {
		once.Do(func() {
			s.mu.Lock()
			s.releaseProviderWorkerLocked(key)
			s.mu.Unlock()
		})
	}
}

func (s *Service) acquireProviderWorker(ctx context.Context, key string) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, domain.StreamError(ctx, "gateway_unavailable")
	}
	if StrictModelSelection(ctx) {
		return func() {}, nil
	}

	now := time.Now()
	s.mu.Lock()
	maxWorkers, maxQueue := s.effectiveProviderWorkersLocked()
	if maxWorkers <= 0 {
		s.mu.Unlock()
		return func() {}, nil
	}
	gate := s.providerGateLocked(key, now)
	if gate.active < maxWorkers && len(gate.waiters) == 0 {
		gate.active++
		gate.lastActive = now
		s.mu.Unlock()
		return s.makeProviderRelease(key), nil
	}
	if len(gate.waiters) >= maxQueue {
		s.mu.Unlock()
		return nil, domain.Err(429, "gateway_busy", "The model gateway is busy")
	}
	w := &providerWaiter{ready: make(chan struct{})}
	gate.waiters = append(gate.waiters, w)
	gate.lastActive = now
	s.mu.Unlock()

	select {
	case <-w.ready:
		if ctx.Err() != nil {
			s.mu.Lock()
			s.releaseProviderWorkerLocked(key)
			s.mu.Unlock()
			return nil, domain.StreamError(ctx, "gateway_unavailable")
		}
		return s.makeProviderRelease(key), nil
	case <-ctx.Done():
		s.mu.Lock()
		select {
		case <-w.ready:
			s.releaseProviderWorkerLocked(key)
			s.mu.Unlock()
			return nil, domain.StreamError(ctx, "gateway_unavailable")
		default:
			for i, candidate := range gate.waiters {
				if candidate == w {
					copy(gate.waiters[i:], gate.waiters[i+1:])
					gate.waiters[len(gate.waiters)-1] = nil
					gate.waiters = gate.waiters[:len(gate.waiters)-1]
					if len(gate.waiters) == 0 {
						gate.waiters = nil
					}
					break
				}
			}
			gate.lastActive = time.Now()
			s.mu.Unlock()
			return nil, domain.StreamError(ctx, "gateway_unavailable")
		}
	}
}

func (s *Service) waitProviderCooldown(ctx context.Context, key string) error {
	if StrictModelSelection(ctx) {
		return nil
	}
	s.mu.Lock()
	var until time.Time
	if gate := s.providers[key]; gate != nil {
		until = gate.cooldownUntil
	}
	s.mu.Unlock()
	if until.IsZero() {
		return sleepWithContext(ctx, 0)
	}
	return sleepWithContext(ctx, time.Until(until))
}

func sleepWithContext(ctx context.Context, d time.Duration) error {
	if err := ctx.Err(); err != nil {
		return domain.StreamError(ctx, "gateway_unavailable")
	}
	if d <= 0 {
		return nil
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return domain.StreamError(ctx, "gateway_unavailable")
	}
}

func isRetryableUpstreamRateLimit(err error) bool {
	var de *domain.Error
	return errors.As(err, &de) && de.Status == 429 && de.Code == "upstream_rate_limited"
}

func (s *Service) planRateLimitRetry(ctx context.Context, key string, attempt int) (time.Duration, bool) {
	if ctx.Err() != nil {
		return 0, false
	}
	s.mu.Lock()
	maxRetries, baseBackoff, maxJitter := s.effectiveRateLimitRetryLocked()
	if attempt >= maxRetries {
		s.mu.Unlock()
		return 0, false
	}
	shift := attempt
	if shift > 4 {
		shift = 4
	}
	backoff := (baseBackoff << shift) + randomJitter(maxJitter)
	if backoff > 0 {
		now := time.Now()
		gate := s.providerGateLocked(key, now)
		until := now.Add(backoff)
		if until.After(gate.cooldownUntil) {
			gate.cooldownUntil = until
		}
		gate.lastActive = now
	}
	s.mu.Unlock()
	return backoff, true
}
