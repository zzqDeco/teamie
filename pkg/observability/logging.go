// Package observability owns service logging and context propagation. It knows
// no IM or Agent types and never inspects request/response bodies.
package observability

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	stackerrors "github.com/pkg/errors"
	"go.elastic.co/ecszap"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

type Config struct {
	Service, Version, SHA, Node string
	Output                      io.Writer
	Level                       zapcore.Level
}

// New uses Elastic's encoder; policy/redaction is a native Zap core hook, not
// an alternative JSON formatter. Container logs keep the existing stderr sink.
func New(c Config) *zap.Logger {
	if c.Output == nil {
		c.Output = os.Stderr
	}
	if c.Node == "" {
		c.Node, _ = os.Hostname()
	}
	core := &policyCore{Core: ecszap.NewCore(ecszap.NewDefaultEncoderConfig(), zapcore.Lock(zapcore.AddSync(c.Output)), c.Level)}
	return zap.New(core, zap.AddCaller()).Named(c.Service).With(
		zap.String("service.name", c.Service), zap.String("service.version", c.Version),
		zap.String("service.environment", "production"), zap.String("service.node.name", c.Node),
		zap.String("event.dataset", c.Service), zap.String("labels.commit_sha", c.SHA))
}

// Configure is called once by each service, before clients, workers or servers.
// There is one SDK context owner and no exporter. NeverSample still creates
// valid root/child contexts; log correlation must not depend on recording.
func Configure(service, version, sha string) func() {
	level := zapcore.InfoLevel
	if raw := os.Getenv("LOG_LEVEL"); raw != "" {
		_ = level.UnmarshalText([]byte(raw))
	}
	config := Config{Service: service, Version: version, SHA: sha, Level: level}
	logger := New(config)
	undo := zap.ReplaceGlobals(logger)
	tp := sdktrace.NewTracerProvider(sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.NeverSample())))
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(Propagator())
	installFrameworkLoggers(config)
	logger.Info("Service process initializing", zap.String("event.action", "service-initializing"), zap.String("event.outcome", "success"))
	return func() {
		logger.Info("Service stopped", zap.String("event.action", "service-stopped"), zap.String("event.outcome", "success"))
		_ = tp.Shutdown(context.Background())
		_ = logger.Sync()
		undo()
	}
}

func Propagator() propagation.TextMapPropagator { return propagation.TraceContext{} }

type fieldsKey struct{}

func WithFields(ctx context.Context, fields ...zap.Field) context.Context {
	old, _ := ctx.Value(fieldsKey{}).([]zap.Field)
	out := append([]zap.Field{}, old...)
	for _, field := range fields {
		found := false
		for i := range out {
			if out[i].Key == field.Key {
				out[i], found = field, true
				break
			}
		}
		if !found {
			out = append(out, field)
		}
	}
	return context.WithValue(ctx, fieldsKey{}, out)
}

// L resolves the span when the record is made, never at child-logger creation.
func L(ctx context.Context) *zap.Logger { return correlated(ctx, zap.L()) }

func correlated(ctx context.Context, logger *zap.Logger) *zap.Logger {
	if ctx == nil {
		return logger
	}
	fields, _ := ctx.Value(fieldsKey{}).([]zap.Field)
	fields = append([]zap.Field{}, fields...)
	if sc := trace.SpanContextFromContext(ctx); sc.IsValid() {
		fields = append(fields, zap.String("trace.id", sc.TraceID().String()), zap.String("span.id", sc.SpanID().String()))
	}
	return logger.With(fields...)
}

type Operation struct {
	ctx    context.Context
	span   trace.Span
	action string
	start  time.Time
	once   sync.Once
}

func Begin(ctx context.Context, action string, fields ...zap.Field) (context.Context, *Operation) {
	return begin(ctx, action, trace.SpanKindInternal, fields...)
}

func begin(ctx context.Context, action string, kind trace.SpanKind, fields ...zap.Field) (context.Context, *Operation) {
	if parent := trace.SpanContextFromContext(ctx); parent.IsValid() {
		fields = append(fields, zap.String("custom.trace.parent_span_id", parent.SpanID().String()))
	}
	ctx, span := otel.Tracer("teamie/observability").Start(ctx, action, trace.WithSpanKind(kind))
	ctx = WithFields(ctx, fields...)
	return ctx, &Operation{ctx: ctx, span: span, action: action, start: time.Now()}
}

func (o *Operation) End(err error, fields ...zap.Field) {
	o.end(err, zapcore.ErrorLevel, zapcore.InfoLevel, "", fields...)
}
func (o *Operation) EndQuiet(err error, fields ...zap.Field) {
	o.end(err, zapcore.ErrorLevel, zapcore.DebugLevel, "", fields...)
}
func (o *Operation) EndRecoverable(err error, fields ...zap.Field) {
	o.end(err, zapcore.WarnLevel, zapcore.DebugLevel, "", fields...)
}
func (o *Operation) end(err error, failureLevel, successLevel zapcore.Level, outcomeOverride string, fields ...zap.Field) {
	o.once.Do(func() {
		defer o.span.End()
		outcome := "success"
		level := successLevel
		if err != nil {
			outcome, level = "failure", failureLevel
			o.span.SetStatus(codes.Error, "operation failed")
			if errors.Is(err, context.Canceled) {
				level = zapcore.InfoLevel
			}
			fields = append(fields, zap.Error(err))
		}
		if err == nil && outcomeOverride != "" {
			outcome = outcomeOverride
		}
		fields = append(fields, zap.String("event.action", o.action), zap.String("event.outcome", outcome), zap.Int64("event.duration", time.Since(o.start).Nanoseconds()))
		L(o.ctx).Log(level, "Operation completed", fields...)
	})
}

// CodeError carries a static, owned diagnostic message. Raw causes remain
// available to errors.Is/As; their untrusted text is never logged implicitly.
type CodeError struct {
	Code  string
	Cause error
}

func (e *CodeError) Error() string              { return e.Code }
func (e *CodeError) Unwrap() error              { return e.Cause }
func (e *CodeError) DiagnosticMessage() string  { return e.Code }
func Diagnostic(code string, cause error) error { return &CodeError{Code: code, Cause: cause} }

func Fingerprint(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func errorFields(err error) (error, []zap.Field) {
	fields := []zap.Field{zap.String("error.type", fmt.Sprintf("%T", err))}
	cause := err
	for i := 0; i < 32; i++ {
		next := errors.Unwrap(cause)
		if next == nil {
			break
		}
		cause = next
	}
	fields = append(fields, zap.String("custom.error.cause_type", fmt.Sprintf("%T", cause)), zap.String("custom.error.cause_fingerprint", Fingerprint(cause.Error())))
	message := "Error details withheld; inspect the diagnostic code and fingerprint"
	var owned interface{ DiagnosticMessage() string }
	if errors.As(err, &owned) {
		message = owned.DiagnosticMessage()
	}
	if errors.Is(err, context.Canceled) {
		message = "Operation canceled"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		message = "Operation deadline exceeded"
	}
	var sqlState interface{ SQLState() string }
	if errors.As(err, &sqlState) {
		fields = append(fields, zap.String("custom.database.sqlstate", sqlState.SQLState()))
	}
	fields = append(fields, zap.String("custom.error.fingerprint", Fingerprint(err.Error())))
	// ecszap currently emits error.message/stack_trace but not error.type. The
	// native hook adds type metadata without serializing an error object itself.
	return stackerrors.WithStack(errors.New(message)), fields
}

type policyCore struct{ zapcore.Core }

func (c *policyCore) With(fields []zap.Field) zapcore.Core {
	return &policyCore{c.Core.With(safeFields(fields))}
}
func (c *policyCore) Check(e zapcore.Entry, ce *zapcore.CheckedEntry) *zapcore.CheckedEntry {
	if c.Enabled(e.Level) {
		return ce.AddCore(e, c)
	}
	return ce
}
func (c *policyCore) Write(e zapcore.Entry, fields []zap.Field) error {
	return c.Core.Write(e, safeFields(fields))
}

func safeFields(fields []zap.Field) []zap.Field {
	out := make([]zap.Field, 0, len(fields)+3)
	for _, f := range fields {
		if f.Type == zapcore.ErrorType && f.Interface != nil {
			err, extra := errorFields(f.Interface.(error))
			out = append(out, zap.Error(err))
			out = append(out, extra...)
			continue
		}
		metric := f.Type == zapcore.Int64Type && (strings.HasSuffix(f.Key, ".bytes") || strings.HasSuffix(f.Key, "_bytes") || strings.HasSuffix(f.Key, "_tokens") || strings.HasSuffix(f.Key, "_token_limit") || strings.HasSuffix(f.Key, ".count"))
		// These two typed fields describe response state, never header/body
		// contents. String/object values under the same keys remain redacted.
		httpState := f.Type == zapcore.Int64Type && f.Key == "custom.http.headers_duration_ns" || f.Type == zapcore.BoolType && f.Key == "custom.http.body_consumed_to_eof"
		if sensitiveKey(f.Key) && !metric && !httpState || f.Type == zapcore.ReflectType || f.Type == zapcore.ObjectMarshalerType || f.Type == zapcore.InlineMarshalerType {
			f = zap.String(f.Key, "[REDACTED]")
		}
		out = append(out, f)
	}
	return out
}

func sensitiveKey(key string) bool {
	key = strings.ToLower(key)
	if strings.Contains(key, "api_key") || strings.Contains(key, "private_key") || strings.Contains(key, "access_key") {
		return true
	}
	for _, part := range strings.FieldsFunc(key, func(r rune) bool { return r == '.' || r == '-' || r == '_' }) {
		switch part {
		case "password", "apikey", "csrf", "authorization", "cookie", "cookies", "token", "secret", "credentials", "prompt", "body", "content", "arguments", "args", "stdout", "stderr", "headers", "config":
			return true
		}
	}
	return false
}

// Fatal keeps startup failures structured and excludes error strings that may
// contain connection URLs or production configuration. Existing exit behavior
// is preserved. It is never used for recoverable request/worker errors.
func Fatal(message string, err error) {
	zap.L().Fatal(message, zap.String("event.action", "service-failed"), zap.String("event.outcome", "failure"), zap.Error(err))
}

// ID returns an opaque diagnostic identifier only when bounded and free from
// control characters; untrusted provider/tool identifiers are hashed otherwise.
func ID(value string) string {
	if len(value) > 160 || strings.IndexFunc(value, func(r rune) bool {
		return !(r >= 'a' && r <= 'z') && !(r >= 'A' && r <= 'Z') && !(r >= '0' && r <= '9') && !strings.ContainsRune("_.:-", r)
	}) >= 0 {
		return "sha256:" + Fingerprint(value)
	}
	return value
}
