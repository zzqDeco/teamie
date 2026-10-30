// Package app orchestrates Marketplace, Developer Platform, and Admin Governance
// use cases using only Go standard library packages and the Marketplace domain layer.
package app

import (
	"context"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"teamie/services/marketplace/internal/domain"
)

const marketplaceFeatureFlag = "im.marketplace.enabled"

// Service coordinates Marketplace catalog, developer self-service, protocol probing,
// platform-admin review/governance, workspace installations, and group context tokens.
type Service struct {
	Tx                 domain.TxManager
	Marketplace        domain.MarketplaceRepository
	Identity           domain.IdentityDirectory
	Chat               domain.ChatScope
	Flags              domain.FeatureFlags
	Verifier           domain.MarketplaceEndpointVerifier
	AppOrigin          string
	RequirePublicHTTPS *bool
	VerifyCooldown     time.Duration
	Now                func() time.Time

	cooldownMu sync.Mutex
	lastVerify map[string]time.Time
}

func (s *Service) nowUTC() time.Time {
	if s != nil && s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

func (s *Service) flagEnabled(key string) bool {
	if s == nil || s.Flags == nil {
		return true
	}
	return s.Flags.Enabled(key)
}

func (s *Service) requireMarketplaceEnabled() error {
	if !s.flagEnabled(marketplaceFeatureFlag) {
		return domain.ErrFeatureDisabled()
	}
	return nil
}

func (s *Service) requirePublicHTTPS() bool {
	if s != nil && s.RequirePublicHTTPS != nil {
		return *s.RequirePublicHTTPS
	}
	if s == nil {
		return false
	}
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(s.AppOrigin)), "https://")
}

// CheckDependencies verifies PostgreSQL, Identity RPC, and Chat RPC readiness.
func (s *Service) CheckDependencies(ctx context.Context) error {
	if s.Tx != nil {
		if err := s.Tx.Ping(ctx); err != nil {
			return err
		}
	}
	if s.Identity != nil {
		if err := s.Identity.Ping(ctx); err != nil {
			return err
		}
	}
	if s.Chat != nil {
		if err := s.Chat.Ping(ctx); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) acquireVerifySlot(app domain.MarketplaceApp) error {
	if s == nil || s.VerifyCooldown <= 0 || app.ID == "" {
		return nil
	}
	now := s.nowUTC()
	if app.LastVerifiedAt != nil && now.Sub(*app.LastVerifiedAt) < s.VerifyCooldown {
		return domain.ErrMarketplaceVerifyRateLimited()
	}
	s.cooldownMu.Lock()
	defer s.cooldownMu.Unlock()
	if s.lastVerify == nil {
		s.lastVerify = make(map[string]time.Time)
	}
	if prev, ok := s.lastVerify[app.ID]; ok && now.Sub(prev) < s.VerifyCooldown {
		return domain.ErrMarketplaceVerifyRateLimited()
	}
	s.lastVerify[app.ID] = now
	return nil
}

func normalizeSlug(raw string) (string, error) {
	slug := strings.ToLower(strings.TrimSpace(raw))
	if !domain.ValidMarketplaceSlug(slug) {
		return "", domain.ErrInvalidMarketplaceSlug()
	}
	return slug, nil
}

func normalizeName(raw string) (string, error) {
	name := strings.TrimSpace(raw)
	n := utf8.RuneCountInString(name)
	if n < 1 || n > 80 {
		return "", domain.ErrInvalidMarketplaceName()
	}
	return name, nil
}

func normalizeDescription(raw string) (string, error) {
	desc := strings.TrimSpace(raw)
	if utf8.RuneCountInString(desc) > 500 {
		return "", domain.ErrInvalidMarketplaceDescription()
	}
	return desc, nil
}

func normalizeNote(raw string, required bool) (string, error) {
	note := strings.TrimSpace(raw)
	if utf8.RuneCountInString(note) > 1000 {
		return "", domain.ErrMarketplaceNoteLength()
	}
	if required && note == "" {
		return "", domain.ErrMarketplaceNoteRequired()
	}
	return note, nil
}

func normalizeSuspendReason(raw string) (string, error) {
	reason := strings.TrimSpace(raw)
	if utf8.RuneCountInString(reason) > 500 {
		return "", domain.ErrMarketplaceNoteLength()
	}
	if reason == "" {
		return "", domain.ErrMarketplaceNoteRequired()
	}
	return reason, nil
}

func (s *Service) validateEndpoints(webURL, mcpURL string, allowEmpty bool) (string, string, error) {
	web := strings.TrimSpace(webURL)
	mcp := strings.TrimSpace(mcpURL)
	reqPublic := s.requirePublicHTTPS()
	if web != "" || !allowEmpty {
		if err := domain.ValidateMarketplaceEndpointURL(web, false, s.AppOrigin, reqPublic); err != nil {
			return "", "", err
		}
	}
	if mcp != "" || !allowEmpty {
		if err := domain.ValidateMarketplaceEndpointURL(mcp, true, s.AppOrigin, reqPublic); err != nil {
			return "", "", err
		}
	}
	return web, mcp, nil
}

func computeHasDraftChanges(app domain.MarketplaceApp) bool {
	return !app.IsPublished ||
		app.DraftName != app.Name ||
		app.DraftDescription != app.Description ||
		app.DraftWebURL != app.WebURL ||
		app.DraftMCPURL != app.MCPURL
}

func (s *Service) enrichApps(ctx context.Context, apps []domain.MarketplaceApp) ([]domain.MarketplaceApp, error) {
	if len(apps) == 0 {
		return apps, nil
	}
	var ownerIDs []string
	seen := make(map[string]struct{})
	for _, a := range apps {
		if a.OwnerID != nil && strings.TrimSpace(*a.OwnerID) != "" {
			id := strings.TrimSpace(*a.OwnerID)
			if _, ok := seen[id]; !ok {
				seen[id] = struct{}{}
				ownerIDs = append(ownerIDs, id)
			}
		}
	}
	names := make(map[string]string, len(ownerIDs))
	if len(ownerIDs) > 0 && s.Identity != nil {
		users, err := s.Identity.Users(ctx, ownerIDs)
		if err != nil {
			return nil, err
		}
		for _, u := range users {
			if strings.TrimSpace(u.DisplayName) != "" {
				names[u.ID] = u.DisplayName
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
			disp := names[ownerID]
			if disp == "" {
				disp = "Teamie 开发者"
			}
			a.OwnerDisplayName = disp
			a.PublisherName = disp
		}
		out[i] = a
	}
	return out, nil
}

func (s *Service) enrichApp(ctx context.Context, app domain.MarketplaceApp) (domain.MarketplaceApp, error) {
	enriched, err := s.enrichApps(ctx, []domain.MarketplaceApp{app})
	if err != nil {
		return domain.MarketplaceApp{}, err
	}
	return enriched[0], nil
}

func (s *Service) enrichReviews(ctx context.Context, reviews []domain.MarketplaceAppReview) ([]domain.MarketplaceAppReview, error) {
	if len(reviews) == 0 {
		return reviews, nil
	}
	var actorIDs []string
	seen := make(map[string]struct{})
	for _, r := range reviews {
		if r.ActorID != "" {
			if _, ok := seen[r.ActorID]; !ok {
				seen[r.ActorID] = struct{}{}
				actorIDs = append(actorIDs, r.ActorID)
			}
		}
	}
	names := make(map[string]string, len(actorIDs))
	if len(actorIDs) > 0 && s.Identity != nil {
		users, err := s.Identity.Users(ctx, actorIDs)
		if err != nil {
			return nil, err
		}
		for _, u := range users {
			names[u.ID] = u.DisplayName
		}
	}
	out := make([]domain.MarketplaceAppReview, len(reviews))
	for i, r := range reviews {
		r.ActorName = names[r.ActorID]
		out[i] = r
	}
	return out, nil
}

func reviewSnapshot(app domain.MarketplaceApp) map[string]any {
	snap := map[string]any{
		"slug":               app.Slug,
		"name":               app.Name,
		"description":        app.Description,
		"webUrl":             app.WebURL,
		"mcpUrl":             app.MCPURL,
		"draftName":          app.DraftName,
		"draftDescription":   app.DraftDescription,
		"draftWebUrl":        app.DraftWebURL,
		"draftMcpUrl":        app.DraftMCPURL,
		"isPublished":        app.IsPublished,
		"publishedVersion":   app.PublishedVersion,
		"reviewStatus":       app.ReviewStatus,
		"submissionRevision": app.SubmissionRevision,
		"adminSuspended":     app.AdminSuspended,
		"suspendedReason":    app.SuspendedReason,
	}
	if app.VerificationReport != nil {
		snap["verification"] = *app.VerificationReport
	}
	return snap
}
