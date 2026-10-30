package app

import (
	"context"

	"teamie/services/marketplace/internal/domain"
)

// ListAdminMarketplaceApps returns all official and developer apps for platform administration.
func (s *Service) ListAdminMarketplaceApps(ctx context.Context, actor domain.User) ([]domain.MarketplaceApp, error) {
	if err := requirePlatformAdmin(actor); err != nil {
		return nil, err
	}
	apps, err := s.Marketplace.ListAllMarketplaceApps(ctx)
	if err != nil {
		return nil, err
	}
	return s.enrichApps(ctx, apps)
}

// GetAdminMarketplaceApp returns full detail and review audit history for any marketplace app.
func (s *Service) GetAdminMarketplaceApp(ctx context.Context, actor domain.User, appID string) (domain.MarketplaceApp, []domain.MarketplaceAppReview, error) {
	if err := requirePlatformAdmin(actor); err != nil {
		return domain.MarketplaceApp{}, nil, err
	}
	app, err := s.Marketplace.GetMarketplaceAppByID(ctx, appID, false)
	if err != nil {
		return domain.MarketplaceApp{}, nil, err
	}
	reviews, err := s.Marketplace.ListMarketplaceAppReviews(ctx, app.ID)
	if err != nil {
		return domain.MarketplaceApp{}, nil, err
	}
	enrichedApp, err := s.enrichApp(ctx, app)
	if err != nil {
		return domain.MarketplaceApp{}, nil, err
	}
	enrichedReviews, err := s.enrichReviews(ctx, reviews)
	if err != nil {
		return domain.MarketplaceApp{}, nil, err
	}
	return enrichedApp, enrichedReviews, nil
}

// VerifyAdminMarketplaceApp runs a live 4-stage protocol verification during admin review/governance.
func (s *Service) VerifyAdminMarketplaceApp(ctx context.Context, actor domain.User, appID string) (domain.MarketplaceApp, domain.MarketplaceEndpointVerification, error) {
	if err := requirePlatformAdmin(actor); err != nil {
		return domain.MarketplaceApp{}, domain.MarketplaceEndpointVerification{}, err
	}
	if err := s.requireMarketplaceEnabled(); err != nil {
		return domain.MarketplaceApp{}, domain.MarketplaceEndpointVerification{}, err
	}
	app, err := s.Marketplace.GetMarketplaceAppByID(ctx, appID, false)
	if err != nil {
		return domain.MarketplaceApp{}, domain.MarketplaceEndpointVerification{}, err
	}
	targetWeb := app.DraftWebURL
	targetMCP := app.DraftMCPURL
	if targetWeb == "" {
		targetWeb = app.WebURL
	}
	if targetMCP == "" {
		targetMCP = app.MCPURL
	}
	webURL, mcpURL, err := s.validateEndpoints(targetWeb, targetMCP, false)
	if err != nil {
		return domain.MarketplaceApp{}, domain.MarketplaceEndpointVerification{}, err
	}
	if err := s.acquireVerifySlot(app.ID); err != nil {
		return domain.MarketplaceApp{}, domain.MarketplaceEndpointVerification{}, err
	}
	secret := app.SigningSecret
	if secret == "" {
		secret, err = s.Marketplace.GetMarketplaceAppSigningSecret(ctx, app.ID)
		if err != nil {
			return domain.MarketplaceApp{}, domain.MarketplaceEndpointVerification{}, err
		}
	}
	if s.Verifier == nil {
		return domain.MarketplaceApp{}, domain.MarketplaceEndpointVerification{}, domain.Err(503, "verifier_unavailable", "Marketplace endpoint verifier is unavailable")
	}
	report, err := s.Verifier.VerifyEndpoints(ctx, domain.VerifyEndpointsInput{
		AppID:         app.ID,
		Slug:          app.Slug,
		WebURL:        webURL,
		MCPURL:        mcpURL,
		SigningSecret: secret,
	})
	if err != nil {
		return domain.MarketplaceApp{}, domain.MarketplaceEndpointVerification{}, err
	}
	if report.VerifiedAt.IsZero() {
		report.VerifiedAt = s.nowUTC()
	}
	saved, err := s.Marketplace.SaveMarketplaceVerificationReport(ctx, app.ID, report.VerifiedAt, report)
	if err != nil {
		return domain.MarketplaceApp{}, domain.MarketplaceEndpointVerification{}, err
	}
	enriched, err := s.enrichApp(ctx, saved)
	if err != nil {
		return domain.MarketplaceApp{}, domain.MarketplaceEndpointVerification{}, err
	}
	return enriched, report, nil
}

// ApproveAdminMarketplaceApp publishes a pending_review submission fenced by expectedRevision.
func (s *Service) ApproveAdminMarketplaceApp(ctx context.Context, actor domain.User, appID string, expectedRevision int, rawNote string) (domain.MarketplaceApp, []domain.MarketplaceAppReview, error) {
	if err := requirePlatformAdmin(actor); err != nil {
		return domain.MarketplaceApp{}, nil, err
	}
	if err := s.requireMarketplaceEnabled(); err != nil {
		return domain.MarketplaceApp{}, nil, err
	}
	note, err := normalizeNote(rawNote, false)
	if err != nil {
		return domain.MarketplaceApp{}, nil, err
	}

	var updated domain.MarketplaceApp
	err = s.Tx.WithinTx(ctx, func(txCtx context.Context) error {
		cur, err := s.Marketplace.GetMarketplaceAppByID(txCtx, appID, true)
		if err != nil {
			return err
		}
		if cur.AdminSuspended {
			return domain.ErrMarketplaceSuspended()
		}
		if cur.ReviewStatus != domain.ReviewStatusPending {
			return domain.ErrMarketplaceInvalidState("Application is not pending review")
		}
		if cur.SubmissionRevision != expectedRevision {
			return domain.ErrMarketplaceRevisionConflict()
		}
		webURL, mcpURL, err := s.validateEndpoints(cur.DraftWebURL, cur.DraftMCPURL, false)
		if err != nil {
			return err
		}
		now := s.nowUTC()
		reviewerID := actor.ID
		cur.Name = cur.DraftName
		cur.Description = cur.DraftDescription
		cur.WebURL = webURL
		cur.MCPURL = mcpURL
		cur.IsPublished = true
		cur.PublishedVersion++
		cur.ReviewStatus = domain.ReviewStatusApproved
		cur.HasDraftChanges = false
		cur.ReviewedBy = &reviewerID
		cur.ReviewedAt = &now
		cur.ReviewNote = note
		cur.UpdatedAt = now

		res, err := s.Marketplace.UpdateMarketplaceApp(txCtx, cur)
		if err != nil {
			return err
		}
		if err := s.Marketplace.InsertMarketplaceAppReview(txCtx, domain.MarketplaceAppReview{
			ID:        domain.NewID(),
			AppID:     res.ID,
			Revision:  res.SubmissionRevision,
			Action:    domain.ReviewActionApprove,
			ActorID:   actor.ID,
			Note:      note,
			WebURL:    res.WebURL,
			MCPURL:    res.MCPURL,
			CreatedAt: now,
		}, reviewSnapshot(res)); err != nil {
			return err
		}
		updated = res
		return nil
	})
	if err != nil {
		return domain.MarketplaceApp{}, nil, err
	}
	reviews, err := s.Marketplace.ListMarketplaceAppReviews(ctx, updated.ID)
	if err != nil {
		return domain.MarketplaceApp{}, nil, err
	}
	enrichedApp, err := s.enrichApp(ctx, updated)
	if err != nil {
		return domain.MarketplaceApp{}, nil, err
	}
	enrichedReviews, err := s.enrichReviews(ctx, reviews)
	if err != nil {
		return domain.MarketplaceApp{}, nil, err
	}
	return enrichedApp, enrichedReviews, nil
}

// RejectAdminMarketplaceApp rejects a pending_review submission fenced by expectedRevision while preserving any prior live version.
func (s *Service) RejectAdminMarketplaceApp(ctx context.Context, actor domain.User, appID string, expectedRevision int, rawNote string) (domain.MarketplaceApp, []domain.MarketplaceAppReview, error) {
	if err := requirePlatformAdmin(actor); err != nil {
		return domain.MarketplaceApp{}, nil, err
	}
	if err := s.requireMarketplaceEnabled(); err != nil {
		return domain.MarketplaceApp{}, nil, err
	}
	note, err := normalizeNote(rawNote, true)
	if err != nil {
		return domain.MarketplaceApp{}, nil, err
	}

	var updated domain.MarketplaceApp
	err = s.Tx.WithinTx(ctx, func(txCtx context.Context) error {
		cur, err := s.Marketplace.GetMarketplaceAppByID(txCtx, appID, true)
		if err != nil {
			return err
		}
		if cur.ReviewStatus != domain.ReviewStatusPending {
			return domain.ErrMarketplaceInvalidState("Application is not pending review")
		}
		if cur.SubmissionRevision != expectedRevision {
			return domain.ErrMarketplaceRevisionConflict()
		}
		now := s.nowUTC()
		reviewerID := actor.ID
		cur.ReviewStatus = domain.ReviewStatusRejected
		cur.ReviewedBy = &reviewerID
		cur.ReviewedAt = &now
		cur.ReviewNote = note
		cur.HasDraftChanges = computeHasDraftChanges(cur)
		cur.UpdatedAt = now

		res, err := s.Marketplace.UpdateMarketplaceApp(txCtx, cur)
		if err != nil {
			return err
		}
		if err := s.Marketplace.InsertMarketplaceAppReview(txCtx, domain.MarketplaceAppReview{
			ID:        domain.NewID(),
			AppID:     res.ID,
			Revision:  res.SubmissionRevision,
			Action:    domain.ReviewActionReject,
			ActorID:   actor.ID,
			Note:      note,
			WebURL:    res.DraftWebURL,
			MCPURL:    res.DraftMCPURL,
			CreatedAt: now,
		}, reviewSnapshot(res)); err != nil {
			return err
		}
		updated = res
		return nil
	})
	if err != nil {
		return domain.MarketplaceApp{}, nil, err
	}
	reviews, err := s.Marketplace.ListMarketplaceAppReviews(ctx, updated.ID)
	if err != nil {
		return domain.MarketplaceApp{}, nil, err
	}
	enrichedApp, err := s.enrichApp(ctx, updated)
	if err != nil {
		return domain.MarketplaceApp{}, nil, err
	}
	enrichedReviews, err := s.enrichReviews(ctx, reviews)
	if err != nil {
		return domain.MarketplaceApp{}, nil, err
	}
	return enrichedApp, enrichedReviews, nil
}

// SuspendAdminMarketplaceApp suspends a marketplace app immediately across catalog, workspaces, and group sessions.
func (s *Service) SuspendAdminMarketplaceApp(ctx context.Context, actor domain.User, appID, rawReason string) (domain.MarketplaceApp, []domain.MarketplaceAppReview, error) {
	if err := requirePlatformAdmin(actor); err != nil {
		return domain.MarketplaceApp{}, nil, err
	}
	if err := s.requireMarketplaceEnabled(); err != nil {
		return domain.MarketplaceApp{}, nil, err
	}
	reason, err := normalizeSuspendReason(rawReason)
	if err != nil {
		return domain.MarketplaceApp{}, nil, err
	}
	var updated domain.MarketplaceApp
	err = s.Tx.WithinTx(ctx, func(txCtx context.Context) error {
		cur, err := s.Marketplace.GetMarketplaceAppByID(txCtx, appID, true)
		if err != nil {
			return err
		}
		now := s.nowUTC()
		cur.AdminSuspended = true
		cur.SuspendedReason = reason
		cur.SuspendedAt = &now
		if cur.ReviewStatus == domain.ReviewStatusPending {
			if cur.IsPublished {
				cur.ReviewStatus = domain.ReviewStatusApproved
			} else {
				cur.ReviewStatus = domain.ReviewStatusRejected
			}
		}
		cur.HasDraftChanges = computeHasDraftChanges(cur)
		cur.UpdatedAt = now
		res, err := s.Marketplace.UpdateMarketplaceApp(txCtx, cur)
		if err != nil {
			return err
		}
		if err := s.Marketplace.InsertMarketplaceAppReview(txCtx, domain.MarketplaceAppReview{
			ID:        domain.NewID(),
			AppID:     res.ID,
			Revision:  res.SubmissionRevision,
			Action:    domain.ReviewActionSuspend,
			ActorID:   actor.ID,
			Note:      reason,
			WebURL:    res.WebURL,
			MCPURL:    res.MCPURL,
			CreatedAt: now,
		}, reviewSnapshot(res)); err != nil {
			return err
		}
		updated = res
		return nil
	})
	if err != nil {
		return domain.MarketplaceApp{}, nil, err
	}
	reviews, err := s.Marketplace.ListMarketplaceAppReviews(ctx, updated.ID)
	if err != nil {
		return domain.MarketplaceApp{}, nil, err
	}
	enrichedApp, err := s.enrichApp(ctx, updated)
	if err != nil {
		return domain.MarketplaceApp{}, nil, err
	}
	enrichedReviews, err := s.enrichReviews(ctx, reviews)
	if err != nil {
		return domain.MarketplaceApp{}, nil, err
	}
	return enrichedApp, enrichedReviews, nil
}

// UnsuspendAdminMarketplaceApp lifts an administrative suspension on a marketplace app.
func (s *Service) UnsuspendAdminMarketplaceApp(ctx context.Context, actor domain.User, appID string) (domain.MarketplaceApp, []domain.MarketplaceAppReview, error) {
	if err := requirePlatformAdmin(actor); err != nil {
		return domain.MarketplaceApp{}, nil, err
	}
	if err := s.requireMarketplaceEnabled(); err != nil {
		return domain.MarketplaceApp{}, nil, err
	}
	var updated domain.MarketplaceApp
	err := s.Tx.WithinTx(ctx, func(txCtx context.Context) error {
		cur, err := s.Marketplace.GetMarketplaceAppByID(txCtx, appID, true)
		if err != nil {
			return err
		}
		now := s.nowUTC()
		cur.AdminSuspended = false
		cur.SuspendedReason = ""
		cur.SuspendedAt = nil
		cur.HasDraftChanges = computeHasDraftChanges(cur)
		cur.UpdatedAt = now
		res, err := s.Marketplace.UpdateMarketplaceApp(txCtx, cur)
		if err != nil {
			return err
		}
		if err := s.Marketplace.InsertMarketplaceAppReview(txCtx, domain.MarketplaceAppReview{
			ID:        domain.NewID(),
			AppID:     res.ID,
			Revision:  res.SubmissionRevision,
			Action:    domain.ReviewActionUnsuspend,
			ActorID:   actor.ID,
			Note:      "",
			WebURL:    res.WebURL,
			MCPURL:    res.MCPURL,
			CreatedAt: now,
		}, reviewSnapshot(res)); err != nil {
			return err
		}
		updated = res
		return nil
	})
	if err != nil {
		return domain.MarketplaceApp{}, nil, err
	}
	reviews, err := s.Marketplace.ListMarketplaceAppReviews(ctx, updated.ID)
	if err != nil {
		return domain.MarketplaceApp{}, nil, err
	}
	enrichedApp, err := s.enrichApp(ctx, updated)
	if err != nil {
		return domain.MarketplaceApp{}, nil, err
	}
	enrichedReviews, err := s.enrichReviews(ctx, reviews)
	if err != nil {
		return domain.MarketplaceApp{}, nil, err
	}
	return enrichedApp, enrichedReviews, nil
}
