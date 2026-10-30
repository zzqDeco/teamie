package app

import (
	"context"
	"strings"
	"time"
	"unicode/utf8"

	"teamie/services/chat/internal/domain"
)

// VerifyAdminMarketplaceApp allows a platform administrator to re-run live protocol verification on any app's draft endpoints.
func (s *Service) VerifyAdminMarketplaceApp(ctx context.Context, actor domain.User, appID string) (domain.MarketplaceApp, domain.MarketplaceEndpointVerification, error) {
	if err := requirePlatformAdmin(actor); err != nil {
		return domain.MarketplaceApp{}, domain.MarketplaceEndpointVerification{}, err
	}
	if err := s.requireFlag("im.marketplace.enabled"); err != nil {
		return domain.MarketplaceApp{}, domain.MarketplaceEndpointVerification{}, err
	}
	app, err := s.Marketplace.GetMarketplaceAppByID(ctx, strings.TrimSpace(appID), false)
	if err != nil {
		return domain.MarketplaceApp{}, domain.MarketplaceEndpointVerification{}, err
	}
	updated, report, err := s.runDraftEndpointVerification(ctx, app, false)
	if err != nil {
		return domain.MarketplaceApp{}, domain.MarketplaceEndpointVerification{}, err
	}
	return s.enrichMarketplaceApps(ctx, []domain.MarketplaceApp{updated})[0], report, nil
}

// ApproveAdminMarketplaceApp atomically promotes a pending_review draft into the live published catalog.
func (s *Service) ApproveAdminMarketplaceApp(ctx context.Context, actor domain.User, appID string, expectedRevision int, note string) (domain.MarketplaceApp, []domain.MarketplaceAppReview, error) {
	if err := requirePlatformAdmin(actor); err != nil {
		return domain.MarketplaceApp{}, nil, err
	}
	if err := s.requireFlag("im.marketplace.enabled"); err != nil {
		return domain.MarketplaceApp{}, nil, err
	}
	note = strings.TrimSpace(note)
	if utf8.RuneCountInString(note) > 1000 {
		return domain.MarketplaceApp{}, nil, domain.ErrMarketplaceNoteLength()
	}
	var updated domain.MarketplaceApp
	var reviews []domain.MarketplaceAppReview
	err := s.Tx.WithinTx(ctx, func(txCtx context.Context) error {
		cur, getErr := s.Marketplace.GetMarketplaceAppByID(txCtx, strings.TrimSpace(appID), true)
		if getErr != nil {
			return getErr
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
		if _, _, vErr := s.validateMarketplaceURLs(cur.DraftWebURL, cur.DraftMCPURL, false); vErr != nil {
			return vErr
		}
		now := time.Now().UTC()
		reviewerID := actor.ID
		cur.Name = cur.DraftName
		cur.Description = cur.DraftDescription
		cur.WebURL = cur.DraftWebURL
		cur.MCPURL = cur.DraftMCPURL
		cur.IsPublished = true
		cur.PublishedVersion++
		cur.HasDraftChanges = false
		cur.ReviewStatus = domain.ReviewStatusApproved
		cur.ReviewedBy = &reviewerID
		cur.ReviewedAt = &now
		cur.ReviewNote = note
		var updErr error
		updated, updErr = s.Marketplace.UpdateMarketplaceApp(txCtx, cur)
		if updErr != nil {
			return updErr
		}
		if err := s.Marketplace.InsertMarketplaceAppReview(txCtx, domain.MarketplaceAppReview{
			ID:       domain.NewID(),
			AppID:    cur.ID,
			Revision: cur.SubmissionRevision,
			Action:   domain.ReviewActionApprove,
			ActorID:  actor.ID,
			Note:     note,
			WebURL:   cur.WebURL,
			MCPURL:   cur.MCPURL,
		}, map[string]any{
			"publishedVersion": cur.PublishedVersion,
			"webUrl":           cur.WebURL,
			"mcpUrl":           cur.MCPURL,
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

// RejectAdminMarketplaceApp rejects a pending_review submission with a required reason while leaving any previously published version untouched.
func (s *Service) RejectAdminMarketplaceApp(ctx context.Context, actor domain.User, appID string, expectedRevision int, note string) (domain.MarketplaceApp, []domain.MarketplaceAppReview, error) {
	if err := requirePlatformAdmin(actor); err != nil {
		return domain.MarketplaceApp{}, nil, err
	}
	if err := s.requireFlag("im.marketplace.enabled"); err != nil {
		return domain.MarketplaceApp{}, nil, err
	}
	note = strings.TrimSpace(note)
	if note == "" {
		return domain.MarketplaceApp{}, nil, domain.ErrMarketplaceNoteRequired()
	}
	if utf8.RuneCountInString(note) > 1000 {
		return domain.MarketplaceApp{}, nil, domain.ErrMarketplaceNoteLength()
	}
	var updated domain.MarketplaceApp
	var reviews []domain.MarketplaceAppReview
	err := s.Tx.WithinTx(ctx, func(txCtx context.Context) error {
		cur, getErr := s.Marketplace.GetMarketplaceAppByID(txCtx, strings.TrimSpace(appID), true)
		if getErr != nil {
			return getErr
		}
		if cur.ReviewStatus != domain.ReviewStatusPending {
			return domain.ErrMarketplaceInvalidState("Application is not pending review")
		}
		if cur.SubmissionRevision != expectedRevision {
			return domain.ErrMarketplaceRevisionConflict()
		}
		now := time.Now().UTC()
		reviewerID := actor.ID
		cur.ReviewStatus = domain.ReviewStatusRejected
		cur.ReviewedBy = &reviewerID
		cur.ReviewedAt = &now
		cur.ReviewNote = note
		var updErr error
		updated, updErr = s.Marketplace.UpdateMarketplaceApp(txCtx, cur)
		if updErr != nil {
			return updErr
		}
		if err := s.Marketplace.InsertMarketplaceAppReview(txCtx, domain.MarketplaceAppReview{
			ID:       domain.NewID(),
			AppID:    cur.ID,
			Revision: cur.SubmissionRevision,
			Action:   domain.ReviewActionReject,
			ActorID:  actor.ID,
			Note:     note,
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

// SuspendAdminMarketplaceApp immediately suspends an application across all workspaces.
func (s *Service) SuspendAdminMarketplaceApp(ctx context.Context, actor domain.User, appID, reason string) (domain.MarketplaceApp, []domain.MarketplaceAppReview, error) {
	if err := requirePlatformAdmin(actor); err != nil {
		return domain.MarketplaceApp{}, nil, err
	}
	if err := s.requireFlag("im.marketplace.enabled"); err != nil {
		return domain.MarketplaceApp{}, nil, err
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return domain.MarketplaceApp{}, nil, domain.ErrMarketplaceNoteRequired()
	}
	if utf8.RuneCountInString(reason) > 500 {
		return domain.MarketplaceApp{}, nil, domain.ErrMarketplaceNoteLength()
	}
	var updated domain.MarketplaceApp
	var reviews []domain.MarketplaceAppReview
	err := s.Tx.WithinTx(ctx, func(txCtx context.Context) error {
		cur, getErr := s.Marketplace.GetMarketplaceAppByID(txCtx, strings.TrimSpace(appID), true)
		if getErr != nil {
			return getErr
		}
		now := time.Now().UTC()
		cur.AdminSuspended = true
		cur.SuspendedReason = reason
		cur.SuspendedAt = &now
		var updErr error
		updated, updErr = s.Marketplace.UpdateMarketplaceApp(txCtx, cur)
		if updErr != nil {
			return updErr
		}
		if err := s.Marketplace.InsertMarketplaceAppReview(txCtx, domain.MarketplaceAppReview{
			ID:       domain.NewID(),
			AppID:    cur.ID,
			Revision: cur.SubmissionRevision,
			Action:   domain.ReviewActionSuspend,
			ActorID:  actor.ID,
			Note:     reason,
			WebURL:   cur.WebURL,
			MCPURL:   cur.MCPURL,
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

// UnsuspendAdminMarketplaceApp lifts a platform suspension on an application.
func (s *Service) UnsuspendAdminMarketplaceApp(ctx context.Context, actor domain.User, appID string) (domain.MarketplaceApp, []domain.MarketplaceAppReview, error) {
	if err := requirePlatformAdmin(actor); err != nil {
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
		cur.AdminSuspended = false
		cur.SuspendedReason = ""
		cur.SuspendedAt = nil
		var updErr error
		updated, updErr = s.Marketplace.UpdateMarketplaceApp(txCtx, cur)
		if updErr != nil {
			return updErr
		}
		if err := s.Marketplace.InsertMarketplaceAppReview(txCtx, domain.MarketplaceAppReview{
			ID:       domain.NewID(),
			AppID:    cur.ID,
			Revision: cur.SubmissionRevision,
			Action:   domain.ReviewActionUnsuspend,
			ActorID:  actor.ID,
			WebURL:   cur.WebURL,
			MCPURL:   cur.MCPURL,
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
