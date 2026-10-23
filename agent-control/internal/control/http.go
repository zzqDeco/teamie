package control

import (
	"context"
	"net/http"

	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/cloudwego/hertz/pkg/common/adaptor"
	httpadapter "teamie/agent-control/internal/control/adapter/http"
	"teamie/agent-control/internal/control/domain"
	"teamie/backend/kitex_gen/im"
	"teamie/backend/pkg/observability"
)

// Handler delegates HTTP routing and DTO binding to the Agent Control HTTP inbound adapter.
type Handler struct {
	Store  *Store
	Origin string
}

type handler func(http.ResponseWriter, *http.Request, *im.User) error

func (h *Handler) httpAdapter() *httpadapter.Adapter {
	var appSvc = (*Store)(nil)
	if h != nil {
		appSvc = h.Store
	}
	var origin string
	if h != nil {
		origin = h.Origin
	}
	a := &httpadapter.Adapter{
		Origin:  origin,
		Version: Version,
		SHA:     SHA,
	}
	if appSvc != nil {
		a.App = appSvc.App()
	}
	return a
}

func (h *Handler) auth(next handler) http.HandlerFunc {
	return h.httpAdapter().Auth(func(w http.ResponseWriter, r *http.Request, ctx context.Context, u domain.UserProfile) error {
		return next(w, r.WithContext(ctx), &im.User{
			Id:           u.ID,
			DisplayName:  u.DisplayName,
			Kind:         u.Kind,
			PlatformRole: u.PlatformRole,
		})
	})
}

func (h *Handler) one(w http.ResponseWriter, r *http.Request, a Agent, status int) error {
	return h.httpAdapter().WriteSingleProfile(w, r, a, status)
}

func (h *Handler) Routes() http.Handler {
	return h.httpAdapter().Routes()
}

func Serve(addr string, h http.Handler) {
	s := server.Default(server.WithHostPorts(addr), server.WithMaxRequestBodySize(65536))
	s.Any("/*path", adaptor.HertzHandler(observability.HTTP(h)))
	s.Spin()
}
