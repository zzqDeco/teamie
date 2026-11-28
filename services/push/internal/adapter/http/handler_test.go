package http_test

import (
	"bytes"
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"teamie/pkg/httpx"
	httpadapter "teamie/services/push/internal/adapter/http"
	"teamie/services/push/internal/app"
	"teamie/services/push/internal/domain"
)

type stubIdentity struct {
	session httpx.Session
}

func (s stubIdentity) Validate(_ context.Context, token string) (httpx.Session, error) {
	if token == strings.Repeat("a", 64) {
		return s.session, nil
	}
	return httpx.Session{}, httpx.Err(401, "unauthenticated", "Sign in to continue")
}
func (s stubIdentity) ValidatePrincipal(context.Context, string, string) (httpx.Session, error) {
	return httpx.Session{}, httpx.Err(401, "unauthenticated", "nope")
}
func (s stubIdentity) Users(context.Context, []string) ([]httpx.User, error) { return nil, nil }
func (s stubIdentity) Ping(context.Context) error                            { return nil }

type stubRepo struct {
	subs []domain.PushSubscription
}

func (r *stubRepo) WithinTx(ctx context.Context, fn func(context.Context) error) error { return fn(ctx) }
func (r *stubRepo) LockUserSubscriptions(context.Context, string) error                { return nil }
func (r *stubRepo) Ping(context.Context) error                                         { return nil }
func (r *stubRepo) ListByUser(_ context.Context, userID string) ([]domain.PushSubscription, error) {
	out := []domain.PushSubscription{}
	for _, s := range r.subs {
		if s.UserID == userID {
			out = append(out, s)
		}
	}
	return out, nil
}
func (r *stubRepo) ListByUserIDs(context.Context, []string) ([]domain.PushSubscription, error) {
	return r.subs, nil
}
func (r *stubRepo) UpsertAndTrim(_ context.Context, sub domain.PushSubscription, _ int) (domain.PushSubscription, error) {
	r.subs = []domain.PushSubscription{sub}
	return sub, nil
}
func (r *stubRepo) DeleteByUserEndpointHash(_ context.Context, userID, hash string) (bool, error) {
	kept := []domain.PushSubscription{}
	removed := false
	for _, s := range r.subs {
		if s.UserID == userID && s.EndpointHash == hash {
			removed = true
			continue
		}
		kept = append(kept, s)
	}
	r.subs = kept
	return removed, nil
}
func (r *stubRepo) DeleteByID(context.Context, string) error                          { return nil }
func (r *stubRepo) RecordDeliveryResult(context.Context, string, bool, string) error  { return nil }

type stubFlags bool

func (f stubFlags) Enabled(key string) bool { return key == "im.push.web" && bool(f) }

func TestHTTPAdapter_AuthAndNoSecretLeak(t *testing.T) {
	vapidPriv, _ := ecdh.P256().GenerateKey(rand.Reader)
	uaPriv, _ := ecdh.P256().GenerateKey(rand.Reader)
	authBytes := make([]byte, 16)
	_, _ = rand.Read(authBytes)

	repo := &stubRepo{}
	svc := &app.Service{
		Tx:            repo,
		Subscriptions: repo,
		Flags:         stubFlags(true),
		VAPID: domain.VAPIDConfig{
			PublicKey:  base64.RawURLEncoding.EncodeToString(vapidPriv.PublicKey().Bytes()),
			PrivateKey: base64.RawURLEncoding.EncodeToString(vapidPriv.Bytes()),
			Subject:    "mailto:ops@teamie.example",
		},
	}
	origin := "https://teamie.example"
	adapter := &httpadapter.Adapter{
		App: svc,
		Auth: stubIdentity{session: httpx.Session{
			User:      httpx.User{ID: "u1", Kind: "human", DisplayName: "Alice"},
			CSRFToken: "csrf-token-123",
		}},
		Origin: origin,
	}
	handler := adapter.Routes()

	// Unauthenticated request returns 401.
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/push/config", nil))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 without cookie, got %d", w.Code)
	}

	authedReq := func(method, path string, body any) *http.Request {
		var buf bytes.Buffer
		if body != nil {
			_ = json.NewEncoder(&buf).Encode(body)
		}
		req := httptest.NewRequest(method, path, &buf)
		req.AddCookie(&http.Cookie{Name: httpx.CookieName, Value: strings.Repeat("a", 64)})
		req.Header.Set("Origin", origin)
		req.Header.Set("X-CSRF-Token", "csrf-token-123")
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		return req
	}

	// PUT without CSRF is rejected.
	badCSRF := authedReq(http.MethodPut, "/api/v1/push/subscriptions", map[string]any{
		"endpoint": "https://fcm.googleapis.com/fcm/send/secret-token-xyz",
		"keys": map[string]string{
			"p256dh": base64.RawURLEncoding.EncodeToString(uaPriv.PublicKey().Bytes()),
			"auth":   base64.RawURLEncoding.EncodeToString(authBytes),
		},
	})
	badCSRF.Header.Set("X-CSRF-Token", "wrong")
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, badCSRF)
	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403 on bad CSRF, got %d", w.Code)
	}

	// PUT valid subscription succeeds and never echoes raw endpoint/keys.
	rawEndpoint := "https://fcm.googleapis.com/fcm/send/secret-token-xyz"
	p256dh := base64.RawURLEncoding.EncodeToString(uaPriv.PublicKey().Bytes())
	auth := base64.RawURLEncoding.EncodeToString(authBytes)
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, authedReq(http.MethodPut, "/api/v1/push/subscriptions", map[string]any{
		"endpoint":  rawEndpoint,
		"keys":      map[string]string{"p256dh": p256dh, "auth": auth},
		"userAgent": "TeamiePWA/1.0",
	}))
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 on PUT, got %d: %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "secret-token-xyz") || strings.Contains(w.Body.String(), p256dh) || strings.Contains(w.Body.String(), auth) {
		t.Fatalf("response leaked raw endpoint or keys: %s", w.Body.String())
	}

	// GET list returns metadata only.
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, authedReq(http.MethodGet, "/api/v1/push/subscriptions", nil))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"endpointHost":"fcm.googleapis.com"`) {
		t.Fatalf("unexpected GET list response: %d %s", w.Code, w.Body.String())
	}

	// DELETE returns 204 and clears subscription.
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, authedReq(http.MethodDelete, "/api/v1/push/subscriptions", map[string]string{"endpoint": rawEndpoint}))
	if w.Code != http.StatusNoContent {
		t.Fatalf("expected 204 on DELETE, got %d: %s", w.Code, w.Body.String())
	}
	if len(repo.subs) != 0 {
		t.Fatalf("expected subscription deleted, still %d", len(repo.subs))
	}
}
