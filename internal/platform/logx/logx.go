// Package logx sets up structured logging and carries a request-scoped
// logger through the context.
package logx

import (
	"context"
	"io"
	"log/slog"
)

// New returns the process logger. In dev mode it writes human-readable text
// at debug level; otherwise JSON at info level.
func New(w io.Writer, dev bool) *slog.Logger {
	if dev {
		return slog.New(slog.NewTextHandler(w, &slog.HandlerOptions{Level: slog.LevelDebug}))
	}
	return slog.New(slog.NewJSONHandler(w, &slog.HandlerOptions{Level: slog.LevelInfo}))
}

// Discard returns a logger that drops everything. Useful in tests.
func Discard() *slog.Logger {
	return slog.New(slog.DiscardHandler)
}

type ctxKey struct{}

// WithLogger returns a copy of ctx carrying l.
func WithLogger(ctx context.Context, l *slog.Logger) context.Context {
	return context.WithValue(ctx, ctxKey{}, l)
}

// FromContext returns the logger stored in ctx, or slog.Default() if there is none.
func FromContext(ctx context.Context) *slog.Logger {
	if l, ok := ctx.Value(ctxKey{}).(*slog.Logger); ok && l != nil {
		return l
	}
	return slog.Default()
}
