package http

import (
	"context"
	chatgen "teamie/contracts/gen/im/hertz_gen/im/chat"
	"teamie/services/chat/internal/domain"
	"time"
)

func toMarketplaceAppDTO(app domain.MarketplaceApp) *chatgen.MarketplaceAppDTO {
	return &chatgen.MarketplaceAppDTO{
		ID:          app.ID,
		Slug:        app.Slug,
		Name:        app.Name,
		Description: app.Description,
		WebUrl:      app.WebURL,
		McpUrl:      app.MCPURL,
		Enabled:     app.Enabled,
		CreatedAt:   app.CreatedAt.UTC().Format(time.RFC3339Nano),
		UpdatedAt:   app.UpdatedAt.UTC().Format(time.RFC3339Nano),
	}
}

func toMarketplaceAppDTOs(apps []domain.MarketplaceApp) []*chatgen.MarketplaceAppDTO {
	out := make([]*chatgen.MarketplaceAppDTO, 0, len(apps))
	for _, app := range apps {
		out = append(out, toMarketplaceAppDTO(app))
	}
	return out
}

func (a *Adapter) ListAdminMarketplaceApps(ctx context.Context, _ *chatgen.GetBootstrapReq) (*chatgen.MarketplaceAppListResp, error) {
	user, ok := actorFromContext(ctx)
	if !ok {
		return nil, domain.Err(401, "unauthenticated", "Sign in to continue")
	}
	apps, err := a.App.ListAdminMarketplaceApps(ctx, user)
	if err != nil {
		return nil, err
	}
	return &chatgen.MarketplaceAppListResp{Apps: toMarketplaceAppDTOs(apps)}, nil
}

func (a *Adapter) CreateAdminMarketplaceApp(ctx context.Context, req *chatgen.CreateMarketplaceAppReq) (*chatgen.CreatedMarketplaceAppResp, error) {
	user, ok := actorFromContext(ctx)
	if !ok {
		return nil, domain.Err(401, "unauthenticated", "Sign in to continue")
	}
	created, secret, err := a.App.CreateAdminMarketplaceApp(ctx, user, domain.CreateMarketplaceAppInput{
		Slug:        req.Slug,
		Name:        req.Name,
		Description: req.GetDescription(),
		WebURL:      req.WebUrl,
		MCPURL:      req.McpUrl,
		Enabled:     req.Enabled,
	})
	if err != nil {
		return nil, err
	}
	return &chatgen.CreatedMarketplaceAppResp{
		App:           toMarketplaceAppDTO(created),
		SigningSecret: secret,
	}, nil
}

func (a *Adapter) UpdateAdminMarketplaceApp(ctx context.Context, req *chatgen.UpdateMarketplaceAppReq) (*chatgen.MarketplaceAppResp, error) {
	user, ok := actorFromContext(ctx)
	if !ok {
		return nil, domain.Err(401, "unauthenticated", "Sign in to continue")
	}
	updated, err := a.App.UpdateAdminMarketplaceApp(ctx, user, req.ID, domain.UpdateMarketplaceAppInput{
		Slug:        req.Slug,
		Name:        req.Name,
		Description: req.Description,
		WebURL:      req.WebUrl,
		MCPURL:      req.McpUrl,
		Enabled:     req.Enabled,
	})
	if err != nil {
		return nil, err
	}
	return &chatgen.MarketplaceAppResp{App: toMarketplaceAppDTO(updated)}, nil
}

func (a *Adapter) RotateAdminMarketplaceAppSecret(ctx context.Context, req *chatgen.MarketplaceAppIDReq) (*chatgen.RotateMarketplaceAppSecretResp, error) {
	user, ok := actorFromContext(ctx)
	if !ok {
		return nil, domain.Err(401, "unauthenticated", "Sign in to continue")
	}
	updated, secret, err := a.App.RotateAdminMarketplaceAppSecret(ctx, user, req.ID)
	if err != nil {
		return nil, err
	}
	return &chatgen.RotateMarketplaceAppSecretResp{
		App:           toMarketplaceAppDTO(updated),
		SigningSecret: secret,
	}, nil
}

func (a *Adapter) DeleteAdminMarketplaceApp(ctx context.Context, req *chatgen.MarketplaceAppIDReq) (*chatgen.EmptyResp, error) {
	user, ok := actorFromContext(ctx)
	if !ok {
		return nil, domain.Err(401, "unauthenticated", "Sign in to continue")
	}
	if err := a.App.DeleteAdminMarketplaceApp(ctx, user, req.ID); err != nil {
		return nil, err
	}
	return &chatgen.EmptyResp{}, nil
}

func (a *Adapter) ListMarketplaceCatalog(ctx context.Context, _ *chatgen.GetBootstrapReq) (*chatgen.MarketplaceAppListResp, error) {
	user, ok := actorFromContext(ctx)
	if !ok {
		return nil, domain.Err(401, "unauthenticated", "Sign in to continue")
	}
	apps, err := a.App.ListMarketplaceCatalog(ctx, user)
	if err != nil {
		return nil, err
	}
	return &chatgen.MarketplaceAppListResp{Apps: toMarketplaceAppDTOs(apps)}, nil
}

func (a *Adapter) ListWorkspaceApps(ctx context.Context, req *chatgen.ListWorkspaceMembersReq) (*chatgen.MarketplaceAppListResp, error) {
	user, ok := actorFromContext(ctx)
	if !ok {
		return nil, domain.Err(401, "unauthenticated", "Sign in to continue")
	}
	apps, err := a.App.ListWorkspaceApps(ctx, user, req.ID)
	if err != nil {
		return nil, err
	}
	return &chatgen.MarketplaceAppListResp{Apps: toMarketplaceAppDTOs(apps)}, nil
}

func (a *Adapter) InstallWorkspaceApp(ctx context.Context, req *chatgen.InstallWorkspaceAppReq) (*chatgen.MarketplaceAppResp, error) {
	user, ok := actorFromContext(ctx)
	if !ok {
		return nil, domain.Err(401, "unauthenticated", "Sign in to continue")
	}
	installed, err := a.App.InstallWorkspaceApp(ctx, user, req.ID, req.AppId)
	if err != nil {
		return nil, err
	}
	return &chatgen.MarketplaceAppResp{App: toMarketplaceAppDTO(installed)}, nil
}

func (a *Adapter) UninstallWorkspaceApp(ctx context.Context, req *chatgen.UninstallWorkspaceAppReq) (*chatgen.EmptyResp, error) {
	user, ok := actorFromContext(ctx)
	if !ok {
		return nil, domain.Err(401, "unauthenticated", "Sign in to continue")
	}
	if err := a.App.UninstallWorkspaceApp(ctx, user, req.ID, req.AppId); err != nil {
		return nil, err
	}
	return &chatgen.EmptyResp{}, nil
}

func (a *Adapter) ListConversationApps(ctx context.Context, req *chatgen.GetReadReceiptsReq) (*chatgen.MarketplaceAppListResp, error) {
	user, ok := actorFromContext(ctx)
	if !ok {
		return nil, domain.Err(401, "unauthenticated", "Sign in to continue")
	}
	apps, err := a.App.ListConversationApps(ctx, user, req.ID)
	if err != nil {
		return nil, err
	}
	return &chatgen.MarketplaceAppListResp{Apps: toMarketplaceAppDTOs(apps)}, nil
}

func (a *Adapter) CreateConversationAppSession(ctx context.Context, req *chatgen.CreateConversationAppSessionReq) (*chatgen.ConversationAppSessionResp, error) {
	user, ok := actorFromContext(ctx)
	if !ok {
		return nil, domain.Err(401, "unauthenticated", "Sign in to continue")
	}
	session, err := a.App.CreateConversationAppSession(ctx, user, req.ID, req.AppId)
	if err != nil {
		return nil, err
	}
	return &chatgen.ConversationAppSessionResp{
		App:       toMarketplaceAppDTO(session.App),
		Token:     session.Token,
		ExpiresAt: session.ExpiresAt.UTC().Format(time.RFC3339Nano),
	}, nil
}
