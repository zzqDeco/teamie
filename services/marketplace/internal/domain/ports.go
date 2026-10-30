package domain

import (
	"context"
	"time"
)

// TxManager coordinates atomic transactions and transaction-scoped advisory locks
// without exposing SQL driver types to the domain or application layers.
type TxManager interface {
	WithinTx(ctx context.Context, fn func(ctx context.Context) error) error
	LockDeveloperQuota(ctx context.Context, ownerID string) error
	Ping(ctx context.Context) error
}

// FeatureFlags exposes runtime feature toggles to the Marketplace application layer.
type FeatureFlags interface {
	Enabled(key string) bool
}

// IdentityDirectory resolves participant profiles across the Identity boundary.
type IdentityDirectory interface {
	Users(ctx context.Context, ids []string) ([]User, error)
	Ping(ctx context.Context) error
}

// ChatScope queries workspace membership/ownership and conversation scope across the Chat RPC boundary.
type ChatScope interface {
	WorkspaceRole(ctx context.Context, workspaceID, userID string) (WorkspaceScope, error)
	ConversationScope(ctx context.Context, conversationID, userID string) (ConversationScope, error)
	Ping(ctx context.Context) error
}

// InstalledWorkspaceMarketplaceApp binds a MarketplaceApp with workspace-level installation flags.
type InstalledWorkspaceMarketplaceApp struct {
	App           MarketplaceApp
	ReviewPreview bool
}

// VerifyEndpointsInput holds parameters for actively probing a deployed WebUI + MCP service.
type VerifyEndpointsInput struct {
	AppID         string
	Slug          string
	WebURL        string
	MCPURL        string
	SigningSecret string
}

// VerificationSaveFence binds a verification report write to the exact draft/live
// configuration and signing secret that were probed so concurrent secret rotation,
// draft edits, or administrative suspension cannot be overwritten by stale probes.
type VerificationSaveFence struct {
	Slug            string
	DraftWebURL     string
	DraftMCPURL     string
	WebURL          string
	MCPURL          string
	SigningSecret   string
	IgnoreSuspended bool
}

// MarketplaceEndpointVerifier probes a developer's deployed Web UI and MCP Server against the platform protocol.
type MarketplaceEndpointVerifier interface {
	VerifyEndpoints(ctx context.Context, input VerifyEndpointsInput) (MarketplaceEndpointVerification, error)
}

// MarketplaceRepository defines persistence operations for platform marketplace apps and workspace installations.
type MarketplaceRepository interface {
	ListAllMarketplaceApps(ctx context.Context) ([]MarketplaceApp, error)
	ListPublishedCatalogApps(ctx context.Context) ([]MarketplaceApp, error)
	ListDeveloperMarketplaceApps(ctx context.Context, ownerID string) ([]MarketplaceApp, error)
	CountDeveloperMarketplaceApps(ctx context.Context, ownerID string) (int, error)
	GetMarketplaceAppByID(ctx context.Context, id string, forUpdate bool) (MarketplaceApp, error)
	GetMarketplaceAppSigningSecret(ctx context.Context, id string) (string, error)
	InsertMarketplaceApp(ctx context.Context, app MarketplaceApp) (MarketplaceApp, error)
	UpdateMarketplaceApp(ctx context.Context, app MarketplaceApp) (MarketplaceApp, error)
	UpdateMarketplaceSigningSecret(ctx context.Context, id, secret string) (MarketplaceApp, error)
	SaveMarketplaceVerificationReport(ctx context.Context, id string, verifiedAt time.Time, report MarketplaceEndpointVerification, fence VerificationSaveFence) (MarketplaceApp, error)
	DeleteMarketplaceApp(ctx context.Context, id string) (bool, error)
	InsertMarketplaceAppReview(ctx context.Context, review MarketplaceAppReview, snapshot any) error
	ListMarketplaceAppReviews(ctx context.Context, appID string) ([]MarketplaceAppReview, error)
	ListWorkspaceMarketplaceApps(ctx context.Context, workspaceID string) ([]InstalledWorkspaceMarketplaceApp, error)
	InstallWorkspaceMarketplaceApp(ctx context.Context, workspaceID, appID, installedBy string, reviewPreview bool) error
	UninstallWorkspaceMarketplaceApp(ctx context.Context, workspaceID, appID string) error
	GetInstalledWorkspaceMarketplaceAppForSession(ctx context.Context, workspaceID, appID string) (InstalledWorkspaceMarketplaceApp, error)
}
