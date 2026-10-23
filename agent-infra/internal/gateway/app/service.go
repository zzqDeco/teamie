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
	"time"

	"teamie/agent-infra/internal/gateway/domain"
)

type expectedConfigIDKey struct{}

// WithExpectedModelConfigID attaches an expected model config ID to ctx.
func WithExpectedModelConfigID(ctx context.Context, configID string) context.Context {
	return context.WithValue(ctx, expectedConfigIDKey{}, strings.TrimSpace(configID))
}

// ExpectedModelConfigID reads the expected model config ID from ctx.
func ExpectedModelConfigID(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	id, _ := ctx.Value(expectedConfigIDKey{}).(string)
	return id
}

// PingStatus holds the gateway readiness and configuration summary.
type PingStatus struct {
	Ready               bool
	Configured          *bool
	Enabled             *bool
	ContextWindowTokens *int64
	ModelConfigID       *string
}

// SaveConfigInput holds the administrator configuration update command.
type SaveConfigInput struct {
	BaseURL              string
	Model                string
	APIKey               *string
	Enabled              bool
	ClearAPIKey          bool
	ContextWindowTokens  *int64
	WebSearchEnabled     *bool
	WebSearchAPIKey      *string
	ClearWebSearchAPIKey bool
}

// Service coordinates Model Gateway use cases, slot admission, and run cancellation.
type Service struct {
	Store    domain.ConfigStore
	Cipher   domain.Cipher
	Token    string
	slots    chan struct{}
	webSlots chan struct{}
	mu       sync.Mutex
	webSeq   uint64
	active   map[string]context.CancelFunc
	canceled map[string]time.Time
}

// New constructs a new gateway application Service.
func New(store domain.ConfigStore, cipher domain.Cipher, token string) *Service {
	return &Service{
		Store:    store,
		Cipher:   cipher,
		Token:    token,
		slots:    make(chan struct{}, 1),
		webSlots: make(chan struct{}, 4),
		active:   make(map[string]context.CancelFunc),
		canceled: make(map[string]time.Time),
	}
}

func (s *Service) ensureInit() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.slots == nil {
		s.slots = make(chan struct{}, 1)
	}
	if s.webSlots == nil {
		s.webSlots = make(chan struct{}, 4)
	}
	if s.active == nil {
		s.active = make(map[string]context.CancelFunc)
	}
	if s.canceled == nil {
		s.canceled = make(map[string]time.Time)
	}
}

// Ping checks service token and returns current gateway configuration readiness.
func (s *Service) Ping(ctx context.Context, serviceToken string) PingStatus {
	if !domain.EqualSecret(serviceToken, s.Token) {
		return PingStatus{Ready: false}
	}
	c, err := s.Store.Load(ctx)
	configured, enabled := c.Configured(), c.Enabled
	cw := c.ContextWindow()
	var configID *string
	if id := c.CountingConfigID(); id != "" {
		configID = &id
	}
	return PingStatus{
		Ready:               err == nil,
		Configured:          &configured,
		Enabled:             &enabled,
		ContextWindowTokens: &cw,
		ModelConfigID:       configID,
	}
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

// LoadConfig loads the persisted gateway configuration.
func (s *Service) LoadConfig(ctx context.Context) (domain.Config, error) {
	return s.Store.Load(ctx)
}

// SaveConfig validates and persists updated administrator gateway configuration.
func (s *Service) SaveConfig(ctx context.Context, in SaveConfigInput) (domain.Config, error) {
	s.ensureInit()
	in.BaseURL = strings.TrimRight(strings.TrimSpace(in.BaseURL), "/")
	in.Model = strings.TrimSpace(in.Model)
	if len(in.Model) > 160 || strings.ContainsAny(in.Model, "\r\n\x00") ||
		in.APIKey != nil && (in.ClearAPIKey || len(*in.APIKey) == 0 || len(*in.APIKey) > 16384 || strings.ContainsAny(*in.APIKey, "\r\n\x00")) {
		return domain.Config{}, domain.Err(400, "invalid_request", "Invalid model configuration")
	}
	if in.WebSearchAPIKey != nil && (in.ClearWebSearchAPIKey || len(*in.WebSearchAPIKey) == 0 || len(*in.WebSearchAPIKey) > 16384 || strings.ContainsAny(*in.WebSearchAPIKey, "\r\n\x00")) {
		return domain.Config{}, domain.Err(400, "invalid_request", "Invalid web search configuration")
	}
	if in.ContextWindowTokens != nil && (*in.ContextWindowTokens < domain.MinContextWindow || *in.ContextWindowTokens > domain.MaxContextWindow) {
		return domain.Config{}, domain.Err(400, "invalid_request", "Context window must be between 4096 and 2000000 tokens")
	}
	validateCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := domain.ValidateEndpoint(validateCtx, in.BaseURL); err != nil {
		return domain.Config{}, err
	}
	c := domain.Config{
		BaseURL:           in.BaseURL,
		Model:             in.Model,
		Enabled:           in.Enabled,
		WebSearchAPIKey:   in.WebSearchAPIKey,
		ClearWebSearchKey: in.ClearWebSearchAPIKey,
	}
	if in.WebSearchEnabled != nil {
		c.WebSearchEnabled = *in.WebSearchEnabled
		c.WebSearchEnabledSet = true
	}
	if in.ContextWindowTokens != nil {
		c.ContextWindowTokens = *in.ContextWindowTokens
	}
	saved, err := s.Store.Save(validateCtx, c, in.APIKey, in.ClearAPIKey, s.Cipher)
	if err != nil {
		return domain.Config{}, err
	}
	s.CancelDisabledActiveRuns(saved)
	return saved, nil
}

// CancelDisabledActiveRuns cancels in-flight model or web-search calls when disabled or unconfigured.
func (s *Service) CancelDisabledActiveRuns(c domain.Config) {
	s.ensureInit()
	if !c.Enabled || !c.Configured() || !c.WebSearchEnabled || !c.WebSearchConfigured() {
		s.mu.Lock()
		for key, cancel := range s.active {
			isWeb := strings.Contains(key, "/web/")
			if (!isWeb && (!c.Enabled || !c.Configured())) || (isWeb && (!c.WebSearchEnabled || !c.WebSearchConfigured())) {
				cancel()
			}
		}
		s.mu.Unlock()
	}
}

// AcquireSlot attempts to acquire the single foreground model execution slot.
func (s *Service) AcquireSlot() (func(), bool) {
	s.ensureInit()
	select {
	case s.slots <- struct{}{}:
		return func() { <-s.slots }, true
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

// RegisterRunCancel registers a cancelable context for an agent run.
func (s *Service) RegisterRunCancel(ctx context.Context, agentID, runID string) (context.Context, func()) {
	s.ensureInit()
	ctx, cancel := context.WithCancel(ctx)
	index := agentID + "/" + runID
	s.mu.Lock()
	s.active[index] = cancel
	if at, ok := s.canceled[index]; ok && time.Since(at) < 2*time.Minute {
		cancel()
	}
	s.mu.Unlock()
	return ctx, func() {
		cancel()
		s.mu.Lock()
		delete(s.active, index)
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
	s.active[webKey] = cancel
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
	if cancel := s.active[index]; cancel != nil {
		cancels = append(cancels, cancel)
	}
	prefix := index + "/web/"
	for key, cancel := range s.active {
		if strings.HasPrefix(key, prefix) && cancel != nil {
			cancels = append(cancels, cancel)
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
