package logger

import (
	"context"
	"log/slog"
	"os"
)

type contextKey string

const requestIDKey contextKey = "requestID"

var defaultLogger *slog.Logger

func init() {
	defaultLogger = slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelDebug,
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
