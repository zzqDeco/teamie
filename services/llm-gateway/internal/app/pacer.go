package app

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"strings"
	"time"

	"teamie/services/llm-gateway/internal/domain"
)

const (
	// DefaultProviderPaceInterval spaces simultaneous burst dispatches targeting the
	// same upstream ProviderProfile while keeping idle single dispatches at 0ms delay.
	DefaultProviderPaceInterval = 200 * time.Millisecond
	// DefaultProviderPaceJitter adds bounded random jitter to burst dispatch spacing.
	DefaultProviderPaceJitter = 100 * time.Millisecond
	// DefaultRateLimitRetries bounds automatic gateway retries on upstream HTTP 429
	// before any streaming chunks have been emitted.
	DefaultRateLimitRetries = 2
	// DefaultRateLimitBaseBackoff is the initial exponential backoff window on 429.
	DefaultRateLimitBaseBackoff = 800 * time.Millisecond
	// DefaultRateLimitMaxJitter is the random jitter added to each 429 backoff window.
	DefaultRateLimitMaxJitter = 400 * time.Millisecond

	maxProviderPaceWait = 15 * time.Second
	providerPaceIdleTTL = 5 * time.Minute
)

type paceConfig struct {
	configured        bool
	interval          time.Duration
	jitter            time.Duration
	retriesConfigured bool
	maxRetries        int
	baseBackoff       time.Duration
	maxJitter         time.Duration
}

// WithProviderPace configures per-ProviderProfile burst dispatch spacing and jitter.
// Passing interval <= 0 and jitter <= 0 disables burst spacing (useful in tight unit tests).
func WithProviderPace(interval, jitter time.Duration) Option {
	return func(s *Service) {
		s.SetProviderPace(interval, jitter)
	}
}

// WithRateLimitRetry configures automatic retry and jittered exponential backoff for
// upstream 429 (upstream_rate_limited) responses before any output chunk is emitted.
func WithRateLimitRetry(maxRetries int, baseBackoff, maxJitter time.Duration) Option {
	return func(s *Service) {
		s.SetRateLimitRetry(maxRetries, baseBackoff, maxJitter)
	}
}

// SetProviderPace updates per-ProviderProfile burst spacing at runtime or in tests.
func (s *Service) SetProviderPace(interval, jitter time.Duration) {
	if interval < 0 {
		interval = 0
	}
	if jitter < 0 {
		jitter = 0
	}
	s.mu.Lock()
	s.pace.configured = true
	s.pace.interval = interval
	s.pace.jitter = jitter
	s.mu.Unlock()
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
	s.pace.retriesConfigured = true
	s.pace.maxRetries = maxRetries
	s.pace.baseBackoff = baseBackoff
	s.pace.maxJitter = maxJitter
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

func providerPaceKey(c domain.ResolvedModel) string {
	if id := strings.TrimSpace(c.ProviderID); id != "" {
		return "provider:" + id
	}
	return "url:" + strings.TrimSpace(c.BaseURL)
}

func (s *Service) effectivePace() (interval, jitter time.Duration) {
	if s.pace.configured {
		return s.pace.interval, s.pace.jitter
	}
	return DefaultProviderPaceInterval, DefaultProviderPaceJitter
}

func (s *Service) effectiveRateLimitRetry() (maxRetries int, baseBackoff, maxJitter time.Duration) {
	if s.pace.retriesConfigured {
		return s.pace.maxRetries, s.pace.baseBackoff, s.pace.maxJitter
	}
	return DefaultRateLimitRetries, DefaultRateLimitBaseBackoff, DefaultRateLimitMaxJitter
}

func (s *Service) reserveProviderDispatch(key string) time.Duration {
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.providerNext == nil {
		s.providerNext = make(map[string]time.Time)
	}
	for k, t := range s.providerNext {
		if now.Sub(t) > providerPaceIdleTTL {
			delete(s.providerNext, k)
		}
	}
	interval, jitter := s.effectivePace()
	step := interval + randomJitter(jitter)
	scheduled := s.providerNext[key]
	if scheduled.IsZero() || !scheduled.After(now) {
		if step > 0 {
			s.providerNext[key] = now.Add(step)
		} else {
			delete(s.providerNext, key)
		}
		return 0
	}
	wait := scheduled.Sub(now)
	if wait > maxProviderPaceWait {
		wait = maxProviderPaceWait
		scheduled = now.Add(wait)
	}
	s.providerNext[key] = scheduled.Add(step)
	return wait
}

func (s *Service) recordProviderCooldown(key string, backoff time.Duration) {
	if backoff <= 0 {
		return
	}
	until := time.Now().Add(backoff)
	s.mu.Lock()
	if s.providerNext == nil {
		s.providerNext = make(map[string]time.Time)
	}
	if until.After(s.providerNext[key]) {
		s.providerNext[key] = until
	}
	s.mu.Unlock()
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

func (s *Service) waitProviderPace(ctx context.Context, key string) error {
	if err := ctx.Err(); err != nil {
		return domain.StreamError(ctx, "gateway_unavailable")
	}
	if StrictModelSelection(ctx) {
		return nil
	}
	wait := s.reserveProviderDispatch(key)
	return sleepWithContext(ctx, wait)
}

func isRetryableUpstreamRateLimit(err error) bool {
	var de *domain.Error
	return errors.As(err, &de) && de.Status == 429 && de.Code == "upstream_rate_limited"
}

func (s *Service) waitRateLimitRetry(ctx context.Context, key string, attempt int) (bool, error) {
	maxRetries, baseBackoff, maxJitter := s.effectiveRateLimitRetry()
	if attempt >= maxRetries || ctx.Err() != nil {
		return false, nil
	}
	shift := attempt
	if shift > 4 {
		shift = 4
	}
	backoff := (baseBackoff << shift) + randomJitter(maxJitter)
	s.recordProviderCooldown(key, backoff)
	if err := sleepWithContext(ctx, backoff); err != nil {
		return false, err
	}
	return true, nil
}
