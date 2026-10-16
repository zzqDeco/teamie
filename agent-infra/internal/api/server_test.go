package api

import (
	"net/http"
	"testing"
	"time"

	"github.com/cloudwego/hertz/pkg/app/server"
)

func TestGatewayCanRemoveWriteBudgetWithoutChangingSandboxDefaults(t *testing.T) {
	handler := http.NewServeMux()
	ordinary := newServer(":0", handler).GetOptions()
	gateway := newServer(":0", handler, server.WithWriteTimeout(0)).GetOptions()
	if ordinary.WriteTimeout != 6*time.Minute || gateway.WriteTimeout != 0 || ordinary.ReadTimeout != 15*time.Second || gateway.ReadTimeout != ordinary.ReadTimeout || !gateway.SenseClientDisconnection {
		t.Fatal("gateway override changed request-read limits, disconnect sensing or ordinary service defaults")
	}
}
