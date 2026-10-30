package app

import (
	"context"
	"errors"
	"strings"

	"teamie/services/marketplace/internal/domain"
)

func requirePlatformAdmin(actor domain.User) error {
	if actor.Kind != "human" || actor.PlatformRole != "admin" {
		return domain.ErrPlatformAdminRequired()
	}
	return nil
}

// CreateAdminMarketplaceApp registers an immediately published official platform app.
func (s *Service) CreateAdminMarketplaceApp(ctx context.Context, actor domain.User, in domain.CreateMarketplaceAppInput) (domain.MarketplaceApp, string, error) {
	if err := requirePlatformAdmin(actor); err != nil {
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
	webURL, mcpURL, err := s.validateEndpoints(in.WebURL, in.MCPURL, false)
	if err != nil {
		return domain.MarketplaceApp{}, "", err
	}
	enabled := true
	if in.Enabled != nil {
		enabled = *in.Enabled
	}
	secret := domain.NewMarketplaceSigningSecret()
	now := s.nowUTC()
	var created domain.MarketplaceApp
	err = s.Tx.WithinTx(ctx, func(txCtx context.Context) error {
		res, insErr := s.Marketplace.InsertMarketplaceApp(txCtx, domain.MarketplaceApp{
			ID:               domain.NewID(),
			Slug:             slug,
			Name:             name,
			Description:      desc,
			WebURL:           webURL,
			MCPURL:           mcpURL,
			SigningSecret:    secret,
			Enabled:          enabled,
			CreatedAt:        now,
			UpdatedAt:        now,
			OwnerID:          nil,
			IsPublished:      true,
			PublishedVersion: 1,
			ReviewStatus:     domain.ReviewStatusApproved,
			DraftName:        name,
			DraftDescription: desc,
			DraftWebURL:      webURL,
			DraftMCPURL:      mcpURL,
			HasDraftChanges:  false,
		})
		if insErr != nil {
			return insErr
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

// UpdateAdminMarketplaceApp updates an existing marketplace app for platform admins.
func (s *Service) UpdateAdminMarketplaceApp(ctx context.Context, actor domain.User, appID string, in domain.UpdateMarketplaceAppInput) (domain.MarketplaceApp, error) {
	if err := requirePlatformAdmin(actor); err != nil {
		return domain.MarketplaceApp{}, err
	}
	if err := s.requireMarketplaceEnabled(); err != nil {
		return domain.MarketplaceApp{}, err
	}
	appID = strings.TrimSpace(appID)
	var updated domain.MarketplaceApp
	err := s.Tx.WithinTx(ctx, func(txCtx context.Context) error {
		cur, err := s.Marketplace.GetMarketplaceAppByID(txCtx, appID, true)
		if err != nil {
			return err
		}
		origSlug := cur.Slug
		origDraftWeb := cur.DraftWebURL
		origDraftMCP := cur.DraftMCPURL
		if in.Slug != nil {
			slug, err := normalizeSlug(*in.Slug)
			if err != nil {
				return err
			}
			cur.Slug = slug
		}
		if in.Name != nil {
			name, err := normalizeName(*in.Name)
			if err != nil {
				return err
			}
			cur.Name = name
			if cur.IsOfficial() {
				cur.DraftName = name
			}
		}
		if in.Description != nil {
			desc, err := normalizeDescription(*in.Description)
			if err != nil {
				return err
			}
			cur.Description = desc
			if cur.IsOfficial() {
				cur.DraftDescription = desc
			}
		}
		if in.WebURL != nil {
			webURL, _, err := s.validateEndpoints(*in.WebURL, cur.MCPURL, cur.MCPURL == "")
			if err != nil {
				return err
			}
			cur.WebURL = webURL
			if cur.IsOfficial() {
				cur.DraftWebURL = webURL
			}
		}
		if in.MCPURL != nil {
			_, mcpURL, err := s.validateEndpoints(cur.WebURL, *in.MCPURL, cur.WebURL == "")
			if err != nil {
				return err
			}
			cur.MCPURL = mcpURL
			if cur.IsOfficial() {
				cur.DraftMCPURL = mcpURL
			}
		}
		if in.Enabled != nil {
			cur.Enabled = *in.Enabled
		}
		if cur.Slug != origSlug || cur.DraftWebURL != origDraftWeb || cur.DraftMCPURL != origDraftMCP {
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

// RotateAdminMarketplaceAppSecret rotates the HS256 signing secret of a marketplace app.
func (s *Service) RotateAdminMarketplaceAppSecret(ctx context.Context, actor domain.User, appID string) (domain.MarketplaceApp, string, error) {
	if err := requirePlatformAdmin(actor); err != nil {
		return domain.MarketplaceApp{}, "", err
	}
	if err := s.requireMarketplaceEnabled(); err != nil {
		return domain.MarketplaceApp{}, "", err
	}
	appID = strings.TrimSpace(appID)
	secret := domain.NewMarketplaceSigningSecret()
	var updated domain.MarketplaceApp
	err := s.Tx.WithinTx(ctx, func(txCtx context.Context) error {
		res, updErr := s.Marketplace.UpdateMarketplaceSigningSecret(txCtx, appID, secret)
		if updErr != nil {
			return updErr
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

// DeleteAdminMarketplaceApp permanently deletes an app and cascades workspace installations.
func (s *Service) DeleteAdminMarketplaceApp(ctx context.Context, actor domain.User, appID string) error {
	if err := requirePlatformAdmin(actor); err != nil {
		return err
	}
	if err := s.requireMarketplaceEnabled(); err != nil {
		return err
	}
	appID = strings.TrimSpace(appID)
	var deleted bool
	err := s.Tx.WithinTx(ctx, func(txCtx context.Context) error {
		var delErr error
		deleted, delErr = s.Marketplace.DeleteMarketplaceApp(txCtx, appID)
		return delErr
	})
	if err != nil {
		return err
	}
	if !deleted {
		return domain.ErrMarketplaceAppUnavailable()
	}
	return nil
}

// ListMarketplaceCatalog returns all enabled, published, non-suspended catalog apps to authenticated human users.
func (s *Service) ListMarketplaceCatalog(ctx context.Context, actor domain.User) ([]domain.MarketplaceApp, error) {
	if actor.Kind != "human" {
		return nil, domain.ErrHumanWorkspaceManagementRequired()
	}
	apps, err := s.Marketplace.ListPublishedCatalogApps(ctx)
	if err != nil {
		return nil, err
	}
	return s.enrichApps(ctx, apps)
}

// ListWorkspaceApps lists installed apps visible and runnable in workspaceID for actorID.
func (s *Service) ListWorkspaceApps(ctx context.Context, workspaceID, actorID string) ([]domain.MarketplaceApp, error) {
	scope, err := s.Chat.WorkspaceRole(ctx, workspaceID, actorID)
	if err != nil {
		return nil, err
	}
	if !scope.Allowed {
		return nil, domain.ErrWorkspaceUnavailable()
	}
	installed, err := s.Marketplace.ListWorkspaceMarketplaceApps(ctx, workspaceID)
	if err != nil {
		return nil, err
	}
	resolved := make([]domain.MarketplaceApp, 0, len(installed))
	for _, item := range installed {
		if app, ok := item.App.ResolveForWorkspace(scope.OwnerID, item.ReviewPreview); ok {
			resolved = append(resolved, app)
		}
	}
	return s.enrichApps(ctx, resolved)
}

// InstallWorkspaceApp installs a catalog, own-draft, or admin-review-preview app into workspaceID.
func (s *Service) InstallWorkspaceApp(ctx context.Context, workspaceID, appID string, actor domain.User) (domain.MarketplaceApp, error) {
	if actor.Kind != "human" {
		return domain.MarketplaceApp{}, domain.ErrHumanWorkspaceManagementRequired()
	}
	if err := s.requireMarketplaceEnabled(); err != nil {
		return domain.MarketplaceApp{}, err
	}
	scope, err := s.Chat.WorkspaceRole(ctx, workspaceID, actor.ID)
	if err != nil {
		return domain.MarketplaceApp{}, err
	}
	if !scope.Allowed {
		return domain.MarketplaceApp{}, domain.ErrWorkspaceUnavailable()
	}
	if scope.Role != "owner" && scope.OwnerID != actor.ID {
		return domain.MarketplaceApp{}, domain.ErrWorkspaceOwnerRequired()
	}
	app, err := s.Marketplace.GetMarketplaceAppByID(ctx, strings.TrimSpace(appID), false)
	if err != nil {
		var derr *domain.Error
		if errors.As(err, &derr) && derr.Status == 404 {
			return domain.MarketplaceApp{}, domain.ErrMarketplaceAppUnavailable()
		}
		return domain.MarketplaceApp{}, err
	}
	if app.AdminSuspended {
		return domain.MarketplaceApp{}, domain.ErrMarketplaceSuspended()
	}
	isDeveloperOwnWorkspace := app.OwnerID != nil && *app.OwnerID != "" && *app.OwnerID == scope.OwnerID
	reviewPreview := actor.PlatformRole == "admin" && !app.IsOfficial() && !isDeveloperOwnWorkspace && app.ReviewStatus == domain.ReviewStatusPending
	resolved, ok := app.ResolveForWorkspace(scope.OwnerID, reviewPreview)
	if !ok {
		return domain.MarketplaceApp{}, domain.ErrMarketplaceAppUnavailable()
	}
	if err := s.Marketplace.InstallWorkspaceMarketplaceApp(ctx, workspaceID, app.ID, actor.ID, reviewPreview); err != nil {
		return domain.MarketplaceApp{}, err
	}
	return s.enrichApp(ctx, resolved)
}

// UninstallWorkspaceApp removes an app installation from workspaceID.
func (s *Service) UninstallWorkspaceApp(ctx context.Context, workspaceID, appID string, actor domain.User) error {
	if actor.Kind != "human" {
		return domain.ErrHumanWorkspaceManagementRequired()
	}
	if err := s.requireMarketplaceEnabled(); err != nil {
		return err
	}
	scope, err := s.Chat.WorkspaceRole(ctx, workspaceID, actor.ID)
	if err != nil {
		return err
	}
	if !scope.Allowed {
		return domain.ErrWorkspaceUnavailable()
	}
	if scope.Role != "owner" && scope.OwnerID != actor.ID {
		return domain.ErrWorkspaceOwnerRequired()
	}
	return s.Marketplace.UninstallWorkspaceMarketplaceApp(ctx, workspaceID, strings.TrimSpace(appID))
}

// ListConversationApps returns runnable workspace apps for a group conversation member.
func (s *Service) ListConversationApps(ctx context.Context, conversationID, actorID string) ([]domain.MarketplaceApp, error) {
	scope, err := s.Chat.ConversationScope(ctx, conversationID, actorID)
	if err != nil {
		return nil, err
	}
	if !scope.Allowed {
		return nil, domain.ErrNotFound()
	}
	if scope.ConversationType != "group" {
		return nil, domain.ErrMarketplaceGroupOnly()
	}
	installed, err := s.Marketplace.ListWorkspaceMarketplaceApps(ctx, scope.WorkspaceID)
	if err != nil {
		return nil, err
	}
	resolved := make([]domain.MarketplaceApp, 0, len(installed))
	for _, item := range installed {
		if app, ok := item.App.ResolveForWorkspace(scope.WorkspaceOwnerID, item.ReviewPreview); ok {
			resolved = append(resolved, app)
		}
	}
	return s.enrichApps(ctx, resolved)
}

// CreateConversationAppSession mints a 1-hour symmetric HS256 context token for a group conversation member.
func (s *Service) CreateConversationAppSession(ctx context.Context, conversationID, appID string, actor domain.User) (domain.ConversationAppSession, error) {
	if err := s.requireMarketplaceEnabled(); err != nil {
		return domain.ConversationAppSession{}, err
	}
	appID = strings.TrimSpace(appID)
	scope, err := s.Chat.ConversationScope(ctx, conversationID, actor.ID)
	if err != nil {
		return domain.ConversationAppSession{}, err
	}
	if !scope.Allowed {
		return domain.ConversationAppSession{}, domain.ErrNotFound()
	}
	if scope.ConversationType != "group" {
		return domain.ConversationAppSession{}, domain.ErrMarketplaceGroupOnly()
	}
	installed, err := s.Marketplace.GetInstalledWorkspaceMarketplaceAppForSession(ctx, scope.WorkspaceID, appID)
	if err != nil {
		var derr *domain.Error
		if errors.As(err, &derr) && derr.Status == 404 {
			return domain.ConversationAppSession{}, domain.ErrMarketplaceAppUnavailable()
		}
		return domain.ConversationAppSession{}, err
	}
	if installed.App.AdminSuspended {
		return domain.ConversationAppSession{}, domain.ErrMarketplaceSuspended()
	}
	resolved, ok := installed.App.ResolveForWorkspace(scope.WorkspaceOwnerID, installed.ReviewPreview)
	if !ok {
		return domain.ConversationAppSession{}, domain.ErrMarketplaceAppUnavailable()
	}
	secret := installed.App.SigningSecret
	if secret == "" {
		secret, err = s.Marketplace.GetMarketplaceAppSigningSecret(ctx, appID)
		if err != nil {
			return domain.ConversationAppSession{}, err
		}
	}
	resolved.SigningSecret = ""
	enriched, err := s.enrichApp(ctx, resolved)
	if err != nil {
		return domain.ConversationAppSession{}, err
	}
	now := s.nowUTC()
	exp := now.Add(domain.MarketplaceSessionTTL)
	token, err := domain.SignMarketplaceContextToken(secret, domain.MarketplaceContextClaims{
		Issuer:         "teamie",
		Audience:       enriched.ID,
		Subject:        actor.ID,
		AppID:          enriched.ID,
		Slug:           enriched.Slug,
		WorkspaceID:    scope.WorkspaceID,
		ConversationID: conversationID,
		UserID:         actor.ID,
		DisplayName:    actor.DisplayName,
		IssuedAt:       now.Unix(),
		ExpiresAt:      exp.Unix(),
	})
	if err != nil {
		return domain.ConversationAppSession{}, err
	}
	return domain.ConversationAppSession{
		App:       enriched,
		Token:     token,
		ExpiresAt: exp,
	}, nil
}
