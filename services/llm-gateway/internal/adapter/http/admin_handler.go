package httpAdapter

import (
	"context"
	"encoding/json"
	"time"

	gatewaygen "teamie/contracts/gen/agent/hertz_gen/agent/gateway"
	"teamie/contracts/gen/agent/hertz_gen/agent/modeltransport"
	"teamie/services/llm-gateway/internal/app"
	"teamie/services/llm-gateway/internal/domain"
)

func (a *Adapter) GetModelCatalog(ctx context.Context, _ *gatewaygen.GetModelCatalogReq) (*gatewaygen.ModelCatalogResp, error) {
	c, err := a.App.LoadConfig(ctx)
	if err != nil {
		return nil, err
	}
	effDefault := c.EffectiveDefaultModelID()
	items := make([]*gatewaygen.PublicModelOptionDTO, 0)
	for _, m := range c.Models {
		rm := c.ResolveModelProfile(m)
		if !rm.Available() {
			continue
		}
		items = append(items, &gatewaygen.PublicModelOptionDTO{
			ID:                  m.ID,
			DisplayName:         rm.DisplayName,
			Description:         m.Description,
			ContextWindowTokens: m.ContextWindow(),
			IsDefault:           m.ID == effDefault,
		})
	}
	resp := &gatewaygen.ModelCatalogResp{
		Models: items,
	}
	if len(items) > 0 && effDefault != "" {
		resp.DefaultModelId = &effDefault
	}
	return resp, nil
}

func (a *Adapter) GetAdminModelConfig(ctx context.Context, _ *gatewaygen.GetAdminModelConfigReq) (*gatewaygen.AdminModelConfigResp, error) {
	c, err := a.App.LoadConfig(ctx)
	if err != nil {
		return nil, err
	}
	return toAdminModelConfigResp(c), nil
}

func (a *Adapter) UpdateAdminModelConfig(context.Context, *gatewaygen.UpdateAdminModelConfigReq) (*gatewaygen.AdminModelConfigResp, error) {
	return nil, domain.Err(409, "config_api_changed", "Refresh this page to update provider, model, routing or web search settings separately")
}

func (a *Adapter) UpdateAdminWebSearchConfig(ctx context.Context, req *gatewaygen.UpdateAdminWebSearchConfigReq) (*gatewaygen.AdminModelConfigResp, error) {
	if req == nil {
		return nil, domain.Err(400, "invalid_request", "Invalid web search configuration")
	}
	c, err := a.App.UpdateWebSearch(ctx, app.UpdateWebSearchInput{Enabled: req.Enabled, APIKey: req.ApiKey, ClearAPIKey: req.GetClearApiKey()})
	if err != nil {
		return nil, err
	}
	return toAdminModelConfigResp(c), nil
}

func (a *Adapter) UpdateAdminModelRouting(ctx context.Context, req *gatewaygen.UpdateAdminModelRoutingReq) (*gatewaygen.AdminModelConfigResp, error) {
	if req == nil {
		return nil, domain.Err(400, "invalid_request", "Invalid routing configuration")
	}
	c, err := a.App.UpdateRouting(ctx, req.DefaultModelId)
	if err != nil {
		return nil, err
	}
	return toAdminModelConfigResp(c), nil
}

func (a *Adapter) CreateAdminModelProvider(ctx context.Context, req *gatewaygen.CreateAdminModelProviderReq) (*gatewaygen.AdminModelConfigResp, error) {
	if req == nil {
		return nil, domain.Err(400, "invalid_request", "Invalid provider configuration")
	}
	c, err := a.App.CreateModelProvider(ctx, app.CreateModelProviderInput{
		ID:          req.GetID(),
		DisplayName: req.GetDisplayName(),
		BaseURL:     req.BaseUrl,
		APIKey:      req.ApiKey,
		Enabled:     req.Enabled,
	})
	if err != nil {
		return nil, err
	}
	return toAdminModelConfigResp(c), nil
}

func (a *Adapter) UpdateAdminModelProvider(ctx context.Context, req *gatewaygen.UpdateAdminModelProviderReq) (*gatewaygen.AdminModelConfigResp, error) {
	if req == nil {
		return nil, domain.Err(400, "invalid_request", "Invalid provider configuration")
	}
	c, err := a.App.UpdateModelProvider(ctx, app.UpdateModelProviderInput{
		ID:          req.ID,
		DisplayName: req.DisplayName,
		BaseURL:     req.BaseUrl,
		APIKey:      req.ApiKey,
		ClearAPIKey: req.GetClearApiKey(),
		Enabled:     req.Enabled,
	})
	if err != nil {
		return nil, err
	}
	return toAdminModelConfigResp(c), nil
}

func (a *Adapter) DeleteAdminModelProvider(ctx context.Context, req *gatewaygen.DeleteAdminModelProviderReq) (*gatewaygen.AdminModelConfigResp, error) {
	if req == nil {
		return nil, domain.Err(400, "invalid_request", "Invalid provider configuration")
	}
	c, err := a.App.DeleteModelProvider(ctx, req.ID)
	if err != nil {
		return nil, err
	}
	return toAdminModelConfigResp(c), nil
}

func (a *Adapter) CreateAdminModelProfile(ctx context.Context, req *gatewaygen.CreateAdminModelProfileReq) (*gatewaygen.AdminModelConfigResp, error) {
	if req == nil {
		return nil, domain.Err(400, "invalid_request", "Invalid model configuration")
	}
	c, err := a.App.CreateModelProfile(ctx, app.CreateModelProfileInput{
		ID:                  req.GetID(),
		ProviderID:          req.ProviderId,
		DisplayName:         req.GetDisplayName(),
		Description:         req.GetDescription(),
		Model:               req.Model,
		Enabled:             req.Enabled,
		ContextWindowTokens: req.ContextWindowTokens,
	})
	if err != nil {
		return nil, err
	}
	return toAdminModelConfigResp(c), nil
}

func (a *Adapter) UpdateAdminModelProfile(ctx context.Context, req *gatewaygen.UpdateAdminModelProfileReq) (*gatewaygen.AdminModelConfigResp, error) {
	if req == nil {
		return nil, domain.Err(400, "invalid_request", "Invalid model configuration")
	}
	c, err := a.App.UpdateModelProfile(ctx, app.UpdateModelProfileInput{
		ID:                  req.ID,
		ProviderID:          req.ProviderId,
		DisplayName:         req.DisplayName,
		Description:         req.Description,
		Model:               req.Model,
		Enabled:             req.Enabled,
		ContextWindowTokens: req.ContextWindowTokens,
	})
	if err != nil {
		return nil, err
	}
	return toAdminModelConfigResp(c), nil
}

func (a *Adapter) DeleteAdminModelProfile(ctx context.Context, req *gatewaygen.DeleteAdminModelProfileReq) (*gatewaygen.AdminModelConfigResp, error) {
	if req == nil {
		return nil, domain.Err(400, "invalid_request", "Invalid model configuration")
	}
	c, err := a.App.DeleteModelProfile(ctx, req.ID)
	if err != nil {
		return nil, err
	}
	return toAdminModelConfigResp(c), nil
}

func (a *Adapter) TestAdminModelConfig(ctx context.Context, _ *gatewaygen.TestAdminModelConfigReq) (*gatewaygen.TestAdminModelConfigResp, error) {
	return a.testAdminModelConfigWithOwner(ctx, adminOwnerFromContext(ctx), "")
}

func (a *Adapter) TestAdminModelProfile(ctx context.Context, req *gatewaygen.TestAdminModelProfileReq) (*gatewaygen.TestAdminModelConfigResp, error) {
	if req == nil {
		return nil, domain.Err(400, "invalid_request", "Invalid model ID")
	}
	if _, err := a.App.ResolveModelProfileForTest(ctx, req.ID); err != nil {
		return nil, err
	}
	return a.testAdminModelConfigWithOwner(ctx, adminOwnerFromContext(ctx), req.ID)
}

func (a *Adapter) testAdminModelConfigWithOwner(ctx context.Context, owner, modelID string) (*gatewaygen.TestAdminModelConfigResp, error) {
	c, err := a.App.LoadConfig(ctx)
	if err != nil {
		return nil, err
	}
	var resolved domain.ResolvedModel
	if modelID != "" {
		exact, ok := c.ResolveExactModel(modelID)
		if !ok {
			return nil, domain.Err(404, "not_found", "Model profile not found")
		}
		resolved = exact
		ctx = app.WithStrictRequestedModelID(ctx, modelID)
	} else {
		resolved = c.ResolveModel("")
	}
	start := time.Now()
	raw, _ := json.Marshal(modeltransport.Request{
		Protocol: modeltransport.Version,
		Messages: json.RawMessage(`[{"role":"user","content_blocks":[{"type":"user_input_text","user_input_text":{"text":"Reply with OK."}}]}]`),
		Options:  json.RawMessage(`{"MaxTokens":8,"AgenticToolChoice":{"Type":"forbidden"}}`),
	})
	res := a.callWithObservability(ctx, owner, "admin-test", "admin-test", start.UTC().Format("20060102T150405.000000000"), string(raw))
	if !res.Ok {
		return nil, domain.Err(int(res.GetStatusCode()), res.GetErrorCode(), res.GetErrorMessage())
	}
	return &gatewaygen.TestAdminModelConfigResp{
		Ok:        true,
		Model:     resolved.Model,
		LatencyMs: time.Since(start).Milliseconds(),
	}, nil
}
