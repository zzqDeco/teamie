// Package app implements the application use cases for the Model Gateway
// bounded context. It orchestrates domain entities, concurrency/slot admission,
// cancellation registries, and outbound ports without importing HTTP, RPC, Eino,
// or database drivers.
package app

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"teamie/services/llm-gateway/internal/domain"
)

// DefaultMaxConcurrentCalls is the default upper bound on concurrent foreground
// model calls across Agents. It is > 1 so concurrent Agents do not serialize on a
// global 1-slot ceiling while still bounding gateway memory and upstream connections.
const DefaultMaxConcurrentCalls = 16

// DefaultActiveReconcileInterval bounds how long an in-flight call on a draining
// gateway slot can outlive a catalog or web-search revocation persisted by a sibling slot.
const DefaultActiveReconcileInterval = 250 * time.Millisecond

// Option configures a gateway Service.
type Option func(*Service)

// WithMaxConcurrentCalls overrides the default foreground model concurrency cap.
func WithMaxConcurrentCalls(n int) Option {
	return func(s *Service) {
		s.SetMaxConcurrentCalls(n)
	}
}

// WithModelTestStore overrides the isolated persistence port for administrator model test diagnostics.
func WithModelTestStore(store domain.ModelTestStore) Option {
	return func(s *Service) {
		s.ModelTests = store
	}
}

// WithPeerBroadcaster configures cross-slot cancellation and configuration reconciliation fanout.
func WithPeerBroadcaster(peers domain.PeerBroadcaster) Option {
	return func(s *Service) {
		s.Peers = peers
		if !s.reconcileConfigured {
			s.reconcileInterval = DefaultActiveReconcileInterval
		}
	}
}

// WithActiveReconcileInterval configures periodic store revalidation while calls are active.
// Passing d <= 0 disables background active-call store polling.
func WithActiveReconcileInterval(d time.Duration) Option {
	return func(s *Service) {
		s.reconcileConfigured = true
		s.reconcileInterval = d
	}
}

func newInstanceID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 16)
	}
	return hex.EncodeToString(b[:])
}

// ParseMaxConcurrentCalls parses GATEWAY_MAX_CONCURRENT_CALLS.
// An empty value returns DefaultMaxConcurrentCalls. A non-empty value must be an
// integer >= 2 so foreground calls across different Agents can execute concurrently
// while remaining bounded.
func ParseMaxConcurrentCalls(raw string) (int, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return DefaultMaxConcurrentCalls, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 2 {
		return 0, domain.Err(400, "invalid_concurrency_cap", "GATEWAY_MAX_CONCURRENT_CALLS must be an integer >= 2")
	}
	return n, nil
}

func defaultSlotCap() int {
	if n, err := ParseMaxConcurrentCalls(os.Getenv("GATEWAY_MAX_CONCURRENT_CALLS")); err == nil {
		return n
	}
	return DefaultMaxConcurrentCalls
}

// Service coordinates Model Gateway use cases, slot admission, and run cancellation.
type Service struct {
	Store               domain.ConfigStore
	ModelTests          domain.ModelTestStore
	Cipher              domain.Cipher
	Model               domain.UpstreamModel
	WebSearch           domain.WebSearchProvider
	Peers               domain.PeerBroadcaster
	Token               string
	WebSearchURL        string
	instanceID          string
	reconcileConfigured bool
	reconcileInterval   time.Duration
	slots               chan struct{}
	inFlight            atomic.Int64
	webSlots            chan struct{}
	modelConfigMu       sync.RWMutex // catalog writes exclude lookup-to-registration, never provider execution
	mu                  sync.Mutex
	modelSeq            uint64
	webSeq              uint64
	active              map[string]activeCall
	canceled            map[string]time.Time
	recentBroadcasts    map[string]time.Time
	watcherStop         chan struct{}
	pool                providerPoolConfig
	providers           map[string]*providerGate
}

type activeCall struct {
	modelID    string
	providerID string
	web        bool
	cancel     context.CancelFunc
}

// New constructs a new gateway application Service.
func New(store domain.ConfigStore, cipher domain.Cipher, model domain.UpstreamModel, webSearch domain.WebSearchProvider, token string, opts ...Option) *Service {
	s := &Service{
		Store:            store,
		Cipher:           cipher,
		Model:            model,
		WebSearch:        webSearch,
		Token:            token,
		WebSearchURL:     domain.DefaultTavilyBaseURL,
		instanceID:       newInstanceID(),
		slots:            make(chan struct{}, defaultSlotCap()),
		webSlots:         make(chan struct{}, 4),
		active:           make(map[string]activeCall),
		canceled:         make(map[string]time.Time),
		recentBroadcasts: make(map[string]time.Time),
	}
	for _, opt := range opts {
		if opt != nil {
			opt(s)
		}
	}
	if s.ModelTests == nil {
		if ts, ok := store.(domain.ModelTestStore); ok {
			s.ModelTests = ts
		}
	}
	return s
}

// InstanceID returns the unique process-local identifier of this gateway Service.
func (s *Service) InstanceID() string {
	s.ensureInit()
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.instanceID
}

func (s *Service) modelTestStore() domain.ModelTestStore {
	if s.ModelTests != nil {
		return s.ModelTests
	}
	if ts, ok := s.Store.(domain.ModelTestStore); ok {
		return ts
	}
	return nil
}

// SetMaxConcurrentCalls configures the foreground model concurrency cap.
// Values <= 0 fall back to DefaultMaxConcurrentCalls.
func (s *Service) SetMaxConcurrentCalls(n int) {
	if n <= 0 {
		n = DefaultMaxConcurrentCalls
	}
	s.mu.Lock()
	s.slots = make(chan struct{}, n)
	s.mu.Unlock()
}

// MaxConcurrentCalls returns the configured foreground model concurrency cap.
func (s *Service) MaxConcurrentCalls() int {
	s.ensureInit()
	s.mu.Lock()
	defer s.mu.Unlock()
	return cap(s.slots)
}

func (s *Service) ensureInit() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.instanceID == "" {
		s.instanceID = newInstanceID()
	}
	if s.slots == nil {
		s.slots = make(chan struct{}, defaultSlotCap())
	}
	if s.webSlots == nil {
		s.webSlots = make(chan struct{}, 4)
	}
	if s.active == nil {
		s.active = make(map[string]activeCall)
	}
	if s.canceled == nil {
		s.canceled = make(map[string]time.Time)
	}
	if s.recentBroadcasts == nil {
		s.recentBroadcasts = make(map[string]time.Time)
	}
}

func (s *Service) maybeStartActiveWatcherLocked() {
	if s.watcherStop != nil || s.reconcileInterval <= 0 || s.Store == nil {
		return
	}
	stopCh := make(chan struct{})
	interval := s.reconcileInterval
	s.watcherStop = stopCh
	go s.runActiveWatcher(stopCh, interval)
}

func (s *Service) maybeStopActiveWatcherLocked() {
	if len(s.active) == 0 && s.watcherStop != nil {
		close(s.watcherStop)
		s.watcherStop = nil
	}
}

func (s *Service) runActiveWatcher(stopCh <-chan struct{}, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-stopCh:
			return
		case <-ticker.C:
			rCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			_ = s.ReconcileConfig(rCtx, "")
			cancel()
		}
	}
}

// InFlightSlots returns the number of currently occupied foreground model calls.
func (s *Service) InFlightSlots() int {
	s.ensureInit()
	s.mu.Lock()
	slots := s.slots
	s.mu.Unlock()
	if slots != nil {
		return len(slots)
	}
	return int(s.inFlight.Load())
}

// AuthorizeBearer checks a service Bearer Authorization header against s.Token.
func (s *Service) AuthorizeBearer(header string) error {
	if !strings.HasPrefix(header, "Bearer ") || !domain.EqualSecret(strings.TrimPrefix(header, "Bearer "), s.Token) {
		return domain.Err(401, "unauthorized", "Invalid service credential")
	}
	return nil
}

// ValidateUnaryChat validates unary RPC chat call credentials and attribution,
// returning protocol_unsupported because model inference requires private HTTP.
func (s *Service) ValidateUnaryChat(serviceToken, ownerID, agentID, sessionID, runID string) error {
	if !domain.EqualSecret(serviceToken, s.Token) {
		return domain.Err(401, "unauthorized", "Invalid service credential")
	}
	if !domain.ValidID(ownerID) || !domain.ValidID(agentID) || !domain.ValidID(sessionID) || !domain.ValidID(runID) {
		return domain.Err(400, "invalid_request", "Invalid attribution identifiers")
	}
	return domain.Err(400, "protocol_unsupported", "Model generation requires the private HTTP transport")
}

// AcquireSlot acquires a bounded foreground model execution slot.
func (s *Service) AcquireSlot() (func(), bool) {
	s.ensureInit()
	s.mu.Lock()
	slots := s.slots
	s.mu.Unlock()
	select {
	case slots <- struct{}{}:
		s.inFlight.Add(1)
		var once sync.Once
		return func() {
			once.Do(func() {
				<-slots
				s.inFlight.Add(-1)
			})
		}, true
	default:
		return nil, false
	}
}

// AcquireWebSlot attempts to acquire a web-search execution slot.
func (s *Service) AcquireWebSlot() (func(), bool) {
	s.ensureInit()
	select {
	case s.webSlots <- struct{}{}:
		return func() { <-s.webSlots }, true
	default:
		return nil, false
	}
}

// lockModelAdmission closes the gap between resolving a model and registering its
// call against a concurrent catalog change. Release it before invoking a provider.
func (s *Service) lockModelAdmission() func() {
	s.modelConfigMu.RLock()
	return sync.OnceFunc(s.modelConfigMu.RUnlock)
}

// RegisterRunCancel tracks the admitted model and provider. A later catalog move
// must not change which provider can revoke this already running call.
func (s *Service) RegisterRunCancel(ctx context.Context, agentID, runID, modelID, providerID string) (context.Context, func()) {
	s.ensureInit()
	ctx, cancel := context.WithCancel(ctx)
	index := agentID + "/" + runID
	s.mu.Lock()
	s.modelSeq++
	modelKey := index + "/model/" + strconv.FormatUint(s.modelSeq, 10)
	s.active[modelKey] = activeCall{modelID: modelID, providerID: providerID, cancel: cancel}
	if at, ok := s.canceled[index]; ok && time.Since(at) < 2*time.Minute {
		cancel()
	} else {
		s.maybeStartActiveWatcherLocked()
	}
	s.mu.Unlock()
	return ctx, func() {
		cancel()
		s.mu.Lock()
		delete(s.active, modelKey)
		s.maybeStopActiveWatcherLocked()
		s.mu.Unlock()
	}
}

// RegisterWebCancel registers a cancelable context for an agent web-search/extract call.
func (s *Service) RegisterWebCancel(ctx context.Context, agentID, runID string) (context.Context, func()) {
	s.ensureInit()
	ctx, cancel := context.WithCancel(ctx)
	index := agentID + "/" + runID
	s.mu.Lock()
	s.webSeq++
	webKey := index + "/web/" + strconv.FormatUint(s.webSeq, 10)
	s.active[webKey] = activeCall{web: true, cancel: cancel}
	if at, ok := s.canceled[index]; ok && time.Since(at) < 2*time.Minute {
		cancel()
	} else {
		s.maybeStartActiveWatcherLocked()
	}
	s.mu.Unlock()
	return ctx, func() {
		cancel()
		s.mu.Lock()
		delete(s.active, webKey)
		s.maybeStopActiveWatcherLocked()
		s.mu.Unlock()
	}
}

func (s *Service) cancelRunLocked(index string, now time.Time) []context.CancelFunc {
	for key, at := range s.canceled {
		if now.Sub(at) > 2*time.Minute {
			delete(s.canceled, key)
		}
	}
	if len(s.canceled) >= 256 {
		var oldestKey string
		var oldest time.Time
		for key, at := range s.canceled {
			if oldestKey == "" || at.Before(oldest) {
				oldestKey = key
				oldest = at
			}
		}
		delete(s.canceled, oldestKey)
	}
	s.canceled[index] = now
	var cancels []context.CancelFunc
	modelPrefix := index + "/model/"
	webPrefix := index + "/web/"
	for key, call := range s.active {
		if (strings.HasPrefix(key, modelPrefix) || strings.HasPrefix(key, webPrefix)) && call.cancel != nil {
			cancels = append(cancels, call.cancel)
		}
	}
	return cancels
}

// CancelRunLocal cancels any active model or web-search call for the given agent and run
// on this gateway instance only, without re-broadcasting to peer slots.
func (s *Service) CancelRunLocal(agentID, runID string) {
	s.ensureInit()
	index := agentID + "/" + runID
	s.mu.Lock()
	cancels := s.cancelRunLocked(index, time.Now())
	s.mu.Unlock()
	for _, cancel := range cancels {
		cancel()
	}
}

// CancelRun cancels any active model or web-search call for the given agent and run
// on this instance and synchronously fans out cancellation to sibling rollout slots.
func (s *Service) CancelRun(agentID, runID string) {
	s.ensureInit()
	index := agentID + "/" + runID
	now := time.Now()
	s.mu.Lock()
	cancels := s.cancelRunLocked(index, now)
	peers := s.Peers
	shouldBroadcast := false
	if peers != nil {
		for key, at := range s.recentBroadcasts {
			if now.Sub(at) > 5*time.Second {
				delete(s.recentBroadcasts, key)
			}
		}
		if last, ok := s.recentBroadcasts[index]; !ok || now.Sub(last) > time.Second {
			if len(s.recentBroadcasts) >= 256 {
				for key := range s.recentBroadcasts {
					delete(s.recentBroadcasts, key)
					break
				}
			}
			s.recentBroadcasts[index] = now
			shouldBroadcast = true
		}
	}
	s.mu.Unlock()
	for _, cancel := range cancels {
		cancel()
	}
	if shouldBroadcast {
		bCtx, bCancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
		_ = peers.BroadcastCancel(bCtx, agentID, runID)
		bCancel()
	}
}

// CancelLocal validates the service token and attribution IDs and cancels the run locally.
func (s *Service) CancelLocal(serviceToken, agentID, runID string) bool {
	if !domain.EqualSecret(serviceToken, s.Token) || !domain.ValidID(agentID) || !domain.ValidID(runID) {
		return false
	}
	s.CancelRunLocal(agentID, runID)
	return true
}

// Cancel validates the service token and attribution IDs and cancels the run across gateway slots.
func (s *Service) Cancel(serviceToken, agentID, runID string) bool {
	if !domain.EqualSecret(serviceToken, s.Token) || !domain.ValidID(agentID) || !domain.ValidID(runID) {
		return false
	}
	s.CancelRun(agentID, runID)
	return true
}

func (s *Service) broadcastReconcile(ctx context.Context, resetProviderID string) {
	s.mu.Lock()
	peers := s.Peers
	s.mu.Unlock()
	if peers == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	bCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 1500*time.Millisecond)
	defer cancel()
	_ = peers.BroadcastReconcile(bCtx, resetProviderID)
}
