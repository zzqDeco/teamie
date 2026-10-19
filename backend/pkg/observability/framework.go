package observability

import (
	"context"
	"fmt"
	"io"
	"log"
	"sync"

	"github.com/cloudwego/hertz/pkg/common/hlog"
	"github.com/cloudwego/kitex/pkg/klog"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

type frameworkLogger struct {
	mu        sync.RWMutex
	config    Config
	logger    *zap.Logger
	component string
	level     int
}

func (l *frameworkLogger) SetOutput(w io.Writer) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.config.Output = w
	l.logger = New(l.config)
}
func (l *frameworkLogger) setLevel(level int) { l.mu.Lock(); defer l.mu.Unlock(); l.level = level }
func (l *frameworkLogger) emit(ctx context.Context, level int, format string, args ...any) {
	l.mu.RLock()
	logger, threshold := l.logger, l.level
	l.mu.RUnlock()
	if level < threshold {
		return
	}
	levels := []zapcore.Level{zapcore.DebugLevel, zapcore.DebugLevel, zapcore.InfoLevel, zapcore.InfoLevel, zapcore.WarnLevel, zapcore.ErrorLevel, zapcore.FatalLevel}
	if level < 0 || level >= len(levels) {
		level = 5
	}
	// Framework arguments may contain whole RPC messages or connection errors
	// with credentials. Retain the static format/type/fingerprint, never args.
	fields := []zap.Field{zap.String("custom.framework.component", l.component), zap.String("custom.framework.diagnostic_fingerprint", Fingerprint(fmt.Sprintf(format, args...)))}
	if len(format) < 512 {
		fields = append(fields, zap.String("custom.framework.template_fingerprint", Fingerprint(format)))
	}
	if ctx != nil {
		logger = correlated(ctx, logger)
	}
	logger.Log(levels[level], "Framework diagnostic", fields...)
}

type hertzLogger struct{ *frameworkLogger }

func (l hertzLogger) SetLevel(level hlog.Level) { l.setLevel(int(level)) }

type kitexLogger struct{ *frameworkLogger }

func (l kitexLogger) SetLevel(level klog.Level) { l.setLevel(int(level)) }

func installFrameworkLoggers(c Config) {
	makeLogger := func(component string) *frameworkLogger {
		level := 2
		if c.Level <= zapcore.DebugLevel {
			level = 1
		}
		return &frameworkLogger{config: c, logger: New(c).WithOptions(zap.AddCallerSkip(2)), component: component, level: level}
	}
	hlog.SetLogger(hertzLogger{makeLogger("hertz")})
	klog.SetLogger(kitexLogger{makeLogger("kitex")})
	log.SetFlags(0)
	log.SetPrefix("")
	log.SetOutput(standardLog{makeLogger("stdlib")})
}

func (l *frameworkLogger) Trace(v ...any) { l.emit(context.Background(), 0, "%v", v...) }
func (l *frameworkLogger) Tracef(format string, v ...any) {
	l.emit(context.Background(), 0, format, v...)
}
func (l *frameworkLogger) CtxTracef(ctx context.Context, format string, v ...any) {
	l.emit(ctx, 0, format, v...)
}
func (l *frameworkLogger) Debug(v ...any) { l.emit(context.Background(), 1, "%v", v...) }
func (l *frameworkLogger) Debugf(format string, v ...any) {
	l.emit(context.Background(), 1, format, v...)
}
func (l *frameworkLogger) CtxDebugf(ctx context.Context, format string, v ...any) {
	l.emit(ctx, 1, format, v...)
}
func (l *frameworkLogger) Info(v ...any) { l.emit(context.Background(), 2, "%v", v...) }
func (l *frameworkLogger) Infof(format string, v ...any) {
	l.emit(context.Background(), 2, format, v...)
}
func (l *frameworkLogger) CtxInfof(ctx context.Context, format string, v ...any) {
	l.emit(ctx, 2, format, v...)
}
func (l *frameworkLogger) Notice(v ...any) { l.emit(context.Background(), 3, "%v", v...) }
func (l *frameworkLogger) Noticef(format string, v ...any) {
	l.emit(context.Background(), 3, format, v...)
}
func (l *frameworkLogger) CtxNoticef(ctx context.Context, format string, v ...any) {
	l.emit(ctx, 3, format, v...)
}
func (l *frameworkLogger) Warn(v ...any) { l.emit(context.Background(), 4, "%v", v...) }
func (l *frameworkLogger) Warnf(format string, v ...any) {
	l.emit(context.Background(), 4, format, v...)
}
func (l *frameworkLogger) CtxWarnf(ctx context.Context, format string, v ...any) {
	l.emit(ctx, 4, format, v...)
}
func (l *frameworkLogger) Error(v ...any) { l.emit(context.Background(), 5, "%v", v...) }
func (l *frameworkLogger) Errorf(format string, v ...any) {
	l.emit(context.Background(), 5, format, v...)
}
func (l *frameworkLogger) CtxErrorf(ctx context.Context, format string, v ...any) {
	l.emit(ctx, 5, format, v...)
}
func (l *frameworkLogger) Fatal(v ...any) { l.emit(context.Background(), 6, "%v", v...) }
func (l *frameworkLogger) Fatalf(format string, v ...any) {
	l.emit(context.Background(), 6, format, v...)
}
func (l *frameworkLogger) CtxFatalf(ctx context.Context, format string, v ...any) {
	l.emit(ctx, 6, format, v...)
}

type standardLog struct{ logger *frameworkLogger }

func (l standardLog) Write(p []byte) (int, error) {
	l.logger.emit(context.Background(), 2, "%s", string(p))
	return len(p), nil
}
