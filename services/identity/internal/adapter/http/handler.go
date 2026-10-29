// Package http implements the inbound HTTP adapter for the Identity service,
// binding generated Thrift HTTP DTOs (teamie/contracts/gen/im/hertz_gen/im/identity) to
// the Identity application layer.
package http

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	hertzapp "github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/common/adaptor"
	"github.com/cloudwego/hertz/pkg/route"
	"go.uber.org/zap"
	identitygen "teamie/contracts/gen/im/hertz_gen/im/identity"
	"teamie/pkg/httpx"
	"teamie/pkg/observability"
	"teamie/services/identity/internal/app"
	"teamie/services/identity/internal/domain"
)

type rateEntry struct {
	Start time.Time
	Count int
}

// RateLimiter enforces per-account and global request rate limits without
// trusting client-supplied forwarding headers.
type RateLimiter struct {
	mu    sync.Mutex
	rates map[string]rateEntry
	hash  func(string) string
}

// Allow checks whether an operation keyed by bucketKey is within rate limits.
// When bucketKey is empty, only the global rate limit is evaluated and no
// per-key bucket is created.
func (rl *RateLimiter) Allow(bucketKey string) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	if rl.rates == nil {
		rl.rates = map[string]rateEntry{}
	}
	now := time.Now()
	for k, v := range rl.rates {
		if now.Sub(v.Start) > time.Minute {
			delete(rl.rates, k)
		}
	}
	if len(rl.rates) > 10000 {
		return false
	}
	hashFn := httpx.Hash
	if rl.hash != nil {
		hashFn = rl.hash
	}
	buckets := map[string]int{"*": 600}
	if normalized := strings.ToLower(strings.TrimSpace(bucketKey)); normalized != "" {
		buckets[hashFn(normalized)] = 20
	}
	allowed := true
	for key, max := range buckets {
		v := rl.rates[key]
		if now.Sub(v.Start) > time.Minute {
			v = rateEntry{Start: now}
		}
		v.Count++
		rl.rates[key] = v
		allowed = allowed && v.Count <= max
	}
	return allowed
}

// BucketCount returns the number of active per-key rate-limit buckets, excluding the global bucket.
func (rl *RateLimiter) BucketCount() int {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	n := 0
	for k := range rl.rates {
		if k != "*" {
			n++
		}
	}
	return n
}

type actorContextKey struct{}

type actorContext struct {
	Session domain.Session
	Token   string
}

// WithActor attaches the authenticated human session and raw session token to ctx.
func WithActor(ctx context.Context, s domain.Session, token string) context.Context {
	return context.WithValue(ctx, actorContextKey{}, actorContext{Session: s, Token: token})
}

func actorFromContext(ctx context.Context) (actorContext, bool) {
	a, ok := ctx.Value(actorContextKey{}).(actorContext)
	return a, ok
}

type issuedTokenSinkKey struct{}

func withIssuedTokenSink(ctx context.Context, dst *string) context.Context {
	return context.WithValue(ctx, issuedTokenSinkKey{}, dst)
}

func recordIssuedToken(ctx context.Context, token string) {
	if dst, ok := ctx.Value(issuedTokenSinkKey{}).(*string); ok && dst != nil {
		*dst = token
	}
}

func issuedTokenFromContext(ctx context.Context) string {
	if dst, ok := ctx.Value(issuedTokenSinkKey{}).(*string); ok && dst != nil {
		return *dst
	}
	return ""
}

// Adapter implements the generated identitygen.IdentityHTTPService contract
// and wires HTTP routes to the Identity application service.
type Adapter struct {
	App     *app.Service
	Auth    httpx.Identity
	Origin  string
	Secure  bool
	Limiter RateLimiter
}

type appIdentityAdapter struct {
	app *app.Service
}

func (i appIdentityAdapter) Validate(ctx context.Context, token string) (httpx.Session, error) {
	ss, err := i.app.Validate(ctx, token)
	if err != nil {
		return httpx.Session{}, err
	}
	return httpx.Session{
		User: httpx.User{
			ID:           ss.User.ID,
			Email:        ss.User.Email,
			DisplayName:  ss.User.DisplayName,
			Kind:         ss.User.Kind,
			PlatformRole: ss.User.PlatformRole,
			CreatedAt:    ss.User.CreatedAt,
		},
		CSRFToken:        ss.CSRFToken,
		ServicePrincipal: ss.ServicePrincipal,
	}, nil
}

func (i appIdentityAdapter) Users(ctx context.Context, ids []string) ([]httpx.User, error) {
	us, err := i.app.GetUsers(ctx, ids)
	if err != nil {
		return nil, err
	}
	out := make([]httpx.User, 0, len(us))
	for _, u := range us {
		out = append(out, httpx.User{
			ID:           u.ID,
			Email:        u.Email,
			DisplayName:  u.DisplayName,
			Kind:         u.Kind,
			PlatformRole: u.PlatformRole,
			CreatedAt:    u.CreatedAt,
		})
	}
	return out, nil
}

func (i appIdentityAdapter) Ping(ctx context.Context) error {
	return i.app.Ping(ctx)
}

func (a *Adapter) authIdentity() httpx.Identity {
	if a.Auth != nil {
		return a.Auth
	}
	return appIdentityAdapter{app: a.App}
}

// Compile-time verification that Adapter implements the generated Thrift HTTP service interface.
var _ identitygen.IdentityHTTPService = (*Adapter)(nil)

func toUserDTO(u domain.User) *identitygen.UserDTO {
	return &identitygen.UserDTO{
		ID:           u.ID,
		Email:        u.Email,
		DisplayName:  u.DisplayName,
		Kind:         u.Kind,
		PlatformRole: u.PlatformRole,
		CreatedAt:    u.CreatedAt.UTC().Format(time.RFC3339Nano),
	}
}

func toSessionResp(s domain.Session) *identitygen.SessionResp {
	return &identitygen.SessionResp{
		User:      toUserDTO(s.User),
		CsrfToken: s.CSRFToken,
	}
}

func formatOptTime(t *time.Time) *string {
	if t == nil {
		return nil
	}
	v := t.UTC().Format(time.RFC3339Nano)
	return &v
}

func toInviteDTO(inv domain.RegistrationInvite) *identitygen.RegistrationInviteDTO {
	return &identitygen.RegistrationInviteDTO{
		ID:        inv.ID,
		CreatedAt: inv.CreatedAt.UTC().Format(time.RFC3339Nano),
		ExpiresAt: inv.ExpiresAt.UTC().Format(time.RFC3339Nano),
		UsedAt:    formatOptTime(inv.UsedAt),
		RevokedAt: formatOptTime(inv.RevokedAt),
		UsedBy:    inv.UsedBy,
	}
}

// --- identitygen.IdentityHTTPService implementation ---

func (a *Adapter) GetRegistrationMetadata(ctx context.Context, _ *identitygen.GetRegistrationMetadataReq) (*identitygen.RegistrationMetadataResp, error) {
	required, err := a.App.BootstrapRequired(ctx)
	if err != nil {
		return nil, err
	}
	return &identitygen.RegistrationMetadataResp{
		Mode:              "invite",
		BootstrapRequired: required,
	}, nil
}

func (a *Adapter) Register(ctx context.Context, req *identitygen.RegisterReq) (*identitygen.SessionResp, error) {
	email, err := domain.NormalizeEmail(req.Email)
	if !a.Limiter.Allow(email) {
		return nil, domain.ErrRateLimited()
	}
	if err != nil {
		return nil, err
	}
	ss, token, err := a.App.Register(ctx, email, req.Password, req.DisplayName, req.InviteCode)
	if err != nil {
		return nil, err
	}
	recordIssuedToken(ctx, token)
	return toSessionResp(ss), nil
}

func (a *Adapter) Login(ctx context.Context, req *identitygen.LoginReq) (*identitygen.SessionResp, error) {
	email, err := domain.NormalizeEmail(req.Email)
	if !a.Limiter.Allow(email) {
		return nil, domain.ErrRateLimited()
	}
	if err != nil {
		return nil, domain.ErrInvalidCredentials()
	}
	ss, token, err := a.App.Login(ctx, email, req.Password)
	if err != nil {
		return nil, err
	}
	recordIssuedToken(ctx, token)
	return toSessionResp(ss), nil
}

func (a *Adapter) GetSession(ctx context.Context, _ *identitygen.GetSessionReq) (*identitygen.SessionResp, error) {
	actor, ok := actorFromContext(ctx)
	if !ok {
		return nil, domain.ErrUnauthenticatedSignIn()
	}
	return toSessionResp(actor.Session), nil
}

func (a *Adapter) Logout(ctx context.Context, _ *identitygen.LogoutReq) (*identitygen.EmptyResp, error) {
	actor, ok := actorFromContext(ctx)
	if !ok {
		return nil, domain.ErrUnauthenticatedSignIn()
	}
	if err := a.App.Logout(ctx, actor.Token); err != nil {
		return nil, err
	}
	return &identitygen.EmptyResp{}, nil
}

func (a *Adapter) ChangePassword(ctx context.Context, req *identitygen.ChangePasswordReq) (*identitygen.EmptyResp, error) {
	actor, ok := actorFromContext(ctx)
	if !ok {
		return nil, domain.ErrUnauthenticatedSignIn()
	}
	if actor.Session.ServicePrincipal {
		return nil, domain.ErrHumanSessionRequired()
	}
	if !a.Limiter.Allow(actor.Session.User.Email) {
		return nil, domain.ErrRateLimited()
	}
	if err := a.App.ChangePassword(ctx, actor.Session.User.ID, actor.Token, req.CurrentPassword, req.NewPassword); err != nil {
		return nil, err
	}
	return &identitygen.EmptyResp{}, nil
}

func (a *Adapter) UpdateProfile(ctx context.Context, req *identitygen.UpdateProfileReq) (*identitygen.UpdateProfileResp, error) {
	actor, ok := actorFromContext(ctx)
	if !ok {
		return nil, domain.ErrUnauthenticatedSignIn()
	}
	u, err := a.App.Profile(ctx, actor.Session.User.ID, req.DisplayName)
	if err != nil {
		return nil, err
	}
	return &identitygen.UpdateProfileResp{User: toUserDTO(u)}, nil
}

func (a *Adapter) ListInvites(ctx context.Context, _ *identitygen.ListInvitesReq) (*identitygen.ListInvitesResp, error) {
	actor, ok := actorFromContext(ctx)
	if !ok {
		return nil, domain.ErrUnauthenticatedSignIn()
	}
	invites, err := a.App.RegistrationInvites(ctx, actor.Session.User.ID)
	if err != nil {
		return nil, err
	}
	out := make([]*identitygen.RegistrationInviteDTO, 0, len(invites))
	for _, inv := range invites {
		out = append(out, toInviteDTO(inv))
	}
	return &identitygen.ListInvitesResp{Invites: out}, nil
}

func (a *Adapter) CreateInvite(ctx context.Context, _ *identitygen.CreateInviteReq) (*identitygen.CreateInviteResp, error) {
	actor, ok := actorFromContext(ctx)
	if !ok {
		return nil, domain.ErrUnauthenticatedSignIn()
	}
	inv, code, err := a.App.CreateRegistrationInvite(ctx, actor.Session.User.ID)
	if err != nil {
		return nil, err
	}
	return &identitygen.CreateInviteResp{
		Invite: &identitygen.CreatedRegistrationInviteDTO{
			ID:        inv.ID,
			CreatedAt: inv.CreatedAt.UTC().Format(time.RFC3339Nano),
			ExpiresAt: inv.ExpiresAt.UTC().Format(time.RFC3339Nano),
			UsedAt:    formatOptTime(inv.UsedAt),
			RevokedAt: formatOptTime(inv.RevokedAt),
			UsedBy:    inv.UsedBy,
			Code:      code,
		},
		URL: "/login?mode=register&inviteCode=" + code,
	}, nil
}

func (a *Adapter) RevokeInvite(ctx context.Context, req *identitygen.RevokeInviteReq) (*identitygen.EmptyResp, error) {
	actor, ok := actorFromContext(ctx)
	if !ok {
		return nil, domain.ErrUnauthenticatedSignIn()
	}
	if err := a.App.RevokeRegistrationInvite(ctx, actor.Session.User.ID, req.ID); err != nil {
		return nil, err
	}
	return &identitygen.EmptyResp{}, nil
}

func (a *Adapter) ListAdminUsers(ctx context.Context, req *identitygen.ListAdminUsersReq) (*identitygen.AdminUserPageResp, error) {
	actor, ok := actorFromContext(ctx)
	if !ok {
		return nil, domain.ErrUnauthenticatedSignIn()
	}
	limit := 0
	if raw := req.GetLimit(); raw != "" {
		var err error
		limit, err = strconv.Atoi(raw)
		if err != nil || limit < 1 {
			return nil, domain.ErrInvalidAdminUsersLimit()
		}
	}
	page, err := a.App.AdminUsers(ctx, actor.Session.User.ID, actor.Token, req.GetCursor(), limit, req.GetQ())
	if err != nil {
		return nil, err
	}
	users := make([]*identitygen.UserDTO, 0, len(page.Users))
	for _, u := range page.Users {
		users = append(users, toUserDTO(u))
	}
	var cursor *string
	if page.Cursor != "" {
		c := page.Cursor
		cursor = &c
	}
	return &identitygen.AdminUserPageResp{Users: users, Cursor: cursor}, nil
}

func (a *Adapter) CreatePasswordResetLink(ctx context.Context, req *identitygen.CreatePasswordResetLinkReq) (*identitygen.CreatePasswordResetLinkResp, error) {
	actor, ok := actorFromContext(ctx)
	if !ok {
		return nil, domain.ErrUnauthenticatedSignIn()
	}
	if !a.Limiter.Allow("password-reset-issue:" + actor.Session.User.ID) {
		return nil, domain.ErrRateLimited()
	}
	reset, token, err := a.App.CreatePasswordResetLink(ctx, actor.Session.User.ID, actor.Token, req.ID)
	if err != nil {
		return nil, err
	}
	return &identitygen.CreatePasswordResetLinkResp{
		Reset: &identitygen.PasswordResetDTO{
			ID:        reset.ID,
			CreatedAt: reset.CreatedAt.UTC().Format(time.RFC3339Nano),
			ExpiresAt: reset.ExpiresAt.UTC().Format(time.RFC3339Nano),
		},
		URL: "/reset-password#token=" + token,
	}, nil
}

func (a *Adapter) InspectPasswordReset(ctx context.Context, req *identitygen.InspectPasswordResetReq) (*identitygen.PasswordResetInspectionResp, error) {
	if !a.Limiter.Allow("password-reset:" + req.Token) {
		return nil, domain.ErrRateLimited()
	}
	res, err := a.App.InspectPasswordReset(ctx, req.Token)
	if err != nil {
		return nil, err
	}
	return &identitygen.PasswordResetInspectionResp{
		Account: &identitygen.PasswordResetAccountDTO{
			ID:          res.Account.ID,
			DisplayName: res.Account.DisplayName,
			EmailMasked: res.Account.EmailMasked,
		},
		ExpiresAt: res.ExpiresAt.UTC().Format(time.RFC3339Nano),
	}, nil
}

func (a *Adapter) ResetPassword(ctx context.Context, req *identitygen.ResetPasswordReq) (*identitygen.EmptyResp, error) {
	if !a.Limiter.Allow("password-reset:" + req.Token) {
		return nil, domain.ErrRateLimited()
	}
	if err := a.App.ResetPassword(ctx, req.Token, req.NewPassword); err != nil {
		return nil, err
	}
	return &identitygen.EmptyResp{}, nil
}

// --- HTTP Transport Wiring ---

func (a *Adapter) setCookie(w http.ResponseWriter, token string, maxAge int) {
	http.SetCookie(w, &http.Cookie{
		Name:     httpx.CookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		Secure:   a.Secure,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   maxAge,
	})
}

func (a *Adapter) decodeStrictJSON(w http.ResponseWriter, r *http.Request, input any) error {
	w.Header().Set("Cache-Control", "no-store")
	if err := httpx.CheckOrigin(r, a.Origin); err != nil {
		return err
	}
	if err := httpx.RequireJSONMediaType(r); err != nil {
		return err
	}
	return httpx.DecodeJSON(w, r, input)
}

type identityResponder struct {
	httpx.Responder
	adapter    *Adapter
	withHuman  func(fn func(w http.ResponseWriter, r *http.Request, ctx context.Context, s httpx.Session) error) http.HandlerFunc
	muxHandler hertzapp.HandlerFunc
}

func (ir identityResponder) HertzHandler(_ identitygen.RouteSpec) hertzapp.HandlerFunc {
	return ir.muxHandler
}

func (ir identityResponder) DecodeRequest(w http.ResponseWriter, r *http.Request, dst any) error {
	switch dst.(type) {
	case *identitygen.RegisterReq, *identitygen.LoginReq:
		if err := httpx.CheckOrigin(r, ir.adapter.Origin); err != nil {
			return err
		}
		return httpx.DecodeJSON(w, r, dst)
	case *identitygen.CreateInviteReq:
		actor, ok := actorFromContext(r.Context())
		if !ok {
			return domain.ErrUnauthenticatedSignIn()
		}
		if err := ir.adapter.App.RequireAdmin(r.Context(), actor.Session.User.ID); err != nil {
			return err
		}
		return httpx.DecodeJSON(w, r, dst)
	case *identitygen.CreatePasswordResetLinkReq:
		actor, ok := actorFromContext(r.Context())
		if !ok {
			return domain.ErrUnauthenticatedSignIn()
		}
		if err := ir.adapter.App.RequireAdmin(r.Context(), actor.Session.User.ID); err != nil {
			return err
		}
		return ir.adapter.decodeStrictJSON(w, r, &struct{}{})
	case *identitygen.InspectPasswordResetReq, *identitygen.ResetPasswordReq:
		return ir.adapter.decodeStrictJSON(w, r, dst)
	default:
		return httpx.DecodeJSON(w, r, dst)
	}
}

func (ir identityResponder) WriteOKHTTP(w http.ResponseWriter, r *http.Request, status int, resp any) {
	if token := issuedTokenFromContext(r.Context()); token != "" {
		if sessResp, ok := resp.(*identitygen.SessionResp); ok && sessResp != nil {
			observability.AnnotateHTTP(w, zap.String("user.id", sessResp.GetUser().GetID()))
		}
		ir.adapter.setCookie(w, token, 7*86400)
	}
	w.Header().Set("Cache-Control", "no-store")
	httpx.WriteJSON(w, status, resp)
}

func (ir identityResponder) WrapRoute(route identitygen.RouteSpec, next http.HandlerFunc) http.HandlerFunc {
	switch route.Name {
	case "GetRegistrationMetadata", "InspectPasswordReset", "ResetPassword":
		return func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Cache-Control", "no-store")
			next(w, r)
		}
	case "Register", "Login":
		return func(w http.ResponseWriter, r *http.Request) {
			var token string
			next(w, r.WithContext(withIssuedTokenSink(r.Context(), &token)))
		}
	default:
		return ir.withHuman(func(w http.ResponseWriter, r *http.Request, ctx context.Context, _ httpx.Session) error {
			w.Header().Set("Cache-Control", "no-store")
			next(w, r.WithContext(ctx))
			return nil
		})
	}
}

// Routes mounts all Identity HTTP endpoints onto an http.ServeMux using the
// generated identitygen.RegisterIdentityHTTPServer stateless interface bindings and DTOs.
func (a *Adapter) Routes() http.Handler {
	m := http.NewServeMux()
	httpx.Operations(m, func(r *http.Request) error { return a.App.Ping(r.Context()) })

	withHuman := func(fn func(w http.ResponseWriter, r *http.Request, ctx context.Context, s httpx.Session) error) http.HandlerFunc {
		return httpx.HumanAuth(a.authIdentity(), a.Origin, func(w http.ResponseWriter, r *http.Request, s httpx.Session) error {
			domSess := domain.Session{
				User: domain.User{
					ID:           s.User.ID,
					Email:        s.User.Email,
					DisplayName:  s.User.DisplayName,
					Kind:         s.User.Kind,
					PlatformRole: s.User.PlatformRole,
					CreatedAt:    s.User.CreatedAt,
				},
				CSRFToken:        s.CSRFToken,
				ServicePrincipal: s.ServicePrincipal,
			}
			ctx := WithActor(r.Context(), domSess, httpx.Token(r))
			return fn(w, r, ctx, s)
		})
	}

	resp := identityResponder{
		adapter:    a,
		withHuman:  withHuman,
		muxHandler: adaptor.HertzHandler(observability.HTTP(m)),
	}
	identitygen.RegisterIdentityHTTPServer(m, a, resp)

	return httpx.WrapHertzRouter(m, func(r *route.RouterGroup) {
		identitygen.RegisterIdentityHTTPServiceHertzServer(r, a, resp)
	})
}
