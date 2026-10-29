// Package httpAdapter implements the inbound HTTP adapter for the Model Gateway,
// satisfying the generated Thrift HTTP contract (gatewaygen.ModelGatewayHTTPService).
package httpAdapter

import (
	"context"
	"net/http"
	"strings"
	"time"

	hertzapp "github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/common/adaptor"
	"github.com/cloudwego/hertz/pkg/route"
	"go.uber.org/zap"
	gatewaygen "teamie/contracts/gen/agent/hertz_gen/agent/gateway"
	"teamie/contracts/gen/agent/hertz_gen/agent/modeltransport"
	"teamie/contracts/gen/agent/kitex_gen/agent"
	"teamie/contracts/gen/im/kitex_gen/im"
	"teamie/pkg/httpx"
	"teamie/pkg/observability"
	"teamie/services/llm-gateway/internal/app"
	"teamie/services/llm-gateway/internal/domain"
)

// AdminAuthorizer authenticates human platform administrators and regular human users on HTTP requests.
type AdminAuthorizer interface {
	Require(r *http.Request) (*im.User, error)
}

type userAuthorizer interface {
	RequireUser(r *http.Request) (*im.User, error)
}

var _ gatewaygen.ModelGatewayHTTPService = (*Adapter)(nil)

// Adapter translates generated Thrift HTTP DTOs and private inference streams into gateway application use cases.
type Adapter struct {
	App *app.Service
}

// New creates a new HTTP Adapter backed by the gateway application service.
func New(svc *app.Service) *Adapter {
	return &Adapter{App: svc}
}

func ptr[T any](v T) *T { return &v }

type adminOwnerContextKey struct{}

func withAdminOwner(ctx context.Context, ownerID string) context.Context {
	return context.WithValue(ctx, adminOwnerContextKey{}, ownerID)
}

func adminOwnerFromContext(ctx context.Context) string {
	if v, ok := ctx.Value(adminOwnerContextKey{}).(string); ok && v != "" {
		return v
	}
	return "admin-test"
}

func toAdminModelConfigResp(c domain.Config) *gatewaygen.AdminModelConfigResp {
	effDefault := c.EffectiveDefaultModelID()
	rawModels := c.Models
	resolved := c.ResolveModel("")
	modelCounts := make(map[string]int32, len(c.Providers))
	for _, m := range rawModels {
		modelCounts[m.ProviderID]++
	}
	providerDTOs := make([]*gatewaygen.AdminModelProviderDTO, 0, len(c.Providers))
	for _, p := range c.Providers {
		displayName := p.DisplayName
		if displayName == "" {
			displayName = p.ID
		}
		providerDTOs = append(providerDTOs, &gatewaygen.AdminModelProviderDTO{
			ID:          p.ID,
			DisplayName: displayName,
			BaseUrl:     p.BaseURL,
			HasApiKey:   len(p.CipherKey) > 0,
			Enabled:     p.Enabled,
			Configured:  p.Configured(),
			ModelCount:  modelCounts[p.ID],
			UpdatedAt:   p.UpdatedAt.UTC().Format(time.RFC3339Nano),
		})
	}
	dtos := make([]*gatewaygen.AdminModelProfileDTO, 0, len(rawModels))
	for _, m := range rawModels {
		rm := c.ResolveModelProfile(m)
		dtos = append(dtos, &gatewaygen.AdminModelProfileDTO{
			ID:                  m.ID,
			ProviderId:          m.ProviderID,
			ProviderDisplayName: rm.ProviderDisplayName,
			ProviderEnabled:     rm.ProviderEnabled,
			EffectiveEnabled:    rm.Enabled,
			DisplayName:         rm.DisplayName,
			Description:         m.Description,
			BaseUrl:             rm.BaseURL,
			Model:               m.Model,
			ContextWindowTokens: m.ContextWindow(),
			HasApiKey:           len(rm.CipherKey) > 0,
			Enabled:             m.Enabled,
			Configured:          rm.Configured(),
			IsDefault:           m.ID == c.Routing.DefaultModelID,
			UpdatedAt:           m.UpdatedAt.UTC().Format(time.RFC3339Nano),
		})
	}
	resp := &gatewaygen.AdminModelConfigResp{
		Config: &gatewaygen.ModelConfigDTO{
			BaseUrl:             resolved.BaseURL,
			Model:               resolved.Model,
			ContextWindowTokens: resolved.ContextWindow(),
			HasApiKey:           len(resolved.CipherKey) > 0,
			Enabled:             resolved.Enabled,
			Configured:          resolved.Configured(),
			WebSearch: &gatewaygen.WebSearchConfigDTO{
				Provider:   "tavily",
				Enabled:    c.WebSearch.Enabled,
				HasApiKey:  len(c.WebSearch.CipherKey) > 0,
				Configured: c.WebSearch.Configured(),
			},
			UpdatedAt: resolved.UpdatedAt.UTC().Format(time.RFC3339Nano),
		},
		Routing:   &gatewaygen.ModelRoutingConfigDTO{DefaultModelId: c.Routing.DefaultModelID, EffectiveModelId: effDefault},
		WebSearch: &gatewaygen.WebSearchConfigDTO{Provider: "tavily", Enabled: c.WebSearch.Enabled, HasApiKey: c.WebSearch.Configured(), Configured: c.WebSearch.Configured(), UpdatedAt: ptr(c.WebSearch.UpdatedAt.UTC().Format(time.RFC3339Nano))},
		Status:    resolved.Status(),
		Providers: providerDTOs,
		Models:    dtos,
	}
	if effDefault != "" {
		resp.DefaultModelId = &effDefault
	}
	return resp
}

func toGatewayChatResponse(r domain.ChatCallResult) *agent.GatewayChatResponse {
	resp := &agent.GatewayChatResponse{
		Ok:         r.OK,
		StatusCode: ptr(int32(r.StatusCode)),
	}
	if r.ModelConfigID != "" {
		cfgID := r.ModelConfigID
		resp.ModelConfigId = &cfgID
	}
	if r.OK {
		body := r.ResponseJSON
		resp.ResponseJson = &body
	} else {
		code := r.ErrorCode
		msg := r.ErrorMessage
		resp.ErrorCode = &code
		resp.ErrorMessage = &msg
	}
	return resp
}

type gatewayResponder struct {
	httpx.Responder
	adapter    *Adapter
	auth       AdminAuthorizer
	muxHandler hertzapp.HandlerFunc
}

func (gr gatewayResponder) HertzHandler(_ gatewaygen.RouteSpec) hertzapp.HandlerFunc {
	return gr.muxHandler
}

func (gatewayResponder) DecodeRequest(w http.ResponseWriter, r *http.Request, dst any) error {
	switch dst.(type) {
	case *gatewaygen.ChatCallReq:
		if err := httpx.DecodeJSONMaxBytes(w, r, domain.MaxRequestBytes*2+4096, dst); err != nil {
			return domain.Err(400, "invalid_request", "Invalid model request")
		}
		return nil
	case *gatewaygen.WebSearchReq:
		if err := httpx.DecodeJSONMaxBytes(w, r, 32<<10, dst); err != nil {
			return domain.Err(400, "invalid_request", "Invalid web search request")
		}
		return nil
	case *gatewaygen.WebExtractReq:
		if err := httpx.DecodeJSONMaxBytes(w, r, 32<<10, dst); err != nil {
			return domain.Err(400, "invalid_request", "Invalid web extract request")
		}
		return nil
	default:
		return httpx.DecodeJSONMaxBytes(w, r, 262144, dst)
	}
}

func (gatewayResponder) WriteBindErrorHTTP(w http.ResponseWriter, _ *http.Request, err error) {
	httpx.Fail(w, err)
}

func (gatewayResponder) WriteDomainErrorHTTP(w http.ResponseWriter, _ *http.Request, err error) {
	httpx.Fail(w, err)
}

func (gatewayResponder) WriteOKHTTP(w http.ResponseWriter, _ *http.Request, status int, resp any) {
	if chatResp, ok := resp.(*gatewaygen.ChatCallResp); ok && chatResp != nil && chatResp.GetModelConfigId() != "" {
		w.Header().Set(modeltransport.ModelConfigIDHeader, chatResp.GetModelConfigId())
		chatResp.ModelConfigId = nil
	}
	httpx.WriteNoStoreJSON(w, status, resp)
}

func (gr gatewayResponder) WrapRoute(route gatewaygen.RouteSpec, next http.HandlerFunc) http.HandlerFunc {
	if strings.HasPrefix(route.Path, "/api/v1/admin/") {
		return func(w http.ResponseWriter, r *http.Request) {
			u, err := gr.auth.Require(r)
			if err != nil {
				httpx.Fail(w, err)
				return
			}
			observability.AnnotateHTTP(w, zap.String("user.id", u.Id))
			next(w, r.WithContext(withAdminOwner(r.Context(), u.Id)))
		}
	}
	if strings.HasPrefix(route.Path, "/api/v1/model-gateway/") {
		return func(w http.ResponseWriter, r *http.Request) {
			var (
				u   *im.User
				err error
			)
			if ua, ok := gr.auth.(userAuthorizer); ok {
				u, err = ua.RequireUser(r)
			} else {
				u, err = gr.auth.Require(r)
			}
			if err != nil {
				httpx.Fail(w, err)
				return
			}
			observability.AnnotateHTTP(w, zap.String("user.id", u.Id))
			next(w, r)
		}
	}
	return func(w http.ResponseWriter, r *http.Request) {
		if err := gr.adapter.App.AuthorizeBearer(r.Header.Get("Authorization")); err != nil {
			httpx.Fail(w, err)
			return
		}
		next(w, r)
	}
}

// Routes binds all generated ModelGatewayHTTPServer routes and operational endpoints.
func (a *Adapter) Routes(auth AdminAuthorizer) http.Handler {
	m := http.NewServeMux()
	httpx.OperationsContext(m, a.App.Store.Ready)
	resp := gatewayResponder{
		adapter:    a,
		auth:       auth,
		muxHandler: adaptor.HertzHandler(observability.HTTP(m)),
	}
	gatewaygen.RegisterModelGatewayHTTPServer(m, a, resp, gatewaygen.ModelGatewayHTTPServiceRouteHandlers{
		StreamChat: a.streamHTTP,
	})
	return httpx.WrapHertzRouter(m, func(r *route.RouterGroup) {
		gatewaygen.RegisterModelGatewayHTTPServiceHertzServer(r, a, resp)
	})
}
