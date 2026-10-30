package http

import (
	"context"

	"go.uber.org/zap"
	chatgen "teamie/contracts/gen/im/hertz_gen/im/chat"
	"teamie/pkg/observability"
	"teamie/services/chat/internal/domain"
)

func (a *Adapter) ListDeveloperMarketplaceApps(ctx context.Context, _ *chatgen.GetBootstrapReq) (*chatgen.DeveloperMarketplaceAppListResp, error) {
	user, ok := actorFromContext(ctx)
	if !ok {
		return nil, domain.Err(401, "unauthenticated", "Sign in to continue")
	}
	apps, err := a.App.ListDeveloperMarketplaceApps(ctx, user)
	if err != nil {
		return nil, err
	}
	return &chatgen.DeveloperMarketplaceAppListResp{Apps: toDeveloperMarketplaceAppDTOs(apps)}, nil
}

func (a *Adapter) CreateDeveloperMarketplaceApp(ctx context.Context, req *chatgen.CreateDeveloperMarketplaceAppReq) (*chatgen.CreatedDeveloperMarketplaceAppResp, error) {
	user, ok := actorFromContext(ctx)
	if !ok {
		return nil, domain.Err(401, "unauthenticated", "Sign in to continue")
	}
	created, secret, err := a.App.CreateDeveloperMarketplaceApp(ctx, user, domain.CreateMarketplaceAppInput{
		Slug:        req.Slug,
		Name:        req.Name,
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
		zap.String("custom.marketplace.app_id", created.ID),
	)
	return &chatgen.CreatedDeveloperMarketplaceAppResp{
		App:           toDeveloperMarketplaceAppDTO(created),
		SigningSecret: secret,
	}, nil
}

func (a *Adapter) GetDeveloperMarketplaceApp(ctx context.Context, req *chatgen.MarketplaceAppIDReq) (*chatgen.DeveloperMarketplaceAppDetailResp, error) {
	user, ok := actorFromContext(ctx)
	if !ok {
		return nil, domain.Err(401, "unauthenticated", "Sign in to continue")
	}
	app, reviews, err := a.App.GetDeveloperMarketplaceApp(ctx, user, req.ID)
	if err != nil {
		return nil, err
	}
	return &chatgen.DeveloperMarketplaceAppDetailResp{
		App:     toDeveloperMarketplaceAppDTO(app),
		Reviews: toMarketplaceAppReviewDTOs(reviews),
	}, nil
}

func (a *Adapter) UpdateDeveloperMarketplaceApp(ctx context.Context, req *chatgen.UpdateMarketplaceAppReq) (*chatgen.DeveloperMarketplaceAppResp, error) {
	user, ok := actorFromContext(ctx)
	if !ok {
		return nil, domain.Err(401, "unauthenticated", "Sign in to continue")
	}
	updated, err := a.App.UpdateDeveloperMarketplaceApp(ctx, user, req.ID, domain.UpdateMarketplaceAppInput{
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
	return &chatgen.DeveloperMarketplaceAppResp{App: toDeveloperMarketplaceAppDTO(updated)}, nil
}

func (a *Adapter) VerifyDeveloperMarketplaceApp(ctx context.Context, req *chatgen.MarketplaceAppIDReq) (*chatgen.VerifyMarketplaceAppResp, error) {
	user, ok := actorFromContext(ctx)
	if !ok {
		return nil, domain.Err(401, "unauthenticated", "Sign in to continue")
	}
	updated, report, err := a.App.VerifyDeveloperMarketplaceApp(ctx, user, req.ID)
	if err != nil {
		return nil, err
	}
	return &chatgen.VerifyMarketplaceAppResp{
		App:          toDeveloperMarketplaceAppDTO(updated),
		Verification: toVerificationDTO(&report),
	}, nil
}

func (a *Adapter) SubmitDeveloperMarketplaceApp(ctx context.Context, req *chatgen.SubmitDeveloperMarketplaceAppReq) (*chatgen.DeveloperMarketplaceAppDetailResp, error) {
	user, ok := actorFromContext(ctx)
	if !ok {
		return nil, domain.Err(401, "unauthenticated", "Sign in to continue")
	}
	updated, reviews, err := a.App.SubmitDeveloperMarketplaceApp(ctx, user, req.ID, req.GetNote())
	if err != nil {
		return nil, err
	}
	observability.L(ctx).Info(
		"Developer marketplace app submitted",
		zap.String("event.action", "developer-marketplace-app-submitted"),
		zap.String("custom.marketplace.app_id", updated.ID),
		zap.Int("custom.marketplace.revision", updated.SubmissionRevision),
	)
	return &chatgen.DeveloperMarketplaceAppDetailResp{
		App:     toDeveloperMarketplaceAppDTO(updated),
		Reviews: toMarketplaceAppReviewDTOs(reviews),
	}, nil
}

func (a *Adapter) WithdrawDeveloperMarketplaceApp(ctx context.Context, req *chatgen.MarketplaceAppIDReq) (*chatgen.DeveloperMarketplaceAppDetailResp, error) {
	user, ok := actorFromContext(ctx)
	if !ok {
		return nil, domain.Err(401, "unauthenticated", "Sign in to continue")
	}
	updated, reviews, err := a.App.WithdrawDeveloperMarketplaceApp(ctx, user, req.ID)
	if err != nil {
		return nil, err
	}
	observability.L(ctx).Info(
		"Developer marketplace app withdrawn",
		zap.String("event.action", "developer-marketplace-app-withdrawn"),
		zap.String("custom.marketplace.app_id", updated.ID),
		zap.Int("custom.marketplace.revision", updated.SubmissionRevision),
	)
	return &chatgen.DeveloperMarketplaceAppDetailResp{
		App:     toDeveloperMarketplaceAppDTO(updated),
		Reviews: toMarketplaceAppReviewDTOs(reviews),
	}, nil
}

func (a *Adapter) RotateDeveloperMarketplaceAppSecret(ctx context.Context, req *chatgen.MarketplaceAppIDReq) (*chatgen.RotateDeveloperMarketplaceAppSecretResp, error) {
	user, ok := actorFromContext(ctx)
	if !ok {
		return nil, domain.Err(401, "unauthenticated", "Sign in to continue")
	}
	updated, secret, err := a.App.RotateDeveloperMarketplaceAppSecret(ctx, user, req.ID)
	if err != nil {
		return nil, err
	}
	observability.L(ctx).Info(
		"Developer marketplace app secret rotated",
		zap.String("event.action", "developer-marketplace-app-secret-rotated"),
		zap.String("custom.marketplace.app_id", updated.ID),
	)
	return &chatgen.RotateDeveloperMarketplaceAppSecretResp{
		App:           toDeveloperMarketplaceAppDTO(updated),
		SigningSecret: secret,
	}, nil
}

func (a *Adapter) DeleteDeveloperMarketplaceApp(ctx context.Context, req *chatgen.MarketplaceAppIDReq) (*chatgen.EmptyResp, error) {
	user, ok := actorFromContext(ctx)
	if !ok {
		return nil, domain.Err(401, "unauthenticated", "Sign in to continue")
	}
	if err := a.App.DeleteDeveloperMarketplaceApp(ctx, user, req.ID); err != nil {
		return nil, err
	}
	return &chatgen.EmptyResp{}, nil
}

func (a *Adapter) GetAdminMarketplaceApp(ctx context.Context, req *chatgen.MarketplaceAppIDReq) (*chatgen.DeveloperMarketplaceAppDetailResp, error) {
	user, ok := actorFromContext(ctx)
	if !ok {
		return nil, domain.Err(401, "unauthenticated", "Sign in to continue")
	}
	app, reviews, err := a.App.GetAdminMarketplaceApp(ctx, user, req.ID)
	if err != nil {
		return nil, err
	}
	return &chatgen.DeveloperMarketplaceAppDetailResp{
		App:     toDeveloperMarketplaceAppDTO(app),
		Reviews: toMarketplaceAppReviewDTOs(reviews),
	}, nil
}

func (a *Adapter) VerifyAdminMarketplaceApp(ctx context.Context, req *chatgen.MarketplaceAppIDReq) (*chatgen.VerifyMarketplaceAppResp, error) {
	user, ok := actorFromContext(ctx)
	if !ok {
		return nil, domain.Err(401, "unauthenticated", "Sign in to continue")
	}
	updated, report, err := a.App.VerifyAdminMarketplaceApp(ctx, user, req.ID)
	if err != nil {
		return nil, err
	}
	return &chatgen.VerifyMarketplaceAppResp{
		App:          toDeveloperMarketplaceAppDTO(updated),
		Verification: toVerificationDTO(&report),
	}, nil
}

func (a *Adapter) ApproveAdminMarketplaceApp(ctx context.Context, req *chatgen.ReviewAdminMarketplaceAppReq) (*chatgen.DeveloperMarketplaceAppDetailResp, error) {
	user, ok := actorFromContext(ctx)
	if !ok {
		return nil, domain.Err(401, "unauthenticated", "Sign in to continue")
	}
	updated, reviews, err := a.App.ApproveAdminMarketplaceApp(ctx, user, req.ID, int(req.ExpectedRevision), req.GetNote())
	if err != nil {
		return nil, err
	}
	observability.L(ctx).Info(
		"Marketplace app approved",
		zap.String("event.action", "admin-marketplace-app-approved"),
		zap.String("custom.marketplace.app_id", updated.ID),
		zap.Int("custom.marketplace.published_version", updated.PublishedVersion),
	)
	return &chatgen.DeveloperMarketplaceAppDetailResp{
		App:     toDeveloperMarketplaceAppDTO(updated),
		Reviews: toMarketplaceAppReviewDTOs(reviews),
	}, nil
}

func (a *Adapter) RejectAdminMarketplaceApp(ctx context.Context, req *chatgen.RejectAdminMarketplaceAppReq) (*chatgen.DeveloperMarketplaceAppDetailResp, error) {
	user, ok := actorFromContext(ctx)
	if !ok {
		return nil, domain.Err(401, "unauthenticated", "Sign in to continue")
	}
	updated, reviews, err := a.App.RejectAdminMarketplaceApp(ctx, user, req.ID, int(req.ExpectedRevision), req.Note)
	if err != nil {
		return nil, err
	}
	observability.L(ctx).Info(
		"Marketplace app rejected",
		zap.String("event.action", "admin-marketplace-app-rejected"),
		zap.String("custom.marketplace.app_id", updated.ID),
		zap.Int("custom.marketplace.revision", updated.SubmissionRevision),
	)
	return &chatgen.DeveloperMarketplaceAppDetailResp{
		App:     toDeveloperMarketplaceAppDTO(updated),
		Reviews: toMarketplaceAppReviewDTOs(reviews),
	}, nil
}

func (a *Adapter) SuspendAdminMarketplaceApp(ctx context.Context, req *chatgen.SuspendAdminMarketplaceAppReq) (*chatgen.DeveloperMarketplaceAppDetailResp, error) {
	user, ok := actorFromContext(ctx)
	if !ok {
		return nil, domain.Err(401, "unauthenticated", "Sign in to continue")
	}
	updated, reviews, err := a.App.SuspendAdminMarketplaceApp(ctx, user, req.ID, req.Reason)
	if err != nil {
		return nil, err
	}
	observability.L(ctx).Info(
		"Marketplace app suspended",
		zap.String("event.action", "admin-marketplace-app-suspended"),
		zap.String("custom.marketplace.app_id", updated.ID),
	)
	return &chatgen.DeveloperMarketplaceAppDetailResp{
		App:     toDeveloperMarketplaceAppDTO(updated),
		Reviews: toMarketplaceAppReviewDTOs(reviews),
	}, nil
}

func (a *Adapter) UnsuspendAdminMarketplaceApp(ctx context.Context, req *chatgen.MarketplaceAppIDReq) (*chatgen.DeveloperMarketplaceAppDetailResp, error) {
	user, ok := actorFromContext(ctx)
	if !ok {
		return nil, domain.Err(401, "unauthenticated", "Sign in to continue")
	}
	updated, reviews, err := a.App.UnsuspendAdminMarketplaceApp(ctx, user, req.ID)
	if err != nil {
		return nil, err
	}
	observability.L(ctx).Info(
		"Marketplace app unsuspended",
		zap.String("event.action", "admin-marketplace-app-unsuspended"),
		zap.String("custom.marketplace.app_id", updated.ID),
	)
	return &chatgen.DeveloperMarketplaceAppDetailResp{
		App:     toDeveloperMarketplaceAppDTO(updated),
		Reviews: toMarketplaceAppReviewDTOs(reviews),
	}, nil
}
