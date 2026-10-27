// Package app implements the application use cases for the Model Gateway
// bounded context. It orchestrates domain entities, concurrency/slot admission,
// cancellation registries, and outbound ports without importing HTTP, RPC, Eino,
// or database drivers.
package app

import (
	"context"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"teamie/services/llm-gateway/internal/domain"
)

// Service coordinates Model Gateway use cases, slot admission, and run cancellation.
type Service struct {
	Store         domain.ConfigStore
	Cipher        domain.Cipher
	Model         domain.UpstreamModel
	WebSearch     domain.WebSearchProvider
	Token         string
	WebSearchURL  string
	slots         chan struct{}
	inFlight      atomic.Int64
	webSlots      chan struct{}
	modelConfigMu sync.RWMutex // catalog writes exclude lookup-to-registration, never provider execution
	mu            sync.Mutex
	modelSeq      uint64
	webSeq        uint64
	active        map[string]activeCall
	canceled      map[string]time.Time
}

type activeCall struct {
	modelID string
	web     bool
	cancel  context.CancelFunc
}

// New constructs a new gateway application Service.
func New(store domain.ConfigStore, cipher domain.Cipher, model domain.UpstreamModel, webSearch domain.WebSearchProvider, token string) *Service {
	return &Service{
		Store:        store,
		Cipher:       cipher,
		Model:        model,
		WebSearch:    webSearch,
		Token:        token,
		WebSearchURL: domain.DefaultTavilyBaseURL,
		webSlots:     make(chan struct{}, 4),
		active:       make(map[string]activeCall),
		canceled:     make(map[string]time.Time),
	}
}

func (s *Service) ensureInit() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.webSlots == nil {
		s.webSlots = make(chan struct{}, 4)
	}
	if s.active == nil {
		s.active = make(map[string]activeCall)
	}
	if s.canceled == nil {
		s.canceled = make(map[string]time.Time)
	}
}

// InFlightSlots returns the number of currently occupied foreground model calls.
func (s *Service) InFlightSlots() int {
	s.ensureInit()
	if s.slots != nil {
		return len(s.slots)
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

// AcquireSlot tracks an in-flight model execution call (or acquires a bounded slot if configured).
func (s *Service) AcquireSlot() (func(), bool) {
	s.ensureInit()
	if s.slots != nil {
		select {
		case s.slots <- struct{}{}:
			s.inFlight.Add(1)
			var once sync.Once
			return func() {
				once.Do(func() {
					<-s.slots
					s.inFlight.Add(-1)
				})
			}, true
		default:
			return nil, false
		}
	}
	s.inFlight.Add(1)
	var once sync.Once
	return func() {
		once.Do(func() {
			s.inFlight.Add(-1)
		})
	}, true
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

// RegisterRunCancel tracks the resolved profile, which can differ from the Agent's selection.
func (s *Service) RegisterRunCancel(ctx context.Context, agentID, runID, modelID string) (context.Context, func()) {
	s.ensureInit()
	ctx, cancel := context.WithCancel(ctx)
	index := agentID + "/" + runID
	s.mu.Lock()
	s.modelSeq++
	modelKey := index + "/model/" + strconv.FormatUint(s.modelSeq, 10)
	s.active[modelKey] = activeCall{modelID: modelID, cancel: cancel}
	if at, ok := s.canceled[index]; ok && time.Since(at) < 2*time.Minute {
		cancel()
	}
	s.mu.Unlock()
	return ctx, func() {
		cancel()
		s.mu.Lock()
		delete(s.active, modelKey)
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
	}
	s.mu.Unlock()
	return ctx, func() {
		cancel()
		s.mu.Lock()
		delete(s.active, webKey)
		s.mu.Unlock()
	}
}

// CancelRun cancels any active model or web-search call for the given agent and run.
func (s *Service) CancelRun(agentID, runID string) {
	s.ensureInit()
	index := agentID + "/" + runID
	s.mu.Lock()
	now := time.Now()
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
	s.mu.Unlock()
	for _, cancel := range cancels {
		cancel()
	}
}

// Cancel validates the service token and attribution IDs and cancels the run.
func (s *Service) Cancel(serviceToken, agentID, runID string) bool {
	if !domain.EqualSecret(serviceToken, s.Token) || !domain.ValidID(agentID) || !domain.ValidID(runID) {
		return false
	}
	s.CancelRun(agentID, runID)
	return true
}
