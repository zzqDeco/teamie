package app

import (
	"context"

	"teamie/services/marketplace/internal/domain"
)

func requireHumanDeveloper(actor domain.User) error {
	if actor.Kind != "human" {
		return domain.ErrHumanDeveloperRequired()
	}
	return nil
}

func ensureDeveloperOwner(app domain.MarketplaceApp, actorID string) error {
	if app.OwnerID == nil || *app.OwnerID == "" || *app.OwnerID != actorID {
		return domain.ErrNotFound()
	}
	return nil
}

// ListDeveloperMarketplaceApps lists all marketplace apps created by the calling human developer.
func (s *Service) ListDeveloperMarketplaceApps(ctx context.Context, actor domain.User) ([]domain.MarketplaceApp, error) {
	if err := requireHumanDeveloper(actor); err != nil {
		return nil, err
	}
	apps, err := s.Marketplace.ListDeveloperMarketplaceApps(ctx, actor.ID)
	if err != nil {
		return nil, err
	}
	return s.enrichApps(ctx, apps)
}

// CreateDeveloperMarketplaceApp creates a new draft developer app under the caller's 20-app quota.
func (s *Service) CreateDeveloperMarketplaceApp(ctx context.Context, actor domain.User, in domain.CreateMarketplaceAppInput) (domain.MarketplaceApp, string, error) {
	if err := requireHumanDeveloper(actor); err != nil {
		return domain.MarketplaceApp{}, "", err
	}
	if err := s.requireMarketplaceEnabled(); err != nil {
		return domain.MarketplaceApp{}, "", err
	}
	slug, err := normalizeSlug(in.Slug)
	if err != nil {
		return domain.MarketplaceApp{}, "", err
	}
	name, err := normalizeName(in.Name)
	if err != nil {
		return domain.MarketplaceApp{}, "", err
	}
	desc, err := normalizeDescription(in.Description)
	if err != nil {
		return domain.MarketplaceApp{}, "", err
	}
	webURL, mcpURL, err := s.validateEndpoints(in.WebURL, in.MCPURL, true)
	if err != nil {
		return domain.MarketplaceApp{}, "", err
	}
	secret := domain.NewMarketplaceSigningSecret()
	ownerID := actor.ID
	now := s.nowUTC()

	var created domain.MarketplaceApp
	err = s.Tx.WithinTx(ctx, func(txCtx context.Context) error {
		if err := s.Tx.LockDeveloperQuota(txCtx, ownerID); err != nil {
			return err
		}
		count, err := s.Marketplace.CountDeveloperMarketplaceApps(txCtx, ownerID)
		if err != nil {
			return err
		}
		if count >= domain.DeveloperAppQuota {
			return domain.ErrDeveloperAppLimitReached()
		}
		res, err := s.Marketplace.InsertMarketplaceApp(txCtx, domain.MarketplaceApp{
			ID:               domain.NewID(),
			Slug:             slug,
			Name:             name,
			Description:      desc,
			WebURL:           "",
			MCPURL:           "",
			SigningSecret:    secret,
			Enabled:          true,
			CreatedAt:        now,
			UpdatedAt:        now,
			OwnerID:          &ownerID,
			IsPublished:      false,
			PublishedVersion: 0,
			ReviewStatus:     domain.ReviewStatusDraft,
			DraftName:        name,
			DraftDescription: desc,
			DraftWebURL:      webURL,
			DraftMCPURL:      mcpURL,
			HasDraftChanges:  false,
		})
		if err != nil {
			return err
		}
		created = res
		return nil
	})
	if err != nil {
		return domain.MarketplaceApp{}, "", err
	}
	enriched, err := s.enrichApp(ctx, created)
	if err != nil {
		return domain.MarketplaceApp{}, "", err
	}
	return enriched, secret, nil
}

// GetDeveloperMarketplaceApp returns a developer's own app and its audit trail.
func (s *Service) GetDeveloperMarketplaceApp(ctx context.Context, actor domain.User, appID string) (domain.MarketplaceApp, []domain.MarketplaceAppReview, error) {
	if err := requireHumanDeveloper(actor); err != nil {
		return domain.MarketplaceApp{}, nil, err
	}
	app, err := s.Marketplace.GetMarketplaceAppByID(ctx, appID, false)
	if err != nil {
		return domain.MarketplaceApp{}, nil, err
	}
	if err := ensureDeveloperOwner(app, actor.ID); err != nil {
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

// UpdateDeveloperMarketplaceApp updates draft fields or enabled toggle on a developer's app.
func (s *Service) UpdateDeveloperMarketplaceApp(ctx context.Context, actor domain.User, appID string, in domain.UpdateMarketplaceAppInput) (domain.MarketplaceApp, error) {
	if err := requireHumanDeveloper(actor); err != nil {
		return domain.MarketplaceApp{}, err
	}
	if err := s.requireMarketplaceEnabled(); err != nil {
		return domain.MarketplaceApp{}, err
	}
	var updated domain.MarketplaceApp
	err := s.Tx.WithinTx(ctx, func(txCtx context.Context) error {
		cur, err := s.Marketplace.GetMarketplaceAppByID(txCtx, appID, true)
		if err != nil {
			return err
		}
		if err := ensureDeveloperOwner(cur, actor.ID); err != nil {
			return err
		}
		if cur.AdminSuspended {
			return domain.ErrMarketplaceSuspended()
		}
		if cur.ReviewStatus == domain.ReviewStatusPending {
			return domain.ErrMarketplaceReviewPending()
		}

		origSlug := cur.Slug
		origWeb := cur.DraftWebURL
		origMCP := cur.DraftMCPURL

		if in.Slug != nil {
			slug, err := normalizeSlug(*in.Slug)
			if err != nil {
				return err
			}
			if slug != cur.Slug && (cur.IsPublished || cur.SubmissionRevision > 0 || cur.ReviewStatus != domain.ReviewStatusDraft) {
				return domain.ErrMarketplaceSlugImmutable()
			}
			cur.Slug = slug
		}
		if in.Name != nil {
			name, err := normalizeName(*in.Name)
			if err != nil {
				return err
			}
			cur.DraftName = name
			if !cur.IsPublished {
				cur.Name = name
			}
		}
		if in.Description != nil {
			desc, err := normalizeDescription(*in.Description)
			if err != nil {
				return err
			}
			cur.DraftDescription = desc
			if !cur.IsPublished {
				cur.Description = desc
			}
		}
		if in.WebURL != nil || in.MCPURL != nil {
			nextWeb := cur.DraftWebURL
			if in.WebURL != nil {
				nextWeb = *in.WebURL
			}
			nextMCP := cur.DraftMCPURL
			if in.MCPURL != nil {
				nextMCP = *in.MCPURL
			}
			validWeb, validMCP, err := s.validateEndpoints(nextWeb, nextMCP, true)
			if err != nil {
				return err
			}
			cur.DraftWebURL = validWeb
			cur.DraftMCPURL = validMCP
		}
		if in.Enabled != nil {
			cur.Enabled = *in.Enabled
		}
		if cur.Slug != origSlug || cur.DraftWebURL != origWeb || cur.DraftMCPURL != origMCP {
			cur.LastVerifiedAt = nil
			cur.VerificationReport = nil
		}
		cur.HasDraftChanges = computeHasDraftChanges(cur)
		cur.UpdatedAt = s.nowUTC()
		res, err := s.Marketplace.UpdateMarketplaceApp(txCtx, cur)
		if err != nil {
			return err
		}
		updated = res
		return nil
	})
	if err != nil {
		return domain.MarketplaceApp{}, err
	}
	return s.enrichApp(ctx, updated)
}

// VerifyDeveloperMarketplaceApp executes the live 4-stage protocol probe against the developer's draft endpoints.
func (s *Service) VerifyDeveloperMarketplaceApp(ctx context.Context, actor domain.User, appID string) (domain.MarketplaceApp, domain.MarketplaceEndpointVerification, error) {
	if err := requireHumanDeveloper(actor); err != nil {
		return domain.MarketplaceApp{}, domain.MarketplaceEndpointVerification{}, err
	}
	if err := s.requireMarketplaceEnabled(); err != nil {
		return domain.MarketplaceApp{}, domain.MarketplaceEndpointVerification{}, err
	}
	app, err := s.Marketplace.GetMarketplaceAppByID(ctx, appID, false)
	if err != nil {
		return domain.MarketplaceApp{}, domain.MarketplaceEndpointVerification{}, err
	}
	if err := ensureDeveloperOwner(app, actor.ID); err != nil {
		return domain.MarketplaceApp{}, domain.MarketplaceEndpointVerification{}, err
	}
	if app.AdminSuspended {
		return domain.MarketplaceApp{}, domain.MarketplaceEndpointVerification{}, domain.ErrMarketplaceSuspended()
	}
	webURL, mcpURL, err := s.validateEndpoints(app.DraftWebURL, app.DraftMCPURL, false)
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

// SubmitDeveloperMarketplaceApp transitions a verified draft into pending_review.
func (s *Service) SubmitDeveloperMarketplaceApp(ctx context.Context, actor domain.User, appID, rawNote string) (domain.MarketplaceApp, []domain.MarketplaceAppReview, error) {
	if err := requireHumanDeveloper(actor); err != nil {
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
		if err := ensureDeveloperOwner(cur, actor.ID); err != nil {
			return err
		}
		if cur.AdminSuspended {
			return domain.ErrMarketplaceSuspended()
		}
		if cur.ReviewStatus == domain.ReviewStatusPending {
			return domain.ErrMarketplaceReviewPending()
		}
		webURL, mcpURL, err := s.validateEndpoints(cur.DraftWebURL, cur.DraftMCPURL, false)
		if err != nil {
			return err
		}
		if cur.IsPublished && !computeHasDraftChanges(cur) {
			return domain.ErrMarketplaceInvalidState("No draft changes to submit for review")
		}
		if cur.LastVerifiedAt == nil || cur.VerificationReport == nil || !cur.VerificationReport.OK ||
			cur.VerificationReport.WebURL != webURL || cur.VerificationReport.MCPURL != mcpURL {
			summary := ""
			if cur.VerificationReport != nil {
				summary = cur.VerificationReport.FirstFailureSummary()
			}
			return domain.ErrMarketplaceVerificationFailed(summary)
		}

		now := s.nowUTC()
		cur.SubmissionRevision++
		cur.ReviewStatus = domain.ReviewStatusPending
		cur.SubmissionNote = note
		cur.SubmittedAt = &now
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
			Action:    domain.ReviewActionSubmit,
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

// WithdrawDeveloperMarketplaceApp returns a pending_review app back to draft (or approved if already published).
func (s *Service) WithdrawDeveloperMarketplaceApp(ctx context.Context, actor domain.User, appID string) (domain.MarketplaceApp, []domain.MarketplaceAppReview, error) {
	if err := requireHumanDeveloper(actor); err != nil {
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
		if err := ensureDeveloperOwner(cur, actor.ID); err != nil {
			return err
		}
		if cur.AdminSuspended {
			return domain.ErrMarketplaceSuspended()
		}
		if cur.ReviewStatus != domain.ReviewStatusPending {
			return domain.ErrMarketplaceInvalidState("Application is not currently pending review")
		}
		now := s.nowUTC()
		if cur.IsPublished {
			cur.ReviewStatus = domain.ReviewStatusApproved
		} else {
			cur.ReviewStatus = domain.ReviewStatusDraft
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
			Action:    domain.ReviewActionWithdraw,
			ActorID:   actor.ID,
			Note:      "",
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

// RotateDeveloperMarketplaceAppSecret issues a fresh HS256 signing secret for a developer's own app.
func (s *Service) RotateDeveloperMarketplaceAppSecret(ctx context.Context, actor domain.User, appID string) (domain.MarketplaceApp, string, error) {
	if err := requireHumanDeveloper(actor); err != nil {
		return domain.MarketplaceApp{}, "", err
	}
	if err := s.requireMarketplaceEnabled(); err != nil {
		return domain.MarketplaceApp{}, "", err
	}
	secret := domain.NewMarketplaceSigningSecret()
	var updated domain.MarketplaceApp
	err := s.Tx.WithinTx(ctx, func(txCtx context.Context) error {
		cur, err := s.Marketplace.GetMarketplaceAppByID(txCtx, appID, true)
		if err != nil {
			return err
		}
		if err := ensureDeveloperOwner(cur, actor.ID); err != nil {
			return err
		}
		if cur.AdminSuspended {
			return domain.ErrMarketplaceSuspended()
		}
		if cur.ReviewStatus == domain.ReviewStatusPending {
			return domain.ErrMarketplaceReviewPending()
		}
		res, err := s.Marketplace.UpdateMarketplaceSigningSecret(txCtx, appID, secret)
		if err != nil {
			return err
		}
		updated = res
		return nil
	})
	if err != nil {
		return domain.MarketplaceApp{}, "", err
	}
	enriched, err := s.enrichApp(ctx, updated)
	if err != nil {
		return domain.MarketplaceApp{}, "", err
	}
	return enriched, secret, nil
}

// DeleteDeveloperMarketplaceApp deletes an unpublished, non-pending, non-suspended draft app.
func (s *Service) DeleteDeveloperMarketplaceApp(ctx context.Context, actor domain.User, appID string) error {
	if err := requireHumanDeveloper(actor); err != nil {
		return err
	}
	if err := s.requireMarketplaceEnabled(); err != nil {
		return err
	}
	return s.Tx.WithinTx(ctx, func(txCtx context.Context) error {
		cur, err := s.Marketplace.GetMarketplaceAppByID(txCtx, appID, true)
		if err != nil {
			return err
		}
		if err := ensureDeveloperOwner(cur, actor.ID); err != nil {
			return err
		}
		if cur.AdminSuspended {
			return domain.ErrMarketplaceSuspended()
		}
		if cur.ReviewStatus == domain.ReviewStatusPending {
			return domain.ErrMarketplaceReviewPending()
		}
		if cur.IsPublished {
			return domain.ErrMarketplaceInvalidState("Published marketplace applications cannot be deleted by the developer")
		}
		deleted, err := s.Marketplace.DeleteMarketplaceApp(txCtx, appID)
		if err != nil {
			return err
		}
		if !deleted {
			return domain.ErrNotFound()
		}
		return nil
	})
}
