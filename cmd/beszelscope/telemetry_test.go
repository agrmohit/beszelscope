package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"

	"github.com/agrmohit/beszelscope/internal/logctx"
)

// recorder is a slog handler that keeps the messages it is given.
type recorder struct {
	mu   *sync.Mutex
	msgs *[]string
}

func newRecorder() recorder { return recorder{&sync.Mutex{}, &[]string{}} }

func (r recorder) Enabled(context.Context, slog.Level) bool { return true }
func (r recorder) WithAttrs([]slog.Attr) slog.Handler       { return r }
func (r recorder) WithGroup(string) slog.Handler            { return r }

func (r recorder) Handle(_ context.Context, rec slog.Record) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	*r.msgs = append(*r.msgs, rec.Message)
	return nil
}

func TestLogger(t *testing.T) {
	// slog stamps local time; a non-UTC zone proves the conversion happens.
	local := time.Local
	time.Local = time.FixedZone("IST", 5*3600+1800)
	t.Cleanup(func() { time.Local = local })
	tp := sdktrace.NewTracerProvider()
	spanCtx, span := tp.Tracer("test").Start(t.Context(), "op")
	defer span.End()
	sc := span.SpanContext()

	tests := []struct {
		name       string
		ctx        context.Context
		level      slog.Level
		log        func(*slog.Logger, context.Context)
		wantLine   map[string]string // keys that must be in the stderr line; nil = no line
		noKeys     []string
		wantExport []string
	}{
		{
			name: "inside a span", ctx: spanCtx, level: slog.LevelInfo,
			log:        func(l *slog.Logger, ctx context.Context) { l.InfoContext(ctx, "a") },
			wantLine:   map[string]string{"msg": "a", "trace_id": sc.TraceID().String(), "span_id": sc.SpanID().String()},
			wantExport: []string{"a"},
		},
		{
			name: "outside a span", ctx: t.Context(), level: slog.LevelInfo,
			log:        func(l *slog.Logger, ctx context.Context) { l.InfoContext(ctx, "b") },
			wantLine:   map[string]string{"msg": "b"},
			noKeys:     []string{"trace_id", "span_id"},
			wantExport: []string{"b"},
		},
		{
			name: "attrs keep the trace ids", ctx: spanCtx, level: slog.LevelInfo,
			log:        func(l *slog.Logger, ctx context.Context) { l.With(slog.String("k", "v")).InfoContext(ctx, "c") },
			wantLine:   map[string]string{"msg": "c", "k": "v", "trace_id": sc.TraceID().String()},
			wantExport: []string{"c"},
		},
		{
			name: "below the level goes nowhere", ctx: t.Context(), level: slog.LevelInfo,
			log: func(l *slog.Logger, ctx context.Context) { l.DebugContext(ctx, "d") },
		},
		{
			name: "debug level lets debug through", ctx: t.Context(), level: slog.LevelDebug,
			log:        func(l *slog.Logger, ctx context.Context) { l.DebugContext(ctx, "e") },
			wantLine:   map[string]string{"msg": "e", "level": "DEBUG"},
			wantExport: []string{"e"},
		},
		{
			name: "error level drops warn", ctx: t.Context(), level: slog.LevelError,
			log: func(l *slog.Logger, ctx context.Context) { l.WarnContext(ctx, "f") },
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out bytes.Buffer
			export := newRecorder()
			tt.log(newLogger(&out, tt.level, export), tt.ctx)

			if !slices.Equal(*export.msgs, tt.wantExport) {
				t.Fatalf("exported = %q, want %q", *export.msgs, tt.wantExport)
			}
			if tt.wantLine == nil {
				if out.Len() > 0 {
					t.Fatalf("stderr = %q, want nothing", out.String())
				}
				return
			}
			var line map[string]any
			if err := json.Unmarshal(out.Bytes(), &line); err != nil {
				t.Fatalf("line %q: %v", out.String(), err)
			}
			for k, v := range tt.wantLine {
				if line[k] != v {
					t.Fatalf("%s = %v, want %q in %s", k, line[k], v, out.String())
				}
			}
			for _, k := range tt.noKeys {
				if _, ok := line[k]; ok {
					t.Fatalf("%s present in %s", k, out.String())
				}
			}
			if ts, _ := line["time"].(string); !strings.HasSuffix(ts, "Z") {
				t.Fatalf("time %q is not UTC", ts)
			}
		})
	}
}

// collector records the OTLP paths and bodies it receives.
type collector struct {
	mu     sync.Mutex
	bodies map[string][]byte
}

func (c *collector) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	c.mu.Lock()
	c.bodies[r.URL.Path] = append(c.bodies[r.URL.Path], body...)
	c.mu.Unlock()
	w.WriteHeader(http.StatusOK)
}

func (c *collector) paths() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	var p []string
	for k := range c.bodies {
		p = append(p, k)
	}
	slices.Sort(p)
	return p
}

// TestExport runs serve against a fake OTLP collector. The exporters read
// OTEL_* from the process, so the rows set it with t.Setenv.
func TestExport(t *testing.T) {
	tests := []struct {
		name      string
		otel      map[string]string // "ENDPOINT" is replaced by the collector URL
		wantPaths []string
		wantBody  []string // strings the trace body must hold
	}{
		{
			name:      "endpoint set",
			otel:      map[string]string{"OTEL_EXPORTER_OTLP_ENDPOINT": "ENDPOINT"},
			wantPaths: []string{"/v1/logs", "/v1/traces"},
			wantBody:  []string{"beszelscope.startup", "GET /api/beszel/info", "service.name", "host.name"},
		},
		{
			name:      "service name from env wins",
			otel:      map[string]string{"OTEL_EXPORTER_OTLP_ENDPOINT": "ENDPOINT", "OTEL_SERVICE_NAME": "scope-renamed"},
			wantPaths: []string{"/v1/logs", "/v1/traces"},
			wantBody:  []string{"scope-renamed"},
		},
		{name: "sdk disabled", otel: map[string]string{"OTEL_EXPORTER_OTLP_ENDPOINT": "ENDPOINT", "OTEL_SDK_DISABLED": "true"}},
		{name: "sdk disabled, any case", otel: map[string]string{"OTEL_EXPORTER_OTLP_ENDPOINT": "ENDPOINT", "OTEL_SDK_DISABLED": "TRUE"}},
		{name: "no endpoint", otel: map[string]string{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := &collector{bodies: map[string][]byte{}}
			srv := httptest.NewServer(c)
			defer srv.Close()
			environ := []string{
				"BESZELSCOPE_URL=" + fakeHub(t, 200), "BESZELSCOPE_ALLOW_INSECURE_HTTP=1",
				"BESZELSCOPE_TOKEN=" + makeToken(t, time.Now().Add(365*24*time.Hour)),
			}
			for k, v := range tt.otel {
				v = strings.ReplaceAll(v, "ENDPOINT", srv.URL)
				t.Setenv(k, v)
				environ = append(environ, k+"="+v)
			}
			if err := run(t.Context(), nil, environ, strings.NewReader(""), io.Discard, io.Discard, nil); err != nil {
				t.Fatalf("run: %v", err)
			}
			if got := c.paths(); !slices.Equal(got, tt.wantPaths) {
				t.Fatalf("collector got %q, want %q", got, tt.wantPaths)
			}
			for _, s := range tt.wantBody {
				if !bytes.Contains(c.bodies["/v1/traces"], []byte(s)) {
					t.Fatalf("trace export lacks %q", s)
				}
			}
		})
	}
}

// loggable resolves to a group, as a LogValuer type would.
type loggable struct{}

func (loggable) LogValue() slog.Value {
	return slog.GroupValue(slog.String("id", "u1"), slog.String("api_key", "k-123"))
}

// TestRedaction: denied keys are masked at any depth, through every way an
// attribute reaches a record, in both the stderr line and the export.
func TestRedaction(t *testing.T) {
	long := strings.Repeat("x", 5000)
	tests := []struct {
		name   string
		log    func(*slog.Logger, context.Context)
		want   []string
		absent []string
	}{
		{
			name: "top-level key", log: func(l *slog.Logger, c context.Context) { l.InfoContext(c, "e", slog.String("token", "t-123")) },
			want: []string{`"token":"***"`}, absent: []string{"t-123"},
		},
		{
			name: "any case", log: func(l *slog.Logger, c context.Context) {
				l.InfoContext(c, "e", slog.String("Authorization", "Bearer b")) //nolint:sloglint // a mixed-case key is the case under test
			},
			want: []string{`"Authorization":"***"`}, absent: []string{"Bearer"},
		},
		{
			name: "inside a group",
			log: func(l *slog.Logger, c context.Context) {
				l.InfoContext(c, "e", slog.Group("db", slog.String("password", "p-123")))
			},
			want: []string{`"db":{"password":"***"}`}, absent: []string{"p-123"},
		},
		{
			name: "bound with Logger.With", log: func(l *slog.Logger, c context.Context) { l.With(slog.String("secret", "s-123")).InfoContext(c, "e") },
			want: []string{`"secret":"***"`}, absent: []string{"s-123"},
		},
		{
			name: "bound to the context",
			log: func(l *slog.Logger, c context.Context) {
				l.InfoContext(logctx.With(c, slog.String("cookie", "c-123")), "e")
			},
			want: []string{`"cookie":"***"`}, absent: []string{"c-123"},
		},
		{
			name: "a LogValuer's group", log: func(l *slog.Logger, c context.Context) { l.InfoContext(c, "e", slog.Any("user", loggable{})) },
			want: []string{`"user":{"id":"u1","api_key":"***"}`}, absent: []string{"k-123"},
		},
		{
			name: "only whole keys: token_expires_at is kept",
			log: func(l *slog.Logger, c context.Context) {
				l.InfoContext(c, "e", slog.String("token_expires_at", "2036"))
			},
			want: []string{`"token_expires_at":"2036"`},
		},
		{
			name: "no central cap: a long stack stays whole", log: func(l *slog.Logger, c context.Context) { l.InfoContext(c, "e", slog.String("stack", long)) },
			want: []string{`"stack":"` + long + `"`},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out, exported bytes.Buffer
			tt.log(newLogger(&out, slog.LevelInfo, slog.NewJSONHandler(&exported, nil)), t.Context())
			for name, got := range map[string]string{"stderr": out.String(), "export": exported.String()} {
				for _, w := range tt.want {
					if !strings.Contains(got, w) {
						t.Errorf("%s lacks %s: %s", name, w, got)
					}
				}
				for _, a := range tt.absent {
					if strings.Contains(got, a) {
						t.Errorf("%s leaks %s: %s", name, a, got)
					}
				}
			}
		})
	}
}
