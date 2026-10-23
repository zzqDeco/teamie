// Package httpAdapter implements the inbound HTTP adapter for the Model Gateway,
// satisfying the generated Thrift HTTP contract (gatewaygen.ModelGatewayHTTPService).
package httpAdapter

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	hertzapp "github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/common/adaptor"
	"github.com/cloudwego/hertz/pkg/protocol/sse"
	"github.com/cloudwego/hertz/pkg/route"
	"go.uber.org/zap"
	gatewaygen "teamie/contracts/gen/agent/hertz_gen/agent/gateway"
	"teamie/contracts/gen/agent/kitex_gen/agent"
	"teamie/contracts/gen/agent/hertz_gen/agent/modeltransport"
	"teamie/contracts/gen/im/kitex_gen/im"
	"teamie/pkg/httpx"
	"teamie/pkg/observability"
	"teamie/services/llm-gateway/internal/app"
	"teamie/services/llm-gateway/internal/domain"
)

// AdminAuthorizer authenticates human platform administrators on HTTP requests.
type AdminAuthorizer interface {
	Require(r *http.Request) (*im.User, error)
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

func toAdminModelConfigResp(c domain.Config) *gatewaygen.AdminModelConfigResp {
	return &gatewaygen.AdminModelConfigResp{
		Config: &gatewaygen.ModelConfigDTO{
			BaseUrl:             c.BaseURL,
			Model:               c.Model,
			ContextWindowTokens: c.ContextWindow(),
			HasApiKey:           len(c.CipherKey) > 0,
			Enabled:             c.Enabled,
			Configured:          c.Configured(),
			WebSearch: &gatewaygen.WebSearchConfigDTO{
				Provider:   "tavily",
				Enabled:    c.WebSearchEnabled,
				HasApiKey:  len(c.WebSearchCipherKey) > 0,
				Configured: c.WebSearchConfigured(),
			},
			UpdatedAt: c.UpdatedAt.UTC().Format(time.RFC3339Nano),
		},
		Status: c.Status(),
	}
}

func (a *Adapter) GetAdminModelConfig(ctx context.Context, _ *gatewaygen.GetAdminModelConfigReq) (*gatewaygen.AdminModelConfigResp, error) {
	c, err := a.App.LoadConfig(ctx)
	if err != nil {
		return nil, err
	}
	return toAdminModelConfigResp(c), nil
}

func (a *Adapter) UpdateAdminModelConfig(ctx context.Context, req *gatewaygen.UpdateAdminModelConfigReq) (*gatewaygen.AdminModelConfigResp, error) {
	if req == nil {
		return nil, domain.Err(400, "invalid_request", "Invalid model configuration")
	}
	c, err := a.App.SaveConfig(ctx, app.SaveConfigInput{
		BaseURL:              req.BaseUrl,
		Model:                req.Model,
		APIKey:               req.ApiKey,
		Enabled:              req.Enabled,
		ClearAPIKey:          req.GetClearApiKey(),
		ContextWindowTokens:  req.ContextWindowTokens,
		WebSearchEnabled:     req.WebSearchEnabled,
		WebSearchAPIKey:      req.WebSearchApiKey,
		ClearWebSearchAPIKey: req.GetClearWebSearchApiKey(),
	})
	if err != nil {
		return nil, err
	}
	return toAdminModelConfigResp(c), nil
}

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

func (a *Adapter) TestAdminModelConfig(ctx context.Context, _ *gatewaygen.TestAdminModelConfigReq) (*gatewaygen.TestAdminModelConfigResp, error) {
	return a.testAdminModelConfigWithOwner(ctx, adminOwnerFromContext(ctx))
}

func (a *Adapter) testAdminModelConfigWithOwner(ctx context.Context, owner string) (*gatewaygen.TestAdminModelConfigResp, error) {
	c, err := a.App.LoadConfig(ctx)
	if err != nil {
		return nil, err
	}
	start := time.Now()
	raw, _ := json.Marshal(modeltransport.Request{
		Protocol: modeltransport.Version,
		Messages: json.RawMessage(`[{"role":"user","content_blocks":[{"type":"user_input_text","user_input_text":{"text":"Reply with OK."}}]}]`),
		Options:  json.RawMessage(`{"MaxTokens":8,"AgenticToolChoice":{"Type":"forbidden"}}`),
	})
	res := a.callWithObservability(ctx, owner, "admin-test", "admin-test", start.UTC().Format("20060102T150405.000000000"), string(raw))
	if !res.Ok {
		return nil, domain.Err(int(res.GetStatusCode()), res.GetErrorCode(), res.GetErrorMessage())
	}
	return &gatewaygen.TestAdminModelConfigResp{
		Ok:        true,
		Model:     c.Model,
		LatencyMs: time.Since(start).Milliseconds(),
	}, nil
}

func (a *Adapter) GenerateChat(ctx context.Context, req *gatewaygen.ChatCallReq) (*gatewaygen.ChatCallResp, error) {
	if req == nil || !domain.ValidID(req.OwnerId) || !domain.ValidID(req.AgentId) || !domain.ValidID(req.SessionId) || !domain.ValidID(req.RunId) || req.GetMessageLimit() < 0 || int(req.GetMessageLimit()) > modeltransport.MaxInputMessages {
		return nil, domain.Err(400, "invalid_request", "Invalid model request")
	}
	if req.GetExpectedModelConfigId() != "" {
		ctx = app.WithExpectedModelConfigID(ctx, req.GetExpectedModelConfigId())
	}
	limit := domain.InputMessageLimit(int(req.GetMessageLimit()))
	res := a.callWithObservability(ctx, req.OwnerId, req.AgentId, req.SessionId, req.RunId, req.RequestJson, limit)
	return &gatewaygen.ChatCallResp{
		Ok:            res.Ok,
		ResponseJson:  res.ResponseJson,
		ErrorCode:     res.ErrorCode,
		ErrorMessage:  res.ErrorMessage,
		StatusCode:    res.StatusCode,
		ModelConfigId: res.ModelConfigId,
	}, nil
}

func (a *Adapter) StreamChat(_ context.Context, _ *gatewaygen.ChatCallReq) (*gatewaygen.StreamEventFrameDTO, error) {
	return nil, domain.Err(400, "invalid_request", "Use HTTP streaming endpoint")
}

// StreamChatSSE writes model stream frames using Hertz's built-in pkg/protocol/sse writer (sse.NewWriter).
func (a *Adapter) StreamChatSSE(ctx context.Context, c *hertzapp.RequestContext, req *gatewaygen.ChatCallReq) error {
	if req == nil || !domain.ValidID(req.OwnerId) || !domain.ValidID(req.AgentId) || !domain.ValidID(req.SessionId) || !domain.ValidID(req.RunId) || req.GetMessageLimit() < 0 || int(req.GetMessageLimit()) > modeltransport.MaxInputMessages {
		return domain.Err(400, "invalid_request", "Invalid model request")
	}
	ctx, operation := gatewayOperation(ctx, req.AgentId, req.SessionId, req.RunId, true)
	writer := sse.NewWriter(c)
	c.Response.Header.Set("Cache-Control", "no-store")
	c.Response.Header.Set("X-Accel-Buffering", "no")
	defer writer.Close()
	outcome := a.App.StreamChat(
		ctx,
		req.OwnerId,
		req.AgentId,
		req.SessionId,
		req.RunId,
		req.GetExpectedModelConfigId(),
		req.RequestJson,
		func(configID string) {
			c.Response.Header.Set(modeltransport.ModelConfigIDHeader, configID)
		},
		func(ev domain.StreamEvent) error {
			payload, err := json.Marshal(ev)
			if err != nil {
				return err
			}
			return writer.WriteEvent("", ev.Type, payload)
		},
		domain.InputMessageLimit(int(req.GetMessageLimit())),
	)
	streamFailure := outcome.Failure
	if streamFailure == nil && outcome.TerminalWriteError != nil {
		streamFailure = observability.Diagnostic("downstream_terminal_write_failed", outcome.TerminalWriteError)
	}
	operation.End(streamFailure,
		zap.String("custom.model.failure_stage", outcome.Phase),
		zap.Int64("custom.model.prompt_tokens", outcome.PromptTokens),
		zap.Int64("custom.model.completion_tokens", outcome.CompletionTokens),
		zap.Int("custom.model.stream.chunk_count", outcome.ChunkCount),
		zap.Int("custom.model.stream.transport_bytes", outcome.TransportBytes),
		zap.String("custom.duration_scope", "admission-through-provider-stream-and-usage-record"),
		zap.Int("custom.retry.attempts", observability.HTTPAttempts(ctx)),
	)
	return outcome.TerminalWriteError
}

func (a *Adapter) WebSearch(ctx context.Context, req *gatewaygen.WebSearchReq) (*gatewaygen.WebSearchResp, error) {
	if req == nil || !domain.ValidID(req.OwnerId) || !domain.ValidID(req.AgentId) || !domain.ValidID(req.SessionId) || !domain.ValidID(req.RunId) {
		return nil, domain.Err(400, "invalid_request", "Invalid web search request")
	}
	run, attempt, _ := strings.Cut(req.RunId, ":")
	ctx, op := observability.Begin(ctx, "web-search-call",
		zap.String("custom.agent.id", observability.ID(req.AgentId)),
		zap.String("custom.runtime.session_id", observability.ID(req.SessionId)),
		zap.String("custom.runtime.run_id", observability.ID(run)),
		zap.String("custom.runtime.attempt_id", observability.ID(attempt)),
	)
	ctx = observability.WithHTTPAttempts(ctx)
	outcome := a.App.ExecuteWebSearch(ctx, domain.WebSearchInput{
		OwnerID:        req.OwnerId,
		AgentID:        req.AgentId,
		SessionID:      req.SessionId,
		RunID:          req.RunId,
		Query:          req.Query,
		MaxResults:     int(req.GetMaxResults()),
		TimeRange:      req.GetTimeRange(),
		IncludeDomains: req.IncludeDomains,
		ExcludeDomains: req.ExcludeDomains,
	})
	var callFailure error
	if outcome.Failure != nil {
		callFailure = outcome.Failure
	}
	op.End(callFailure,
		zap.String("custom.model.failure_stage", outcome.Phase),
		zap.Int("custom.web.result_count", len(outcome.Results)),
		zap.Int("custom.web.response_bytes", outcome.ResponseBytes),
		zap.Int("custom.retry.attempts", observability.HTTPAttempts(ctx)),
	)
	if outcome.Failure != nil {
		code := outcome.Failure.Code
		msg := outcome.Failure.Message
		st := int32(outcome.Failure.Status)
		return &gatewaygen.WebSearchResp{
			Ok:           false,
			ErrorCode:    &code,
			ErrorMessage: &msg,
			StatusCode:   &st,
		}, nil
	}
	items := make([]*gatewaygen.WebSearchResultDTO, 0, len(outcome.Results))
	for _, r := range outcome.Results {
		dto := &gatewaygen.WebSearchResultDTO{
			Title:   r.Title,
			URL:     r.URL,
			Snippet: r.Snippet,
			Score:   ptr(r.Score),
		}
		if r.PublishedDate != "" {
			dto.PublishedDate = ptr(r.PublishedDate)
		}
		items = append(items, dto)
	}
	return &gatewaygen.WebSearchResp{
		Ok:         true,
		Results:    items,
		StatusCode: ptr(int32(200)),
	}, nil
}

func (a *Adapter) WebExtract(ctx context.Context, req *gatewaygen.WebExtractReq) (*gatewaygen.WebExtractResp, error) {
	if req == nil || !domain.ValidID(req.OwnerId) || !domain.ValidID(req.AgentId) || !domain.ValidID(req.SessionId) || !domain.ValidID(req.RunId) {
		return nil, domain.Err(400, "invalid_request", "Invalid web extract request")
	}
	run, attempt, _ := strings.Cut(req.RunId, ":")
	ctx, op := observability.Begin(ctx, "web-extract-call",
		zap.String("custom.agent.id", observability.ID(req.AgentId)),
		zap.String("custom.runtime.session_id", observability.ID(req.SessionId)),
		zap.String("custom.runtime.run_id", observability.ID(run)),
		zap.String("custom.runtime.attempt_id", observability.ID(attempt)),
	)
	ctx = observability.WithHTTPAttempts(ctx)
	outcome := a.App.ExecuteWebExtract(ctx, domain.WebExtractInput{
		OwnerID:   req.OwnerId,
		AgentID:   req.AgentId,
		SessionID: req.SessionId,
		RunID:     req.RunId,
		URLs:      req.Urls,
	})
	var callFailure error
	if outcome.Failure != nil {
		callFailure = outcome.Failure
	}
	op.End(callFailure,
		zap.String("custom.model.failure_stage", outcome.Phase),
		zap.Int("custom.web.url_count", outcome.URLCount),
		zap.Int("custom.web.result_count", len(outcome.Results)),
		zap.Int("custom.web.failed_url_count", len(outcome.FailedResults)),
		zap.Int("custom.web.response_bytes", outcome.ResponseBytes),
		zap.Int("custom.retry.attempts", observability.HTTPAttempts(ctx)),
	)
	failed := make([]*gatewaygen.WebExtractFailureDTO, 0, len(outcome.FailedResults))
	for _, f := range outcome.FailedResults {
		errText := f.Error
		failed = append(failed, &gatewaygen.WebExtractFailureDTO{
			URL:   f.URL,
			Error: &errText,
		})
	}
	if outcome.Failure != nil {
		code := outcome.Failure.Code
		msg := outcome.Failure.Message
		st := int32(outcome.Failure.Status)
		return &gatewaygen.WebExtractResp{
			Ok:            false,
			FailedResults: failed,
			ErrorCode:     &code,
			ErrorMessage:  &msg,
			StatusCode:    &st,
		}, nil
	}
	items := make([]*gatewaygen.WebExtractResultDTO, 0, len(outcome.Results))
	for _, r := range outcome.Results {
		items = append(items, &gatewaygen.WebExtractResultDTO{
			URL:     r.URL,
			Content: r.Content,
		})
	}
	return &gatewaygen.WebExtractResp{
		Ok:            true,
		Results:       items,
		FailedResults: failed,
		StatusCode:    ptr(int32(200)),
	}, nil
}

func gatewayOperation(ctx context.Context, id, session, execution string, streaming bool) (context.Context, *observability.Operation) {
	run, attempt, _ := strings.Cut(execution, ":")
	ctx, op := observability.Begin(ctx, "model-provider-call",
		zap.String("custom.agent.id", observability.ID(id)),
		zap.String("custom.runtime.session_id", observability.ID(session)),
		zap.String("custom.runtime.run_id", observability.ID(run)),
		zap.String("custom.runtime.attempt_id", observability.ID(attempt)),
		zap.Bool("custom.model.streaming", streaming),
	)
	ctx = observability.WithHTTPAttempts(ctx)
	observability.L(ctx).Info("Model provider request started", zap.String("event.action", "model-provider-call-started"))
	return ctx, op
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

// CallWithObservability executes a unary model inference call with observability tracing/logging.
func (a *Adapter) callWithObservability(ctx context.Context, owner, id, session, run, raw string, messageLimits ...int) *agent.GatewayChatResponse {
	ctx, operation := gatewayOperation(ctx, id, session, run, false)
	outcome := a.App.CallChat(ctx, owner, id, session, run, raw, messageLimits...)
	operation.End(outcome.Failure,
		zap.String("custom.model.failure_stage", outcome.Phase),
		zap.Int64("custom.model.prompt_tokens", outcome.PromptTokens),
		zap.Int64("custom.model.completion_tokens", outcome.CompletionTokens),
		zap.String("custom.duration_scope", "admission-through-provider-response-and-usage-record"),
		zap.Int("custom.retry.attempts", observability.HTTPAttempts(ctx)),
	)
	return toGatewayChatResponse(outcome.Result)
}

func (a *Adapter) decodeHTTPCall(w http.ResponseWriter, r *http.Request) (*modeltransport.CallRequest, bool) {
	if err := a.App.AuthorizeBearer(r.Header.Get("Authorization")); err != nil {
		httpx.Fail(w, err)
		return nil, false
	}
	r.Body = http.MaxBytesReader(w, r.Body, domain.MaxRequestBytes*2+4096)
	var in modeltransport.CallRequest
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if dec.Decode(&in) != nil || dec.Decode(new(any)) != io.EOF || !domain.ValidID(in.OwnerID) || !domain.ValidID(in.AgentID) || !domain.ValidID(in.SessionID) || !domain.ValidID(in.RunID) || in.MessageLimit < 0 || in.MessageLimit > modeltransport.MaxInputMessages {
		httpx.Fail(w, domain.Err(400, "invalid_request", "Invalid model request"))
		return nil, false
	}
	return &in, true
}

type httpSSEExtWriter struct {
	w           http.ResponseWriter
	rc          *hertzapp.RequestContext
	wroteHeader bool
}

func (e *httpSSEExtWriter) syncHeaders() {
	if e.wroteHeader {
		return
	}
	e.wroteHeader = true
	if e.rc != nil {
		e.rc.Response.Header.VisitAll(func(k, v []byte) {
			e.w.Header().Set(string(k), string(v))
		})
	}
}

func (e *httpSSEExtWriter) Write(p []byte) (int, error) {
	e.syncHeaders()
	return e.w.Write(p)
}

func (e *httpSSEExtWriter) Flush() error {
	e.syncHeaders()
	return http.NewResponseController(e.w).Flush()
}

func (e *httpSSEExtWriter) Finalize() error {
	return nil
}

func (a *Adapter) streamHTTP(w http.ResponseWriter, r *http.Request) {
	in, ok := a.decodeHTTPCall(w, r)
	if !ok {
		return
	}
	if strings.Contains(r.Header.Get("Accept"), "text/event-stream") {
		msgLimit := int32(in.MessageLimit)
		req := &gatewaygen.ChatCallReq{
			OwnerId:      in.OwnerID,
			AgentId:      in.AgentID,
			SessionId:    in.SessionID,
			RunId:        in.RunID,
			RequestJson:  in.RequestJSON,
			MessageLimit: &msgLimit,
		}
		if expected := r.Header.Get(modeltransport.ModelConfigIDHeader); expected != "" {
			req.ExpectedModelConfigId = &expected
		}
		sseCtx := hertzapp.NewContext(0)
		sseCtx.Response.HijackWriter(&httpSSEExtWriter{w: w, rc: sseCtx})
		_ = a.StreamChatSSE(r.Context(), sseCtx, req)
		return
	}
	ctx, operation := gatewayOperation(r.Context(), in.AgentID, in.SessionID, in.RunID, true)
	r = r.WithContext(ctx)
	w.Header().Set("Content-Type", "application/x-ndjson")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	enc := json.NewEncoder(w)
	outcome := a.App.StreamChat(
		r.Context(),
		in.OwnerID,
		in.AgentID,
		in.SessionID,
		in.RunID,
		r.Header.Get(modeltransport.ModelConfigIDHeader),
		in.RequestJSON,
		func(configID string) {
			w.Header().Set(modeltransport.ModelConfigIDHeader, configID)
		},
		func(ev domain.StreamEvent) error {
			if err := enc.Encode(ev); err != nil {
				return err
			}
			return http.NewResponseController(w).Flush()
		},
		in.InputMessageLimit(),
	)
	streamFailure := outcome.Failure
	if streamFailure == nil && outcome.TerminalWriteError != nil {
		streamFailure = observability.Diagnostic("downstream_terminal_write_failed", outcome.TerminalWriteError)
	}
	operation.End(streamFailure,
		zap.String("custom.model.failure_stage", outcome.Phase),
		zap.Int64("custom.model.prompt_tokens", outcome.PromptTokens),
		zap.Int64("custom.model.completion_tokens", outcome.CompletionTokens),
		zap.Int("custom.model.stream.chunk_count", outcome.ChunkCount),
		zap.Int("custom.model.stream.transport_bytes", outcome.TransportBytes),
		zap.String("custom.duration_scope", "admission-through-provider-stream-and-usage-record"),
		zap.Int("custom.retry.attempts", observability.HTTPAttempts(ctx)),
	)
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
