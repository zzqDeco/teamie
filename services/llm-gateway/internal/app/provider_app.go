package app

import (
	"context"
	"strings"
	"time"

	"teamie/services/llm-gateway/internal/domain"
)

// CreateModelProviderInput holds the command to create an upstream model provider connection.
type CreateModelProviderInput struct {
	ID          string
	DisplayName string
	BaseURL     string
	APIKey      *string
	Enabled     bool
}

// UpdateModelProviderInput holds the command to update an existing upstream model provider connection.
type UpdateModelProviderInput struct {
	ID          string
	DisplayName *string
	BaseURL     *string
	APIKey      *string
	ClearAPIKey bool
	Enabled     *bool
}

// CreateModelProvider validates and adds a new upstream provider to the gateway catalog.
func (s *Service) CreateModelProvider(ctx context.Context, in CreateModelProviderInput) (domain.Config, error) {
	s.ensureInit()
	in.ID = strings.TrimSpace(in.ID)
	in.DisplayName = strings.TrimSpace(in.DisplayName)
	in.BaseURL = strings.TrimRight(strings.TrimSpace(in.BaseURL), "/")
	if len(in.DisplayName) > 160 || strings.ContainsAny(in.DisplayName, "\r\n\x00") {
		return domain.Config{}, domain.Err(400, "invalid_request", "Invalid provider display name")
	}
	if in.ID != "" && !domain.ValidProviderID(in.ID) {
		return domain.Config{}, domain.Err(400, "invalid_request", "Invalid provider ID")
	}
	if in.APIKey != nil && (len(*in.APIKey) == 0 || len(*in.APIKey) > 16384 || strings.ContainsAny(*in.APIKey, "\r\n\x00")) {
		return domain.Config{}, domain.Err(400, "invalid_request", "Invalid provider API key")
	}
	validateCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := domain.ValidateEndpoint(validateCtx, in.BaseURL); err != nil {
		return domain.Config{}, err
	}
	s.modelConfigMu.Lock()
	defer s.modelConfigMu.Unlock()
	saved, err := s.Store.UpdateCatalog(validateCtx, func(providers []domain.ProviderProfile, models []domain.ModelProfile) ([]domain.ProviderProfile, []domain.ModelProfile, error) {
		id := in.ID
		if id == "" {
			id = domain.SuggestProviderID(in.DisplayName, in.BaseURL, providers)
		} else {
			for _, p := range providers {
				if p.ID == id {
					return nil, nil, domain.Err(409, "provider_exists", "Provider ID already exists")
				}
			}
		}
		var cipherKey []byte
		if in.APIKey != nil {
			enc, err := s.Cipher.Encrypt(*in.APIKey)
			if err != nil {
				return nil, nil, err
			}
			cipherKey = enc
		}
		displayName := in.DisplayName
		if displayName == "" {
			displayName = id
		}
		now := time.Now().UTC()
		provider := domain.ProviderProfile{
			ID:          id,
			DisplayName: displayName,
			BaseURL:     in.BaseURL,
			CipherKey:   cipherKey,
			Enabled:     in.Enabled,
			CreatedAt:   now,
			UpdatedAt:   now,
		}
		return append(providers, provider), models, nil
	})
	if err != nil {
		return domain.Config{}, err
	}
	s.cancelUnavailableModels(saved)
	return saved, nil
}

// UpdateModelProvider validates and updates an existing upstream provider in the gateway catalog.
func (s *Service) UpdateModelProvider(ctx context.Context, in UpdateModelProviderInput) (domain.Config, error) {
	s.ensureInit()
	in.ID = strings.TrimSpace(in.ID)
	if !domain.ValidProviderID(in.ID) {
		return domain.Config{}, domain.Err(400, "invalid_request", "Invalid provider ID")
	}
	if in.DisplayName != nil {
		trimmed := strings.TrimSpace(*in.DisplayName)
		if len(trimmed) > 160 || strings.ContainsAny(trimmed, "\r\n\x00") {
			return domain.Config{}, domain.Err(400, "invalid_request", "Invalid provider display name")
		}
		in.DisplayName = &trimmed
	}
	if in.APIKey != nil && (in.ClearAPIKey || len(*in.APIKey) == 0 || len(*in.APIKey) > 16384 || strings.ContainsAny(*in.APIKey, "\r\n\x00")) {
		return domain.Config{}, domain.Err(400, "invalid_request", "Invalid provider API key")
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
	s.modelConfigMu.Lock()
	defer s.modelConfigMu.Unlock()
	saved, err := s.Store.UpdateCatalog(validateCtx, func(providers []domain.ProviderProfile, models []domain.ModelProfile) ([]domain.ProviderProfile, []domain.ModelProfile, error) {
		idx := -1
		for i, p := range providers {
			if p.ID == in.ID {
				idx = i
				break
			}
		}
		if idx < 0 {
			return nil, nil, domain.Err(404, "not_found", "Model provider not found")
		}
		p := providers[idx]
		if in.BaseURL != nil {
			p.BaseURL = *in.BaseURL
		}
		if in.DisplayName != nil {
			p.DisplayName = *in.DisplayName
			if p.DisplayName == "" {
				p.DisplayName = p.ID
			}
		}
		if in.Enabled != nil {
			p.Enabled = *in.Enabled
		}
		if in.ClearAPIKey {
			p.CipherKey = nil
		}
		if in.APIKey != nil {
			enc, err := s.Cipher.Encrypt(*in.APIKey)
			if err != nil {
				return nil, nil, err
			}
			p.CipherKey = enc
		}
		p.UpdatedAt = time.Now().UTC()
		providers[idx] = p
		return providers, models, nil
	})
	if err != nil {
		return domain.Config{}, err
	}
	s.cancelUnavailableModels(saved)
	return saved, nil
}

// DeleteModelProvider removes an empty upstream provider from the gateway catalog.
// Providers that still own models return 409 provider_in_use.
func (s *Service) DeleteModelProvider(ctx context.Context, id string) (domain.Config, error) {
	s.ensureInit()
	id = strings.TrimSpace(id)
	if !domain.ValidProviderID(id) {
		return domain.Config{}, domain.Err(400, "invalid_request", "Invalid provider ID")
	}
	s.modelConfigMu.Lock()
	defer s.modelConfigMu.Unlock()
	saved, err := s.Store.UpdateCatalog(ctx, func(providers []domain.ProviderProfile, models []domain.ModelProfile) ([]domain.ProviderProfile, []domain.ModelProfile, error) {
		filtered := make([]domain.ProviderProfile, 0, len(providers))
		found := false
		for _, p := range providers {
			if p.ID == id {
				found = true
				continue
			}
			filtered = append(filtered, p)
		}
		if !found {
			return nil, nil, domain.Err(404, "not_found", "Model provider not found")
		}
		for _, m := range models {
			if m.ProviderID == id {
				return nil, nil, domain.Err(409, "provider_in_use", "Delete or move all models under this provider before deleting it")
			}
		}
		return filtered, models, nil
	})
	if err != nil {
		return domain.Config{}, err
	}
	s.cancelUnavailableModels(saved)
	return saved, nil
}
