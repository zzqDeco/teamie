package httpx

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/cloudwego/hertz/pkg/app/server"
)

type sessionReadIdentity struct{}

func (sessionReadIdentity) Validate(context.Context, string) (Session, error) {
	return Session{User: User{ID: "cookie-account", Kind: "human"}, CSRFToken: "cookie-session"}, nil
}
func (sessionReadIdentity) Users(context.Context, []string) ([]User, error) { return nil, nil }
func (sessionReadIdentity) Ping(context.Context) error                      { return nil }

func TestProtectedReadsRejectAChangedBrowserCookie(t *testing.T) {
	for _, tt := range []struct {
		method, path, csrf string
		want               int
	}{
		{"GET", "/api/v1/bootstrap", "old-session", 401},
		{"HEAD", "/api/v1/bootstrap", "old-session", 401},
		{"GET", "/api/v1/bootstrap", "cookie-session", 204},
		{"GET", "/api/v1/bootstrap", "", 204},
		{"GET", "/api/v1/auth/session", "old-session", 204},
		{"POST", "/api/v1/conversations/c/read", "old-session", 403},
	} {
		t.Run(tt.method+tt.path+tt.csrf, func(t *testing.T) {
			r := httptest.NewRequest(tt.method, tt.path, nil)
			r.Header.Set("X-CSRF-Token", tt.csrf)
			r.Header.Set("Origin", "https://teamie.example")
			w := httptest.NewRecorder()
			called := false
			Auth(sessionReadIdentity{}, "https://teamie.example", func(w http.ResponseWriter, _ *http.Request, _ Session) error {
				called = true
				w.WriteHeader(204)
				return nil
			})(w, r)
			if w.Code != tt.want || called != (tt.want == 204) {
				t.Fatalf("status=%d called=%v body=%s", w.Code, called, w.Body.String())
			}
			if tt.want == 401 && !strings.Contains(w.Body.String(), `"session_changed"`) {
				t.Fatal(w.Body.String())
			}
		})
	}
}

func TestGatewayCanRemoveWriteBudgetWithoutChangingSandboxDefaults(t *testing.T) {
	handler := http.NewServeMux()
	ordinary := NewServerWithOptions(":0", handler).GetOptions()
	gateway := NewServerWithOptions(":0", handler, server.WithWriteTimeout(0)).GetOptions()
	if ordinary.WriteTimeout != 6*time.Minute || gateway.WriteTimeout != 0 || ordinary.ReadTimeout != 15*time.Second || gateway.ReadTimeout != ordinary.ReadTimeout || !gateway.SenseClientDisconnection {
		t.Fatal("gateway override changed request-read limits, disconnect sensing or ordinary service defaults")
	}
}
