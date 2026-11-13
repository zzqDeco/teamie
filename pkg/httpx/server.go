package httpx

import (
	"bytes"
	"context"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/cloudwego/hertz/pkg/common/adaptor"
	"github.com/cloudwego/hertz/pkg/common/config"
	"github.com/cloudwego/hertz/pkg/route"
	"go.uber.org/zap"
	"teamie/pkg/observability"
)

const (
	// DefaultDrainGrace is the pre-shutdown readiness drain window during which
	// /readyz returns 503 while HTTP and RPC listeners continue serving traffic.
	DefaultDrainGrace = 2000 * time.Millisecond

	// DefaultShutdownTimeout bounds graceful HTTP/RPC in-flight completion after drain grace.
	DefaultShutdownTimeout = 20 * time.Second
)

var (
	readyzPathBytes   = []byte("/readyz")
	drainingBodyBytes = []byte("{\"status\":\"draining\"}\n")
	serverControllers sync.Map // *server.Hertz -> *DrainController
)

type hertzRequestContextKey struct{}

// WithHertzContext attaches the active Hertz RequestContext to ctx so adapted handlers
// can access native Hertz streaming writers (such as sse.NewWriter).
func WithHertzContext(ctx context.Context, c *app.RequestContext) context.Context {
	return context.WithValue(ctx, hertzRequestContextKey{}, c)
}

// HertzContext retrieves the active Hertz RequestContext from ctx if running inside Hertz.
func HertzContext(ctx context.Context) *app.RequestContext {
	if c, ok := ctx.Value(hertzRequestContextKey{}).(*app.RequestContext); ok {
		return c
	}
	return nil
}

// HertzRouteRegistrar is implemented by HTTP handlers that register their
// generated Thrift IDL routes directly onto a native Hertz route.RouterGroup.
type HertzRouteRegistrar interface {
	RegisterHertz(r *route.RouterGroup)
}

type hertzRouter struct {
	http.Handler
	register func(r *route.RouterGroup)
}

func (hr hertzRouter) RegisterHertz(r *route.RouterGroup) {
	if hr.register != nil {
		hr.register(r)
	}
}

// WrapHertzRouter returns an http.Handler that also implements HertzRouteRegistrar
// so both httptest/ServeHTTP callers and native Hertz servers share the same adapter.
func WrapHertzRouter(mux http.Handler, register func(r *route.RouterGroup)) http.Handler {
	return hertzRouter{Handler: mux, register: register}
}

// ParseDrainGrace parses TEAMIE_DRAIN_GRACE, falling back to DefaultDrainGrace when unset or invalid.
func ParseDrainGrace() time.Duration {
	raw := strings.TrimSpace(os.Getenv("TEAMIE_DRAIN_GRACE"))
	if raw == "" {
		return DefaultDrainGrace
	}
	if raw == "0" {
		return 0
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d < 0 {
		return DefaultDrainGrace
	}
	return d
}

// LifecycleOption configures a DrainController.
type LifecycleOption func(*DrainController)

// WithDrainGrace overrides the readiness drain grace duration before server shutdown begins.
func WithDrainGrace(grace time.Duration) LifecycleOption {
	return func(d *DrainController) {
		if grace < 0 {
			grace = 0
		}
		d.mu.Lock()
		d.drainGrace = grace
		d.mu.Unlock()
	}
}

// WithShutdownTimeout overrides the bounded timeout for pre-shutdown and shutdown hooks.
func WithShutdownTimeout(timeout time.Duration) LifecycleOption {
	return func(d *DrainController) {
		if timeout <= 0 {
			timeout = DefaultShutdownTimeout
		}
		d.mu.Lock()
		d.shutdownTimeout = timeout
		d.mu.Unlock()
	}
}

// OnDrainStart registers a fast callback invoked synchronously as soon as drain starts
// (at t=0, before the drain grace window elapses).
func OnDrainStart(fn func()) LifecycleOption {
	return func(d *DrainController) {
		d.AddOnDrainStart(fn)
	}
}

// OnPreShutdown registers a hook executed after the drain grace window and right before
// Hertz begins closing HTTP connections (for example, sending WebSocket 1001 Going Away frames).
func OnPreShutdown(fn func(context.Context)) LifecycleOption {
	return func(d *DrainController) {
		d.AddPreShutdownHook(fn)
	}
}

// OnShutdown registers a hook executed concurrently with Hertz HTTP graceful shutdown
// after pre-shutdown hooks complete (for example, stopping a Kitex RPC server).
func OnShutdown(fn func(context.Context)) LifecycleOption {
	return func(d *DrainController) {
		d.AddShutdownHook(fn)
	}
}

// DrainController coordinates the unified graceful drain state machine for HTTP and RPC servers.
type DrainController struct {
	draining        atomic.Bool
	once            sync.Once
	drainCh         chan struct{}
	mu              sync.Mutex
	drainGrace      time.Duration
	shutdownTimeout time.Duration
	onDrainStart    []func()
	preShutdown     []func(context.Context)
	onShutdown      []func(context.Context)
}

// NewDrainController creates a DrainController initialized from TEAMIE_DRAIN_GRACE and opts.
func NewDrainController(opts ...LifecycleOption) *DrainController {
	d := &DrainController{
		drainCh:         make(chan struct{}),
		drainGrace:      ParseDrainGrace(),
		shutdownTimeout: DefaultShutdownTimeout,
	}
	d.Apply(opts...)
	return d
}

// Apply applies LifecycleOptions to an existing DrainController.
func (d *DrainController) Apply(opts ...LifecycleOption) {
	for _, opt := range opts {
		if opt != nil {
			opt(d)
		}
	}
}

// Draining reports whether the service has entered the draining state.
func (d *DrainController) Draining() bool {
	return d != nil && d.draining.Load()
}

// DrainChan closes as soon as BeginDrain is triggered.
func (d *DrainController) DrainChan() <-chan struct{} {
	return d.drainCh
}

// AddOnDrainStart appends a synchronous callback invoked when drain starts.
func (d *DrainController) AddOnDrainStart(fn func()) {
	if d == nil || fn == nil {
		return
	}
	d.mu.Lock()
	if d.draining.Load() {
		d.mu.Unlock()
		fn()
		return
	}
	d.onDrainStart = append(d.onDrainStart, fn)
	d.mu.Unlock()
}

// AddPreShutdownHook appends a hook executed after drain grace and before HTTP shutdown.
func (d *DrainController) AddPreShutdownHook(fn func(context.Context)) {
	if d == nil || fn == nil {
		return
	}
	d.mu.Lock()
	d.preShutdown = append(d.preShutdown, fn)
	d.mu.Unlock()
}

// AddShutdownHook appends a hook executed concurrently with Hertz HTTP shutdown.
func (d *DrainController) AddShutdownHook(fn func(context.Context)) {
	if d == nil || fn == nil {
		return
	}
	d.mu.Lock()
	d.onShutdown = append(d.onShutdown, fn)
	d.mu.Unlock()
}

// BeginDrain transitions the controller into draining state immediately (idempotent).
func (d *DrainController) BeginDrain() bool {
	if d == nil {
		return false
	}
	transitioned := false
	d.once.Do(func() {
		transitioned = true
		d.mu.Lock()
		d.draining.Store(true)
		grace := d.drainGrace
		callbacks := append([]func(){}, d.onDrainStart...)
		d.mu.Unlock()

		for _, fn := range callbacks {
			fn()
		}
		close(d.drainCh)
		observability.L(nil).Info(
			"service draining",
			zap.String("event.action", "service-draining"),
			zap.Int64("event.duration", grace.Nanoseconds()),
		)
	})
	return transitioned
}

func (d *DrainController) snapshotShutdownConfig() (time.Duration, time.Duration, []func(context.Context), []func(context.Context)) {
	d.mu.Lock()
	defer d.mu.Unlock()
	pre := append([]func(context.Context){}, d.preShutdown...)
	shut := append([]func(context.Context){}, d.onShutdown...)
	return d.drainGrace, d.shutdownTimeout, pre, shut
}

// DrainControllerFor returns (or initializes) the DrainController attached to h.
func DrainControllerFor(h *server.Hertz) *DrainController {
	if h == nil {
		return NewDrainController()
	}
	if v, ok := serverControllers.Load(h); ok {
		return v.(*DrainController)
	}
	ctrl := NewDrainController()
	actual, _ := serverControllers.LoadOrStore(h, ctrl)
	return actual.(*DrainController)
}

// MountHertz mounts handler onto a Hertz server, registering native RouterGroup
// routes when handler implements HertzRouteRegistrar and gating /readyz on drain state.
func MountHertz(h *server.Hertz, handler http.Handler) {
	MountHertzWithDrain(h, handler, DrainControllerFor(h))
}

// MountHertzWithDrain mounts handler onto h using an explicit DrainController.
func MountHertzWithDrain(h *server.Hertz, handler http.Handler, ctrl *DrainController) {
	if ctrl == nil {
		ctrl = DrainControllerFor(h)
	} else if h != nil {
		serverControllers.Store(h, ctrl)
	}

	h.Use(func(ctx context.Context, c *app.RequestContext) {
		if ctrl.Draining() {
			if bytes.Equal(c.Path(), readyzPathBytes) {
				c.Response.Header.SetConnectionClose(true)
				c.Response.Header.SetContentTypeBytes([]byte("application/json"))
				c.SetStatusCode(http.StatusServiceUnavailable)
				if string(c.Method()) != http.MethodHead {
					c.Response.SetBodyRaw(drainingBodyBytes)
				}
				c.Abort()
				return
			}
			c.Next(WithHertzContext(ctx, c))
			if c.Response.StatusCode() != http.StatusSwitchingProtocols && c.Response.GetHijackWriter() == nil {
				c.Response.Header.SetConnectionClose(true)
			}
			return
		}
		c.Next(WithHertzContext(ctx, c))
	})

	obs := observability.HTTP(handler)
	adapted := adaptor.HertzHandler(obs)
	if reg, ok := handler.(HertzRouteRegistrar); ok {
		reg.RegisterHertz(&h.RouterGroup)
		h.GET("/healthz", adapted)
		h.GET("/readyz", adapted)
		h.GET("/version", adapted)
		noRoute := func(ctx context.Context, c *app.RequestContext) {
			c.SetStatusCode(http.StatusOK)
			adapted(ctx, c)
			if c.Response.GetHijackWriter() != nil && !c.Response.HasBodyBytes() {
				c.Response.BodyBuffer().WriteByte(0)
			}
		}
		h.NoRoute(noRoute)
		h.NoMethod(noRoute)
		return
	}
	h.Any("/*path", adapted)
}

// NewServer constructs a standard Hertz HTTP server with streaming body and disabled
// multipart pre-parsing so handlers authenticate and enforce capacity bounds first.
func NewServer(addr string, handler http.Handler, options ...config.Option) *server.Hertz {
	defaults := []config.Option{
		server.WithHostPorts(addr),
		server.WithMaxRequestBodySize(131072),
		server.WithStreamBody(true),
		server.WithDisablePreParseMultipartForm(true),
		server.WithReadTimeout(15 * time.Second),
		server.WithWriteTimeout(30 * time.Second),
	}
	h := server.Default(append(defaults, options...)...)
	MountHertz(h, handler)
	return h
}

// RunServer runs h until SIGINT/SIGTERM or programmatic BeginDrain, then executes
// the deterministic graceful drain sequence:
//  1. Flip /readyz to 503 Service Unavailable immediately while keeping /healthz 200.
//  2. Keep HTTP and RPC listeners open during TEAMIE_DRAIN_GRACE.
//  3. Run pre-shutdown hooks (e.g. WebSocket 1001 Going Away frames).
//  4. Gracefully shut down Hertz HTTP and registered shutdown hooks (e.g. Kitex RPC).
func RunServer(h *server.Hertz, opts ...LifecycleOption) {
	ctrl := DrainControllerFor(h)
	ctrl.Apply(opts...)
	defer serverControllers.Delete(h)

	signals := []os.Signal{syscall.SIGINT, syscall.SIGTERM}
	if !signal.Ignored(syscall.SIGHUP) {
		signals = append(signals, syscall.SIGHUP)
	}
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, signals...)
	defer signal.Stop(sigCh)

	errCh := make(chan error, 1)
	go func() {
		errCh <- h.Run()
	}()

	runExited := false
	select {
	case <-sigCh:
		ctrl.BeginDrain()
	case <-ctrl.DrainChan():
	case err := <-errCh:
		runExited = true
		ctrl.BeginDrain()
		if err != nil {
			observability.L(nil).Error(
				"http server exited unexpectedly",
				zap.String("event.action", "service-drain-error"),
				zap.Error(observability.Diagnostic("http_server_run_failed", err)),
			)
		}
	}

	grace, shutdownTimeout, preHooks, shutdownHooks := ctrl.snapshotShutdownConfig()
	if !runExited && grace > 0 {
		timer := time.NewTimer(grace)
		select {
		case <-timer.C:
		case err := <-errCh:
			runExited = true
			timer.Stop()
			if err != nil {
				observability.L(nil).Error(
					"http server exited during drain grace",
					zap.String("event.action", "service-drain-error"),
					zap.Error(observability.Diagnostic("http_server_run_failed", err)),
				)
			}
		}
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()

	for _, hook := range preHooks {
		hook(shutdownCtx)
	}

	var wg sync.WaitGroup
	if !runExited {
		if opt := h.GetOptions(); opt != nil {
			opt.ExitWaitTimeout = shutdownTimeout
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := h.Shutdown(shutdownCtx); err != nil {
				observability.L(nil).Warn(
					"http server graceful shutdown incomplete",
					zap.String("event.action", "service-drain-timeout"),
					zap.Error(observability.Diagnostic("http_server_shutdown_failed", err)),
				)
			}
		}()
	}
	for _, hook := range shutdownHooks {
		hfn := hook
		wg.Add(1)
		go func() {
			defer wg.Done()
			hfn(shutdownCtx)
		}()
	}
	wg.Wait()

	if !runExited {
		select {
		case <-errCh:
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// Serve starts a Hertz HTTP server with streaming body, disabled multipart pre-parsing,
// and unified SIGTERM/SIGINT graceful draining.
func Serve(addr string, handler http.Handler) {
	ServeLifecycle(addr, handler)
}

// ServeLifecycle starts a standard Hertz HTTP server with unified graceful draining and hooks.
func ServeLifecycle(addr string, handler http.Handler, opts ...LifecycleOption) {
	h := NewServer(addr, handler)
	RunServer(h, opts...)
}

// ServeWithOptions starts a Hertz HTTP server with longer default write timeout (6m),
// caller-supplied option overrides, and unified SIGTERM/SIGINT graceful draining.
func ServeWithOptions(addr string, handler http.Handler, options ...config.Option) {
	ServeWithOptionsLifecycle(addr, handler, nil, options...)
}

// ServeWithOptionsLifecycle starts a NewServerWithOptions Hertz server with unified graceful draining and hooks.
func ServeWithOptionsLifecycle(addr string, handler http.Handler, lifecycleOpts []LifecycleOption, options ...config.Option) {
	h := NewServerWithOptions(addr, handler, options...)
	RunServer(h, lifecycleOpts...)
}

// NewServerWithOptions constructs a Hertz server with 516 KiB max body, 15s read timeout,
// 6m write timeout, and client disconnection sensing, plus any caller option overrides.
func NewServerWithOptions(addr string, handler http.Handler, options ...config.Option) *server.Hertz {
	defaults := []config.Option{
		server.WithHostPorts(addr),
		server.WithMaxRequestBodySize(528384),
		server.WithReadTimeout(15 * time.Second),
		server.WithWriteTimeout(6 * time.Minute),
		server.WithSenseClientDisconnection(true),
	}
	h := server.Default(append(defaults, options...)...)
	MountHertz(h, handler)
	return h
}
