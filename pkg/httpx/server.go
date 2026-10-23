package httpx

import (
	"net/http"
	"time"

	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/cloudwego/hertz/pkg/common/adaptor"
	"github.com/cloudwego/hertz/pkg/common/config"
	"teamie/pkg/observability"
)

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
	h.Any("/*path", adaptor.HertzHandler(observability.HTTP(handler)))
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
	h.Any("/*path", adaptor.HertzHandler(observability.HTTP(handler)))
	return h
}
