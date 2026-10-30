package app

import (
	"context"
	"strings"
	"time"
	"unicode/utf8"

	"teamie/services/chat/internal/domain"
)

func requireHumanDeveloper(actor domain.User) error {
	if actor.Kind != "human" {
		return domain.ErrHumanDeveloperRequired()
	}
	return nil
}

func assertDeveloperOwner(app domain.MarketplaceApp, actorID string) error {
	if app.OwnerID == nil || strings.TrimSpace(*app.OwnerID) != actorID {
		return domain.ErrMarketplaceAppUnavailable()
	}
	return nil
}

// ListDeveloperMarketplaceApps lists all marketplace apps owned by the authenticated human developer.
// Read access stays available even when write feature flags are disabled.
func (s *Service) ListDeveloperMarketplaceApps(ctx context.Context, actor domain.User) ([]domain.MarketplaceApp, error) {
	if err := requireHumanDeveloper(actor); err != nil {
		return nil, err
	}
	apps, err := s.Marketplace.ListDeveloperMarketplaceApps(ctx, actor.ID)
	if err != nil {
		return nil, err
	}
	return s.enrichMarketplaceApps(ctx, apps), nil
}

// GetDeveloperMarketplaceApp returns a single developer-owned app and its review history.
func (s *Service) GetDeveloperMarketplaceApp(ctx context.Context, actor domain.User, appID string) (domain.MarketplaceApp, []domain.MarketplaceAppReview, error) {
	if err := requireHumanDeveloper(actor); err != nil {
		return domain.MarketplaceApp{}, nil, err
	}
	app, err := s.Marketplace.GetMarketplaceAppByID(ctx, strings.TrimSpace(appID), false)
	if err != nil {
		return domain.MarketplaceApp{}, nil, err
	}
	if err := assertDeveloperOwner(app, actor.ID); err != nil {
		return domain.MarketplaceApp{}, nil, err
	}
	reviews, err := s.Marketplace.ListMarketplaceAppReviews(ctx, app.ID)
	if err != nil {
		return domain.MarketplaceApp{}, nil, err
	}
	return s.enrichMarketplaceApps(ctx, []domain.MarketplaceApp{app})[0], s.enrichMarketplaceReviews(ctx, reviews), nil
}

// CreateDeveloperMarketplaceApp creates a draft app owned by the human developer and returns its initial signingSecret.
// webUrl and mcpUrl may be left empty at initial creation so the developer can deploy their service with AppID + SigningSecret first.
func (s *Service) CreateDeveloperMarketplaceApp(ctx context.Context, actor domain.User, in domain.CreateMarketplaceAppInput) (domain.MarketplaceApp, string, error) {
	if err := requireHumanDeveloper(actor); err != nil {
		return domain.MarketplaceApp{}, "", err
	}
	if err := s.requireFlag("im.marketplace.enabled"); err != nil {
		return domain.MarketplaceApp{}, "", err
	}
	slug, name, desc, err := validateMarketplaceAppBase(in.Slug, in.Name, in.Description)
	if err != nil {
		return domain.MarketplaceApp{}, "", err
	}
	draftWebURL, draftMCPURL, err := s.validateMarketplaceURLs(in.WebURL, in.MCPURL, true)
	if err != nil {
		return domain.MarketplaceApp{}, "", err
	}
	secret := domain.NewMarketplaceSigningSecret()
	ownerID := actor.ID
	var created domain.MarketplaceApp
	err = s.Tx.WithinTx(ctx, func(txCtx context.Context) error {
		if err := s.Tx.LockPrincipalMembership(txCtx, "dev-app:"+actor.ID); err != nil {
			return err
		}
		count, cntErr := s.Marketplace.CountDeveloperMarketplaceApps(txCtx, actor.ID)
		if cntErr != nil {
			return cntErr
		}
		if count >= domain.DeveloperAppQuota {
			return domain.ErrDeveloperAppLimitReached()
		}
		var insErr error
		created, insErr = s.Marketplace.InsertMarketplaceApp(txCtx, domain.MarketplaceApp{
			ID:               domain.NewID(),
			Slug:             slug,
			Name:             name,
			Description:      desc,
			WebURL:           "",
			MCPURL:           "",
			SigningSecret:    secret,
			Enabled:          true,
			OwnerID:          &ownerID,
			IsPublished:      false,
			PublishedVersion: 0,
			ReviewStatus:     domain.ReviewStatusDraft,
			DraftName:        name,
			DraftDescription: desc,
			DraftWebURL:      draftWebURL,
			DraftMCPURL:      draftMCPURL,
			HasDraftChanges:  true,
		})
		return insErr
	})
	if err != nil {
		return domain.MarketplaceApp{}, "", err
	}
	return s.enrichMarketplaceApps(ctx, []domain.MarketplaceApp{created})[0], secret, nil
}

// UpdateDeveloperMarketplaceApp updates draft fields or toggles developer availability on an owned app.
func (s *Service) UpdateDeveloperMarketplaceApp(ctx context.Context, actor domain.User, appID string, in domain.UpdateMarketplaceAppInput) (domain.MarketplaceApp, error) {
	if err := requireHumanDeveloper(actor); err != nil {
		return domain.MarketplaceApp{}, err
	}
	if err := s.requireFlag("im.marketplace.enabled"); err != nil {
		return domain.MarketplaceApp{}, err
	}
	onlyToggleEnabled := in.Enabled != nil && in.Slug == nil && in.Name == nil && in.Description == nil && in.WebURL == nil && in.MCPURL == nil
	var updated domain.MarketplaceApp
	err := s.Tx.WithinTx(ctx, func(txCtx context.Context) error {
		cur, getErr := s.Marketplace.GetMarketplaceAppByID(txCtx, strings.TrimSpace(appID), true)
		if getErr != nil {
			return getErr
		}
		if err := assertDeveloperOwner(cur, actor.ID); err != nil {
			return err
		}
		if onlyToggleEnabled {
			cur.Enabled = *in.Enabled
			var updErr error
			updated, updErr = s.Marketplace.UpdateMarketplaceApp(txCtx, cur)
			return updErr
		}
		if cur.ReviewStatus == domain.ReviewStatusPending {
			return domain.ErrMarketplaceReviewPending()
		}
		if in.Slug != nil {
			nextSlug := strings.ToLower(strings.TrimSpace(*in.Slug))
			if nextSlug != cur.Slug {
				if cur.IsPublished || cur.SubmissionRevision > 0 {
					return domain.ErrMarketplaceSlugImmutable()
				}
				if !domain.ValidMarketplaceSlug(nextSlug) {
					return domain.ErrInvalidMarketplaceSlug()
				}
				cur.Slug = nextSlug
			}
		}
		if in.Name != nil {
			name := strings.TrimSpace(*in.Name)
			if n := utf8.RuneCountInString(name); n < 1 || n > 80 {
				return domain.ErrInvalidMarketplaceName()
			}
			cur.DraftName = name
			if !cur.IsPublished {
				cur.Name = name
			}
		}
		if in.Description != nil {
			desc := strings.TrimSpace(*in.Description)
			if utf8.RuneCountInString(desc) > 500 {
				return domain.ErrInvalidMarketplaceDescription()
			}
			cur.DraftDescription = desc
			if !cur.IsPublished {
				cur.Description = desc
			}
		}
		urlsChanged := false
		if in.WebURL != nil {
			webURL, _, vErr := s.validateMarketplaceURLs(*in.WebURL, cur.DraftMCPURL, true)
			if vErr != nil {
				return vErr
			}
			if webURL != cur.DraftWebURL {
				urlsChanged = true
				cur.DraftWebURL = webURL
			}
		}
		if in.MCPURL != nil {
			_, mcpURL, vErr := s.validateMarketplaceURLs(cur.DraftWebURL, *in.MCPURL, true)
			if vErr != nil {
				return vErr
			}
			if mcpURL != cur.DraftMCPURL {
				urlsChanged = true
				cur.DraftMCPURL = mcpURL
			}
		}
		if in.Enabled != nil {
			cur.Enabled = *in.Enabled
		}
		if urlsChanged {
			cur.LastVerifiedAt = nil
			cur.VerificationReport = nil
		}
		cur.HasDraftChanges = !cur.IsPublished ||
			cur.DraftName != cur.Name ||
			cur.DraftDescription != cur.Description ||
			cur.DraftWebURL != cur.WebURL ||
			cur.DraftMCPURL != cur.MCPURL
		if cur.HasDraftChanges {
			cur.ReviewStatus = domain.ReviewStatusDraft
		} else if cur.IsPublished {
			cur.ReviewStatus = domain.ReviewStatusApproved
		}
		var updErr error
		updated, updErr = s.Marketplace.UpdateMarketplaceApp(txCtx, cur)
		return updErr
	})
	if err != nil {
		return domain.MarketplaceApp{}, err
	}
	return s.enrichMarketplaceApps(ctx, []domain.MarketplaceApp{updated})[0], nil
}

func (s *Service) runDraftEndpointVerification(ctx context.Context, app domain.MarketplaceApp, enforceCooldown bool) (domain.MarketplaceApp, domain.MarketplaceEndpointVerification, error) {
	if s.Verifier == nil {
		return domain.MarketplaceApp{}, domain.MarketplaceEndpointVerification{}, domain.ErrMarketplaceVerificationFailed("Endpoint verifier is not configured")
	}
	if strings.TrimSpace(app.DraftWebURL) == "" || strings.TrimSpace(app.DraftMCPURL) == "" {
		return domain.MarketplaceApp{}, domain.MarketplaceEndpointVerification{}, domain.ErrInvalidMarketplaceURL()
	}
	if enforceCooldown && s.VerifyCooldown > 0 && app.LastVerifiedAt != nil && time.Since(*app.LastVerifiedAt) < s.VerifyCooldown {
		return domain.MarketplaceApp{}, domain.MarketplaceEndpointVerification{}, domain.ErrMarketplaceVerifyRateLimited()
	}
	secret, err := s.Marketplace.GetMarketplaceAppSigningSecret(ctx, app.ID)
	if err != nil {
		return domain.MarketplaceApp{}, domain.MarketplaceEndpointVerification{}, err
	}
	report, err := s.Verifier.VerifyEndpoints(ctx, domain.VerifyEndpointsInput{
		AppID:         app.ID,
		Slug:          app.Slug,
		WebURL:        app.DraftWebURL,
		MCPURL:        app.DraftMCPURL,
		SigningSecret: secret,
	})
	if err != nil {
		return domain.MarketplaceApp{}, domain.MarketplaceEndpointVerification{}, err
	}
	updated, saveErr := s.Marketplace.SaveMarketplaceVerificationReport(ctx, app.ID, report.VerifiedAt, report)
	if saveErr != nil {
		return domain.MarketplaceApp{}, domain.MarketplaceEndpointVerification{}, saveErr
	}
	return updated, report, nil
}

// VerifyDeveloperMarketplaceApp runs the live platform protocol probe against the developer's deployed Web UI and MCP Server.
func (s *Service) VerifyDeveloperMarketplaceApp(ctx context.Context, actor domain.User, appID string) (domain.MarketplaceApp, domain.MarketplaceEndpointVerification, error) {
	if err := requireHumanDeveloper(actor); err != nil {
		return domain.MarketplaceApp{}, domain.MarketplaceEndpointVerification{}, err
	}
	if err := s.requireFlag("im.marketplace.enabled"); err != nil {
		return domain.MarketplaceApp{}, domain.MarketplaceEndpointVerification{}, err
	}
	app, err := s.Marketplace.GetMarketplaceAppByID(ctx, strings.TrimSpace(appID), false)
	if err != nil {
		return domain.MarketplaceApp{}, domain.MarketplaceEndpointVerification{}, err
	}
	if err := assertDeveloperOwner(app, actor.ID); err != nil {
		return domain.MarketplaceApp{}, domain.MarketplaceEndpointVerification{}, err
	}
	updated, report, err := s.runDraftEndpointVerification(ctx, app, true)
	if err != nil {
		return domain.MarketplaceApp{}, domain.MarketplaceEndpointVerification{}, err
	}
	return s.enrichMarketplaceApps(ctx, []domain.MarketplaceApp{updated})[0], report, nil
}

// SubmitDeveloperMarketplaceApp verifies that the deployed Web UI + MCP Server pass protocol checks and transitions the app to pending_review.
func (s *Service) SubmitDeveloperMarketplaceApp(ctx context.Context, actor domain.User, appID, note string) (domain.MarketplaceApp, []domain.MarketplaceAppReview, error) {
	if err := requireHumanDeveloper(actor); err != nil {
		return domain.MarketplaceApp{}, nil, err
	}
	if err := s.requireFlag("im.marketplace.enabled"); err != nil {
		return domain.MarketplaceApp{}, nil, err
	}
	note = strings.TrimSpace(note)
	if utf8.RuneCountInString(note) > 1000 {
		return domain.MarketplaceApp{}, nil, domain.ErrMarketplaceNoteLength()
	}
	snapshotApp, err := s.Marketplace.GetMarketplaceAppByID(ctx, strings.TrimSpace(appID), false)
	if err != nil {
		return domain.MarketplaceApp{}, nil, err
	}
	if err := assertDeveloperOwner(snapshotApp, actor.ID); err != nil {
		return domain.MarketplaceApp{}, nil, err
	}
	if snapshotApp.AdminSuspended {
		return domain.MarketplaceApp{}, nil, domain.ErrMarketplaceSuspended()
	}
	if snapshotApp.ReviewStatus == domain.ReviewStatusPending {
		return domain.MarketplaceApp{}, nil, domain.ErrMarketplaceInvalidState("Application is already pending review")
	}
	if snapshotApp.IsPublished && !snapshotApp.HasDraftChanges {
		return domain.MarketplaceApp{}, nil, domain.ErrMarketplaceInvalidState("No unpublished draft changes to submit")
	}

	_, report, err := s.runDraftEndpointVerification(ctx, snapshotApp, false)
	if err != nil {
		return domain.MarketplaceApp{}, nil, err
	}
	if !report.OK {
		return domain.MarketplaceApp{}, nil, domain.ErrMarketplaceVerificationFailed(report.FirstFailureSummary())
	}

	var updated domain.MarketplaceApp
	var reviews []domain.MarketplaceAppReview
	err = s.Tx.WithinTx(ctx, func(txCtx context.Context) error {
		cur, getErr := s.Marketplace.GetMarketplaceAppByID(txCtx, snapshotApp.ID, true)
		if getErr != nil {
			return getErr
		}
		if err := assertDeveloperOwner(cur, actor.ID); err != nil {
			return err
		}
		if cur.AdminSuspended {
			return domain.ErrMarketplaceSuspended()
		}
		if cur.ReviewStatus == domain.ReviewStatusPending {
			return domain.ErrMarketplaceInvalidState("Application is already pending review")
		}
		if cur.DraftWebURL != report.WebURL || cur.DraftMCPURL != report.MCPURL {
			return domain.ErrMarketplaceRevisionConflict()
		}
		now := time.Now().UTC()
		cur.SubmissionRevision++
		cur.ReviewStatus = domain.ReviewStatusPending
		cur.SubmissionNote = note
		cur.SubmittedAt = &now
		cur.LastVerifiedAt = &report.VerifiedAt
		cur.VerificationReport = &report
		var updErr error
		updated, updErr = s.Marketplace.UpdateMarketplaceApp(txCtx, cur)
		if updErr != nil {
			return updErr
		}
		if err := s.Marketplace.InsertMarketplaceAppReview(txCtx, domain.MarketplaceAppReview{
			ID:       domain.NewID(),
			AppID:    cur.ID,
			Revision: cur.SubmissionRevision,
			Action:   domain.ReviewActionSubmit,
			ActorID:  actor.ID,
			Note:     note,
			WebURL:   cur.DraftWebURL,
			MCPURL:   cur.DraftMCPURL,
		}, map[string]any{
			"draftName":    cur.DraftName,
			"draftWebUrl":  cur.DraftWebURL,
			"draftMcpUrl":  cur.DraftMCPURL,
			"verification": report,
		}); err != nil {
			return err
		}
		var listErr error
		reviews, listErr = s.Marketplace.ListMarketplaceAppReviews(txCtx, cur.ID)
		return listErr
	})
	if err != nil {
		return domain.MarketplaceApp{}, nil, err
	}
	return s.enrichMarketplaceApps(ctx, []domain.MarketplaceApp{updated})[0], s.enrichMarketplaceReviews(ctx, reviews), nil
}

// WithdrawDeveloperMarketplaceApp withdraws a pending review back to draft so the developer can edit it.
func (s *Service) WithdrawDeveloperMarketplaceApp(ctx context.Context, actor domain.User, appID string) (domain.MarketplaceApp, []domain.MarketplaceAppReview, error) {
	if err := requireHumanDeveloper(actor); err != nil {
		return domain.MarketplaceApp{}, nil, err
	}
	if err := s.requireFlag("im.marketplace.enabled"); err != nil {
		return domain.MarketplaceApp{}, nil, err
	}
	var updated domain.MarketplaceApp
	var reviews []domain.MarketplaceAppReview
	err := s.Tx.WithinTx(ctx, func(txCtx context.Context) error {
		cur, getErr := s.Marketplace.GetMarketplaceAppByID(txCtx, strings.TrimSpace(appID), true)
		if getErr != nil {
			return getErr
		}
		if err := assertDeveloperOwner(cur, actor.ID); err != nil {
			return err
		}
		if cur.ReviewStatus != domain.ReviewStatusPending {
			return domain.ErrMarketplaceInvalidState("Only pending review submissions can be withdrawn")
		}
		cur.ReviewStatus = domain.ReviewStatusDraft
		var updErr error
		updated, updErr = s.Marketplace.UpdateMarketplaceApp(txCtx, cur)
		if updErr != nil {
			return updErr
		}
		if err := s.Marketplace.InsertMarketplaceAppReview(txCtx, domain.MarketplaceAppReview{
			ID:       domain.NewID(),
			AppID:    cur.ID,
			Revision: cur.SubmissionRevision,
			Action:   domain.ReviewActionWithdraw,
			ActorID:  actor.ID,
			WebURL:   cur.DraftWebURL,
			MCPURL:   cur.DraftMCPURL,
		}, nil); err != nil {
			return err
		}
		var listErr error
		reviews, listErr = s.Marketplace.ListMarketplaceAppReviews(txCtx, cur.ID)
		return listErr
	})
	if err != nil {
		return domain.MarketplaceApp{}, nil, err
	}
	return s.enrichMarketplaceApps(ctx, []domain.MarketplaceApp{updated})[0], s.enrichMarketplaceReviews(ctx, reviews), nil
}

// RotateDeveloperMarketplaceAppSecret generates a new signingSecret for an owned app.
func (s *Service) RotateDeveloperMarketplaceAppSecret(ctx context.Context, actor domain.User, appID string) (domain.MarketplaceApp, string, error) {
	if err := requireHumanDeveloper(actor); err != nil {
		return domain.MarketplaceApp{}, "", err
	}
	if err := s.requireFlag("im.marketplace.enabled"); err != nil {
		return domain.MarketplaceApp{}, "", err
	}
	secret := domain.NewMarketplaceSigningSecret()
	var updated domain.MarketplaceApp
	err := s.Tx.WithinTx(ctx, func(txCtx context.Context) error {
		cur, getErr := s.Marketplace.GetMarketplaceAppByID(txCtx, strings.TrimSpace(appID), true)
		if getErr != nil {
			return getErr
		}
		if err := assertDeveloperOwner(cur, actor.ID); err != nil {
			return err
		}
		var updErr error
		updated, updErr = s.Marketplace.UpdateMarketplaceSigningSecret(txCtx, cur.ID, secret)
		return updErr
	})
	if err != nil {
		return domain.MarketplaceApp{}, "", err
	}
	return s.enrichMarketplaceApps(ctx, []domain.MarketplaceApp{updated})[0], secret, nil
}

// DeleteDeveloperMarketplaceApp deletes an unpublished, non-pending draft app owned by the developer.
func (s *Service) DeleteDeveloperMarketplaceApp(ctx context.Context, actor domain.User, appID string) error {
	if err := requireHumanDeveloper(actor); err != nil {
		return err
	}
	if err := s.requireFlag("im.marketplace.enabled"); err != nil {
		return err
	}
	return s.Tx.WithinTx(ctx, func(txCtx context.Context) error {
		cur, getErr := s.Marketplace.GetMarketplaceAppByID(txCtx, strings.TrimSpace(appID), true)
		if getErr != nil {
			return getErr
		}
		if err := assertDeveloperOwner(cur, actor.ID); err != nil {
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
		_, delErr := s.Marketplace.DeleteMarketplaceApp(txCtx, cur.ID)
		return delErr
	})
}
