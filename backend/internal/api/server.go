package api

import (
	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/cloudwego/hertz/pkg/common/adaptor"
	"net/http"
	"time"
)

// Keep native Hertz transport while sharing net/http's audited cookie and WS APIs.
func Serve(addr string, handler http.Handler) {
	h := server.Default(server.WithHostPorts(addr), server.WithMaxRequestBodySize(131072), server.WithReadTimeout(15*time.Second), server.WithWriteTimeout(30*time.Second))
	h.Any("/*path", adaptor.HertzHandler(handler))
	h.Spin()
}
