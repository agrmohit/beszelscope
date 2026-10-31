package logctx

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
)

func TestHandler(t *testing.T) {
	tests := []struct {
		name string
		ctx  func(context.Context) context.Context
		log  func(*slog.Logger, context.Context)
		want string // the record's attributes as JSON, after msg
	}{
		{
			name: "nothing bound", ctx: func(c context.Context) context.Context { return c },
			log:  func(l *slog.Logger, c context.Context) { l.InfoContext(c, "e", slog.Int("n", 1)) },
			want: `"n":1}`,
		},
		{
			name: "bound after the call site's own", ctx: func(c context.Context) context.Context { return With(c, slog.String("tool", "x")) },
			log:  func(l *slog.Logger, c context.Context) { l.InfoContext(c, "e", slog.Int("n", 1)) },
			want: `"n":1,"tool":"x"}`,
		},
		{
			name: "nested binds keep the outer ones",
			ctx: func(c context.Context) context.Context {
				return With(With(c, slog.String("a", "1")), slog.String("b", "2"))
			},
			log:  func(l *slog.Logger, c context.Context) { l.InfoContext(c, "e") },
			want: `"a":"1","b":"2"}`,
		},
		{
			// A shared backing array would let the second bind overwrite the first.
			name: "siblings do not share a backing array",
			// Three binds leave the slice with spare capacity for append to reuse.
			ctx: func(c context.Context) context.Context {
				base := With(With(With(c, slog.String("a", "1")), slog.String("b", "2")), slog.String("c", "3"))
				first := With(base, slog.String("x", "kept"))
				_ = With(base, slog.String("y", "overwrites"))
				return first
			},
			log:  func(l *slog.Logger, c context.Context) { l.InfoContext(c, "e") },
			want: `"a":"1","b":"2","c":"3","x":"kept"}`,
		},
		{
			name: "WithAttrs keeps the wrapper", ctx: func(c context.Context) context.Context { return With(c, slog.String("b", "2")) },
			log:  func(l *slog.Logger, c context.Context) { l.With(slog.String("a", "1")).InfoContext(c, "e") },
			want: `"a":"1","b":"2"}`,
		},
		{
			name: "WithGroup keeps the wrapper", ctx: func(c context.Context) context.Context { return With(c, slog.String("b", "2")) },
			log:  func(l *slog.Logger, c context.Context) { l.WithGroup("g").InfoContext(c, "e") },
			want: `"g":{"b":"2"}}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			l := slog.New(Handler{slog.NewJSONHandler(&buf, &slog.HandlerOptions{
				ReplaceAttr: func(_ []string, a slog.Attr) slog.Attr {
					if a.Key == slog.TimeKey || a.Key == slog.LevelKey {
						return slog.Attr{}
					}
					return a
				},
			})})
			tt.log(l, tt.ctx(t.Context()))
			if got := strings.TrimSpace(buf.String()); got != `{"msg":"e",`+tt.want {
				t.Fatalf("got  %s\nwant {\"msg\":\"e\",%s", got, tt.want)
			}
		})
	}
}
