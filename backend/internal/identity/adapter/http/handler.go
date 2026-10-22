// Package http implements the inbound HTTP adapter for the Identity service,
// binding generated Thrift HTTP DTOs (teamie/backend/hertz_gen/im/identity) to
// the Identity application layer.
package http

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"
	identitygen "teamie/backend/hertz_gen/im/identity"
	"teamie/backend/internal/api"
	"teamie/backend/internal/identity/app"
	"teamie/backend/internal/identity/domain"
	"teamie/backend/pkg/httpx"
	"teamie/backend/pkg/observability"
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
	hashFn := api.Hash
	if rl.hash != nil {
		hashFn = rl.hash
	}
	allowed := true
	for key, max := range map[string]int{"*": 600, hashFn(strings.ToLower(strings.TrimSpace(bucketKey))): 20} {
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

// Adapter implements the generated identitygen.IdentityHTTPService contract
// and wires HTTP routes to the Identity application service.
type Adapter struct {
	App     *app.Service
	Auth    api.Identity
	Origin  string
	Secure  bool
	Limiter RateLimiter
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
	ss, _, err := a.App.Register(ctx, req.Email, req.Password, req.DisplayName, req.InviteCode)
	if err != nil {
		return nil, err
	}
	return toSessionResp(ss), nil
}

func (a *Adapter) Login(ctx context.Context, req *identitygen.LoginReq) (*identitygen.SessionResp, error) {
	ss, _, err := a.App.Login(ctx, req.Email, req.Password)
	if err != nil {
		return nil, err
	}
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
	if err := a.App.ResetPassword(ctx, req.Token, req.NewPassword); err != nil {
		return nil, err
	}
	return &identitygen.EmptyResp{}, nil
}

// --- HTTP Transport Wiring ---

func (a *Adapter) setCookie(w http.ResponseWriter, token string, maxAge int) {
	http.SetCookie(w, &http.Cookie{
		Name:     api.CookieName,
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
	if err := api.CheckOrigin(r, a.Origin); err != nil {
		return err
	}
	if err := httpx.RequireJSONMediaType(r); err != nil {
		return err
	}
	return httpx.DecodeJSON(w, r, input)
}

// Routes mounts all Identity HTTP endpoints onto an http.ServeMux using the
// generated hertz_gen/im/identity request and response DTOs.
func (a *Adapter) Routes() http.Handler {
	m := http.NewServeMux()
	api.Operations(m, func(r *http.Request) error { return a.App.Ping(r.Context()) })

	m.HandleFunc("GET /api/v1/auth/registration", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		resp, err := a.GetRegistrationMetadata(r.Context(), &identitygen.GetRegistrationMetadataReq{})
		if err != nil {
			httpx.WriteError(w, err)
			return
		}
		httpx.WriteJSON(w, http.StatusOK, resp)
	})

	m.HandleFunc("POST /api/v1/auth/register", func(w http.ResponseWriter, r *http.Request) {
		if err := api.CheckOrigin(r, a.Origin); err != nil {
			httpx.WriteError(w, err)
			return
		}
		var req identitygen.RegisterReq
		if err := httpx.DecodeJSON(w, r, &req); err != nil {
			httpx.WriteError(w, err)
			return
		}
		if !a.Limiter.Allow(req.Email) {
			httpx.WriteError(w, domain.ErrRateLimited())
			return
		}
		ss, token, err := a.App.Register(r.Context(), req.Email, req.Password, req.DisplayName, req.InviteCode)
		if err != nil {
			httpx.WriteError(w, err)
			return
		}
		observability.AnnotateHTTP(w, zap.String("user.id", ss.User.ID))
		a.setCookie(w, token, 7*86400)
		w.Header().Set("Cache-Control", "no-store")
		httpx.WriteJSON(w, http.StatusCreated, toSessionResp(ss))
	})

	m.HandleFunc("POST /api/v1/auth/login", func(w http.ResponseWriter, r *http.Request) {
		if err := api.CheckOrigin(r, a.Origin); err != nil {
			httpx.WriteError(w, err)
			return
		}
		var req identitygen.LoginReq
		if err := httpx.DecodeJSON(w, r, &req); err != nil {
			httpx.WriteError(w, err)
			return
		}
		if !a.Limiter.Allow(req.Email) {
			httpx.WriteError(w, domain.ErrRateLimited())
			return
		}
		ss, token, err := a.App.Login(r.Context(), req.Email, req.Password)
		if err != nil {
			httpx.WriteError(w, err)
			return
		}
		observability.AnnotateHTTP(w, zap.String("user.id", ss.User.ID))
		a.setCookie(w, token, 7*86400)
		w.Header().Set("Cache-Control", "no-store")
		httpx.WriteJSON(w, http.StatusOK, toSessionResp(ss))
	})

	withHuman := func(fn func(w http.ResponseWriter, r *http.Request, ctx context.Context, s api.Session) error) http.HandlerFunc {
		return api.HumanAuth(a.Auth, a.Origin, func(w http.ResponseWriter, r *http.Request, s api.Session) error {
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
			ctx := WithActor(r.Context(), domSess, api.Token(r))
			return fn(w, r, ctx, s)
		})
	}

	m.HandleFunc("GET /api/v1/auth/session", withHuman(func(w http.ResponseWriter, _ *http.Request, ctx context.Context, _ api.Session) error {
		w.Header().Set("Cache-Control", "no-store")
		resp, err := a.GetSession(ctx, &identitygen.GetSessionReq{})
		if err != nil {
			return err
		}
		httpx.WriteJSON(w, http.StatusOK, resp)
		return nil
	}))

	m.HandleFunc("POST /api/v1/auth/logout", withHuman(func(w http.ResponseWriter, _ *http.Request, ctx context.Context, _ api.Session) error {
		if _, err := a.Logout(ctx, &identitygen.LogoutReq{}); err != nil {
			return err
		}
		httpx.WriteJSON(w, http.StatusNoContent, nil)
		return nil
	}))

	m.HandleFunc("POST /api/v1/auth/password", withHuman(func(w http.ResponseWriter, r *http.Request, ctx context.Context, s api.Session) error {
		w.Header().Set("Cache-Control", "no-store")
		if s.ServicePrincipal {
			return domain.ErrHumanSessionRequired()
		}
		if !a.Limiter.Allow(s.User.Email) {
			return domain.ErrRateLimited()
		}
		var req identitygen.ChangePasswordReq
		if err := httpx.DecodeJSON(w, r, &req); err != nil {
			return err
		}
		if _, err := a.ChangePassword(ctx, &req); err != nil {
			return err
		}
		httpx.WriteJSON(w, http.StatusNoContent, nil)
		return nil
	}))

	m.HandleFunc("PATCH /api/v1/auth/profile", withHuman(func(w http.ResponseWriter, r *http.Request, ctx context.Context, _ api.Session) error {
		var req identitygen.UpdateProfileReq
		if err := httpx.DecodeJSON(w, r, &req); err != nil {
			return err
		}
		resp, err := a.UpdateProfile(ctx, &req)
		if err != nil {
			return err
		}
		httpx.WriteJSON(w, http.StatusOK, resp)
		return nil
	}))

	m.HandleFunc("GET /api/v1/auth/admin/invites", withHuman(func(w http.ResponseWriter, _ *http.Request, ctx context.Context, _ api.Session) error {
		w.Header().Set("Cache-Control", "no-store")
		resp, err := a.ListInvites(ctx, &identitygen.ListInvitesReq{})
		if err != nil {
			return err
		}
		httpx.WriteJSON(w, http.StatusOK, resp)
		return nil
	}))

	m.HandleFunc("POST /api/v1/auth/admin/invites", withHuman(func(w http.ResponseWriter, r *http.Request, ctx context.Context, s api.Session) error {
		w.Header().Set("Cache-Control", "no-store")
		if err := a.App.RequireAdmin(ctx, s.User.ID); err != nil {
			return err
		}
		var req identitygen.CreateInviteReq
		if err := httpx.DecodeJSON(w, r, &req); err != nil {
			return err
		}
		resp, err := a.CreateInvite(ctx, &req)
		if err != nil {
			return err
		}
		httpx.WriteJSON(w, http.StatusCreated, resp)
		return nil
	}))

	m.HandleFunc("DELETE /api/v1/auth/admin/invites/{id}", withHuman(func(w http.ResponseWriter, r *http.Request, ctx context.Context, _ api.Session) error {
		w.Header().Set("Cache-Control", "no-store")
		if _, err := a.RevokeInvite(ctx, &identitygen.RevokeInviteReq{ID: r.PathValue("id")}); err != nil {
			return err
		}
		httpx.WriteJSON(w, http.StatusNoContent, nil)
		return nil
	}))

	m.HandleFunc("GET /api/v1/auth/admin/users", withHuman(func(w http.ResponseWriter, r *http.Request, ctx context.Context, _ api.Session) error {
		w.Header().Set("Cache-Control", "no-store")
		q := r.URL.Query()
		cursor := q.Get("cursor")
		limit := q.Get("limit")
		search := q.Get("q")
		resp, err := a.ListAdminUsers(ctx, &identitygen.ListAdminUsersReq{
			Cursor: &cursor,
			Limit:  &limit,
			Q:      &search,
		})
		if err != nil {
			return err
		}
		httpx.WriteJSON(w, http.StatusOK, resp)
		return nil
	}))

	m.HandleFunc("POST /api/v1/auth/admin/users/{id}/password-reset-links", withHuman(func(w http.ResponseWriter, r *http.Request, ctx context.Context, s api.Session) error {
		w.Header().Set("Cache-Control", "no-store")
		if err := a.App.RequireAdmin(ctx, s.User.ID); err != nil {
			return err
		}
		if err := a.decodeStrictJSON(w, r, &struct{}{}); err != nil {
			return err
		}
		if !a.Limiter.Allow("password-reset-issue:" + s.User.ID) {
			return domain.ErrRateLimited()
		}
		resp, err := a.CreatePasswordResetLink(ctx, &identitygen.CreatePasswordResetLinkReq{ID: r.PathValue("id")})
		if err != nil {
			return err
		}
		httpx.WriteJSON(w, http.StatusCreated, resp)
		return nil
	}))

	m.HandleFunc("POST /api/v1/auth/password-reset/inspect", func(w http.ResponseWriter, r *http.Request) {
		var req identitygen.InspectPasswordResetReq
		if err := a.decodeStrictJSON(w, r, &req); err != nil {
			httpx.WriteError(w, err)
			return
		}
		if !a.Limiter.Allow("password-reset:" + req.Token) {
			httpx.WriteError(w, domain.ErrRateLimited())
			return
		}
		resp, err := a.InspectPasswordReset(r.Context(), &req)
		if err != nil {
			httpx.WriteError(w, err)
			return
		}
		httpx.WriteJSON(w, http.StatusOK, resp)
	})

	m.HandleFunc("POST /api/v1/auth/password-reset", func(w http.ResponseWriter, r *http.Request) {
		var req identitygen.ResetPasswordReq
		if err := a.decodeStrictJSON(w, r, &req); err != nil {
			httpx.WriteError(w, err)
			return
		}
		if !a.Limiter.Allow("password-reset:" + req.Token) {
			httpx.WriteError(w, domain.ErrRateLimited())
			return
		}
		if _, err := a.ResetPassword(r.Context(), &req); err != nil {
			httpx.WriteError(w, err)
			return
		}
		httpx.WriteJSON(w, http.StatusNoContent, nil)
	})

	return m
}
