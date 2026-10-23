package app

import (
	"context"
	"strings"
	"time"

	"teamie/services/llm-gateway/internal/domain"
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
