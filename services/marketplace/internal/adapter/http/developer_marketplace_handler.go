package http

import (
	"context"

	"go.uber.org/zap"
	marketplacegen "teamie/contracts/gen/im/hertz_gen/im/marketplace"
	"teamie/pkg/observability"
	"teamie/services/marketplace/internal/domain"
)

func (a *Adapter) ListDeveloperMarketplaceApps(ctx context.Context, _ *marketplacegen.EmptyMarketplaceReq) (*marketplacegen.DeveloperMarketplaceAppListResp, error) {
	user, err := requireActor(ctx)
	if err != nil {
		return nil, err
	}
	apps, err := a.App.ListDeveloperMarketplaceApps(ctx, user)
	if err != nil {
		return nil, err
	}
	out := make([]*marketplacegen.DeveloperMarketplaceAppDTO, 0, len(apps))
	for _, item := range apps {
		out = append(out, toDeveloperMarketplaceAppDTO(item))
	}
	return &marketplacegen.DeveloperMarketplaceAppListResp{Apps: out}, nil
}

func (a *Adapter) CreateDeveloperMarketplaceApp(ctx context.Context, req *marketplacegen.CreateDeveloperMarketplaceAppReq) (*marketplacegen.CreatedDeveloperMarketplaceAppResp, error) {
	user, err := requireActor(ctx)
	if err != nil {
		return nil, err
	}
	app, secret, err := a.App.CreateDeveloperMarketplaceApp(ctx, user, domain.CreateMarketplaceAppInput{
		Slug:        req.GetSlug(),
		Name:        req.GetName(),
		Description: req.GetDescription(),
		WebURL:      req.GetWebUrl(),
		MCPURL:      req.GetMcpUrl(),
	})
	if err != nil {
		return nil, err
	}
	observability.L(ctx).Info(
		"Developer marketplace app created",
		zap.String("event.action", "developer-marketplace-app-created"),
		zap.String("event.outcome", "success"),
		zap.String("custom.marketplace.app_id", app.ID),
	)
	return &marketplacegen.CreatedDeveloperMarketplaceAppResp{
		App:           toDeveloperMarketplaceAppDTO(app),
		SigningSecret: secret,
	}, nil
}

func (a *Adapter) GetDeveloperMarketplaceApp(ctx context.Context, req *marketplacegen.MarketplaceAppIDReq) (*marketplacegen.DeveloperMarketplaceAppDetailResp, error) {
	user, err := requireActor(ctx)
	if err != nil {
		return nil, err
	}
	app, reviews, err := a.App.GetDeveloperMarketplaceApp(ctx, user, req.GetID())
	if err != nil {
		return nil, err
	}
	return &marketplacegen.DeveloperMarketplaceAppDetailResp{
		App:     toDeveloperMarketplaceAppDTO(app),
		Reviews: toReviewDTOs(reviews),
	}, nil
}

func (a *Adapter) UpdateDeveloperMarketplaceApp(ctx context.Context, req *marketplacegen.UpdateMarketplaceAppReq) (*marketplacegen.DeveloperMarketplaceAppResp, error) {
	user, err := requireActor(ctx)
	if err != nil {
		return nil, err
	}
	app, err := a.App.UpdateDeveloperMarketplaceApp(ctx, user, req.GetID(), domain.UpdateMarketplaceAppInput{
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
	return &marketplacegen.DeveloperMarketplaceAppResp{App: toDeveloperMarketplaceAppDTO(app)}, nil
}

func (a *Adapter) VerifyDeveloperMarketplaceApp(ctx context.Context, req *marketplacegen.MarketplaceAppIDReq) (*marketplacegen.VerifyMarketplaceAppResp, error) {
	user, err := requireActor(ctx)
	if err != nil {
		return nil, err
	}
	app, report, err := a.App.VerifyDeveloperMarketplaceApp(ctx, user, req.GetID())
	if err != nil {
		return nil, err
	}
	return &marketplacegen.VerifyMarketplaceAppResp{
		App:          toDeveloperMarketplaceAppDTO(app),
		Verification: toVerificationDTO(&report),
	}, nil
}

func (a *Adapter) SubmitDeveloperMarketplaceApp(ctx context.Context, req *marketplacegen.SubmitDeveloperMarketplaceAppReq) (*marketplacegen.DeveloperMarketplaceAppDetailResp, error) {
	user, err := requireActor(ctx)
	if err != nil {
		return nil, err
	}
	app, reviews, err := a.App.SubmitDeveloperMarketplaceApp(ctx, user, req.GetID(), req.GetNote())
	if err != nil {
		return nil, err
	}
	observability.L(ctx).Info(
		"Developer marketplace app submitted",
		zap.String("event.action", "developer-marketplace-app-submitted"),
		zap.String("event.outcome", "success"),
		zap.String("custom.marketplace.app_id", app.ID),
		zap.Int("custom.marketplace.revision", app.SubmissionRevision),
	)
	return &marketplacegen.DeveloperMarketplaceAppDetailResp{
		App:     toDeveloperMarketplaceAppDTO(app),
		Reviews: toReviewDTOs(reviews),
	}, nil
}

func (a *Adapter) WithdrawDeveloperMarketplaceApp(ctx context.Context, req *marketplacegen.MarketplaceAppIDReq) (*marketplacegen.DeveloperMarketplaceAppDetailResp, error) {
	user, err := requireActor(ctx)
	if err != nil {
		return nil, err
	}
	app, reviews, err := a.App.WithdrawDeveloperMarketplaceApp(ctx, user, req.GetID())
	if err != nil {
		return nil, err
	}
	observability.L(ctx).Info(
		"Developer marketplace app withdrawn",
		zap.String("event.action", "developer-marketplace-app-withdrawn"),
		zap.String("event.outcome", "success"),
		zap.String("custom.marketplace.app_id", app.ID),
		zap.Int("custom.marketplace.revision", app.SubmissionRevision),
	)
	return &marketplacegen.DeveloperMarketplaceAppDetailResp{
		App:     toDeveloperMarketplaceAppDTO(app),
		Reviews: toReviewDTOs(reviews),
	}, nil
}

func (a *Adapter) RotateDeveloperMarketplaceAppSecret(ctx context.Context, req *marketplacegen.MarketplaceAppIDReq) (*marketplacegen.RotateDeveloperMarketplaceAppSecretResp, error) {
	user, err := requireActor(ctx)
	if err != nil {
		return nil, err
	}
	app, secret, err := a.App.RotateDeveloperMarketplaceAppSecret(ctx, user, req.GetID())
	if err != nil {
		return nil, err
	}
	observability.L(ctx).Info(
		"Developer marketplace app secret rotated",
		zap.String("event.action", "developer-marketplace-app-secret-rotated"),
		zap.String("event.outcome", "success"),
		zap.String("custom.marketplace.app_id", app.ID),
	)
	return &marketplacegen.RotateDeveloperMarketplaceAppSecretResp{
		App:           toDeveloperMarketplaceAppDTO(app),
		SigningSecret: secret,
	}, nil
}

func (a *Adapter) DeleteDeveloperMarketplaceApp(ctx context.Context, req *marketplacegen.MarketplaceAppIDReq) (*marketplacegen.EmptyResp, error) {
	user, err := requireActor(ctx)
	if err != nil {
		return nil, err
	}
	if err := a.App.DeleteDeveloperMarketplaceApp(ctx, user, req.GetID()); err != nil {
		return nil, err
	}
	return &marketplacegen.EmptyResp{}, nil
}

func (a *Adapter) ListAdminMarketplaceApps(ctx context.Context, _ *marketplacegen.EmptyMarketplaceReq) (*marketplacegen.DeveloperMarketplaceAppListResp, error) {
	user, err := requireActor(ctx)
	if err != nil {
		return nil, err
	}
	apps, err := a.App.ListAdminMarketplaceApps(ctx, user)
	if err != nil {
		return nil, err
	}
	out := make([]*marketplacegen.DeveloperMarketplaceAppDTO, 0, len(apps))
	for _, item := range apps {
		out = append(out, toDeveloperMarketplaceAppDTO(item))
	}
	return &marketplacegen.DeveloperMarketplaceAppListResp{Apps: out}, nil
}

func (a *Adapter) GetAdminMarketplaceApp(ctx context.Context, req *marketplacegen.MarketplaceAppIDReq) (*marketplacegen.DeveloperMarketplaceAppDetailResp, error) {
	user, err := requireActor(ctx)
	if err != nil {
		return nil, err
	}
	app, reviews, err := a.App.GetAdminMarketplaceApp(ctx, user, req.GetID())
	if err != nil {
		return nil, err
	}
	return &marketplacegen.DeveloperMarketplaceAppDetailResp{
		App:     toDeveloperMarketplaceAppDTO(app),
		Reviews: toReviewDTOs(reviews),
	}, nil
}

func (a *Adapter) CreateAdminMarketplaceApp(ctx context.Context, req *marketplacegen.CreateMarketplaceAppReq) (*marketplacegen.CreatedMarketplaceAppResp, error) {
	user, err := requireActor(ctx)
	if err != nil {
		return nil, err
	}
	app, secret, err := a.App.CreateAdminMarketplaceApp(ctx, user, domain.CreateMarketplaceAppInput{
		Slug:        req.GetSlug(),
		Name:        req.GetName(),
		Description: req.GetDescription(),
		WebURL:      req.GetWebUrl(),
		MCPURL:      req.GetMcpUrl(),
		Enabled:     req.Enabled,
	})
	if err != nil {
		return nil, err
	}
	return &marketplacegen.CreatedMarketplaceAppResp{
		App:           toMarketplaceAppDTO(app),
		SigningSecret: secret,
	}, nil
}

func (a *Adapter) UpdateAdminMarketplaceApp(ctx context.Context, req *marketplacegen.UpdateMarketplaceAppReq) (*marketplacegen.MarketplaceAppResp, error) {
	user, err := requireActor(ctx)
	if err != nil {
		return nil, err
	}
	app, err := a.App.UpdateAdminMarketplaceApp(ctx, user, req.GetID(), domain.UpdateMarketplaceAppInput{
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
	return &marketplacegen.MarketplaceAppResp{App: toMarketplaceAppDTO(app)}, nil
}

func (a *Adapter) VerifyAdminMarketplaceApp(ctx context.Context, req *marketplacegen.MarketplaceAppIDReq) (*marketplacegen.VerifyMarketplaceAppResp, error) {
	user, err := requireActor(ctx)
	if err != nil {
		return nil, err
	}
	app, report, err := a.App.VerifyAdminMarketplaceApp(ctx, user, req.GetID())
	if err != nil {
		return nil, err
	}
	return &marketplacegen.VerifyMarketplaceAppResp{
		App:          toDeveloperMarketplaceAppDTO(app),
		Verification: toVerificationDTO(&report),
	}, nil
}

func (a *Adapter) ApproveAdminMarketplaceApp(ctx context.Context, req *marketplacegen.ReviewAdminMarketplaceAppReq) (*marketplacegen.DeveloperMarketplaceAppDetailResp, error) {
	user, err := requireActor(ctx)
	if err != nil {
		return nil, err
	}
	app, reviews, err := a.App.ApproveAdminMarketplaceApp(ctx, user, req.GetID(), int(req.GetExpectedRevision()), req.GetNote())
	if err != nil {
		return nil, err
	}
	observability.L(ctx).Info(
		"Admin marketplace app approved",
		zap.String("event.action", "admin-marketplace-app-approved"),
		zap.String("event.outcome", "success"),
		zap.String("custom.marketplace.app_id", app.ID),
		zap.Int("custom.marketplace.revision", app.SubmissionRevision),
		zap.Int("custom.marketplace.published_version", app.PublishedVersion),
	)
	return &marketplacegen.DeveloperMarketplaceAppDetailResp{
		App:     toDeveloperMarketplaceAppDTO(app),
		Reviews: toReviewDTOs(reviews),
	}, nil
}

func (a *Adapter) RejectAdminMarketplaceApp(ctx context.Context, req *marketplacegen.RejectAdminMarketplaceAppReq) (*marketplacegen.DeveloperMarketplaceAppDetailResp, error) {
	user, err := requireActor(ctx)
	if err != nil {
		return nil, err
	}
	app, reviews, err := a.App.RejectAdminMarketplaceApp(ctx, user, req.GetID(), int(req.GetExpectedRevision()), req.GetNote())
	if err != nil {
		return nil, err
	}
	observability.L(ctx).Info(
		"Admin marketplace app rejected",
		zap.String("event.action", "admin-marketplace-app-rejected"),
		zap.String("event.outcome", "success"),
		zap.String("custom.marketplace.app_id", app.ID),
		zap.Int("custom.marketplace.revision", app.SubmissionRevision),
	)
	return &marketplacegen.DeveloperMarketplaceAppDetailResp{
		App:     toDeveloperMarketplaceAppDTO(app),
		Reviews: toReviewDTOs(reviews),
	}, nil
}

func (a *Adapter) SuspendAdminMarketplaceApp(ctx context.Context, req *marketplacegen.SuspendAdminMarketplaceAppReq) (*marketplacegen.DeveloperMarketplaceAppDetailResp, error) {
	user, err := requireActor(ctx)
	if err != nil {
		return nil, err
	}
	app, reviews, err := a.App.SuspendAdminMarketplaceApp(ctx, user, req.GetID(), req.GetReason())
	if err != nil {
		return nil, err
	}
	observability.L(ctx).Info(
		"Admin marketplace app suspended",
		zap.String("event.action", "admin-marketplace-app-suspended"),
		zap.String("event.outcome", "success"),
		zap.String("custom.marketplace.app_id", app.ID),
	)
	return &marketplacegen.DeveloperMarketplaceAppDetailResp{
		App:     toDeveloperMarketplaceAppDTO(app),
		Reviews: toReviewDTOs(reviews),
	}, nil
}

func (a *Adapter) UnsuspendAdminMarketplaceApp(ctx context.Context, req *marketplacegen.MarketplaceAppIDReq) (*marketplacegen.DeveloperMarketplaceAppDetailResp, error) {
	user, err := requireActor(ctx)
	if err != nil {
		return nil, err
	}
	app, reviews, err := a.App.UnsuspendAdminMarketplaceApp(ctx, user, req.GetID())
	if err != nil {
		return nil, err
	}
	observability.L(ctx).Info(
		"Admin marketplace app unsuspended",
		zap.String("event.action", "admin-marketplace-app-unsuspended"),
		zap.String("event.outcome", "success"),
		zap.String("custom.marketplace.app_id", app.ID),
	)
	return &marketplacegen.DeveloperMarketplaceAppDetailResp{
		App:     toDeveloperMarketplaceAppDTO(app),
		Reviews: toReviewDTOs(reviews),
	}, nil
}

func (a *Adapter) RotateAdminMarketplaceAppSecret(ctx context.Context, req *marketplacegen.MarketplaceAppIDReq) (*marketplacegen.RotateMarketplaceAppSecretResp, error) {
	user, err := requireActor(ctx)
	if err != nil {
		return nil, err
	}
	app, secret, err := a.App.RotateAdminMarketplaceAppSecret(ctx, user, req.GetID())
	if err != nil {
		return nil, err
	}
	return &marketplacegen.RotateMarketplaceAppSecretResp{
		App:           toMarketplaceAppDTO(app),
		SigningSecret: secret,
	}, nil
}

func (a *Adapter) DeleteAdminMarketplaceApp(ctx context.Context, req *marketplacegen.MarketplaceAppIDReq) (*marketplacegen.EmptyResp, error) {
	user, err := requireActor(ctx)
	if err != nil {
		return nil, err
	}
	if err := a.App.DeleteAdminMarketplaceApp(ctx, user, req.GetID()); err != nil {
		return nil, err
	}
	return &marketplacegen.EmptyResp{}, nil
}
