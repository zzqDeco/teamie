// Package app implements the application use cases for the Sandbox bounded
// context. It orchestrates domain entities, validation policies, and outbound
// ports without importing HTTP, RPC, or external infrastructure libraries.
package app

import (
	"context"
	"strings"
	"sync"
	"time"

	"teamie/agent-infra/internal/sandbox/domain"
)

// Service coordinates Sandbox use cases, slot admission, and run cancellation.
type Service struct {
	Engine        domain.Engine
	Token         string
	Image         string
	WorkspaceRoot string
	StateRoot     string
	slots         chan struct{}
	mu            sync.Mutex
	active        map[string]context.CancelFunc
	canceled      map[string]time.Time
}

// New constructs a new Sandbox application Service.
func New(engine domain.Engine, token, image, workspaceRoot, stateRoot string) *Service {
	return &Service{
		Engine:        engine,
		Token:         token,
		Image:         image,
		WorkspaceRoot: workspaceRoot,
		StateRoot:     stateRoot,
		slots:         make(chan struct{}, 1),
		active:        make(map[string]context.CancelFunc),
		canceled:      make(map[string]time.Time),
	}
}

// Ready checks whether the underlying container engine is ready for the configured runner image.
func (s *Service) Ready(ctx context.Context) error {
	return s.Engine.Ready(ctx, s.Image)
}

// Authorize checks the service bearer token in constant time.
func (s *Service) Authorize(serviceToken string) bool {
	return domain.EqualSecret(serviceToken, s.Token)
}

// ValidateExecRequest validates an execution request and returns the effective executionID and timeout.
func (s *Service) ValidateExecRequest(serviceToken, agentID, runID, requestID, command string, timeoutSeconds int32, executionID *string) (effExecutionID string, effTimeout int32, errCode string) {
	if !s.Authorize(serviceToken) {
		return "", 0, "unauthorized"
	}
	if !domain.ValidID(agentID) || !domain.ValidID(runID) || !domain.ValidID(requestID) ||
		len(command) == 0 || len(command) > 16384 || strings.ContainsRune(command, 0) ||
		timeoutSeconds < 0 || timeoutSeconds > 60 {
		return "", 0, "invalid_request"
	}
	effExecutionID = runID
	if executionID != nil {
		effExecutionID = *executionID
		if !domain.ValidID(effExecutionID) {
			return "", 0, "invalid_request"
		}
	}
	effTimeout = timeoutSeconds
	if effTimeout == 0 {
		effTimeout = 30
	}
	return effExecutionID, effTimeout, ""
}

// Cancel validates the service token and attribution IDs and records cancellation.
func (s *Service) Cancel(serviceToken, agentID, runID string) bool {
	if !s.Authorize(serviceToken) || !domain.ValidID(agentID) || !domain.ValidID(runID) {
		return false
	}
	s.mu.Lock()
	if s.canceled == nil {
		s.canceled = make(map[string]time.Time)
	}
	if s.active == nil {
		s.active = make(map[string]context.CancelFunc)
	}
	index := agentID + "/" + runID
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
	cancel := s.active[index]
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	return true
}
