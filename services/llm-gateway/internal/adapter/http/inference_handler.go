package httpAdapter

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	hertzapp "github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/protocol/sse"
	"go.uber.org/zap"
	gatewaygen "teamie/contracts/gen/agent/hertz_gen/agent/gateway"
	"teamie/contracts/gen/agent/hertz_gen/agent/modeltransport"
	"teamie/contracts/gen/agent/kitex_gen/agent"
	"teamie/pkg/httpx"
	"teamie/pkg/observability"
	"teamie/services/llm-gateway/internal/app"
	"teamie/services/llm-gateway/internal/domain"
)

func (a *Adapter) GenerateChat(ctx context.Context, req *gatewaygen.ChatCallReq) (*gatewaygen.ChatCallResp, error) {
	if req == nil || !domain.ValidID(req.OwnerId) || !domain.ValidID(req.AgentId) || !domain.ValidID(req.SessionId) || !domain.ValidID(req.RunId) || req.GetMessageLimit() < 0 || int(req.GetMessageLimit()) > modeltransport.MaxInputMessages {
		return nil, domain.Err(400, "invalid_request", "Invalid model request")
	}
	if req.GetExpectedModelConfigId() != "" {
		ctx = app.WithExpectedModelConfigID(ctx, req.GetExpectedModelConfigId())
	}
	if req.GetModelId() != "" {
		ctx = app.WithRequestedModelID(ctx, req.GetModelId())
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
	if req.GetModelId() != "" {
		ctx = app.WithRequestedModelID(ctx, req.GetModelId())
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

// callWithObservability executes a unary model inference call with observability tracing/logging.
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
	ctx := r.Context()
	if in.ModelID != "" {
		ctx = app.WithRequestedModelID(ctx, in.ModelID)
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
		if in.ModelID != "" {
			req.ModelId = &in.ModelID
		}
		if expected := r.Header.Get(modeltransport.ModelConfigIDHeader); expected != "" {
			req.ExpectedModelConfigId = &expected
		}
		sseCtx := hertzapp.NewContext(0)
		sseCtx.Response.HijackWriter(&httpSSEExtWriter{w: w, rc: sseCtx})
		_ = a.StreamChatSSE(ctx, sseCtx, req)
		return
	}
	ctx, operation := gatewayOperation(ctx, in.AgentID, in.SessionID, in.RunID, true)
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
