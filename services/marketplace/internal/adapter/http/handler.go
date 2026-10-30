// Package http implements the inbound HTTP adapter for the Marketplace service,
// binding generated Thrift HTTP DTOs (teamie/contracts/gen/im/hertz_gen/im/marketplace)
// to the Marketplace application layer.
package http

import (
	"context"
	"net/http"
	"time"

	hertzapp "github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/common/adaptor"
	"github.com/cloudwego/hertz/pkg/route"
	marketplacegen "teamie/contracts/gen/im/hertz_gen/im/marketplace"
	"teamie/pkg/httpx"
	"teamie/pkg/observability"
	"teamie/services/marketplace/internal/app"
	"teamie/services/marketplace/internal/domain"
)

type actorContextKey struct{}

// WithActor attaches the authenticated domain.User to ctx.
func WithActor(ctx context.Context, u domain.User) context.Context {
	return context.WithValue(ctx, actorContextKey{}, u)
}

func actorFromContext(ctx context.Context) (domain.User, bool) {
	u, ok := ctx.Value(actorContextKey{}).(domain.User)
	return u, ok
}

// Adapter implements marketplacegen.MarketplaceHTTPService and mounts Hertz/Mux routes.
type Adapter struct {
	App    *app.Service
	Auth   httpx.Identity
	Origin string
}

var _ marketplacegen.MarketplaceHTTPService = (*Adapter)(nil)

func formatOptTime(t *time.Time) *string {
	if t == nil || t.IsZero() {
		return nil
	}
	v := t.UTC().Format(time.RFC3339Nano)
	return &v
}

func optString(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func toMarketplaceAppDTO(a domain.MarketplaceApp) *marketplacegen.MarketplaceAppDTO {
	isOfficial := a.IsOfficial()
	servingDraft := a.ServingDraft
	pubVer := int32(a.PublishedVersion)
	return &marketplacegen.MarketplaceAppDTO{
		ID:               a.ID,
		Slug:             a.Slug,
		Name:             a.Name,
		Description:      a.Description,
		WebUrl:           a.WebURL,
		McpUrl:           a.MCPURL,
		Enabled:          a.Enabled,
		CreatedAt:        a.CreatedAt.UTC().Format(time.RFC3339Nano),
		UpdatedAt:        a.UpdatedAt.UTC().Format(time.RFC3339Nano),
		PublisherName:    optString(a.PublisherName),
		IsOfficial:       &isOfficial,
		ServingDraft:     &servingDraft,
		PublishedVersion: &pubVer,
	}
}

func toVerificationDTO(v *domain.MarketplaceEndpointVerification) *marketplacegen.MarketplaceEndpointVerificationDTO {
	if v == nil {
		return nil
	}
	checks := make([]*marketplacegen.EndpointProbeCheckDTO, 0, len(v.Checks))
	for _, c := range v.Checks {
		checks = append(checks, &marketplacegen.EndpointProbeCheckDTO{
			Stage:   c.Stage,
			Ok:      c.OK,
			Code:    c.Code,
			Summary: c.Summary,
		})
	}
	tools := make([]*marketplacegen.DiscoveredMCPToolDTO, 0, len(v.Tools))
	for _, t := range v.Tools {
		tools = append(tools, &marketplacegen.DiscoveredMCPToolDTO{
			Name:            t.Name,
			ExposedName:     t.ExposedName,
			Description:     t.Description,
			InputSchemaJson: optString(t.InputSchemaJSON),
		})
	}
	return &marketplacegen.MarketplaceEndpointVerificationDTO{
		Ok:                 v.OK,
		VerifiedAt:         v.VerifiedAt.UTC().Format(time.RFC3339Nano),
		WebUrl:             v.WebURL,
		McpUrl:             v.MCPURL,
		McpServerName:      optString(v.MCPServerName),
		McpServerVersion:   optString(v.MCPServerVersion),
		McpProtocolVersion: optString(v.MCPProtocolVersion),
		Checks:             checks,
		Tools:              tools,
	}
}

func toDeveloperMarketplaceAppDTO(a domain.MarketplaceApp) *marketplacegen.DeveloperMarketplaceAppDTO {
	var ownerID *string
	if a.OwnerID != nil && *a.OwnerID != "" {
		v := *a.OwnerID
		ownerID = &v
	}
	return &marketplacegen.DeveloperMarketplaceAppDTO{
		ID:                 a.ID,
		Slug:               a.Slug,
		Name:               a.Name,
		Description:        a.Description,
		WebUrl:             a.WebURL,
		McpUrl:             a.MCPURL,
		Enabled:            a.Enabled,
		CreatedAt:          a.CreatedAt.UTC().Format(time.RFC3339Nano),
		UpdatedAt:          a.UpdatedAt.UTC().Format(time.RFC3339Nano),
		OwnerId:            ownerID,
		OwnerDisplayName:   optString(a.OwnerDisplayName),
		IsOfficial:         a.IsOfficial(),
		IsPublished:        a.IsPublished,
		PublishedVersion:   int32(a.PublishedVersion),
		ReviewStatus:       a.ReviewStatus,
		DraftName:          a.DraftName,
		DraftDescription:   a.DraftDescription,
		DraftWebUrl:        a.DraftWebURL,
		DraftMcpUrl:        a.DraftMCPURL,
		HasDraftChanges:    a.HasDraftChanges,
		SubmissionRevision: int32(a.SubmissionRevision),
		SubmissionNote:     a.SubmissionNote,
		SubmittedAt:        formatOptTime(a.SubmittedAt),
		ReviewedAt:         formatOptTime(a.ReviewedAt),
		ReviewNote:         a.ReviewNote,
		AdminSuspended:     a.AdminSuspended,
		SuspendedReason:    a.SuspendedReason,
		LastVerifiedAt:     formatOptTime(a.LastVerifiedAt),
		VerificationReport: toVerificationDTO(a.VerificationReport),
	}
}

func toReviewDTOs(rs []domain.MarketplaceAppReview) []*marketplacegen.MarketplaceAppReviewDTO {
	out := make([]*marketplacegen.MarketplaceAppReviewDTO, 0, len(rs))
	for _, r := range rs {
		out = append(out, &marketplacegen.MarketplaceAppReviewDTO{
			ID:        r.ID,
			AppId:     r.AppID,
			Revision:  int32(r.Revision),
			Action:    r.Action,
			ActorId:   r.ActorID,
			ActorName: optString(r.ActorName),
			Note:      r.Note,
			WebUrl:    r.WebURL,
			McpUrl:    r.MCPURL,
			CreatedAt: r.CreatedAt.UTC().Format(time.RFC3339Nano),
		})
	}
	return out
}

type marketplaceResponder struct {
	httpx.Responder
	withAuth   func(next func(w http.ResponseWriter, r *http.Request, ctx context.Context, s httpx.Session) error) http.HandlerFunc
	muxHandler hertzapp.HandlerFunc
}

func (mr marketplaceResponder) HertzHandler(_ marketplacegen.RouteSpec) hertzapp.HandlerFunc {
	return mr.muxHandler
}

func (mr marketplaceResponder) WrapRoute(_ marketplacegen.RouteSpec, next http.HandlerFunc) http.HandlerFunc {
	return mr.withAuth(func(w http.ResponseWriter, r *http.Request, ctx context.Context, _ httpx.Session) error {
		next(w, r.WithContext(ctx))
		return nil
	})
}

// Routes mounts operational endpoints (/healthz, /readyz, /version) and all 26
// generated Marketplace HTTP routes with cookie/CSRF + service-principal auth.
func (a *Adapter) Routes() http.Handler {
	m := http.NewServeMux()
	httpx.Operations(m, func(r *http.Request) error {
		return a.App.CheckDependencies(r.Context())
	})

	withAuth := func(next func(w http.ResponseWriter, r *http.Request, ctx context.Context, s httpx.Session) error) http.HandlerFunc {
		return httpx.Auth(a.Auth, a.Origin, func(w http.ResponseWriter, r *http.Request, s httpx.Session) error {
			w.Header().Set("Cache-Control", "no-store")
			ctx := WithActor(r.Context(), domain.User{
				ID:           s.User.ID,
				Email:        s.User.Email,
				DisplayName:  s.User.DisplayName,
				Kind:         s.User.Kind,
				PlatformRole: s.User.PlatformRole,
				CreatedAt:    s.User.CreatedAt,
			})
			return next(w, r, ctx, s)
		})
	}

	resp := marketplaceResponder{
		withAuth:   withAuth,
		muxHandler: adaptor.HertzHandler(observability.HTTP(m)),
	}
	marketplacegen.RegisterMarketplaceHTTPServer(m, a, resp, marketplacegen.MarketplaceHTTPServiceRouteHandlers{})

	return httpx.WrapHertzRouter(m, func(r *route.RouterGroup) {
		marketplacegen.RegisterMarketplaceHTTPServiceHertzServer(r, a, resp)
	})
}
