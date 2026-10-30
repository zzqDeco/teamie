package app

import (
	"context"
	"strings"
	"time"
	"unicode/utf8"

	"teamie/services/chat/internal/domain"
)

func requirePlatformAdmin(actor domain.User) error {
	if actor.Kind != "human" || actor.PlatformRole != "admin" {
		return domain.ErrPlatformAdminRequired()
	}
	return nil
}

func validateMarketplaceAppBase(slug, name, desc string) (string, string, string, error) {
	slug = strings.ToLower(strings.TrimSpace(slug))
	name = strings.TrimSpace(name)
	desc = strings.TrimSpace(desc)
	if !domain.ValidMarketplaceSlug(slug) {
		return "", "", "", domain.ErrInvalidMarketplaceSlug()
	}
	if n := utf8.RuneCountInString(name); n < 1 || n > 80 {
		return "", "", "", domain.ErrInvalidMarketplaceName()
	}
	if utf8.RuneCountInString(desc) > 500 {
		return "", "", "", domain.ErrInvalidMarketplaceDescription()
	}
	return slug, name, desc, nil
}

func (s *Service) validateMarketplaceURLs(webURL, mcpURL string, allowEmpty bool) (string, string, error) {
	webURL = strings.TrimSpace(webURL)
	mcpURL = strings.TrimSpace(mcpURL)
	if webURL == "" && allowEmpty {
		// allowed in initial draft
	} else if err := domain.ValidateMarketplaceEndpointURL(webURL, false, s.AppOrigin, s.RequirePublicHTTPS); err != nil {
		return "", "", err
	}
	if mcpURL == "" && allowEmpty {
		// allowed in initial draft
	} else if err := domain.ValidateMarketplaceEndpointURL(mcpURL, true, s.AppOrigin, s.RequirePublicHTTPS); err != nil {
		return "", "", err
	}
	return webURL, mcpURL, nil
}

func (s *Service) enrichMarketplaceApps(ctx context.Context, apps []domain.MarketplaceApp) []domain.MarketplaceApp {
	if len(apps) == 0 {
		return apps
	}
	ownerSet := map[string]struct{}{}
	ownerIDs := make([]string, 0, len(apps))
	for _, a := range apps {
		if a.OwnerID != nil && strings.TrimSpace(*a.OwnerID) != "" {
			id := strings.TrimSpace(*a.OwnerID)
			if _, seen := ownerSet[id]; !seen {
				ownerSet[id] = struct{}{}
				ownerIDs = append(ownerIDs, id)
			}
		}
	}
	names := map[string]string{}
	if len(ownerIDs) > 0 && s.Identity != nil {
		if users, err := s.Identity.Users(ctx, ownerIDs); err == nil {
			for _, u := range users {
				if strings.TrimSpace(u.DisplayName) != "" {
					names[u.ID] = u.DisplayName
				}
			}
		}
	}
	out := make([]domain.MarketplaceApp, len(apps))
	for i, a := range apps {
		if a.IsOfficial() {
			a.PublisherName = domain.OfficialPublisherLabel
			a.OwnerDisplayName = domain.OfficialPublisherLabel
		} else {
			ownerID := strings.TrimSpace(*a.OwnerID)
			display := names[ownerID]
			if display == "" {
				display = "Teamie 开发者"
			}
			a.OwnerDisplayName = display
			a.PublisherName = display
		}
		out[i] = a
	}
	return out
}

func (s *Service) enrichMarketplaceReviews(ctx context.Context, reviews []domain.MarketplaceAppReview) []domain.MarketplaceAppReview {
	if len(reviews) == 0 || s.Identity == nil {
		return reviews
	}
	seen := map[string]struct{}{}
	ids := make([]string, 0, len(reviews))
	for _, r := range reviews {
		if r.ActorID != "" {
			if _, ok := seen[r.ActorID]; !ok {
				seen[r.ActorID] = struct{}{}
				ids = append(ids, r.ActorID)
			}
		}
	}
	names := map[string]string{}
	if len(ids) > 0 {
		if users, err := s.Identity.Users(ctx, ids); err == nil {
			for _, u := range users {
				names[u.ID] = u.DisplayName
			}
		}
	}
	out := make([]domain.MarketplaceAppReview, len(reviews))
	for i, r := range reviews {
		r.ActorName = names[r.ActorID]
		out[i] = r
	}
	return out
}

// ListAdminMarketplaceApps lists all registered and developer-submitted marketplace apps for platform admins.
func (s *Service) ListAdminMarketplaceApps(ctx context.Context, actor domain.User) ([]domain.MarketplaceApp, error) {
	if err := requirePlatformAdmin(actor); err != nil {
		return nil, err
	}
	apps, err := s.Marketplace.ListAllMarketplaceApps(ctx)
	if err != nil {
		return nil, err
	}
	return s.enrichMarketplaceApps(ctx, apps), nil
}

// GetAdminMarketplaceApp returns an app and its review audit trail for a platform admin.
func (s *Service) GetAdminMarketplaceApp(ctx context.Context, actor domain.User, appID string) (domain.MarketplaceApp, []domain.MarketplaceAppReview, error) {
	if err := requirePlatformAdmin(actor); err != nil {
		return domain.MarketplaceApp{}, nil, err
	}
	app, err := s.Marketplace.GetMarketplaceAppByID(ctx, strings.TrimSpace(appID), false)
	if err != nil {
		return domain.MarketplaceApp{}, nil, err
	}
	reviews, err := s.Marketplace.ListMarketplaceAppReviews(ctx, app.ID)
	if err != nil {
		return domain.MarketplaceApp{}, nil, err
	}
	enriched := s.enrichMarketplaceApps(ctx, []domain.MarketplaceApp{app})
	return enriched[0], s.enrichMarketplaceReviews(ctx, reviews), nil
}

// CreateAdminMarketplaceApp registers an official platform marketplace app (immediately published) and returns its initial signingSecret.
func (s *Service) CreateAdminMarketplaceApp(ctx context.Context, actor domain.User, in domain.CreateMarketplaceAppInput) (domain.MarketplaceApp, string, error) {
	if err := requirePlatformAdmin(actor); err != nil {
		return domain.MarketplaceApp{}, "", err
	}
	if err := s.requireFlag("im.marketplace.enabled"); err != nil {
		return domain.MarketplaceApp{}, "", err
	}
	slug, name, desc, err := validateMarketplaceAppBase(in.Slug, in.Name, in.Description)
	if err != nil {
		return domain.MarketplaceApp{}, "", err
	}
	webURL, mcpURL, err := s.validateMarketplaceURLs(in.WebURL, in.MCPURL, false)
	if err != nil {
		return domain.MarketplaceApp{}, "", err
	}
	enabled := true
	if in.Enabled != nil {
		enabled = *in.Enabled
	}
	secret := domain.NewMarketplaceSigningSecret()
	var created domain.MarketplaceApp
	err = s.Tx.WithinTx(ctx, func(txCtx context.Context) error {
		var insErr error
		created, insErr = s.Marketplace.InsertMarketplaceApp(txCtx, domain.MarketplaceApp{
			ID:               domain.NewID(),
			Slug:             slug,
			Name:             name,
			Description:      desc,
			WebURL:           webURL,
			MCPURL:           mcpURL,
			SigningSecret:    secret,
			Enabled:          enabled,
			IsPublished:      true,
			PublishedVersion: 1,
			ReviewStatus:     domain.ReviewStatusApproved,
			DraftName:        name,
			DraftDescription: desc,
			DraftWebURL:      webURL,
			DraftMCPURL:      mcpURL,
			HasDraftChanges:  false,
		})
		return insErr
	})
	if err != nil {
		return domain.MarketplaceApp{}, "", err
	}
	return s.enrichMarketplaceApps(ctx, []domain.MarketplaceApp{created})[0], secret, nil
}

// UpdateAdminMarketplaceApp updates an existing marketplace app for platform admins.
func (s *Service) UpdateAdminMarketplaceApp(ctx context.Context, actor domain.User, appID string, in domain.UpdateMarketplaceAppInput) (domain.MarketplaceApp, error) {
	if err := requirePlatformAdmin(actor); err != nil {
		return domain.MarketplaceApp{}, err
	}
	if err := s.requireFlag("im.marketplace.enabled"); err != nil {
		return domain.MarketplaceApp{}, err
	}
	var updated domain.MarketplaceApp
	err := s.Tx.WithinTx(ctx, func(txCtx context.Context) error {
		cur, getErr := s.Marketplace.GetMarketplaceAppByID(txCtx, strings.TrimSpace(appID), true)
		if getErr != nil {
			return getErr
		}
		if in.Slug != nil {
			slug := strings.ToLower(strings.TrimSpace(*in.Slug))
			if !domain.ValidMarketplaceSlug(slug) {
				return domain.ErrInvalidMarketplaceSlug()
			}
			cur.Slug = slug
		}
		if in.Name != nil {
			name := strings.TrimSpace(*in.Name)
			if n := utf8.RuneCountInString(name); n < 1 || n > 80 {
				return domain.ErrInvalidMarketplaceName()
			}
			cur.Name = name
			if cur.IsOfficial() {
				cur.DraftName = name
			}
		}
		if in.Description != nil {
			desc := strings.TrimSpace(*in.Description)
			if utf8.RuneCountInString(desc) > 500 {
				return domain.ErrInvalidMarketplaceDescription()
			}
			cur.Description = desc
			if cur.IsOfficial() {
				cur.DraftDescription = desc
			}
		}
		if in.WebURL != nil {
			webURL, _, vErr := s.validateMarketplaceURLs(*in.WebURL, cur.MCPURL, cur.MCPURL == "")
			if vErr != nil {
				return vErr
			}
			cur.WebURL = webURL
			if cur.IsOfficial() {
				cur.DraftWebURL = webURL
			}
		}
		if in.MCPURL != nil {
			_, mcpURL, vErr := s.validateMarketplaceURLs(cur.WebURL, *in.MCPURL, cur.WebURL == "")
			if vErr != nil {
				return vErr
			}
			cur.MCPURL = mcpURL
			if cur.IsOfficial() {
				cur.DraftMCPURL = mcpURL
			}
		}
		if in.Enabled != nil {
			cur.Enabled = *in.Enabled
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

// RotateAdminMarketplaceAppSecret generates a new signingSecret for an app.
func (s *Service) RotateAdminMarketplaceAppSecret(ctx context.Context, actor domain.User, appID string) (domain.MarketplaceApp, string, error) {
	if err := requirePlatformAdmin(actor); err != nil {
		return domain.MarketplaceApp{}, "", err
	}
	if err := s.requireFlag("im.marketplace.enabled"); err != nil {
		return domain.MarketplaceApp{}, "", err
	}
	secret := domain.NewMarketplaceSigningSecret()
	var updated domain.MarketplaceApp
	err := s.Tx.WithinTx(ctx, func(txCtx context.Context) error {
		var updErr error
		updated, updErr = s.Marketplace.UpdateMarketplaceSigningSecret(txCtx, strings.TrimSpace(appID), secret)
		return updErr
	})
	if err != nil {
		return domain.MarketplaceApp{}, "", err
	}
	return s.enrichMarketplaceApps(ctx, []domain.MarketplaceApp{updated})[0], secret, nil
}

// DeleteAdminMarketplaceApp removes a marketplace app and cascades workspace installations.
func (s *Service) DeleteAdminMarketplaceApp(ctx context.Context, actor domain.User, appID string) error {
	if err := requirePlatformAdmin(actor); err != nil {
		return err
	}
	if err := s.requireFlag("im.marketplace.enabled"); err != nil {
		return err
	}
	var deleted bool
	err := s.Tx.WithinTx(ctx, func(txCtx context.Context) error {
		var delErr error
		deleted, delErr = s.Marketplace.DeleteMarketplaceApp(txCtx, strings.TrimSpace(appID))
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

// ListMarketplaceCatalog lists approved, enabled, non-suspended marketplace apps to any authenticated human user.
func (s *Service) ListMarketplaceCatalog(ctx context.Context, actor domain.User) ([]domain.MarketplaceApp, error) {
	if actor.Kind != "human" {
		return nil, domain.ErrHumanWorkspaceManagementRequired()
	}
	apps, err := s.Marketplace.ListPublishedCatalogApps(ctx)
	if err != nil {
		return nil, err
	}
	return s.enrichMarketplaceApps(ctx, apps), nil
}

// ListWorkspaceApps lists active marketplace apps installed in a workspace for a current workspace member.
func (s *Service) ListWorkspaceApps(ctx context.Context, actor domain.User, workspaceID string) ([]domain.MarketplaceApp, error) {
	var raw []domain.InstalledWorkspaceMarketplaceApp
	err := s.Tx.WithinTx(ctx, func(txCtx context.Context) error {
		if err := s.Workspaces.WorkspaceLock(txCtx, workspaceID, actor.ID, false, false); err != nil {
			return err
		}
		var listErr error
		raw, listErr = s.Marketplace.ListWorkspaceMarketplaceApps(txCtx, workspaceID)
		return listErr
	})
	if err != nil {
		return nil, err
	}
	resolved := make([]domain.MarketplaceApp, 0, len(raw))
	for _, item := range raw {
		if app, ok := item.App.ResolveForWorkspace(item.WorkspaceOwnerID, item.ReviewPreview); ok {
			resolved = append(resolved, app)
		}
	}
	return s.enrichMarketplaceApps(ctx, resolved), nil
}

// InstallWorkspaceApp installs a published catalog app (or a developer's own draft / admin's pending-review preview) into an owned workspace.
func (s *Service) InstallWorkspaceApp(ctx context.Context, actor domain.User, workspaceID, appID string) (domain.MarketplaceApp, error) {
	if actor.Kind != "human" {
		return domain.MarketplaceApp{}, domain.ErrHumanWorkspaceManagementRequired()
	}
	if err := s.requireFlag("im.marketplace.enabled"); err != nil {
		return domain.MarketplaceApp{}, err
	}
	var installed domain.MarketplaceApp
	err := s.Tx.WithinTx(ctx, func(txCtx context.Context) error {
		if err := s.Workspaces.WorkspaceLock(txCtx, workspaceID, actor.ID, true, true); err != nil {
			return err
		}
		app, getErr := s.Marketplace.GetMarketplaceAppByID(txCtx, strings.TrimSpace(appID), false)
		if getErr != nil {
			return domain.ErrMarketplaceAppUnavailable()
		}
		reviewPreview := actor.PlatformRole == "admin" && !app.IsOfficial() && (app.OwnerID == nil || *app.OwnerID != actor.ID) && app.ReviewStatus == domain.ReviewStatusPending
		resolved, ok := app.ResolveForWorkspace(actor.ID, reviewPreview)
		if !ok {
			return domain.ErrMarketplaceAppUnavailable()
		}
		if err := s.Marketplace.InstallWorkspaceMarketplaceApp(txCtx, workspaceID, app.ID, actor.ID, reviewPreview); err != nil {
			return err
		}
		installed = resolved
		return nil
	})
	if err != nil {
		return domain.MarketplaceApp{}, err
	}
	return s.enrichMarketplaceApps(ctx, []domain.MarketplaceApp{installed})[0], nil
}

// UninstallWorkspaceApp removes an app installation from a workspace. Only the human workspace owner can uninstall apps.
func (s *Service) UninstallWorkspaceApp(ctx context.Context, actor domain.User, workspaceID, appID string) error {
	if actor.Kind != "human" {
		return domain.ErrHumanWorkspaceManagementRequired()
	}
	if err := s.requireFlag("im.marketplace.enabled"); err != nil {
		return err
	}
	return s.Tx.WithinTx(ctx, func(txCtx context.Context) error {
		if err := s.Workspaces.WorkspaceLock(txCtx, workspaceID, actor.ID, true, true); err != nil {
			return err
		}
		return s.Marketplace.UninstallWorkspaceMarketplaceApp(txCtx, workspaceID, strings.TrimSpace(appID))
	})
}

// ListConversationApps returns the workspace-installed marketplace apps available in a group conversation.
func (s *Service) ListConversationApps(ctx context.Context, actor domain.User, conversationID string) ([]domain.MarketplaceApp, error) {
	var raw []domain.InstalledWorkspaceMarketplaceApp
	err := s.Tx.WithinTx(ctx, func(txCtx context.Context) error {
		wsID, err := s.Conversations.GetConversationWorkspaceID(txCtx, conversationID)
		if err != nil {
			return err
		}
		if err = s.Workspaces.WorkspaceLock(txCtx, wsID, actor.ID, false, false); err != nil {
			return err
		}
		if err = s.Conversations.LockConversationForShare(txCtx, conversationID); err != nil {
			return err
		}
		c, err := s.Conversations.ConversationAccess(txCtx, conversationID, actor.ID)
		if err != nil {
			return err
		}
		if c.Type != "group" {
			return domain.ErrMarketplaceGroupOnly()
		}
		raw, err = s.Marketplace.ListWorkspaceMarketplaceApps(txCtx, c.WorkspaceID)
		return err
	})
	if err != nil {
		return nil, err
	}
	resolved := make([]domain.MarketplaceApp, 0, len(raw))
	for _, item := range raw {
		if app, ok := item.App.ResolveForWorkspace(item.WorkspaceOwnerID, item.ReviewPreview); ok {
			resolved = append(resolved, app)
		}
	}
	return s.enrichMarketplaceApps(ctx, resolved), nil
}

// CreateConversationAppSession issues a short-lived symmetric HS256 Context Token for a human or Agent member of a group conversation.
func (s *Service) CreateConversationAppSession(ctx context.Context, actor domain.User, conversationID, appID string) (domain.ConversationAppSession, error) {
	if err := s.requireFlag("im.marketplace.enabled"); err != nil {
		return domain.ConversationAppSession{}, err
	}
	var session domain.ConversationAppSession
	err := s.Tx.WithinTx(ctx, func(txCtx context.Context) error {
		wsID, err := s.Conversations.GetConversationWorkspaceID(txCtx, conversationID)
		if err != nil {
			return err
		}
		if err = s.Workspaces.WorkspaceLock(txCtx, wsID, actor.ID, false, false); err != nil {
			return err
		}
		if err = s.Conversations.LockConversationForShare(txCtx, conversationID); err != nil {
			return err
		}
		c, err := s.Conversations.ConversationAccess(txCtx, conversationID, actor.ID)
		if err != nil {
			return err
		}
		if c.Type != "group" {
			return domain.ErrMarketplaceGroupOnly()
		}
		installed, err := s.Marketplace.GetInstalledWorkspaceMarketplaceAppForSession(txCtx, c.WorkspaceID, strings.TrimSpace(appID))
		if err != nil {
			return err
		}
		resolved, ok := installed.App.ResolveForWorkspace(installed.WorkspaceOwnerID, installed.ReviewPreview)
		if !ok {
			return domain.ErrMarketplaceAppUnavailable()
		}
		now := time.Now().UTC()
		exp := now.Add(domain.MarketplaceSessionTTL)
		token, err := domain.SignMarketplaceContextToken(installed.App.SigningSecret, domain.MarketplaceContextClaims{
			Issuer:         "teamie",
			Audience:       resolved.ID,
			Subject:        actor.ID,
			AppID:          resolved.ID,
			Slug:           resolved.Slug,
			WorkspaceID:    c.WorkspaceID,
			ConversationID: c.ID,
			UserID:         actor.ID,
			DisplayName:    actor.DisplayName,
			IssuedAt:       now.Unix(),
			ExpiresAt:      exp.Unix(),
		})
		if err != nil {
			return err
		}
		resolved.SigningSecret = ""
		session = domain.ConversationAppSession{
			App:       resolved,
			Token:     token,
			ExpiresAt: exp,
		}
		return nil
	})
	if err != nil {
		return domain.ConversationAppSession{}, err
	}
	session.App = s.enrichMarketplaceApps(ctx, []domain.MarketplaceApp{session.App})[0]
	return session, nil
}
