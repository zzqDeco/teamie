package app

import (
	"context"
	"strings"
	"time"

	"teamie/services/llm-gateway/internal/domain"
)

type expectedConfigIDKey struct{}
type requestedModelIDKey struct{}

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

// RequestedModelID reads the requested model profile ID from ctx.
func RequestedModelID(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	id, _ := ctx.Value(requestedModelIDKey{}).(string)
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

// CreateModelProfileInput holds the command to create a new model profile.
type CreateModelProfileInput struct {
	ID                  string
	DisplayName         string
	Description         string
	BaseURL             string
	Model               string
	APIKey              *string
	Enabled             bool
	ContextWindowTokens *int64
}

// UpdateModelProfileInput holds the command to update an existing model profile.
type UpdateModelProfileInput struct {
	ID                  string
	DisplayName         *string
	Description         *string
	BaseURL             *string
	Model               *string
	APIKey              *string
	ClearAPIKey         bool
	Enabled             *bool
	ContextWindowTokens *int64
}

// Ping checks service token and returns current gateway configuration readiness for the requested model (or default fallback).
func (s *Service) Ping(ctx context.Context, serviceToken string, modelIDs ...string) PingStatus {
	if !domain.EqualSecret(serviceToken, s.Token) {
		return PingStatus{Ready: false}
	}
	c, err := s.Store.Load(ctx)
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

// CreateModelProfile validates and adds a new model profile to the gateway catalog.
func (s *Service) CreateModelProfile(ctx context.Context, in CreateModelProfileInput) (domain.Config, error) {
	s.ensureInit()
	in.ID = strings.TrimSpace(in.ID)
	in.DisplayName = strings.TrimSpace(in.DisplayName)
	in.Description = strings.TrimSpace(in.Description)
	in.BaseURL = strings.TrimRight(strings.TrimSpace(in.BaseURL), "/")
	in.Model = strings.TrimSpace(in.Model)
	if in.Model == "" || len(in.Model) > 160 || strings.ContainsAny(in.Model, "\r\n\x00") ||
		len(in.DisplayName) > 160 || strings.ContainsAny(in.DisplayName, "\r\n\x00") ||
		len(in.Description) > 512 || strings.ContainsAny(in.Description, "\r\n\x00") {
		return domain.Config{}, domain.Err(400, "invalid_request", "Invalid model configuration")
	}
	if in.ID != "" && !domain.ValidModelID(in.ID) {
		return domain.Config{}, domain.Err(400, "invalid_request", "Invalid model ID")
	}
	if in.APIKey != nil && (len(*in.APIKey) == 0 || len(*in.APIKey) > 16384 || strings.ContainsAny(*in.APIKey, "\r\n\x00")) {
		return domain.Config{}, domain.Err(400, "invalid_request", "Invalid model API key")
	}
	cw := domain.DefaultContextWindow
	if in.ContextWindowTokens != nil {
		if *in.ContextWindowTokens < domain.MinContextWindow || *in.ContextWindowTokens > domain.MaxContextWindow {
			return domain.Config{}, domain.Err(400, "invalid_request", "Context window must be between 4096 and 2000000 tokens")
		}
		cw = *in.ContextWindowTokens
	}
	validateCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := domain.ValidateEndpoint(validateCtx, in.BaseURL); err != nil {
		return domain.Config{}, err
	}
	saved, err := s.Store.UpdateModels(validateCtx, func(models []domain.ModelProfile) ([]domain.ModelProfile, error) {
		id := in.ID
		if id == "" {
			id = domain.SuggestModelID(in.DisplayName, in.Model, models)
		} else {
			for _, m := range models {
				if m.ID == id {
					return nil, domain.Err(409, "model_exists", "Model ID already exists")
				}
			}
		}
		var cipherKey []byte
		if in.APIKey != nil {
			enc, err := s.Cipher.Encrypt(*in.APIKey)
			if err != nil {
				return nil, err
			}
			cipherKey = enc
		}
		displayName := in.DisplayName
		if displayName == "" {
			displayName = in.Model
		}
		now := time.Now().UTC()
		profile := domain.ModelProfile{
			ID:                  id,
			DisplayName:         displayName,
			Description:         in.Description,
			BaseURL:             in.BaseURL,
			Model:               in.Model,
			CipherKey:           cipherKey,
			ContextWindowTokens: cw,
			Enabled:             in.Enabled,
			CreatedAt:           now,
			UpdatedAt:           now,
		}
		return append(models, profile), nil
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
	if in.APIKey != nil && (in.ClearAPIKey || len(*in.APIKey) == 0 || len(*in.APIKey) > 16384 || strings.ContainsAny(*in.APIKey, "\r\n\x00")) {
		return domain.Config{}, domain.Err(400, "invalid_request", "Invalid model API key")
	}
	if in.ContextWindowTokens != nil && (*in.ContextWindowTokens < domain.MinContextWindow || *in.ContextWindowTokens > domain.MaxContextWindow) {
		return domain.Config{}, domain.Err(400, "invalid_request", "Context window must be between 4096 and 2000000 tokens")
	}
	validateCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if in.BaseURL != nil {
		trimmed := strings.TrimRight(strings.TrimSpace(*in.BaseURL), "/")
		if err := domain.ValidateEndpoint(validateCtx, trimmed); err != nil {
			return domain.Config{}, err
		}
		in.BaseURL = &trimmed
	}
	saved, err := s.Store.UpdateModels(validateCtx, func(models []domain.ModelProfile) ([]domain.ModelProfile, error) {
		idx := -1
		for i, m := range models {
			if m.ID == in.ID {
				idx = i
				break
			}
		}
		if idx < 0 {
			return nil, domain.Err(404, "not_found", "Model profile not found")
		}
		m := models[idx]
		if in.BaseURL != nil {
			m.BaseURL = *in.BaseURL
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
		if in.ClearAPIKey {
			m.CipherKey = nil
		}
		if in.APIKey != nil {
			enc, err := s.Cipher.Encrypt(*in.APIKey)
			if err != nil {
				return nil, err
			}
			m.CipherKey = enc
		}
		m.UpdatedAt = time.Now().UTC()
		models[idx] = m
		return models, nil
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
	saved, err := s.Store.UpdateModels(ctx, func(models []domain.ModelProfile) ([]domain.ModelProfile, error) {
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
			return nil, domain.Err(404, "not_found", "Model profile not found")
		}
		return filtered, nil
	})
	if err != nil {
		return domain.Config{}, err
	}
	s.cancelUnavailableModels(saved)
	return saved, nil
}

// ResolveModelProfileForTest validates that a specific model profile exists, is configured, and is enabled for admin testing.
func (s *Service) ResolveModelProfileForTest(ctx context.Context, id string) (domain.ModelProfile, error) {
	id = strings.TrimSpace(id)
	if !domain.ValidModelID(id) {
		return domain.ModelProfile{}, domain.Err(400, "invalid_request", "Invalid model ID")
	}
	c, err := s.Store.Load(ctx)
	if err != nil {
		return domain.ModelProfile{}, err
	}
	for _, m := range c.Models {
		if m.ID == id {
			if !m.Configured() {
				return domain.ModelProfile{}, domain.Err(409, "model_unconfigured", "The selected model profile is not configured")
			}
			if !m.Enabled {
				return domain.ModelProfile{}, domain.Err(409, "model_disabled", "The selected model profile is disabled")
			}
			return m, nil
		}
	}
	return domain.ModelProfile{}, domain.Err(404, "not_found", "Model profile not found")
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
	m := c.ResolveModel("")
	if !m.Enabled || !m.Configured() {
		s.cancelActiveCalls(false)
	}
}
func (s *Service) cancelActiveCalls(web bool) {
	s.ensureInit()
	s.mu.Lock()
	defer s.mu.Unlock()
	for key, cancel := range s.active {
		if strings.Contains(key, "/web/") == web {
			cancel()
		}
	}
}
