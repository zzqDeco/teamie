package observability

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	netpprof "net/http/pprof"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

const (
	maxCPUProfileSeconds       = 60
	maxTraceSeconds            = 10
	maxDeltaProfileSeconds     = 30
	defaultBlockProfileRate    = 10_000 // 10µs per blocking event during windowed capture
	defaultMutexProfileFrac    = 5      // 1/5 mutex contention events during windowed capture
	pprofServerWriteTimeoutSec = 75
)

// pprofSampling serializes active time-windowed captures (CPU, execution trace,
// and delta heap/goroutine/block/mutex windows) across the entire Go process so
// overlapping operator requests cannot clobber runtime rates or exhaust tight
// container memory limits.
var pprofSampling atomic.Bool

func normalizePprofAddr(raw string) (addr string, disabled bool, err error) {
	trimmed := strings.TrimSpace(raw)
	switch strings.ToLower(trimmed) {
	case "", "off", "false", "disabled", "none", "0":
		return "", true, nil
	}
	host, portStr, splitErr := net.SplitHostPort(trimmed)
	if splitErr != nil {
		return "", false, Diagnostic("pprof_invalid_addr", splitErr)
	}
	port, portErr := strconv.Atoi(portStr)
	if portErr != nil || port < 0 || port > 65535 {
		return "", false, Diagnostic("pprof_invalid_port", fmt.Errorf("invalid port %q", portStr))
	}
	if host == "" {
		return "", false, Diagnostic("pprof_non_loopback_addr", errors.New("wildcard host is forbidden for pprof"))
	}
	if strings.EqualFold(host, "localhost") {
		return net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), false, nil
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return "", false, Diagnostic("pprof_non_loopback_addr", fmt.Errorf("non-loopback host %q is forbidden for pprof", host))
	}
	return net.JoinHostPort(ip.String(), strconv.Itoa(port)), false, nil
}

// StartPprof starts an isolated, loopback-only HTTP server serving guarded Go
// runtime pprof endpoints. It never registers routes on business HTTP servers
// and rejects any non-loopback bind address.
func StartPprof(rawAddr string) (boundAddr string, stop func(context.Context) error, err error) {
	normalized, disabled, err := normalizePprofAddr(rawAddr)
	if err != nil {
		zap.L().Error("Rejected unsafe pprof address",
			zap.String("event.action", "pprof-rejected"),
			zap.String("event.outcome", "failure"),
			zap.Error(err),
		)
		return "", nil, err
	}
	if disabled {
		return "", nil, nil
	}

	ln, listenErr := net.Listen("tcp", normalized)
	if listenErr != nil {
		wrapped := Diagnostic("pprof_listen_failed", listenErr)
		zap.L().Error("Failed to bind pprof loopback listener",
			zap.String("event.action", "pprof-listen-failed"),
			zap.String("event.outcome", "failure"),
			zap.Error(wrapped),
		)
		return "", nil, wrapped
	}
	if tcpAddr, ok := ln.Addr().(*net.TCPAddr); !ok || !tcpAddr.IP.IsLoopback() {
		_ = ln.Close()
		wrapped := Diagnostic("pprof_non_loopback_addr", errors.New("resolved listener is not loopback"))
		zap.L().Error("Closed non-loopback pprof listener",
			zap.String("event.action", "pprof-rejected"),
			zap.String("event.outcome", "failure"),
			zap.Error(wrapped),
		)
		return "", nil, wrapped
	}

	actualAddr := ln.Addr().String()
	srv := &http.Server{
		Handler:           newPprofMux(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      pprofServerWriteTimeoutSec * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	zap.L().Info("Loopback pprof listener started",
		zap.String("event.action", "pprof-listening"),
		zap.String("event.outcome", "success"),
		zap.String("custom.pprof.addr", actualAddr),
	)

	go func() {
		if serveErr := srv.Serve(ln); serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			zap.L().Warn("Loopback pprof listener stopped unexpectedly",
				zap.String("event.action", "pprof-failed"),
				zap.String("event.outcome", "failure"),
				zap.Error(Diagnostic("pprof_serve_failed", serveErr)),
			)
		}
	}()

	var stopped atomic.Bool
	return actualAddr, func(ctx context.Context) error {
		if !stopped.CompareAndSwap(false, true) {
			return nil
		}
		shutdownErr := srv.Shutdown(ctx)
		zap.L().Info("Loopback pprof listener stopped",
			zap.String("event.action", "pprof-stopped"),
			zap.String("event.outcome", "success"),
			zap.String("custom.pprof.addr", actualAddr),
		)
		return shutdownErr
	}, nil
}

func configurePprof(rawAddr string) func(context.Context) error {
	_, stop, _ := StartPprof(rawAddr)
	return stop
}

func newPprofMux() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET /debug/pprof/", auditPprof("index", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/debug/pprof/" {
			writePprofError(w, http.StatusNotFound, "unknown pprof profile")
			return
		}
		if _, ok := parseDebugParam(w, r); !ok {
			return
		}
		netpprof.Index(w, r)
	})))
	mux.Handle("GET /debug/pprof/cmdline", auditPprof("cmdline", http.HandlerFunc(serveSanitizedCmdline)))
	mux.Handle("GET /debug/pprof/symbol", auditPprof("symbol", http.HandlerFunc(netpprof.Symbol)))
	mux.Handle("POST /debug/pprof/symbol", auditPprof("symbol", http.HandlerFunc(netpprof.Symbol)))
	mux.Handle("GET /debug/pprof/profile", auditPprof("profile", http.HandlerFunc(serveCPUProfile)))
	mux.Handle("GET /debug/pprof/trace", auditPprof("trace", http.HandlerFunc(serveExecutionTrace)))

	for _, name := range []string{"heap", "allocs", "goroutine", "threadcreate", "block", "mutex"} {
		profileName := name
		mux.Handle("GET /debug/pprof/"+profileName, auditPprof(profileName, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			serveNamedProfile(w, r, profileName)
		})))
	}
	return mux
}

func serveSanitizedCmdline(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	base := "service"
	if len(os.Args) > 0 && os.Args[0] != "" {
		base = filepath.Base(os.Args[0])
	}
	_, _ = w.Write([]byte(base))
}

func serveCPUProfile(w http.ResponseWriter, r *http.Request) {
	if len(r.URL.Query()["debug"]) > 0 {
		writePprofError(w, http.StatusBadRequest, "debug parameter is not supported for CPU profile")
		return
	}
	sec, ok := parseWindowSeconds(w, r, 30, maxCPUProfileSeconds)
	if !ok {
		return
	}
	setRecordedSeconds(w, sec)
	if !pprofSampling.CompareAndSwap(false, true) {
		writePprofError(w, http.StatusConflict, "another active profile capture is already in progress")
		return
	}
	defer pprofSampling.Store(false)
	netpprof.Profile(w, r)
}

func serveExecutionTrace(w http.ResponseWriter, r *http.Request) {
	if len(r.URL.Query()["debug"]) > 0 {
		writePprofError(w, http.StatusBadRequest, "debug parameter is not supported for execution trace")
		return
	}
	sec, ok := parseWindowSeconds(w, r, 1, maxTraceSeconds)
	if !ok {
		return
	}
	setRecordedSeconds(w, sec)
	if !pprofSampling.CompareAndSwap(false, true) {
		writePprofError(w, http.StatusConflict, "another active profile capture is already in progress")
		return
	}
	defer pprofSampling.Store(false)
	netpprof.Trace(w, r)
}

func serveNamedProfile(w http.ResponseWriter, r *http.Request, name string) {
	debug, ok := parseDebugParam(w, r)
	if !ok {
		return
	}
	if name == "heap" {
		if !validateGCParam(w, r) {
			return
		}
	} else if len(r.URL.Query()["gc"]) > 0 {
		writePprofError(w, http.StatusBadRequest, "gc parameter is only valid for heap profile")
		return
	}

	sec, ok := parseOptionalDeltaSeconds(w, r, maxDeltaProfileSeconds)
	if !ok {
		return
	}
	if sec > 0 && debug != 0 {
		writePprofError(w, http.StatusBadRequest, "seconds and debug parameters cannot be combined")
		return
	}
	setRecordedSeconds(w, sec)

	if sec > 0 {
		if !pprofSampling.CompareAndSwap(false, true) {
			writePprofError(w, http.StatusConflict, "another active profile capture is already in progress")
			return
		}
		defer pprofSampling.Store(false)

		switch name {
		case "block":
			runtime.SetBlockProfileRate(defaultBlockProfileRate)
			defer runtime.SetBlockProfileRate(0)
		case "mutex":
			prev := runtime.SetMutexProfileFraction(defaultMutexProfileFrac)
			if prev > 0 {
				defer runtime.SetMutexProfileFraction(prev)
			} else {
				defer runtime.SetMutexProfileFraction(0)
			}
		}
	}

	netpprof.Handler(name).ServeHTTP(w, r)
}

func parseDebugParam(w http.ResponseWriter, r *http.Request) (int, bool) {
	vals := r.URL.Query()["debug"]
	if len(vals) == 0 || (len(vals) == 1 && vals[0] == "") {
		return 0, true
	}
	if len(vals) > 1 {
		writePprofError(w, http.StatusBadRequest, "duplicate debug parameter")
		return 0, false
	}
	switch vals[0] {
	case "0":
		return 0, true
	case "1":
		return 1, true
	default:
		writePprofError(w, http.StatusBadRequest, "debug parameter must be 0 or 1")
		return 0, false
	}
}

func validateGCParam(w http.ResponseWriter, r *http.Request) bool {
	vals := r.URL.Query()["gc"]
	if len(vals) == 0 || (len(vals) == 1 && (vals[0] == "" || vals[0] == "0" || vals[0] == "1")) {
		return true
	}
	writePprofError(w, http.StatusBadRequest, "gc parameter must be 0 or 1")
	return false
}

func parseWindowSeconds(w http.ResponseWriter, r *http.Request, defaultSec, maxSec int) (int, bool) {
	vals := r.URL.Query()["seconds"]
	if len(vals) == 0 || (len(vals) == 1 && vals[0] == "") {
		return defaultSec, true
	}
	if len(vals) > 1 {
		writePprofError(w, http.StatusBadRequest, "duplicate seconds parameter")
		return 0, false
	}
	sec, err := strconv.Atoi(vals[0])
	if err != nil || sec < 1 || sec > maxSec {
		writePprofError(w, http.StatusBadRequest, fmt.Sprintf("seconds must be an integer between 1 and %d", maxSec))
		return 0, false
	}
	return sec, true
}

func parseOptionalDeltaSeconds(w http.ResponseWriter, r *http.Request, maxSec int) (int, bool) {
	vals := r.URL.Query()["seconds"]
	if len(vals) == 0 || (len(vals) == 1 && (vals[0] == "" || vals[0] == "0")) {
		return 0, true
	}
	if len(vals) > 1 {
		writePprofError(w, http.StatusBadRequest, "duplicate seconds parameter")
		return 0, false
	}
	sec, err := strconv.Atoi(vals[0])
	if err != nil || sec < 1 || sec > maxSec {
		writePprofError(w, http.StatusBadRequest, fmt.Sprintf("seconds must be 0 or an integer between 1 and %d", maxSec))
		return 0, false
	}
	return sec, true
}

func writePprofError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(msg + "\n"))
}

type pprofAuditWriter struct {
	http.ResponseWriter
	status  int
	bytes   int64
	seconds int
}

func (w *pprofAuditWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *pprofAuditWriter) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
	w.ResponseWriter.WriteHeader(status)
}

func (w *pprofAuditWriter) Write(p []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	n, err := w.ResponseWriter.Write(p)
	w.bytes += int64(n)
	return n, err
}

func (w *pprofAuditWriter) Flush() {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	_ = http.NewResponseController(w.ResponseWriter).Flush()
}

func setRecordedSeconds(w http.ResponseWriter, sec int) {
	if aw, ok := w.(*pprofAuditWriter); ok {
		aw.seconds = sec
	}
}

func auditPprof(profile string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Cache-Control", "no-store")
		aw := &pprofAuditWriter{ResponseWriter: w}
		defer func() {
			status := aw.status
			if status == 0 {
				status = http.StatusOK
			}
			outcome := "success"
			level := zapcore.InfoLevel
			if status >= 500 {
				outcome = "failure"
				level = zapcore.ErrorLevel
			} else if status >= 400 || r.Context().Err() != nil {
				outcome = "failure"
				level = zapcore.WarnLevel
			}
			zap.L().Log(level, "Profile capture completed",
				zap.String("event.action", "pprof-capture"),
				zap.String("event.outcome", outcome),
				zap.Int64("event.duration", time.Since(start).Nanoseconds()),
				zap.String("custom.pprof.profile", profile),
				zap.Int("custom.pprof.seconds", aw.seconds),
				zap.String("http.request.method", safeHTTPMethod(r.Method)),
				zap.Int("http.response.status_code", status),
				zap.Int64("http.response.body.bytes", aw.bytes),
			)
		}()
		next.ServeHTTP(aw, r)
	})
}

func safeHTTPMethod(method string) string {
	switch method {
	case http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete, http.MethodHead, http.MethodOptions:
		return method
	default:
		return "OTHER"
	}
}
