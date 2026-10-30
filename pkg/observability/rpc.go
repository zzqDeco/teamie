package observability

import (
	"context"
	"reflect"
	rpprof "runtime/pprof"
	"strconv"

	"github.com/bytedance/gopkg/cloud/metainfo"
	"github.com/cloudwego/kitex/client"
	"github.com/cloudwego/kitex/pkg/endpoint"
	"github.com/cloudwego/kitex/pkg/rpcinfo"
	"github.com/cloudwego/kitex/server"
	"github.com/cloudwego/kitex/transport"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// Transient metainfo is serialized by Kitex's native TTHeader handler. It is
// overwritten from the current operation on every call, never forwarded as a
// stale upstream parent. No Thrift business schema or credentials are changed.
func RPCClient() client.Option { return client.WithSuite(clientSuite{}) }

type clientSuite struct{}

func (clientSuite) Options() []client.Option {
	return []client.Option{client.WithTransportProtocol(transport.TTHeaderFramed), client.WithMiddleware(rpcMiddleware(false))}
}
func RPCServer() server.Option { return server.WithMiddleware(rpcMiddleware(true)) }

var rpcFields = map[string]string{
	"labels.test_run_id":        "teamie-test-run-id",
	"custom.agent.id":           "teamie-agent-id",
	"custom.runtime.session_id": "teamie-session-id",
	"custom.runtime.run_id":     "teamie-run-id",
	"custom.runtime.attempt_id": "teamie-attempt-id",
	"custom.tool.call_id":       "teamie-call-id",
	"custom.model.step":         "teamie-model-step",
}

func rpcMiddleware(inbound bool) endpoint.Middleware {
	return func(next endpoint.Endpoint) endpoint.Endpoint {
		return func(ctx context.Context, request, response any) (err error) {
			kind, action := trace.SpanKindClient, "rpc-client"
			if inbound {
				carrier := propagation.MapCarrier{}
				for _, key := range []string{"traceparent", "tracestate"} {
					if value, ok := metainfo.GetValue(ctx, key); ok {
						carrier[key] = value
					}
				}
				ctx = Propagator().Extract(ctx, carrier)
				for key, meta := range rpcFields {
					if value, ok := metainfo.GetValue(ctx, meta); ok && value != "" {
						if key == "labels.test_run_id" && !validTestLabel(value) {
							continue
						}
						if key == "custom.model.step" {
							if n, e := strconv.ParseInt(value, 10, 64); e == nil && n >= 0 {
								ctx = WithFields(ctx, zap.Int64(key, n))
							}
							continue
						}
						ctx = WithFields(ctx, zap.String(key, ID(value)))
					}
				}
				kind, action = trace.SpanKindServer, "rpc-server"
			}
			method, service := "unknown", "unknown"
			if info := rpcinfo.GetRPCInfo(ctx); info != nil && info.Invocation() != nil {
				method, service = info.Invocation().MethodName(), info.Invocation().ServiceName()
			}
			ctx, op := begin(ctx, action, kind, zap.String("custom.rpc.system", "kitex"), zap.String("custom.rpc.method", method), zap.String("custom.rpc.service", service))
			if !inbound {
				carrier := propagation.MapCarrier{}
				Propagator().Inject(ctx, carrier)
				for _, key := range []string{"traceparent", "tracestate"} {
					ctx = metainfo.DelValue(ctx, key)
					if value := carrier[key]; value != "" {
						ctx = metainfo.WithValue(ctx, key, value)
					}
				}
				fields, _ := ctx.Value(fieldsKey{}).([]zap.Field)
				for _, f := range fields {
					if meta, ok := rpcFields[f.Key]; ok {
						if f.Key == "labels.test_run_id" && !validTestLabel(f.String) {
							continue
						}
						value := f.String
						if f.Type == zapcore.Int64Type {
							value = strconv.FormatInt(f.Integer, 10)
						}
						ctx = metainfo.WithValue(ctx, meta, ID(value))
					}
				}
			}
			defer func() {
				code := responseCode(response)
				failure := err
				if failure == nil && code != "" {
					failure = Diagnostic("rpc_business_failure", nil)
				}
				successLevel := zapcore.InfoLevel
				switch method {
				case "Ping", "ListAgents", "ReportRuntime", "ValidateSession", "ValidatePrincipalToken", "GetTask", "ReadTaskOutput", "CheckAccess", "GetWorkspaces", "GetUsers", "GetParticipantWorkspaces", "GetWorkspaceParticipants", "Get":
					successLevel = zapcore.DebugLevel
				}
				op.end(failure, zapcore.ErrorLevel, successLevel, "", zap.String("custom.rpc.error_code", code), zap.String("custom.duration_scope", "single-rpc-invocation"))
			}()
			if inbound {
				var callErr error
				rpprof.Do(ctx, rpprof.Labels("component", "rpc-server", "rpc.service", ID(service), "rpc.method", ID(method)), func(pctx context.Context) {
					callErr = next(pctx, request, response)
				})
				return callErr
			}
			return next(ctx, request, response)
		}
	}
}

// Only the generated result's Success envelope is inspected. No reflection of
// arguments, body fields, tokens, filesystem paths or model results is logged.
func responseCode(response any) string {
	v := reflect.ValueOf(response)
	if v.Kind() != reflect.Pointer || v.IsNil() {
		return ""
	}
	v = v.Elem()
	if v.Kind() != reflect.Struct {
		return ""
	}
	success := v.FieldByName("Success")
	if !success.IsValid() || !success.CanInterface() {
		return ""
	}
	if failure, ok := success.Interface().(interface {
		GetOk() bool
		GetErrorCode() string
	}); ok && !failure.GetOk() {
		if code := failure.GetErrorCode(); code != "" {
			return ID(code)
		}
		return "operation_rejected"
	}
	if health, ok := success.Interface().(interface{ GetReady() bool }); ok && !health.GetReady() {
		return "not_ready"
	}
	return ""
}
