package app

import (
	"context"
	"strings"
	"time"

	"teamie/services/chat/internal/domain"
)

func requirePlatformAdmin(actor domain.User) error {
	if actor.Kind != "human" || actor.PlatformRole != "admin" {
		return domain.ErrPlatformAdminRequired()
	}
	return nil
}

// ListAdminMarketplaceApps returns all catalog apps (enabled and disabled) for a platform admin.
func (s *Service) ListAdminMarketplaceApps(ctx context.Context, actor domain.User) ([]domain.MarketplaceApp, error) {
	if err := requirePlatformAdmin(actor); err != nil {
		return nil, err
	}
	return s.Marketplace.ListAllMarketplaceApps(ctx)
}

// CreateAdminMarketplaceApp registers a new Web + MCP app in the platform catalog and returns its one-time signing secret.
func (s *Service) CreateAdminMarketplaceApp(ctx context.Context, actor domain.User, in domain.CreateMarketplaceAppInput) (domain.MarketplaceApp, string, error) {
	if err := requirePlatformAdmin(actor); err != nil {
		return domain.MarketplaceApp{}, "", err
	}
	if err := s.requireFlag("im.marketplace.enabled"); err != nil {
		return domain.MarketplaceApp{}, "", err
	}
	slug := strings.TrimSpace(in.Slug)
	if !domain.ValidMarketplaceSlug(slug) {
		return domain.MarketplaceApp{}, "", domain.ErrInvalidMarketplaceSlug()
	}
	name := strings.TrimSpace(in.Name)
	if !domain.BoundedString(name, 1, 80) {
		return domain.MarketplaceApp{}, "", domain.ErrInvalidMarketplaceName()
	}
	desc := strings.TrimSpace(in.Description)
	if len([]rune(desc)) > 500 {
		return domain.MarketplaceApp{}, "", domain.ErrInvalidMarketplaceDescription()
	}
	webURL := strings.TrimSpace(in.WebURL)
	mcpURL := strings.TrimSpace(in.MCPURL)
	if !domain.ValidMarketplaceHTTPURL(webURL) || !domain.ValidMarketplaceHTTPURL(mcpURL) {
		return domain.MarketplaceApp{}, "", domain.ErrInvalidMarketplaceURL()
	}
	enabled := true
	if in.Enabled != nil {
		enabled = *in.Enabled
	}
	secret := domain.NewMarketplaceSigningSecret()
	app := domain.MarketplaceApp{
		ID:            domain.NewID(),
		Slug:          slug,
		Name:          name,
		Description:   desc,
		WebURL:        webURL,
		MCPURL:        mcpURL,
		SigningSecret: secret,
		Enabled:       enabled,
	}
	created, err := s.Marketplace.InsertMarketplaceApp(ctx, app)
	if err != nil {
		return domain.MarketplaceApp{}, "", err
	}
	return created, secret, nil
}

// UpdateAdminMarketplaceApp updates an existing platform marketplace app.
func (s *Service) UpdateAdminMarketplaceApp(ctx context.Context, actor domain.User, id string, in domain.UpdateMarketplaceAppInput) (domain.MarketplaceApp, error) {
	if err := requirePlatformAdmin(actor); err != nil {
		return domain.MarketplaceApp{}, err
	}
	if err := s.requireFlag("im.marketplace.enabled"); err != nil {
		return domain.MarketplaceApp{}, err
	}
	var updated domain.MarketplaceApp
	err := s.Tx.WithinTx(ctx, func(txCtx context.Context) error {
		current, getErr := s.Marketplace.GetMarketplaceAppByID(txCtx, strings.TrimSpace(id), true)
		if getErr != nil {
			return getErr
		}
		if in.Slug != nil {
			slug := strings.TrimSpace(*in.Slug)
			if !domain.ValidMarketplaceSlug(slug) {
				return domain.ErrInvalidMarketplaceSlug()
			}
			current.Slug = slug
		}
		if in.Name != nil {
			name := strings.TrimSpace(*in.Name)
			if !domain.BoundedString(name, 1, 80) {
				return domain.ErrInvalidMarketplaceName()
			}
			current.Name = name
		}
		if in.Description != nil {
			desc := strings.TrimSpace(*in.Description)
			if len([]rune(desc)) > 500 {
				return domain.ErrInvalidMarketplaceDescription()
			}
			current.Description = desc
		}
		if in.WebURL != nil {
			webURL := strings.TrimSpace(*in.WebURL)
			if !domain.ValidMarketplaceHTTPURL(webURL) {
				return domain.ErrInvalidMarketplaceURL()
			}
			current.WebURL = webURL
		}
		if in.MCPURL != nil {
			mcpURL := strings.TrimSpace(*in.MCPURL)
			if !domain.ValidMarketplaceHTTPURL(mcpURL) {
				return domain.ErrInvalidMarketplaceURL()
			}
			current.MCPURL = mcpURL
		}
		if in.Enabled != nil {
			current.Enabled = *in.Enabled
		}
		var updErr error
		updated, updErr = s.Marketplace.UpdateMarketplaceApp(txCtx, current)
		return updErr
	})
	return updated, err
}

// RotateAdminMarketplaceAppSecret rotates the HMAC signing secret for a platform marketplace app.
func (s *Service) RotateAdminMarketplaceAppSecret(ctx context.Context, actor domain.User, id string) (domain.MarketplaceApp, string, error) {
	if err := requirePlatformAdmin(actor); err != nil {
		return domain.MarketplaceApp{}, "", err
	}
	if err := s.requireFlag("im.marketplace.enabled"); err != nil {
		return domain.MarketplaceApp{}, "", err
	}
	var updated domain.MarketplaceApp
	var secret string
	err := s.Tx.WithinTx(ctx, func(txCtx context.Context) error {
		current, getErr := s.Marketplace.GetMarketplaceAppByID(txCtx, strings.TrimSpace(id), true)
		if getErr != nil {
			return getErr
		}
		secret = domain.NewMarketplaceSigningSecret()
		current.SigningSecret = secret
		var updErr error
		updated, updErr = s.Marketplace.UpdateMarketplaceApp(txCtx, current)
		return updErr
	})
	return updated, secret, err
}

// DeleteAdminMarketplaceApp deletes a marketplace app from the platform catalog (and cascades workspace installations).
func (s *Service) DeleteAdminMarketplaceApp(ctx context.Context, actor domain.User, id string) error {
	if err := requirePlatformAdmin(actor); err != nil {
		return err
	}
	if err := s.requireFlag("im.marketplace.enabled"); err != nil {
		return err
	}
	deleted, err := s.Marketplace.DeleteMarketplaceApp(ctx, strings.TrimSpace(id))
	if err != nil {
		return err
	}
	if !deleted {
		return domain.ErrMarketplaceAppUnavailable()
	}
	return nil
}

// ListMarketplaceCatalog lists all enabled marketplace apps for human users browsing the catalog.
func (s *Service) ListMarketplaceCatalog(ctx context.Context, actor domain.User) ([]domain.MarketplaceApp, error) {
	if actor.Kind != "human" {
		return nil, domain.ErrHumanWorkspaceManagementRequired()
	}
	return s.Marketplace.ListEnabledMarketplaceApps(ctx)
}

// ListWorkspaceApps lists enabled marketplace apps installed in a workspace for a current workspace member.
func (s *Service) ListWorkspaceApps(ctx context.Context, actor domain.User, workspaceID string) ([]domain.MarketplaceApp, error) {
	var out []domain.MarketplaceApp
	err := s.Tx.WithinTx(ctx, func(txCtx context.Context) error {
		if err := s.Workspaces.WorkspaceLock(txCtx, workspaceID, actor.ID, false, false); err != nil {
			return err
		}
		var listErr error
		out, listErr = s.Marketplace.ListWorkspaceMarketplaceApps(txCtx, workspaceID)
		return listErr
	})
	return out, err
}

// InstallWorkspaceApp installs an enabled catalog app into a workspace. Only the human workspace owner can install apps.
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
		if getErr != nil || !app.Enabled {
			return domain.ErrMarketplaceAppUnavailable()
		}
		if err := s.Marketplace.InstallWorkspaceMarketplaceApp(txCtx, workspaceID, app.ID, actor.ID); err != nil {
			return err
		}
		installed = app
		return nil
	})
	return installed, err
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
// Returns 403 marketplace_group_only if the conversation is a channel or direct message.
func (s *Service) ListConversationApps(ctx context.Context, actor domain.User, conversationID string) ([]domain.MarketplaceApp, error) {
	var out []domain.MarketplaceApp
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
		out, err = s.Marketplace.ListWorkspaceMarketplaceApps(txCtx, c.WorkspaceID)
		return err
	})
	return out, err
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
		app, err := s.Marketplace.GetInstalledWorkspaceMarketplaceApp(txCtx, c.WorkspaceID, strings.TrimSpace(appID))
		if err != nil {
			return err
		}
		now := time.Now().UTC()
		exp := now.Add(domain.MarketplaceSessionTTL)
		token, err := domain.SignMarketplaceContextToken(app.SigningSecret, domain.MarketplaceContextClaims{
			Issuer:         "teamie",
			Audience:       app.ID,
			Subject:        actor.ID,
			AppID:          app.ID,
			Slug:           app.Slug,
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
		session = domain.ConversationAppSession{
			App:       app,
			Token:     token,
			ExpiresAt: exp,
		}
		return nil
	})
	return session, err
}
