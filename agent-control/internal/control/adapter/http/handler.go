// Package http implements the inbound HTTP adapter for the Agent Control Plane,
// binding generated Thrift HTTP DTOs (teamie/agent-protocol/hertz_gen/agent/control)
// to the Control Plane application layer.
package http

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"go.uber.org/zap"
	"teamie/agent-control/internal/control/app"
	"teamie/agent-control/internal/control/domain"
	controlgen "teamie/agent-protocol/hertz_gen/agent/control"
	"teamie/backend/pkg/httpx"
	"teamie/backend/pkg/observability"
)

type actorContextKey struct{}

// WithActor attaches the authenticated human user profile to ctx.
func WithActor(ctx context.Context, u domain.UserProfile) context.Context {
	return context.WithValue(ctx, actorContextKey{}, u)
}

func actorFromContext(ctx context.Context) (domain.UserProfile, bool) {
	u, ok := ctx.Value(actorContextKey{}).(domain.UserProfile)
	return u, ok
}

// Adapter implements controlgen.AgentControlHTTPService and wires HTTP routes.
type Adapter struct {
	App     *app.Service
	Origin  string
	Version string
	SHA     string
}

var _ controlgen.AgentControlHTTPService = (*Adapter)(nil)

func toPublicAgentDTO(p domain.PublicAgent) *controlgen.PublicAgentDTO {
	ws := p.WorkspaceIDs
	if ws == nil {
		ws = []string{}
	}
	return &controlgen.PublicAgentDTO{
		ID:           p.ID,
		PrincipalId:  p.PrincipalID,
		OwnerId:      p.OwnerID,
		OwnerName:    p.OwnerName,
		Name:         p.Name,
		Description:  p.Description,
		WorkspaceIds: ws,
		Enabled:      p.Enabled,
		CreatedAt:    p.CreatedAt.Format(time.RFC3339Nano),
		UpdatedAt:    p.UpdatedAt.Format(time.RFC3339Nano),
	}
}

func toPublicAgentDTOList(agents []domain.Agent, workspaceID string) []*controlgen.PublicAgentDTO {
	profiles := domain.PublicProfiles(agents, workspaceID)
	out := make([]*controlgen.PublicAgentDTO, len(profiles))
	for i, p := range profiles {
		out[i] = toPublicAgentDTO(p)
	}
	return out
}

func toCreateChanges(req *controlgen.CreateAgentReq) domain.Changes {
	ch := domain.Changes{
		Name:        req.Name,
		Description: req.Description,
		Enabled:     req.Enabled,
	}
	if req.WorkspaceIds != nil {
		ws := req.WorkspaceIds
		ch.WorkspaceIDs = &ws
	}
	return ch
}

func toUpdateChanges(req *controlgen.UpdateAgentReq) domain.Changes {
	ch := domain.Changes{
		Name:        req.Name,
		Description: req.Description,
		Enabled:     req.Enabled,
	}
	if req.WorkspaceIds != nil {
		ws := req.WorkspaceIds
		ch.WorkspaceIDs = &ws
	}
	return ch
}

// --- controlgen.AgentControlHTTPService implementation ---

func (a *Adapter) ListOwnedAgents(ctx context.Context, _ *controlgen.ListOwnedAgentsReq) (*controlgen.AgentListResp, error) {
	u, ok := actorFromContext(ctx)
	if !ok {
		return nil, domain.ErrUnauthenticated("Sign in to continue")
	}
	list, err := a.App.ListOwned(ctx, u.ID)
	if err != nil {
		return nil, err
	}
	list, err = a.App.Decorate(ctx, list)
	if err != nil {
		return nil, err
	}
	return &controlgen.AgentListResp{Agents: toPublicAgentDTOList(list, "")}, nil
}

func (a *Adapter) CreateAgent(ctx context.Context, req *controlgen.CreateAgentReq) (*controlgen.AgentResp, error) {
	u, ok := actorFromContext(ctx)
	if !ok {
		return nil, domain.ErrUnauthenticated("Sign in to continue")
	}
	ag, err := a.App.Create(ctx, u.ID, toCreateChanges(req))
	if err != nil {
		return nil, err
	}
	decorated, err := a.App.Decorate(ctx, []domain.Agent{ag})
	if err != nil {
		return nil, err
	}
	return &controlgen.AgentResp{Agent: toPublicAgentDTO(domain.PublicProfile(decorated[0]))}, nil
}

func (a *Adapter) GetAgentProfile(ctx context.Context, req *controlgen.GetAgentProfileReq) (*controlgen.AgentResp, error) {
	u, ok := actorFromContext(ctx)
	if !ok {
		return nil, domain.ErrUnauthenticated("Sign in to continue")
	}
	ag, err := a.App.Owned(ctx, u.ID, req.ID)
	if err != nil {
		return nil, err
	}
	decorated, err := a.App.Decorate(ctx, []domain.Agent{ag})
	if err != nil {
		return nil, err
	}
	return &controlgen.AgentResp{Agent: toPublicAgentDTO(domain.PublicProfile(decorated[0]))}, nil
}

func (a *Adapter) UpdateAgent(ctx context.Context, req *controlgen.UpdateAgentReq) (*controlgen.AgentResp, error) {
	u, ok := actorFromContext(ctx)
	if !ok {
		return nil, domain.ErrUnauthenticated("Sign in to continue")
	}
	ag, err := a.App.Update(ctx, u.ID, req.ID, toUpdateChanges(req))
	if err != nil {
		return nil, err
	}
	decorated, err := a.App.Decorate(ctx, []domain.Agent{ag})
	if err != nil {
		return nil, err
	}
	return &controlgen.AgentResp{Agent: toPublicAgentDTO(domain.PublicProfile(decorated[0]))}, nil
}

func (a *Adapter) DeleteAgent(ctx context.Context, req *controlgen.DeleteAgentReq) (*controlgen.EmptyResp, error) {
	u, ok := actorFromContext(ctx)
	if !ok {
		return nil, domain.ErrUnauthenticated("Sign in to continue")
	}
	if err := a.App.Delete(ctx, u.ID, req.ID); err != nil {
		return nil, err
	}
	return &controlgen.EmptyResp{}, nil
}

func (a *Adapter) ListWorkspaceAgents(ctx context.Context, req *controlgen.ListWorkspaceAgentsReq) (*controlgen.AgentListResp, error) {
	u, ok := actorFromContext(ctx)
	if !ok {
		return nil, domain.ErrUnauthenticated("Sign in to continue")
	}
	list, err := a.App.Discover(ctx, req.ID, u.ID)
	if err != nil {
		return nil, err
	}
	list, err = a.App.Decorate(ctx, list)
	if err != nil {
		return nil, err
	}
	return &controlgen.AgentListResp{Agents: toPublicAgentDTOList(list, req.ID)}, nil
}

// --- HTTP Transport Helpers & Routing ---

func Write(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	if v != nil {
		_ = json.NewEncoder(w).Encode(v)
	}
}

func Fail(w http.ResponseWriter, err error) {
	observability.RecordError(w, err)
	var coded httpx.CodedError
	if errors.As(err, &coded) {
		Write(w, coded.StatusCode(), map[string]any{
			"error": map[string]string{
				"code":    coded.ErrorCode(),
				"message": coded.Error(),
			},
		})
		return
	}
	Write(w, http.StatusServiceUnavailable, map[string]any{
		"error": map[string]string{
			"code":    "control_unavailable",
			"message": "Agent control is temporarily unavailable",
		},
	})
}

func Decode(w http.ResponseWriter, r *http.Request, v any) error {
	r.Body = http.MaxBytesReader(w, r.Body, 65536)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		return domain.ErrInvalidRequest("Invalid Agent request")
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return domain.ErrInvalidRequest("Expected one JSON object")
	}
	return nil
}

// Auth wraps an authenticated human browser endpoint with cookie, CSRF, and Origin checks.
func (a *Adapter) Auth(next func(w http.ResponseWriter, r *http.Request, ctx context.Context, u domain.UserProfile) error) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			Fail(w, domain.ErrForbidden("Human browser session required"))
			return
		}
		cookie, err := r.Cookie("teamie_session")
		if err != nil {
			Fail(w, domain.ErrUnauthenticated("Sign in to continue"))
			return
		}
		if a.App == nil || a.App.IM == nil {
			Fail(w, domain.ErrControlUnavailable(""))
			return
		}
		user, csrf, err := a.App.IM.ValidateSession(r.Context(), cookie.Value)
		if err != nil {
			Fail(w, err)
			return
		}
		if user == nil || user.Kind != "human" {
			Fail(w, domain.ErrForbidden("A human browser session is required"))
			return
		}
		observability.AnnotateHTTP(w, zap.String("user.id", user.ID))
		r = r.WithContext(observability.WithFields(r.Context(), zap.String("user.id", user.ID)))
		if r.Method == "GET" || r.Method == "HEAD" {
			if provided := r.Header.Get("X-CSRF-Token"); provided != "" && subtle.ConstantTimeCompare([]byte(csrf), []byte(provided)) != 1 {
				Fail(w, domain.ErrSessionChanged())
				return
			}
		}
		if r.Method != "GET" && r.Method != "HEAD" {
			origin := r.Header.Get("Origin")
			if origin != "" && origin != a.Origin || origin == "" && r.Header.Get("Sec-Fetch-Site") == "cross-site" {
				Fail(w, domain.ErrUntrustedOrigin())
				return
			}
			provided := r.Header.Get("X-CSRF-Token")
			if provided == "" || subtle.ConstantTimeCompare([]byte(csrf), []byte(provided)) != 1 {
				Fail(w, domain.ErrCSRFFailed())
				return
			}
		}
		ctx := WithActor(r.Context(), *user)
		if err := next(w, r, ctx, *user); err != nil {
			Fail(w, err)
		}
	}
}

// WriteSingleProfile decorates a single domain.Agent and writes its 10-field public profile.
func (a *Adapter) WriteSingleProfile(w http.ResponseWriter, r *http.Request, ag domain.Agent, status int) error {
	out, err := a.App.Decorate(r.Context(), []domain.Agent{ag})
	if err != nil {
		return err
	}
	Write(w, status, &controlgen.AgentResp{Agent: toPublicAgentDTO(domain.PublicProfile(out[0]))})
	return nil
}

// Routes mounts all Agent Control HTTP endpoints onto an http.ServeMux.
func (a *Adapter) Routes() http.Handler {
	m := http.NewServeMux()
	m.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		Fail(w, domain.ErrNotFound("Resource is unavailable"))
	})
	m.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		Write(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	m.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		if a.App == nil || a.App.Ready(r.Context()) != nil {
			Write(w, http.StatusServiceUnavailable, map[string]string{"status": "unavailable"})
			return
		}
		Write(w, http.StatusOK, map[string]string{"status": "ready"})
	})
	m.HandleFunc("GET /version", func(w http.ResponseWriter, _ *http.Request) {
		Write(w, http.StatusOK, map[string]string{"version": a.Version, "sha": a.SHA})
	})
	m.HandleFunc("GET /api/v1/agents", a.Auth(func(w http.ResponseWriter, _ *http.Request, ctx context.Context, _ domain.UserProfile) error {
		resp, err := a.ListOwnedAgents(ctx, &controlgen.ListOwnedAgentsReq{})
		if err != nil {
			return err
		}
		Write(w, http.StatusOK, resp)
		return nil
	}))
	m.HandleFunc("POST /api/v1/agents", a.Auth(func(w http.ResponseWriter, r *http.Request, ctx context.Context, _ domain.UserProfile) error {
		var req controlgen.CreateAgentReq
		if err := Decode(w, r, &req); err != nil {
			return err
		}
		resp, err := a.CreateAgent(ctx, &req)
		if err != nil {
			return err
		}
		Write(w, http.StatusCreated, resp)
		return nil
	}))
	m.HandleFunc("GET /api/v1/agents/{id}", a.Auth(func(w http.ResponseWriter, r *http.Request, ctx context.Context, _ domain.UserProfile) error {
		resp, err := a.GetAgentProfile(ctx, &controlgen.GetAgentProfileReq{ID: r.PathValue("id")})
		if err != nil {
			return err
		}
		Write(w, http.StatusOK, resp)
		return nil
	}))
	m.HandleFunc("PATCH /api/v1/agents/{id}", a.Auth(func(w http.ResponseWriter, r *http.Request, ctx context.Context, _ domain.UserProfile) error {
		var req controlgen.UpdateAgentReq
		if err := Decode(w, r, &req); err != nil {
			return err
		}
		req.ID = r.PathValue("id")
		resp, err := a.UpdateAgent(ctx, &req)
		if err != nil {
			return err
		}
		Write(w, http.StatusOK, resp)
		return nil
	}))
	m.HandleFunc("DELETE /api/v1/agents/{id}", a.Auth(func(w http.ResponseWriter, r *http.Request, ctx context.Context, _ domain.UserProfile) error {
		if _, err := a.DeleteAgent(ctx, &controlgen.DeleteAgentReq{ID: r.PathValue("id")}); err != nil {
			return err
		}
		Write(w, http.StatusNoContent, nil)
		return nil
	}))
	m.HandleFunc("GET /api/v1/workspaces/{id}/agents", a.Auth(func(w http.ResponseWriter, r *http.Request, ctx context.Context, _ domain.UserProfile) error {
		resp, err := a.ListWorkspaceAgents(ctx, &controlgen.ListWorkspaceAgentsReq{ID: r.PathValue("id")})
		if err != nil {
			return err
		}
		Write(w, http.StatusOK, resp)
		return nil
	}))
	return m
}
