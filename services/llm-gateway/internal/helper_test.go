package gateway_test

import (
	"context"
	"errors"
	"net"
	"net/http"

	"teamie/contracts/gen/agent/kitex_gen/agent"
	"teamie/pkg/rpcx"
	httpAdapter "teamie/services/llm-gateway/internal/adapter/http"
	rpcAdapter "teamie/services/llm-gateway/internal/adapter/rpc"
	"teamie/services/llm-gateway/internal/app"
	"teamie/services/llm-gateway/internal/domain"
	"teamie/services/llm-gateway/internal/infra/crypto"
	"teamie/services/llm-gateway/internal/infra/pg"
	"teamie/services/llm-gateway/internal/infra/upstream"
)

type Config = domain.Config
type Usage = domain.Usage
type Store = domain.ConfigStore
type Cipher = domain.Cipher
type StreamEvent = domain.StreamEvent
type Provider = upstream.Provider

const (
	MaxRequestBytes        = domain.MaxRequestBytes
	MaxResponseBytes       = domain.MaxResponseBytes
	defaultTavilyBaseURL   = domain.DefaultTavilyBaseURL
	maxExtractContentBytes = domain.MaxExtractContentBytes
	maxSearchSnippetBytes  = domain.MaxSearchSnippetBytes
	maxSearchTitleBytes    = domain.MaxSearchTitleBytes
)

func NewCipher(master string) (Cipher, error) { return crypto.NewCipher(master) }
func Open(ctx context.Context, url string) (*pg.PGStore, error) {
	return pg.Open(ctx, url)
}
func mergeSavedConfig(old, c Config, key *string, clear bool, cipher Cipher) (Config, error) {
	return domain.MergeSavedConfig(old, c, key, clear, cipher)
}
func PublicIP(ip net.IP) bool                          { return domain.PublicIP(ip) }
func ValidateEndpoint(ctx context.Context, raw string) error { return domain.ValidateEndpoint(ctx, raw) }
func NewHTTPClient() *http.Client                      { return upstream.NewHTTPClient() }
func normalizeRequest(raw, model string) ([]byte, error) {
	return domain.NormalizeModeWithOutputLimit(raw, model, false, 4096)
}
func normalizeModeWithOutputLimit(raw, model string, streaming bool, outputLimit int, messageLimits ...int) ([]byte, error) {
	return domain.NormalizeModeWithOutputLimit(raw, model, streaming, outputLimit, messageLimits...)
}
func providerContextError(raw []byte) error { return domain.ProviderContextError(raw) }
func guardStreamingResponse(response *http.Response, key string, ctx context.Context) (*http.Response, error) {
	return upstream.GuardStreamingResponse(response, key, ctx)
}
func decodedCredential(value any, key string) bool { return domain.DecodedCredential(value, key) }

type credentialStreamGuard struct {
	inner *domain.CredentialStreamGuard
}

func newCredentialStreamGuard(key string) *credentialStreamGuard {
	return &credentialStreamGuard{inner: domain.NewCredentialStreamGuard(key)}
}
func (g *credentialStreamGuard) push(raw string, emit func(string) error) error {
	return g.inner.Push(raw, emit)
}
func (g *credentialStreamGuard) finish(emit func(string) error) error {
	return g.inner.Finish(emit)
}

func guardErrorCode(err error) string {
	var de *domain.Error
	if errors.As(err, &de) {
		return de.Code
	}
	return ""
}

type dynamicProvider struct {
	s *Service
}

func (d dynamicProvider) Chat(ctx context.Context, c Config, key, raw string, messageLimits ...int) (string, int64, int64, error) {
	return d.s.Provider.Chat(ctx, c, key, raw, messageLimits...)
}
func (d dynamicProvider) Stream(ctx context.Context, c Config, key, raw string, emit func(string) error, messageLimits ...int) (int64, int64, error) {
	return d.s.Provider.Stream(ctx, c, key, raw, emit, messageLimits...)
}
func (d dynamicProvider) Search(ctx context.Context, baseURL, apiKey string, payload map[string]any, maxResults int) ([]domain.WebSearchItem, int, error) {
	if d.s.WebSearchURL != "" {
		baseURL = d.s.WebSearchURL
	}
	return d.s.Provider.Search(ctx, baseURL, apiKey, payload, maxResults)
}
func (d dynamicProvider) Extract(ctx context.Context, baseURL, apiKey string, payload map[string]any) ([]domain.WebExtractItem, []domain.WebExtractFailure, int, error) {
	if d.s.WebSearchURL != "" {
		baseURL = d.s.WebSearchURL
	}
	return d.s.Provider.Extract(ctx, baseURL, apiKey, payload)
}

// Service composes app.Service, rpcAdapter.Server, and httpAdapter.Adapter for tests.
type Service struct {
	Store        Store
	Cipher       Cipher
	Provider     Provider
	Token        string
	WebSearchURL string
	App          *app.Service
	RPC          *rpcAdapter.Server
	HTTP         *httpAdapter.Adapter
}

func New(store Store, cipher Cipher, token string) *Service {
	s := &Service{
		Store:        store,
		Cipher:       cipher,
		Provider:     Provider{HTTP: NewHTTPClient()},
		Token:        token,
		WebSearchURL: defaultTavilyBaseURL,
	}
	dp := dynamicProvider{s: s}
	s.App = app.New(store, cipher, dp, dp, token)
	s.RPC = rpcAdapter.New(s.App)
	s.HTTP = httpAdapter.New(s.App)
	return s
}

func (s *Service) Routes(auth rpcx.AdminAuth) http.Handler {
	s.App.WebSearchURL = s.WebSearchURL
	return s.HTTP.Routes(auth)
}

func (s *Service) Ping(ctx context.Context, r *agent.PingRequest) (*agent.PingResponse, error) {
	return s.RPC.Ping(ctx, r)
}

func (s *Service) Chat(ctx context.Context, r *agent.GatewayChatRequest) (*agent.GatewayChatResponse, error) {
	return s.RPC.Chat(ctx, r)
}

func (s *Service) Cancel(ctx context.Context, r *agent.CancelRequest) (*agent.Ack, error) {
	return s.RPC.Cancel(ctx, r)
}

func (s *Service) CancelRun(id, run string) {
	s.App.CancelRun(id, run)
}

func (s *Service) call(ctx context.Context, owner, id, session, run, raw string, messageLimits ...int) *agent.GatewayChatResponse {
	res := s.App.CallChat(ctx, owner, id, session, run, raw, messageLimits...).Result
	out := &agent.GatewayChatResponse{
		Ok:         res.OK,
		StatusCode: ptr(int32(res.StatusCode)),
	}
	if res.ModelConfigID != "" {
		cfgID := res.ModelConfigID
		out.ModelConfigId = &cfgID
	}
	if res.OK {
		body := res.ResponseJSON
		out.ResponseJson = &body
	} else {
		code := res.ErrorCode
		msg := res.ErrorMessage
		out.ErrorCode = &code
		out.ErrorMessage = &msg
	}
	return out
}

func ptr[T any](v T) *T { return &v }
