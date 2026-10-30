package http

import (
	"context"
	"time"

	marketplacegen "teamie/contracts/gen/im/hertz_gen/im/marketplace"
	"teamie/services/marketplace/internal/domain"
)

func requireActor(ctx context.Context) (domain.User, error) {
	u, ok := actorFromContext(ctx)
	if !ok {
		return domain.User{}, domain.Err(401, "unauthenticated", "Sign in to continue")
	}
	return u, nil
}

func (a *Adapter) ListMarketplaceCatalog(ctx context.Context, _ *marketplacegen.EmptyMarketplaceReq) (*marketplacegen.MarketplaceAppListResp, error) {
	if _, err := requireActor(ctx); err != nil {
		return nil, err
	}
	apps, err := a.App.ListMarketplaceCatalog(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]*marketplacegen.MarketplaceAppDTO, 0, len(apps))
	for _, item := range apps {
		out = append(out, toMarketplaceAppDTO(item))
	}
	return &marketplacegen.MarketplaceAppListResp{Apps: out}, nil
}

func (a *Adapter) ListWorkspaceApps(ctx context.Context, req *marketplacegen.MarketplaceWorkspaceIDReq) (*marketplacegen.MarketplaceAppListResp, error) {
	user, err := requireActor(ctx)
	if err != nil {
		return nil, err
	}
	apps, err := a.App.ListWorkspaceApps(ctx, req.GetID(), user.ID)
	if err != nil {
		return nil, err
	}
	out := make([]*marketplacegen.MarketplaceAppDTO, 0, len(apps))
	for _, item := range apps {
		out = append(out, toMarketplaceAppDTO(item))
	}
	return &marketplacegen.MarketplaceAppListResp{Apps: out}, nil
}

func (a *Adapter) InstallWorkspaceApp(ctx context.Context, req *marketplacegen.InstallWorkspaceAppReq) (*marketplacegen.MarketplaceAppResp, error) {
	user, err := requireActor(ctx)
	if err != nil {
		return nil, err
	}
	app, err := a.App.InstallWorkspaceApp(ctx, req.GetID(), req.GetAppId(), user)
	if err != nil {
		return nil, err
	}
	return &marketplacegen.MarketplaceAppResp{App: toMarketplaceAppDTO(app)}, nil
}

func (a *Adapter) UninstallWorkspaceApp(ctx context.Context, req *marketplacegen.UninstallWorkspaceAppReq) (*marketplacegen.EmptyResp, error) {
	user, err := requireActor(ctx)
	if err != nil {
		return nil, err
	}
	if err := a.App.UninstallWorkspaceApp(ctx, req.GetID(), req.GetAppId(), user); err != nil {
		return nil, err
	}
	return &marketplacegen.EmptyResp{}, nil
}

func (a *Adapter) ListConversationApps(ctx context.Context, req *marketplacegen.MarketplaceConversationIDReq) (*marketplacegen.MarketplaceAppListResp, error) {
	user, err := requireActor(ctx)
	if err != nil {
		return nil, err
	}
	apps, err := a.App.ListConversationApps(ctx, req.GetID(), user.ID)
	if err != nil {
		return nil, err
	}
	out := make([]*marketplacegen.MarketplaceAppDTO, 0, len(apps))
	for _, item := range apps {
		out = append(out, toMarketplaceAppDTO(item))
	}
	return &marketplacegen.MarketplaceAppListResp{Apps: out}, nil
}

func (a *Adapter) CreateConversationAppSession(ctx context.Context, req *marketplacegen.CreateConversationAppSessionReq) (*marketplacegen.ConversationAppSessionResp, error) {
	user, err := requireActor(ctx)
	if err != nil {
		return nil, err
	}
	sess, err := a.App.CreateConversationAppSession(ctx, req.GetID(), req.GetAppId(), user)
	if err != nil {
		return nil, err
	}
	return &marketplacegen.ConversationAppSessionResp{
		App:       toMarketplaceAppDTO(sess.App),
		Token:     sess.Token,
		ExpiresAt: sess.ExpiresAt.UTC().Format(time.RFC3339),
	}, nil
}
