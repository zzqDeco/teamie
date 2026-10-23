package httpx

import (
	"context"
	"net/http"
	"time"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/cloudwego/hertz/pkg/common/adaptor"
	"github.com/cloudwego/hertz/pkg/common/config"
	"github.com/cloudwego/hertz/pkg/route"
	"teamie/pkg/observability"
)

type hertzRequestContextKey struct{}

// WithHertzContext attaches the active Hertz RequestContext to ctx so adapted handlers
// can access native Hertz streaming writers (such as sse.NewWriter).
func WithHertzContext(ctx context.Context, c *app.RequestContext) context.Context {
	return context.WithValue(ctx, hertzRequestContextKey{}, c)
}

// HertzContext retrieves the active Hertz RequestContext from ctx if running inside Hertz.
func HertzContext(ctx context.Context) *app.RequestContext {
	if c, ok := ctx.Value(hertzRequestContextKey{}).(*app.RequestContext); ok {
		return c
	}
	return nil
}

// HertzRouteRegistrar is implemented by HTTP handlers that register their
// generated Thrift IDL routes directly onto a native Hertz route.RouterGroup.
type HertzRouteRegistrar interface {
	RegisterHertz(r *route.RouterGroup)
}

type hertzRouter struct {
	http.Handler
	register func(r *route.RouterGroup)
}

func (hr hertzRouter) RegisterHertz(r *route.RouterGroup) {
	if hr.register != nil {
		hr.register(r)
	}
}

// WrapHertzRouter returns an http.Handler that also implements HertzRouteRegistrar
// so both httptest/ServeHTTP callers and native Hertz servers share the same adapter.
func WrapHertzRouter(mux http.Handler, register func(r *route.RouterGroup)) http.Handler {
	return hertzRouter{Handler: mux, register: register}
}

// MountHertz mounts handler onto a Hertz server, registering native RouterGroup
// routes when handler implements HertzRouteRegistrar.
func MountHertz(h *server.Hertz, handler http.Handler) {
	h.Use(func(ctx context.Context, c *app.RequestContext) {
		c.Next(WithHertzContext(ctx, c))
	})
	obs := observability.HTTP(handler)
	if reg, ok := handler.(HertzRouteRegistrar); ok {
		reg.RegisterHertz(&h.RouterGroup)
		h.NoRoute(adaptor.HertzHandler(obs))
		return
	}
	h.Any("/*path", adaptor.HertzHandler(obs))
}

// Serve starts a Hertz HTTP server with streaming body and disabled multipart pre-parsing
// so handlers authenticate and enforce capacity bounds before consuming upload bodies.
func Serve(addr string, handler http.Handler) {
	h := server.Default(
		server.WithHostPorts(addr),
		server.WithMaxRequestBodySize(131072),
		server.WithStreamBody(true),
		server.WithDisablePreParseMultipartForm(true),
		server.WithReadTimeout(15*time.Second),
		server.WithWriteTimeout(30*time.Second),
	)
	MountHertz(h, handler)
	h.Spin()
}

// ServeWithOptions starts a Hertz HTTP server with longer default write timeout (6m)
// and caller-supplied option overrides (used by llm-gateway and sandbox).
func ServeWithOptions(addr string, handler http.Handler, options ...config.Option) {
	NewServerWithOptions(addr, handler, options...).Spin()
}

// NewServerWithOptions constructs a Hertz server with 516 KiB max body, 15s read timeout,
// 6m write timeout, and client disconnection sensing, plus any caller option overrides.
func NewServerWithOptions(addr string, handler http.Handler, options ...config.Option) *server.Hertz {
	defaults := []config.Option{
		server.WithHostPorts(addr),
		server.WithMaxRequestBodySize(528384),
		server.WithReadTimeout(15 * time.Second),
		server.WithWriteTimeout(6 * time.Minute),
		server.WithSenseClientDisconnection(true),
	}
	h := server.Default(append(defaults, options...)...)
	MountHertz(h, handler)
	return h
}
