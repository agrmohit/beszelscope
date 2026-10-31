// Package logctx binds log attributes to a context, so that every record
// logged with that context carries them.
// A tool call binds its tool name once, and the hub request lines it causes
// say which call they belong to.
package logctx

import (
	"context"
	"log/slog"
	"slices"
)

type key struct{}

// With returns ctx with attrs bound after any already bound.
func With(ctx context.Context, attrs ...slog.Attr) context.Context {
	return context.WithValue(ctx, key{}, append(slices.Clip(Attrs(ctx)), attrs...))
}

// Attrs returns the attributes bound to ctx, or nil.
func Attrs(ctx context.Context) []slog.Attr {
	attrs, _ := ctx.Value(key{}).([]slog.Attr)
	return attrs
}

// Handler adds the attributes bound to a record's context, then passes the
// record to the wrapped handler.
type Handler struct{ slog.Handler }

// Handle adds the bound attributes to r.
func (h Handler) Handle(ctx context.Context, r slog.Record) error {
	if attrs := Attrs(ctx); len(attrs) > 0 {
		r = r.Clone()
		r.AddAttrs(attrs...)
	}
	return h.Handler.Handle(ctx, r) //nolint:wrapcheck // a handler passes its inner handler's error through
}

// WithAttrs keeps the wrapper around the derived handler.
func (h Handler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return Handler{h.Handler.WithAttrs(attrs)}
}

// WithGroup keeps the wrapper around the derived handler.
func (h Handler) WithGroup(name string) slog.Handler {
	return Handler{h.Handler.WithGroup(name)}
}
