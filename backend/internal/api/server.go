package api

import (
	"teamie/backend/pkg/observability"
	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/cloudwego/hertz/pkg/common/adaptor"
	"net/http"
	"time"
)

// Keep native Hertz transport while sharing net/http's audited cookie and WS APIs.
func Serve(addr string, handler http.Handler) {
	// Stream before handlers admit/bound large uploads. Multipart pre-parsing
	// would consume the complete body before authentication and capacity checks.
	h := server.Default(server.WithHostPorts(addr), server.WithMaxRequestBodySize(131072), server.WithStreamBody(true), server.WithDisablePreParseMultipartForm(true), server.WithReadTimeout(15*time.Second), server.WithWriteTimeout(30*time.Second))
	h.Any("/*path", adaptor.HertzHandler(observability.HTTP(handler)))
	h.Spin()
}
