package observability

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	rpprof "runtime/pprof"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

type responseWriter struct {
	http.ResponseWriter
	status int
	bytes  int64
	err    error
	fields []zap.Field
}

func (w *responseWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
func (w *responseWriter) WriteHeader(status int) {
	if w.status != 0 {
		return
	}
	if status >= 200 || status == http.StatusSwitchingProtocols {
		w.status = status
	}
	w.ResponseWriter.WriteHeader(status)
}
func (w *responseWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	n, err := w.ResponseWriter.Write(b)
	w.bytes += int64(n)
	if err != nil {
		w.err = err
	}
	return n, err
}
func (w *responseWriter) Flush() { _ = w.FlushError() }
func (w *responseWriter) FlushError() error {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	err := http.NewResponseController(w.ResponseWriter).Flush()
	if err != nil {
		w.err = err
	}
	return err
}
func (w *responseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	conn, rw, err := http.NewResponseController(w.ResponseWriter).Hijack()
	if err == nil && w.status == 0 {
		w.status = http.StatusSwitchingProtocols
	}
	if err != nil {
		w.err = err
	}
	return conn, rw, err
}

// RecordError lets an existing HTTP error boundary attach the original failure
// to its one completion event. Responses and public error contracts are intact.
func AnnotateHTTP(w http.ResponseWriter, fields ...zap.Field) {
	for w != nil {
		if state, ok := w.(*responseWriter); ok {
			state.fields = append(state.fields, fields...)
			return
		}
		u, ok := w.(interface{ Unwrap() http.ResponseWriter })
		if !ok {
			return
		}
		w = u.Unwrap()
	}
}

func RecordError(w http.ResponseWriter, err error) {
	for w != nil {
		if state, ok := w.(*responseWriter); ok {
			state.err = err
			return
		}
		u, ok := w.(interface{ Unwrap() http.ResponseWriter })
		if !ok {
			return
		}
		w = u.Unwrap()
	}
}

func HTTP(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		parent := Propagator().Extract(r.Context(), propagation.HeaderCarrier(r.Header))
		if label := r.Header.Get("X-Teamie-Test-Run-ID"); validTestLabel(label) {
			parent = WithFields(parent, zap.String("labels.test_run_id", label))
		}
		ctx, op := begin(parent, "http-request", trace.SpanKindServer)
		r = r.WithContext(ctx)
		state := &responseWriter{ResponseWriter: w}
		defer func() {
			panicValue := recover()
			if panicValue != nil {
				state.err = Diagnostic("http_handler_panic", fmt.Errorf("panic: %v", panicValue))
				if state.status == 0 {
					state.status = http.StatusInternalServerError
				}
			}
			if state.status == 0 {
				state.status = http.StatusOK
			}
			// Route templates exclude invitation/reset tokens and arbitrary path
			// parameters. Never log URL queries, headers or request bodies.
			route := r.Pattern
			if route == "" {
				route = "unmatched"
			}
			fields := []zap.Field{zap.String("http.request.method", r.Method), zap.String("custom.http.route", route), zap.Int("http.response.status_code", state.status), zap.Int64("http.response.body.bytes", state.bytes), zap.String("custom.duration_scope", "handler-and-response-stream")}
			fields = append(fields, state.fields...)
			err := state.err
			if err == nil && state.status >= 500 {
				err = Diagnostic("http_server_error", nil)
			}
			if err == nil && state.status >= 400 && state.status < 500 {
				err = Diagnostic("http_request_rejected", nil)
			}
			if r.Context().Err() != nil {
				err = r.Context().Err()
			}
			if state.status == 401 || state.status == 403 {
				fields = append(fields, zap.Bool("custom.auth.denied", true))
			}
			// Probe suppression is result-aware and restricted to the actual
			// health paths, so spoofing a probe UA cannot hide business traffic.
			if err == nil && state.status >= 200 && state.status < 300 && time.Since(op.start) < time.Second && (r.URL.Path == "/healthz" || r.URL.Path == "/readyz") {
				op.span.End()
				return
			}
			level := zapcore.ErrorLevel
			if state.status < 500 {
				level = zapcore.WarnLevel
			}
			successLevel := zapcore.InfoLevel
			if r.Pattern == "GET /api/v1/sync" || r.Pattern == "POST /api/v1/conversations/{id}/typing" {
				successLevel = zapcore.DebugLevel
			}
			op.end(err, level, successLevel, "", fields...)
			if panicValue != nil {
				panic(panicValue)
			} // Recovery remains owned by Hertz/net/http.
		}()
		rpprof.Do(ctx, rpprof.Labels("component", "http-server", "http.method", safeHTTPMethod(r.Method)), func(pctx context.Context) {
			next.ServeHTTP(state, r.WithContext(pctx))
		})
	})
}

// Transport measures through EOF/Close, including streaming body consumption.
// It adds no deadlines, buffering, body logging or credential-bearing URLs.
type Transport struct {
	Base  http.RoundTripper
	Peer  string
	Quiet bool
}

func (t Transport) CloseIdleConnections() {
	base := t.Base
	if base == nil {
		base = http.DefaultTransport
	}
	if closer, ok := base.(interface{ CloseIdleConnections() }); ok {
		closer.CloseIdleConnections()
	}
}

type httpAttemptsKey struct{}

func WithHTTPAttempts(ctx context.Context) context.Context {
	return context.WithValue(ctx, httpAttemptsKey{}, &atomic.Int32{})
}
func HTTPAttempts(ctx context.Context) int {
	if counter, ok := ctx.Value(httpAttemptsKey{}).(*atomic.Int32); ok {
		return int(counter.Load())
	}
	return 0
}

func (t Transport) RoundTrip(request *http.Request) (*http.Response, error) {
	ctx, op := begin(request.Context(), "http-dependency", trace.SpanKindClient, zap.String("custom.peer.service", t.Peer))
	attempt := 1
	if counter, ok := ctx.Value(httpAttemptsKey{}).(*atomic.Int32); ok {
		attempt = int(counter.Add(1))
	}
	request = request.Clone(ctx)
	request.Header = request.Header.Clone()
	Propagator().Inject(ctx, propagation.HeaderCarrier(request.Header))
	if fields, ok := ctx.Value(fieldsKey{}).([]zap.Field); ok {
		for _, field := range fields {
			if field.Key == "labels.test_run_id" && validTestLabel(field.String) {
				request.Header.Set("X-Teamie-Test-Run-ID", field.String)
			}
		}
	}
	base := t.Base
	if base == nil {
		base = http.DefaultTransport
	}
	response, err := base.RoundTrip(request)
	fields := []zap.Field{zap.String("http.request.method", request.Method), zap.Int("custom.retry.attempt", attempt), zap.String("custom.duration_scope", "request-through-response-body")}
	if err != nil {
		op.End(err, fields...)
		return response, err
	}
	fields = append(fields, zap.Int("http.response.status_code", response.StatusCode), zap.Int64("custom.http.headers_duration_ns", time.Since(op.start).Nanoseconds()))
	var statusErr error
	if response.StatusCode >= 500 {
		statusErr = Diagnostic("dependency_http_error", nil)
	}
	if response.Body == nil {
		op.End(statusErr, fields...)
		return response, nil
	}
	originalBody := response.Body
	body := &responseBody{ReadCloser: originalBody, finish: func(bodyErr error, bytes int64, eof bool) {
		if bodyErr == nil {
			bodyErr = statusErr
		}
		if ctx.Err() != nil {
			bodyErr = ctx.Err()
		}
		outcome := ""
		if !eof && bodyErr == nil {
			outcome = "unknown"
		}
		successLevel := zapcore.InfoLevel
		if t.Quiet || request.Method == http.MethodGet && (request.URL.Path == "/api/v1/sync" || request.URL.Path == "/readyz" || request.URL.Path == "/healthz") || request.Method == http.MethodPost && strings.HasSuffix(request.URL.Path, "/typing") {
			successLevel = zapcore.DebugLevel
		}
		op.end(bodyErr, zapcore.ErrorLevel, successLevel, outcome, append(fields, zap.Int64("http.response.body.bytes", bytes), zap.Bool("custom.http.body_consumed_to_eof", eof))...)
	}}
	if originalBody == http.NoBody {
		body.eof.Store(true)
	}
	response.Body = body
	// Docker attach upgrades HTTP to a duplex connection. Preserve its native
	// io.ReadWriteCloser contract; logging must never break the command gate.
	if writer, ok := originalBody.(io.Writer); ok {
		response.Body = &readWriteBody{responseBody: body, writer: writer}
	}
	return response, nil
}

func validTestLabel(value string) bool {
	if len(value) == 0 || len(value) > 64 {
		return false
	}
	for _, r := range value {
		if !(r >= 'a' && r <= 'z') && !(r >= 'A' && r <= 'Z') && !(r >= '0' && r <= '9') && r != '-' && r != '_' && r != '.' {
			return false
		}
	}
	return true
}

type readWriteBody struct {
	*responseBody
	writer io.Writer
}

func (b *readWriteBody) Write(p []byte) (int, error) {
	n, err := b.writer.Write(p)
	if err != nil {
		b.complete(err)
	}
	return n, err
}

type responseBody struct {
	io.ReadCloser
	once   sync.Once
	finish func(error, int64, bool)
	bytes  atomic.Int64
	eof    atomic.Bool
}

func (b *responseBody) complete(err error) {
	b.once.Do(func() { b.finish(err, b.bytes.Load(), b.eof.Load()) })
}
func (b *responseBody) Read(dst []byte) (int, error) {
	n, err := b.ReadCloser.Read(dst)
	b.bytes.Add(int64(n))
	if errors.Is(err, io.EOF) {
		b.eof.Store(true)
		b.complete(nil)
	} else if err != nil {
		b.complete(err)
	}
	return n, err
}
func (b *responseBody) Close() error {
	err := b.ReadCloser.Close()
	if err != nil {
		b.complete(err)
	} else {
		b.complete(nil)
	}
	return err
}

// Client preserves timeouts and transport policy while enabling correlation.
func Client(base *http.Client, peer string) *http.Client {
	if base == nil {
		base = http.DefaultClient
	}
	copy := *base
	copy.Transport = Transport{Base: base.Transport, Peer: strings.TrimSpace(peer)}
	return &copy
}

var _ http.Hijacker = (*responseWriter)(nil)
var _ http.Flusher = (*responseWriter)(nil)
