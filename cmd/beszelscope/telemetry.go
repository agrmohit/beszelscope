package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"time"

	"github.com/agrmohit/beszelscope/internal/logctx"

	"go.opentelemetry.io/contrib/bridges/otelslog"
	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploghttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
	"go.opentelemetry.io/otel/trace"
)

// telemetry holds the trace provider and, when export is on, the OTLP log
// handler. Nothing here is installed as an OTel global.
type telemetry struct {
	tracer   trace.TracerProvider
	logs     slog.Handler // nil when export is off
	warning  error        // resource detection was incomplete; export still works
	shutdown []func(context.Context) error
}

// exportOn reports whether the standard OTel environment asks for export.
// The exporters read the rest of OTEL_* themselves.
func exportOn(env map[string]string) bool {
	return env["OTEL_EXPORTER_OTLP_ENDPOINT"] != "" && !strings.EqualFold(env["OTEL_SDK_DISABLED"], "true")
}

// newTelemetry builds OTLP/HTTP trace and log export when exportOn. Without
// it, spans are still made so that log lines carry a trace_id per call.
func newTelemetry(ctx context.Context, env map[string]string) (*telemetry, error) {
	if !exportOn(env) {
		tp := sdktrace.NewTracerProvider()
		return &telemetry{tracer: tp, shutdown: []func(context.Context) error{tp.Shutdown}}, nil
	}

	res, err := resource.New(ctx,
		resource.WithTelemetrySDK(), resource.WithHost(), resource.WithHostID(),
		resource.WithOS(), resource.WithProcess(), resource.WithContainer(),
		resource.WithAttributes(semconv.ServiceName("beszelscope"), semconv.ServiceVersion(version)),
		resource.WithFromEnv(), // last, so OTEL_SERVICE_NAME and OTEL_RESOURCE_ATTRIBUTES win
	)
	// resource.New always returns a usable resource; an error only means some
	// attributes are missing, which must not stop the server.
	warning := err

	spans, err := otlptracehttp.New(ctx)
	if err != nil {
		return nil, fmt.Errorf("otlp trace exporter: %w", err)
	}
	records, err := otlploghttp.New(ctx)
	if err != nil {
		return nil, fmt.Errorf("otlp log exporter: %w", err)
	}
	tp := sdktrace.NewTracerProvider(sdktrace.WithBatcher(spans), sdktrace.WithResource(res))
	lp := sdklog.NewLoggerProvider(sdklog.WithProcessor(sdklog.NewBatchProcessor(records)), sdklog.WithResource(res))
	return &telemetry{
		tracer:   tp,
		logs:     otelslog.NewHandler("github.com/agrmohit/beszelscope", otelslog.WithLoggerProvider(lp), otelslog.WithVersion(version)),
		warning:  warning,
		shutdown: []func(context.Context) error{tp.Shutdown, lp.Shutdown},
	}, nil
}

// flush sends what is still buffered, waiting at most 2 s so that a dead
// collector cannot hold up exit. It runs after ctx may be cancelled.
func (t *telemetry) flush(ctx context.Context, log *slog.Logger) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	for _, shutdown := range t.shutdown {
		if err := shutdown(ctx); err != nil {
			log.WarnContext(ctx, "telemetry.flush_failed", slog.String("error", err.Error()))
		}
	}
}

// newLogger returns the JSON logger on w. Every line carries trace_id and
// span_id from its context, and its source file and line. export, when not
// nil, gets the same records.
func newLogger(w io.Writer, level slog.Level, export slog.Handler) *slog.Logger {
	var h slog.Handler = traceHandler{slog.NewJSONHandler(w, &slog.HandlerOptions{AddSource: true, Level: level, ReplaceAttr: utcTime})}
	if export != nil {
		h = slog.NewMultiHandler(h, levelHandler{export, level})
	}
	// Outside both destinations, so neither gets a bound field unredacted.
	return slog.New(logctx.Handler{Handler: redactHandler{h}})
}

// denied are attribute keys whose values never reach a log, whatever the
// call site passes: the backstop behind config.Secret's own LogValue.
var denied = map[string]bool{
	"password": true, "authorization": true, "token": true, "api_key": true, "secret": true, "cookie": true,
}

// redactHandler masks denied keys, at any depth, in every record and in
// attributes bound with Logger.With. There is no central length cap: one
// would cut the stack traces of panic records.
type redactHandler struct{ slog.Handler }

func (h redactHandler) Handle(ctx context.Context, r slog.Record) error {
	out := slog.NewRecord(r.Time, r.Level, r.Message, r.PC)
	r.Attrs(func(a slog.Attr) bool {
		out.AddAttrs(redact(a))
		return true
	})
	return h.Handler.Handle(ctx, out) //nolint:wrapcheck // a handler passes its inner handler's error through
}

func (h redactHandler) WithAttrs(as []slog.Attr) slog.Handler {
	masked := make([]slog.Attr, len(as))
	for i, a := range as {
		masked[i] = redact(a)
	}
	return redactHandler{h.Handler.WithAttrs(masked)}
}

func (h redactHandler) WithGroup(name string) slog.Handler {
	return redactHandler{h.Handler.WithGroup(name)}
}

func redact(a slog.Attr) slog.Attr {
	if denied[strings.ToLower(a.Key)] {
		return slog.String(a.Key, "***")
	}
	a.Value = a.Value.Resolve()
	if a.Value.Kind() == slog.KindGroup {
		inner := a.Value.Group()
		masked := make([]slog.Attr, len(inner))
		for i, g := range inner {
			masked[i] = redact(g)
		}
		return slog.Attr{Key: a.Key, Value: slog.GroupValue(masked...)}
	}
	return a
}

func utcTime(groups []string, a slog.Attr) slog.Attr {
	if a.Key == slog.TimeKey && len(groups) == 0 {
		a.Value = slog.TimeValue(a.Value.Time().UTC())
	}
	return a
}

type traceHandler struct{ slog.Handler }

func (h traceHandler) Handle(ctx context.Context, r slog.Record) error {
	if sc := trace.SpanContextFromContext(ctx); sc.IsValid() {
		r.AddAttrs(slog.String("trace_id", sc.TraceID().String()), slog.String("span_id", sc.SpanID().String()))
	}
	return h.Handler.Handle(ctx, r) //nolint:wrapcheck // a handler passes its inner handler's error through
}

func (h traceHandler) WithAttrs(as []slog.Attr) slog.Handler {
	return traceHandler{h.Handler.WithAttrs(as)}
}

func (h traceHandler) WithGroup(name string) slog.Handler {
	return traceHandler{h.Handler.WithGroup(name)}
}

// levelHandler drops records below min. The OTel bridge has no level of its own.
type levelHandler struct {
	slog.Handler
	min slog.Level
}

func (h levelHandler) Enabled(ctx context.Context, l slog.Level) bool {
	return l >= h.min && h.Handler.Enabled(ctx, l)
}

func (h levelHandler) WithAttrs(as []slog.Attr) slog.Handler {
	return levelHandler{h.Handler.WithAttrs(as), h.min}
}

func (h levelHandler) WithGroup(name string) slog.Handler {
	return levelHandler{h.Handler.WithGroup(name), h.min}
}
