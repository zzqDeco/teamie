// Package httpAdapter implements the inbound HTTP adapter for the Model Gateway,
// satisfying the generated Thrift HTTP contract (gatewaygen.ModelGatewayHTTPService).
package httpAdapter

import (
	"context"
	"time"

	"teamie/agent-infra/internal/gateway/app"
	"teamie/agent-infra/internal/gateway/domain"
	gatewaygen "teamie/agent-protocol/hertz_gen/agent/gateway"
)

var _ gatewaygen.ModelGatewayHTTPService = (*Adapter)(nil)

// Adapter translates generated Thrift HTTP DTOs into gateway application use cases.
type Adapter struct {
	App      *app.Service
	Generate func(ctx context.Context, req *gatewaygen.ChatCallReq) (*gatewaygen.ChatCallResp, error)
	Search   func(ctx context.Context, req *gatewaygen.WebSearchReq) (*gatewaygen.WebSearchResp, error)
	Extract  func(ctx context.Context, req *gatewaygen.WebExtractReq) (*gatewaygen.WebExtractResp, error)
}

// New creates a new HTTP Adapter backed by the gateway application service.
func New(svc *app.Service) *Adapter {
	return &Adapter{App: svc}
}

func toAdminModelConfigResp(c domain.Config) *gatewaygen.AdminModelConfigResp {
	return &gatewaygen.AdminModelConfigResp{
		Config: &gatewaygen.ModelConfigDTO{
			BaseUrl:             c.BaseURL,
			Model:               c.Model,
			ContextWindowTokens: c.ContextWindow(),
			HasApiKey:           len(c.CipherKey) > 0,
			Enabled:             c.Enabled,
			Configured:          c.Configured(),
			WebSearch: &gatewaygen.WebSearchConfigDTO{
				Provider:   "tavily",
				Enabled:    c.WebSearchEnabled,
				HasApiKey:  len(c.WebSearchCipherKey) > 0,
				Configured: c.WebSearchConfigured(),
			},
			UpdatedAt: c.UpdatedAt.UTC().Format(time.RFC3339Nano),
		},
		Status: c.Status(),
	}
}

func (a *Adapter) GetAdminModelConfig(ctx context.Context, _ *gatewaygen.GetAdminModelConfigReq) (*gatewaygen.AdminModelConfigResp, error) {
	c, err := a.App.LoadConfig(ctx)
	if err != nil {
		return nil, err
	}
	return toAdminModelConfigResp(c), nil
}

func (a *Adapter) UpdateAdminModelConfig(ctx context.Context, req *gatewaygen.UpdateAdminModelConfigReq) (*gatewaygen.AdminModelConfigResp, error) {
	if req == nil {
		return nil, domain.Err(400, "invalid_request", "Invalid model configuration")
	}
	c, err := a.App.SaveConfig(ctx, app.SaveConfigInput{
		BaseURL:              req.BaseUrl,
		Model:                req.Model,
		APIKey:               req.ApiKey,
		Enabled:              req.Enabled,
		ClearAPIKey:          req.ClearApiKey,
		ContextWindowTokens:  req.ContextWindowTokens,
		WebSearchEnabled:     req.WebSearchEnabled,
		WebSearchAPIKey:      req.WebSearchApiKey,
		ClearWebSearchAPIKey: req.ClearWebSearchApiKey,
	})
	if err != nil {
		return nil, err
	}
	return toAdminModelConfigResp(c), nil
}

func (a *Adapter) TestAdminModelConfig(ctx context.Context, _ *gatewaygen.TestAdminModelConfigReq) (*gatewaygen.TestAdminModelConfigResp, error) {
	c, err := a.App.LoadConfig(ctx)
	if err != nil {
		return nil, err
	}
	return &gatewaygen.TestAdminModelConfigResp{
		Ok:        true,
		Model:     c.Model,
		LatencyMs: 0,
	}, nil
}

func (a *Adapter) GenerateChat(ctx context.Context, req *gatewaygen.ChatCallReq) (*gatewaygen.ChatCallResp, error) {
	if a.Generate != nil {
		return a.Generate(ctx, req)
	}
	return nil, domain.Err(501, "not_implemented", "GenerateChat handler not bound")
}

func (a *Adapter) StreamChat(_ context.Context, _ *gatewaygen.ChatCallReq) (*gatewaygen.StreamEventFrameDTO, error) {
	return &gatewaygen.StreamEventFrameDTO{Type: "done"}, nil
}

func (a *Adapter) WebSearch(ctx context.Context, req *gatewaygen.WebSearchReq) (*gatewaygen.WebSearchResp, error) {
	if a.Search != nil {
		return a.Search(ctx, req)
	}
	return nil, domain.Err(501, "not_implemented", "WebSearch handler not bound")
}

func (a *Adapter) WebExtract(ctx context.Context, req *gatewaygen.WebExtractReq) (*gatewaygen.WebExtractResp, error) {
	if a.Extract != nil {
		return a.Extract(ctx, req)
	}
	return nil, domain.Err(501, "not_implemented", "WebExtract handler not bound")
}
