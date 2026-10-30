package app

import (
	"context"
	"strings"

	"teamie/services/marketplace/internal/domain"
)

func requireHumanDeveloper(actor domain.User) error {
	if actor.Kind != "human" {
		return domain.ErrHumanDeveloperRequired()
	}
	return nil
}

func ensureDeveloperOwner(app domain.MarketplaceApp, actorID string) error {
	if app.OwnerID == nil || strings.TrimSpace(*app.OwnerID) != actorID {
		return domain.ErrMarketplaceAppUnavailable()
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
			HasDraftChanges:  true,
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
	app, err := s.Marketplace.GetMarketplaceAppByID(ctx, strings.TrimSpace(appID), false)
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
// Toggling only Enabled remains permitted while a draft submission is pending_review; it never
// clears or bypasses AdminSuspended.
func (s *Service) UpdateDeveloperMarketplaceApp(ctx context.Context, actor domain.User, appID string, in domain.UpdateMarketplaceAppInput) (domain.MarketplaceApp, error) {
	if err := requireHumanDeveloper(actor); err != nil {
		return domain.MarketplaceApp{}, err
	}
	if err := s.requireMarketplaceEnabled(); err != nil {
		return domain.MarketplaceApp{}, err
	}
	appID = strings.TrimSpace(appID)
	onlyToggleEnabled := in.Enabled != nil && in.Slug == nil && in.Name == nil && in.Description == nil && in.WebURL == nil && in.MCPURL == nil

	var updated domain.MarketplaceApp
	err := s.Tx.WithinTx(ctx, func(txCtx context.Context) error {
		cur, err := s.Marketplace.GetMarketplaceAppByID(txCtx, appID, true)
		if err != nil {
			return err
		}
		if err := ensureDeveloperOwner(cur, actor.ID); err != nil {
			return err
		}
		if onlyToggleEnabled {
			cur.Enabled = *in.Enabled
			cur.UpdatedAt = s.nowUTC()
			res, updErr := s.Marketplace.UpdateMarketplaceApp(txCtx, cur)
			if updErr != nil {
				return updErr
			}
			updated = res
			return nil
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
			if slug != cur.Slug && (cur.IsPublished || cur.SubmissionRevision > 0) {
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
		if cur.HasDraftChanges {
			cur.ReviewStatus = domain.ReviewStatusDraft
		} else if cur.IsPublished {
			cur.ReviewStatus = domain.ReviewStatusApproved
		}
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

func (s *Service) probeAppSnapshot(
	ctx context.Context,
	app domain.MarketplaceApp,
	webURL, mcpURL string,
	enforceCooldown bool,
) (domain.MarketplaceEndpointVerification, string, error) {
	if s.Verifier == nil {
		return domain.MarketplaceEndpointVerification{}, "", domain.ErrMarketplaceVerificationFailed("Endpoint verifier is not configured")
	}
	if enforceCooldown {
		if err := s.acquireVerifySlot(app); err != nil {
			return domain.MarketplaceEndpointVerification{}, "", err
		}
	}
	secret := app.SigningSecret
	if secret == "" {
		var err error
		secret, err = s.Marketplace.GetMarketplaceAppSigningSecret(ctx, app.ID)
		if err != nil {
			return domain.MarketplaceEndpointVerification{}, "", err
		}
	}
	report, err := s.Verifier.VerifyEndpoints(ctx, domain.VerifyEndpointsInput{
		AppID:         app.ID,
		Slug:          app.Slug,
		WebURL:        webURL,
		MCPURL:        mcpURL,
		SigningSecret: secret,
	})
	if err != nil {
		return domain.MarketplaceEndpointVerification{}, "", err
	}
	if report.VerifiedAt.IsZero() {
		report.VerifiedAt = s.nowUTC()
	}
	return report, secret, nil
}

// VerifyDeveloperMarketplaceApp executes the live 4-stage protocol probe against the developer's draft endpoints.
func (s *Service) VerifyDeveloperMarketplaceApp(ctx context.Context, actor domain.User, appID string) (domain.MarketplaceApp, domain.MarketplaceEndpointVerification, error) {
	if err := requireHumanDeveloper(actor); err != nil {
		return domain.MarketplaceApp{}, domain.MarketplaceEndpointVerification{}, err
	}
	if err := s.requireMarketplaceEnabled(); err != nil {
		return domain.MarketplaceApp{}, domain.MarketplaceEndpointVerification{}, err
	}
	app, err := s.Marketplace.GetMarketplaceAppByID(ctx, strings.TrimSpace(appID), false)
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
	report, secret, err := s.probeAppSnapshot(ctx, app, webURL, mcpURL, true)
	if err != nil {
		return domain.MarketplaceApp{}, domain.MarketplaceEndpointVerification{}, err
	}
	saved, err := s.Marketplace.SaveMarketplaceVerificationReport(ctx, app.ID, report.VerifiedAt, report, domain.VerificationSaveFence{
		Slug:            app.Slug,
		DraftWebURL:     app.DraftWebURL,
		DraftMCPURL:     app.DraftMCPURL,
		WebURL:          app.WebURL,
		MCPURL:          app.MCPURL,
		SigningSecret:   secret,
		IgnoreSuspended: false,
	})
	if err != nil {
		return domain.MarketplaceApp{}, domain.MarketplaceEndpointVerification{}, err
	}
	enriched, err := s.enrichApp(ctx, saved)
	if err != nil {
		return domain.MarketplaceApp{}, domain.MarketplaceEndpointVerification{}, err
	}
	return enriched, report, nil
}

// SubmitDeveloperMarketplaceApp runs a fresh live protocol probe against the current draft endpoints
// outside the write transaction, then locks the app row to fence against concurrent draft/secret/status
// changes before transitioning into pending_review.
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

	snapshotApp, err := s.Marketplace.GetMarketplaceAppByID(ctx, strings.TrimSpace(appID), false)
	if err != nil {
		return domain.MarketplaceApp{}, nil, err
	}
	if err := ensureDeveloperOwner(snapshotApp, actor.ID); err != nil {
		return domain.MarketplaceApp{}, nil, err
	}
	if snapshotApp.AdminSuspended {
		return domain.MarketplaceApp{}, nil, domain.ErrMarketplaceSuspended()
	}
	if snapshotApp.ReviewStatus == domain.ReviewStatusPending {
		return domain.MarketplaceApp{}, nil, domain.ErrMarketplaceInvalidState("Application is already pending review")
	}
	webURL, mcpURL, err := s.validateEndpoints(snapshotApp.DraftWebURL, snapshotApp.DraftMCPURL, false)
	if err != nil {
		return domain.MarketplaceApp{}, nil, err
	}
	if snapshotApp.IsPublished && !computeHasDraftChanges(snapshotApp) {
		return domain.MarketplaceApp{}, nil, domain.ErrMarketplaceInvalidState("No unpublished draft changes to submit")
	}

	report, snapshotSecret, err := s.probeAppSnapshot(ctx, snapshotApp, webURL, mcpURL, false)
	if err != nil {
		return domain.MarketplaceApp{}, nil, err
	}
	if !report.OK {
		if _, saveErr := s.Marketplace.SaveMarketplaceVerificationReport(ctx, snapshotApp.ID, report.VerifiedAt, report, domain.VerificationSaveFence{
			Slug:            snapshotApp.Slug,
			DraftWebURL:     snapshotApp.DraftWebURL,
			DraftMCPURL:     snapshotApp.DraftMCPURL,
			WebURL:          snapshotApp.WebURL,
			MCPURL:          snapshotApp.MCPURL,
			SigningSecret:   snapshotSecret,
			IgnoreSuspended: false,
		}); saveErr != nil {
			return domain.MarketplaceApp{}, nil, saveErr
		}
		return domain.MarketplaceApp{}, nil, domain.ErrMarketplaceVerificationFailed("")
	}

	var updated domain.MarketplaceApp
	var reviews []domain.MarketplaceAppReview
	err = s.Tx.WithinTx(ctx, func(txCtx context.Context) error {
		cur, err := s.Marketplace.GetMarketplaceAppByID(txCtx, snapshotApp.ID, true)
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
			return domain.ErrMarketplaceInvalidState("Application is already pending review")
		}
		if cur.IsPublished && !computeHasDraftChanges(cur) {
			return domain.ErrMarketplaceInvalidState("No unpublished draft changes to submit")
		}
		curSecret, err := s.Marketplace.GetMarketplaceAppSigningSecret(txCtx, cur.ID)
		if err != nil {
			return err
		}
		if cur.Slug != snapshotApp.Slug ||
			cur.DraftName != snapshotApp.DraftName ||
			cur.DraftDescription != snapshotApp.DraftDescription ||
			cur.DraftWebURL != webURL ||
			cur.DraftMCPURL != mcpURL ||
			curSecret != snapshotSecret ||
			cur.SubmissionRevision != snapshotApp.SubmissionRevision {
			return domain.ErrMarketplaceRevisionConflict()
		}

		now := s.nowUTC()
		cur.SubmissionRevision++
		cur.ReviewStatus = domain.ReviewStatusPending
		cur.SubmissionNote = note
		cur.SubmittedAt = &now
		cur.LastVerifiedAt = &report.VerifiedAt
		cur.VerificationReport = &report
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
		revs, err := s.Marketplace.ListMarketplaceAppReviews(txCtx, res.ID)
		if err != nil {
			return err
		}
		updated = res
		reviews = revs
		return nil
	})
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

// WithdrawDeveloperMarketplaceApp returns a pending_review submission back to draft so the developer can edit it.
func (s *Service) WithdrawDeveloperMarketplaceApp(ctx context.Context, actor domain.User, appID string) (domain.MarketplaceApp, []domain.MarketplaceAppReview, error) {
	if err := requireHumanDeveloper(actor); err != nil {
		return domain.MarketplaceApp{}, nil, err
	}
	if err := s.requireMarketplaceEnabled(); err != nil {
		return domain.MarketplaceApp{}, nil, err
	}
	appID = strings.TrimSpace(appID)
	var updated domain.MarketplaceApp
	var reviews []domain.MarketplaceAppReview
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
			return domain.ErrMarketplaceInvalidState("Only pending review submissions can be withdrawn")
		}
		now := s.nowUTC()
		cur.ReviewStatus = domain.ReviewStatusDraft
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
		revs, err := s.Marketplace.ListMarketplaceAppReviews(txCtx, res.ID)
		if err != nil {
			return err
		}
		updated = res
		reviews = revs
		return nil
	})
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
	appID = strings.TrimSpace(appID)
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
		res, err := s.Marketplace.UpdateMarketplaceSigningSecret(txCtx, cur.ID, secret)
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
	appID = strings.TrimSpace(appID)
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
		if cur.IsPublished || cur.PublishedVersion > 0 {
			return domain.ErrMarketplaceInvalidState("Published applications cannot be deleted by the developer; disable the app or request platform removal")
		}
		deleted, err := s.Marketplace.DeleteMarketplaceApp(txCtx, cur.ID)
		if err != nil {
			return err
		}
		if !deleted {
			return domain.ErrMarketplaceAppUnavailable()
		}
		return nil
	})
}
