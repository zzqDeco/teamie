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
	IsDefault           bool
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
	IsDefault           *bool
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
	c = c.ResolveModel(requestedModelID)
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
	cmd := domain.Config{
		ModelsMutation: func(old domain.Config, crypto domain.Cipher) (domain.Config, error) {
			models := old.EffectiveModels()
			id := in.ID
			if id == "" {
				id = domain.SuggestModelID(in.DisplayName, in.Model, models)
			} else {
				for _, m := range models {
					if m.ID == id {
						return domain.Config{}, domain.Err(409, "model_exists", "Model ID already exists")
					}
				}
			}
			var cipherKey []byte
			if in.APIKey != nil {
				enc, err := crypto.Encrypt(*in.APIKey)
				if err != nil {
					return domain.Config{}, err
				}
				cipherKey = enc
			} else {
				for _, m := range models {
					if m.BaseURL == in.BaseURL && len(m.CipherKey) > 0 {
						cipherKey = m.CipherKey
						break
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
			next := old
			next.Models = append(models, profile)
			if in.IsDefault || len(models) == 0 || next.DefaultModelID == "" {
				next.DefaultModelID = id
			}
			next.SyncPrimaryFromModels()
			return next, nil
		},
	}
	saved, err := s.Store.Save(validateCtx, cmd, nil, false, s.Cipher)
	if err != nil {
		return domain.Config{}, err
	}
	s.CancelDisabledActiveRuns(saved)
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
	cmd := domain.Config{
		ModelsMutation: func(old domain.Config, crypto domain.Cipher) (domain.Config, error) {
			models := old.EffectiveModels()
			idx := -1
			for i, m := range models {
				if m.ID == in.ID {
					idx = i
					break
				}
			}
			if idx < 0 {
				return domain.Config{}, domain.Err(404, "not_found", "Model profile not found")
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
				enc, err := crypto.Encrypt(*in.APIKey)
				if err != nil {
					return domain.Config{}, err
				}
				m.CipherKey = enc
			}
			m.UpdatedAt = time.Now().UTC()
			models[idx] = m
			next := old
			next.Models = models
			if in.IsDefault != nil && *in.IsDefault {
				next.DefaultModelID = m.ID
			}
			next.SyncPrimaryFromModels()
			return next, nil
		},
	}
	saved, err := s.Store.Save(validateCtx, cmd, nil, false, s.Cipher)
	if err != nil {
		return domain.Config{}, err
	}
	s.CancelDisabledActiveRuns(saved)
	return saved, nil
}

// DeleteModelProfile removes a model profile from the gateway catalog.
func (s *Service) DeleteModelProfile(ctx context.Context, id string) (domain.Config, error) {
	s.ensureInit()
	id = strings.TrimSpace(id)
	if !domain.ValidModelID(id) {
		return domain.Config{}, domain.Err(400, "invalid_request", "Invalid model ID")
	}
	cmd := domain.Config{
		ModelsMutation: func(old domain.Config, _ domain.Cipher) (domain.Config, error) {
			models := old.EffectiveModels()
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
				return domain.Config{}, domain.Err(404, "not_found", "Model profile not found")
			}
			next := old
			next.Models = filtered
			next.SyncPrimaryFromModels()
			return next, nil
		},
	}
	saved, err := s.Store.Save(ctx, cmd, nil, false, s.Cipher)
	if err != nil {
		return domain.Config{}, err
	}
	s.CancelDisabledActiveRuns(saved)
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
	for _, m := range c.EffectiveModels() {
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
