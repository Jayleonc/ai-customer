package logger

import (
	"context"
	"log/slog"
	"os"
)

type ctxKey string

const requestIDKey ctxKey = "request_id"

func Init() {
	// initialize default JSON logger
	h := slog.NewJSONHandler(os.Stdout, nil)
	slog.SetDefault(slog.New(h))
}

func WithRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, requestIDKey, id)
}

func FromContext(ctx context.Context) *slog.Logger {
	if v, ok := ctx.Value(requestIDKey).(string); ok && v != "" {
		return slog.With("request_id", v)
	}
	return slog.Default()
}
