package logger

import (
	"context"
	"log/slog"
	"os"
	"strings"
)

type contextKey string

const requestIDKey contextKey = "requestID"

var defaultLogger *slog.Logger

func init() {
	level := slog.LevelDebug
	// 通过环境变量 LOG_LEVEL 控制日志级别（debug/info/warn/error）
	if env := os.Getenv("LOG_LEVEL"); env != "" {
		switch strings.ToLower(env) {
		case "debug":
			level = slog.LevelDebug
		case "info":
			level = slog.LevelInfo
		case "warn", "warning":
			level = slog.LevelWarn
		case "error":
			level = slog.LevelError
		}
	}
	defaultLogger = slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: level,
	}))
}

func WithRequestID(ctx context.Context, requestID string) context.Context {
	return context.WithValue(ctx, requestIDKey, requestID)
}

func getLogger(ctx context.Context) *slog.Logger {
	if ctx == nil {
		return defaultLogger
	}
	if rid, ok := ctx.Value(requestIDKey).(string); ok {
		return defaultLogger.With("request_id", rid)
	}
	return defaultLogger
}

func Info(ctx context.Context, msg string, args ...any) {
	getLogger(ctx).Info(msg, args...)
}

func Error(ctx context.Context, msg string, args ...any) {
	getLogger(ctx).Error(msg, args...)
}

func Debug(ctx context.Context, msg string, args ...any) {
	getLogger(ctx).Debug(msg, args...)
}

func Warn(ctx context.Context, msg string, args ...any) {
	getLogger(ctx).Warn(msg, args...)
}
