// Package http implements the inbound HTTP adapter for the Push service,
// binding generated Thrift HTTP DTOs (teamie/contracts/gen/im/hertz_gen/im/push)
// to the Push application layer.
package http

import (
	"context"
	"net/http"
	"time"

	hertzapp "github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/common/adaptor"
	"github.com/cloudwego/hertz/pkg/route"
	pushgen "teamie/contracts/gen/im/hertz_gen/im/push"
	"teamie/pkg/httpx"
	"teamie/pkg/observability"
	"teamie/services/push/internal/app"
	"teamie/services/push/internal/domain"
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

// Adapter implements pushgen.PushHTTPService and mounts Hertz/Mux routes.
type Adapter struct {
	App    *app.Service
	Auth   httpx.Identity
	Origin string
}

var _ pushgen.PushHTTPService = (*Adapter)(nil)

func optTime(t *time.Time) *string {
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

func toSubscriptionDTO(s domain.PushSubscription) *pushgen.PushSubscriptionDTO {
	return &pushgen.PushSubscriptionDTO{
		ID:            s.ID,
		EndpointHash:  s.EndpointHash,
		EndpointHost:  s.EndpointHost,
		UserAgent:     s.UserAgent,
		CreatedAt:     s.CreatedAt.UTC().Format(time.RFC3339Nano),
		UpdatedAt:     s.UpdatedAt.UTC().Format(time.RFC3339Nano),
		LastSuccessAt: optTime(s.LastSuccessAt),
	}
}

func (a *Adapter) GetPushConfig(ctx context.Context, _ *pushgen.EmptyPushReq) (*pushgen.PushConfigResp, error) {
	actor, ok := actorFromContext(ctx)
	if !ok {
		return nil, domain.ErrUnauthenticated()
	}
	cfg, err := a.App.Config(ctx, actor)
	if err != nil {
		return nil, err
	}
	return &pushgen.PushConfigResp{
		Enabled:          cfg.Enabled,
		VapidPublicKey:   optString(cfg.VAPIDPublicKey),
		MaxSubscriptions: int32(cfg.MaxSubscriptions),
	}, nil
}

func (a *Adapter) ListPushSubscriptions(ctx context.Context, _ *pushgen.EmptyPushReq) (*pushgen.ListPushSubscriptionsResp, error) {
	actor, ok := actorFromContext(ctx)
	if !ok {
		return nil, domain.ErrUnauthenticated()
	}
	subs, err := a.App.ListSubscriptions(ctx, actor)
	if err != nil {
		return nil, err
	}
	out := make([]*pushgen.PushSubscriptionDTO, 0, len(subs))
	for _, s := range subs {
		out = append(out, toSubscriptionDTO(s))
	}
	return &pushgen.ListPushSubscriptionsResp{Subscriptions: out}, nil
}

func (a *Adapter) UpsertPushSubscription(ctx context.Context, req *pushgen.UpsertPushSubscriptionReq) (*pushgen.PushSubscriptionResp, error) {
	actor, ok := actorFromContext(ctx)
	if !ok {
		return nil, domain.ErrUnauthenticated()
	}
	if req == nil || req.Keys == nil {
		return nil, domain.ErrInvalidSubscriptionKeys()
	}
	ua := ""
	if req.UserAgent != nil {
		ua = *req.UserAgent
	}
	saved, err := a.App.UpsertSubscription(ctx, actor, domain.UpsertSubscriptionInput{
		Endpoint:  req.Endpoint,
		P256DH:    req.Keys.P256dh,
		Auth:      req.Keys.Auth,
		UserAgent: ua,
	})
	if err != nil {
		return nil, err
	}
	return &pushgen.PushSubscriptionResp{Subscription: toSubscriptionDTO(saved)}, nil
}

func (a *Adapter) DeletePushSubscription(ctx context.Context, req *pushgen.DeletePushSubscriptionReq) (*pushgen.EmptyResp, error) {
	actor, ok := actorFromContext(ctx)
	if !ok {
		return nil, domain.ErrUnauthenticated()
	}
	endpoint := ""
	if req != nil {
		endpoint = req.Endpoint
	}
	if err := a.App.DeleteSubscription(ctx, actor, endpoint); err != nil {
		return nil, err
	}
	return &pushgen.EmptyResp{}, nil
}

type pushResponder struct {
	httpx.Responder
	withAuth   func(next func(w http.ResponseWriter, r *http.Request, ctx context.Context, s httpx.Session) error) http.HandlerFunc
	muxHandler hertzapp.HandlerFunc
}

func (pr pushResponder) HertzHandler(_ pushgen.RouteSpec) hertzapp.HandlerFunc {
	return pr.muxHandler
}

func (pr pushResponder) WrapRoute(_ pushgen.RouteSpec, next http.HandlerFunc) http.HandlerFunc {
	return pr.withAuth(func(w http.ResponseWriter, r *http.Request, ctx context.Context, _ httpx.Session) error {
		next(w, r.WithContext(ctx))
		return nil
	})
}

// Routes mounts operational endpoints (/healthz, /readyz, /version) and all 4
// generated Push HTTP routes with human cookie/Origin/CSRF authentication.
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

	resp := pushResponder{
		withAuth:   withAuth,
		muxHandler: adaptor.HertzHandler(observability.HTTP(m)),
	}
	pushgen.RegisterPushHTTPServer(m, a, resp, pushgen.PushHTTPServiceRouteHandlers{})

	return httpx.WrapHertzRouter(m, func(r *route.RouterGroup) {
		pushgen.RegisterPushHTTPServiceHertzServer(r, a, resp)
	})
}
