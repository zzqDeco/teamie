package app

import (
	"context"
	"strings"
	"time"

	"teamie/services/llm-gateway/internal/domain"
)

type expectedConfigIDKey struct{}
type requestedModelIDKey struct{}
type strictModelSelectionKey struct{}

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

// WithRequestedModelID attaches a requested model profile ID to ctx.
func WithRequestedModelID(ctx context.Context, modelID string) context.Context {
	return context.WithValue(ctx, requestedModelIDKey{}, strings.TrimSpace(modelID))
}

// WithStrictRequestedModelID attaches a requested model profile ID that must not fall back to another model.
func WithStrictRequestedModelID(ctx context.Context, modelID string) context.Context {
	ctx = WithRequestedModelID(ctx, modelID)
	return context.WithValue(ctx, strictModelSelectionKey{}, true)
}

// RequestedModelID reads the requested model profile ID from ctx.
func RequestedModelID(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	id, _ := ctx.Value(requestedModelIDKey{}).(string)
	return id
}

// StrictModelSelection reports whether ctx forbids fallback to another model.
func StrictModelSelection(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	strict, _ := ctx.Value(strictModelSelectionKey{}).(bool)
	return strict
}

// PingStatus holds the gateway readiness and configuration summary.
type PingStatus struct {
	Ready               bool
	Configured          *bool
	Enabled             *bool
	ContextWindowTokens *int64
	ModelConfigID       *string
}

// CreateModelProfileInput holds the command to create a new model profile under an existing provider.
type CreateModelProfileInput struct {
	ID                  string
	ProviderID          string
	DisplayName         string
	Description         string
	Model               string
	Enabled             bool
	ContextWindowTokens *int64
}

// UpdateModelProfileInput holds the command to update an existing model profile.
type UpdateModelProfileInput struct {
	ID                  string
	ProviderID          *string
	DisplayName         *string
	Description         *string
	Model               *string
	Enabled             *bool
	ContextWindowTokens *int64
}

type pingConfigLoader interface {
	LoadForPing(ctx context.Context) (domain.Config, error)
}

// Ping checks service token and returns current gateway configuration readiness for the requested model (or default fallback).
func (s *Service) Ping(ctx context.Context, serviceToken string, modelIDs ...string) PingStatus {
	if !domain.EqualSecret(serviceToken, s.Token) {
		return PingStatus{Ready: false}
	}
	var (
		c   domain.Config
		err error
	)
	if loader, ok := s.Store.(pingConfigLoader); ok {
		c, err = loader.LoadForPing(ctx)
	} else {
		c, err = s.Store.Load(ctx)
	}
	var requestedModelID string
	if len(modelIDs) > 0 {
		requestedModelID = modelIDs[0]
	}
	resolved := c.ResolveModel(requestedModelID)
	configured, enabled := resolved.Configured(), resolved.Enabled
	cw := resolved.ContextWindow()
	var configID *string
	if id := resolved.CountingConfigID(); id != "" {
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

// LoadConfig loads the persisted gateway configuration.
func (s *Service) LoadConfig(ctx context.Context) (domain.Config, error) {
	return s.Store.Load(ctx)
}

func providerExists(providers []domain.ProviderProfile, id string) bool {
	for _, p := range providers {
		if p.ID == id {
			return true
		}
	}
	return false
}

// CreateModelProfile validates and adds a new model profile to the gateway catalog.
func (s *Service) CreateModelProfile(ctx context.Context, in CreateModelProfileInput) (domain.Config, error) {
	s.ensureInit()
	in.ID = strings.TrimSpace(in.ID)
	in.ProviderID = strings.TrimSpace(in.ProviderID)
	in.DisplayName = strings.TrimSpace(in.DisplayName)
	in.Description = strings.TrimSpace(in.Description)
	in.Model = strings.TrimSpace(in.Model)
	if !domain.ValidProviderID(in.ProviderID) {
		return domain.Config{}, domain.Err(400, "invalid_request", "Invalid model provider ID")
	}
	if in.Model == "" || len(in.Model) > 160 || strings.ContainsAny(in.Model, "\r\n\x00") ||
		len(in.DisplayName) > 160 || strings.ContainsAny(in.DisplayName, "\r\n\x00") ||
		len(in.Description) > 512 || strings.ContainsAny(in.Description, "\r\n\x00") {
		return domain.Config{}, domain.Err(400, "invalid_request", "Invalid model configuration")
	}
	if in.ID != "" && !domain.ValidModelID(in.ID) {
		return domain.Config{}, domain.Err(400, "invalid_request", "Invalid model ID")
	}
	cw := domain.DefaultContextWindow
	if in.ContextWindowTokens != nil {
		if *in.ContextWindowTokens < domain.MinContextWindow || *in.ContextWindowTokens > domain.MaxContextWindow {
			return domain.Config{}, domain.Err(400, "invalid_request", "Context window must be between 4096 and 2000000 tokens")
		}
		cw = *in.ContextWindowTokens
	}
	s.modelConfigMu.Lock()
	defer s.modelConfigMu.Unlock()
	saved, err := s.Store.UpdateCatalog(ctx, func(providers []domain.ProviderProfile, models []domain.ModelProfile) ([]domain.ProviderProfile, []domain.ModelProfile, error) {
		if !providerExists(providers, in.ProviderID) {
			return nil, nil, domain.Err(404, "not_found", "Model provider not found")
		}
		id := in.ID
		if id == "" {
			id = domain.SuggestModelID(in.DisplayName, in.Model, models)
		} else {
			for _, m := range models {
				if m.ID == id {
					return nil, nil, domain.Err(409, "model_exists", "Model ID already exists")
				}
			}
		}
		displayName := in.DisplayName
		if displayName == "" {
			displayName = in.Model
		}
		now := time.Now().UTC()
		profile := domain.ModelProfile{
			ID:                  id,
			ProviderID:          in.ProviderID,
			DisplayName:         displayName,
			Description:         in.Description,
			Model:               in.Model,
			ContextWindowTokens: cw,
			Enabled:             in.Enabled,
			CreatedAt:           now,
			UpdatedAt:           now,
		}
		return providers, append(models, profile), nil
	})
	if err != nil {
		return domain.Config{}, err
	}
	s.cancelUnavailableModels(saved)
	return saved, nil
}

// UpdateModelProfile validates and updates an existing model profile in the gateway catalog.
func (s *Service) UpdateModelProfile(ctx context.Context, in UpdateModelProfileInput) (domain.Config, error) {
	s.ensureInit()
	in.ID = strings.TrimSpace(in.ID)
	if !domain.ValidModelID(in.ID) {
		return domain.Config{}, domain.Err(400, "invalid_request", "Invalid model ID")
	}
	if in.ProviderID != nil {
		trimmed := strings.TrimSpace(*in.ProviderID)
		if !domain.ValidProviderID(trimmed) {
			return domain.Config{}, domain.Err(400, "invalid_request", "Invalid model provider ID")
		}
		in.ProviderID = &trimmed
	}
	if in.Model != nil {
		trimmed := strings.TrimSpace(*in.Model)
		if trimmed == "" || len(trimmed) > 160 || strings.ContainsAny(trimmed, "\r\n\x00") {
			return domain.Config{}, domain.Err(400, "invalid_request", "Invalid model configuration")
		}
		in.Model = &trimmed
	}
	if in.DisplayName != nil {
		trimmed := strings.TrimSpace(*in.DisplayName)
		if len(trimmed) > 160 || strings.ContainsAny(trimmed, "\r\n\x00") {
			return domain.Config{}, domain.Err(400, "invalid_request", "Invalid model display name")
		}
		in.DisplayName = &trimmed
	}
	if in.Description != nil {
		trimmed := strings.TrimSpace(*in.Description)
		if len(trimmed) > 512 || strings.ContainsAny(trimmed, "\r\n\x00") {
			return domain.Config{}, domain.Err(400, "invalid_request", "Invalid model description")
		}
		in.Description = &trimmed
	}
	if in.ContextWindowTokens != nil && (*in.ContextWindowTokens < domain.MinContextWindow || *in.ContextWindowTokens > domain.MaxContextWindow) {
		return domain.Config{}, domain.Err(400, "invalid_request", "Context window must be between 4096 and 2000000 tokens")
	}
	s.modelConfigMu.Lock()
	defer s.modelConfigMu.Unlock()
	saved, err := s.Store.UpdateCatalog(ctx, func(providers []domain.ProviderProfile, models []domain.ModelProfile) ([]domain.ProviderProfile, []domain.ModelProfile, error) {
		idx := -1
		for i, m := range models {
			if m.ID == in.ID {
				idx = i
				break
			}
		}
		if idx < 0 {
			return nil, nil, domain.Err(404, "not_found", "Model profile not found")
		}
		if in.ProviderID != nil && !providerExists(providers, *in.ProviderID) {
			return nil, nil, domain.Err(404, "not_found", "Model provider not found")
		}
		m := models[idx]
		if in.ProviderID != nil {
			m.ProviderID = *in.ProviderID
		}
		if in.Model != nil {
			m.Model = *in.Model
		}
		if in.DisplayName != nil {
			m.DisplayName = *in.DisplayName
			if m.DisplayName == "" {
				m.DisplayName = m.Model
			}
		}
		if in.Description != nil {
			m.Description = *in.Description
		}
		if in.ContextWindowTokens != nil {
			m.ContextWindowTokens = *in.ContextWindowTokens
		}
		if in.Enabled != nil {
			m.Enabled = *in.Enabled
		}
		m.UpdatedAt = time.Now().UTC()
		models[idx] = m
		return providers, models, nil
	})
	if err != nil {
		return domain.Config{}, err
	}
	s.cancelUnavailableModels(saved)
	return saved, nil
}

// DeleteModelProfile removes a model profile from the gateway catalog.
func (s *Service) DeleteModelProfile(ctx context.Context, id string) (domain.Config, error) {
	s.ensureInit()
	id = strings.TrimSpace(id)
	if !domain.ValidModelID(id) {
		return domain.Config{}, domain.Err(400, "invalid_request", "Invalid model ID")
	}
	s.modelConfigMu.Lock()
	defer s.modelConfigMu.Unlock()
	saved, err := s.Store.UpdateCatalog(ctx, func(providers []domain.ProviderProfile, models []domain.ModelProfile) ([]domain.ProviderProfile, []domain.ModelProfile, error) {
		filtered := make([]domain.ModelProfile, 0, len(models))
		found := false
		for _, m := range models {
			if m.ID == id {
				found = true
				continue
			}
			filtered = append(filtered, m)
		}
		if !found {
			return nil, nil, domain.Err(404, "not_found", "Model profile not found")
		}
		return providers, filtered, nil
	})
	if err != nil {
		return domain.Config{}, err
	}
	s.cancelUnavailableModels(saved)
	return saved, nil
}

// ResolveModelProfileForTest validates that a specific model profile and its provider exist, are configured, and are enabled for admin testing.
func (s *Service) ResolveModelProfileForTest(ctx context.Context, id string) (domain.ResolvedModel, error) {
	id = strings.TrimSpace(id)
	if !domain.ValidModelID(id) {
		return domain.ResolvedModel{}, domain.Err(400, "invalid_request", "Invalid model ID")
	}
	c, err := s.Store.Load(ctx)
	if err != nil {
		return domain.ResolvedModel{}, err
	}
	rm, ok := c.ResolveExactModel(id)
	if !ok {
		return domain.ResolvedModel{}, domain.Err(404, "not_found", "Model profile not found")
	}
	if !rm.Configured() {
		return domain.ResolvedModel{}, domain.Err(409, "model_unconfigured", "The selected model profile or provider is not configured")
	}
	if !rm.ProviderEnabled {
		return domain.ResolvedModel{}, domain.Err(409, "model_disabled", "The selected model's provider is disabled")
	}
	if !rm.ModelEnabled {
		return domain.ResolvedModel{}, domain.Err(409, "model_disabled", "The selected model profile is disabled")
	}
	return rm, nil
}

// UpdateRouting changes only the administrator's selected default model.
func (s *Service) UpdateRouting(ctx context.Context, id string) (domain.Config, error) {
	id = strings.TrimSpace(id)
	if id != "" && !domain.ValidModelID(id) {
		return domain.Config{}, domain.Err(400, "invalid_request", "Invalid model ID")
	}
	return s.Store.UpdateRouting(ctx, domain.ModelRoutingConfig{DefaultModelID: id})
}

type UpdateWebSearchInput struct {
	Enabled     *bool
	APIKey      *string
	ClearAPIKey bool
}

// UpdateWebSearch mutates only search settings; the returned snapshot is read-only.
func (s *Service) UpdateWebSearch(ctx context.Context, in UpdateWebSearchInput) (domain.Config, error) {
	if in.APIKey != nil && (in.ClearAPIKey || len(*in.APIKey) == 0 || len(*in.APIKey) > 16384 || strings.ContainsAny(*in.APIKey, "\r\n\x00")) {
		return domain.Config{}, domain.Err(400, "invalid_request", "Invalid web search API key")
	}
	saved, err := s.Store.UpdateWebSearch(ctx, func(old domain.WebSearchConfig) (domain.WebSearchConfig, error) {
		if in.Enabled != nil {
			old.Enabled = *in.Enabled
		}
		if in.ClearAPIKey {
			old.CipherKey = nil
		}
		if in.APIKey != nil {
			encrypted, err := s.Cipher.Encrypt(*in.APIKey)
			if err != nil {
				return domain.WebSearchConfig{}, err
			}
			old.CipherKey = encrypted
		}
		return old, nil
	})
	if err != nil {
		return domain.Config{}, err
	}
	if !saved.WebSearch.Enabled || !saved.WebSearch.Configured() {
		s.cancelActiveCalls(true)
	}
	return saved, nil
}

func (s *Service) cancelUnavailableModels(c domain.Config) {
	models := make(map[string]bool, len(c.Models))
	for _, m := range c.Models {
		models[m.ID] = m.Enabled
	}
	providers := make(map[string]bool, len(c.Providers))
	for _, p := range c.Providers {
		providers[p.ID] = p.Enabled && p.Configured()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, call := range s.active {
		if !call.web && (!models[call.modelID] || !providers[call.providerID]) {
			call.cancel()
		}
	}
}

func (s *Service) cancelActiveCalls(web bool) {
	s.ensureInit()
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, call := range s.active {
		if call.web == web {
			call.cancel()
		}
	}
}
