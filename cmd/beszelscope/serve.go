package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"

	"github.com/agrmohit/beszelscope/internal/config"
	"github.com/agrmohit/beszelscope/internal/hub"
	"github.com/agrmohit/beszelscope/internal/mcptools"
	"github.com/agrmohit/beszelscope/internal/report"
)

// serve runs the MCP server until stdin closes or ctx is cancelled.
func serve(ctx context.Context, env map[string]string, stdin io.Reader, stdout, stderr io.Writer, tokens tokenStore) error {
	stored, err := storedToken(ctx, tokens, env["BESZELSCOPE_URL"])
	if err != nil {
		return err
	}
	cfg, err := config.Parse(env, stored)
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}
	tel, err := newTelemetry(ctx, env)
	if err != nil {
		return err
	}
	log := newLogger(stderr, cfg.LogLevel, tel.logs)
	defer tel.flush(ctx, log)
	if tel.warning != nil {
		log.WarnContext(ctx, "telemetry.partial_resource", slog.String("error", tel.warning.Error()))
	}

	h := newHubClient(cfg, tel.tracer, log)
	hubVersion := startup(ctx, tel.tracer.Tracer("github.com/agrmohit/beszelscope/cmd/beszelscope"), log, h, cfg, time.Now())

	srv := mcptools.NewServer(mcptools.Deps{
		Hub: h, HubURL: cfg.HubURL, HubVersion: hubVersion, TokenExpiresAt: cfg.TokenExpiresAt,
		Logger: log, Tracer: tel.tracer, Now: time.Now, Version: version,
	})
	err = srv.Run(ctx, &mcp.IOTransport{Reader: io.NopCloser(stdin), Writer: nopWriteCloser{stdout}})
	if errors.Is(err, context.Canceled) {
		return nil // a signal is a normal way to stop
	}
	if err != nil {
		return fmt.Errorf("serve: %w", err)
	}
	return nil
}

// storedToken returns the Keychain token for the hub in rawURL, or "" when
// there is none or no Keychain. config.Parse reports a bad URL itself.
func storedToken(ctx context.Context, tokens tokenStore, rawURL string) (string, error) {
	u, err := url.Parse(rawURL)
	if tokens == nil || err != nil || u.Hostname() == "" {
		return "", nil
	}
	tok, err := tokens.Get(ctx, u.Hostname())
	if err != nil {
		return "", fmt.Errorf("keychain: %w", err)
	}
	return tok, nil
}

// newHubClient returns the hub client. Each request gets an otelhttp span, a
// W3C traceparent header and, inside the span, a hub.request log line. The
// propagator is passed in: otelhttp's default is the global, a no-op here.
func newHubClient(cfg config.Config, tp trace.TracerProvider, log *slog.Logger) *hub.Client {
	transport := otelhttp.NewTransport(hub.LogTransport(hub.NewTransport(), log),
		otelhttp.WithTracerProvider(tp),
		otelhttp.WithPropagators(propagation.TraceContext{}),
		otelhttp.WithSpanNameFormatter(func(_ string, r *http.Request) string { return r.Method + " " + r.URL.Path }),
	)
	return hub.New(hub.Options{
		BaseURL:     cfg.HubURL,
		Token:       cfg.Token.Reveal(),
		HTTP:        &http.Client{Transport: transport},
		Timeout:     5 * time.Second,
		LiveTimeout: 7 * time.Second, // the hub waits on the agent
		RetryDelay:  300 * time.Millisecond,
		MaxParallel: 8,
	})
}

// startup checks the token and the hub in one trace, warming the connection,
// and returns the hub version, or "" if the hub did not answer. It never
// fails: with a dead token or hub, every tool says so itself.
func startup(ctx context.Context, tracer trace.Tracer, log *slog.Logger, h *hub.Client, cfg config.Config, now time.Time) string {
	ctx, span := tracer.Start(ctx, "beszelscope.startup")
	defer span.End()
	span.SetAttributes(
		attribute.String("beszelscope.token.source", string(cfg.TokenSource)),
		attribute.String("beszelscope.token.expires_at", cfg.TokenExpiresAt.UTC().Format(time.RFC3339)),
	)
	for _, w := range report.TokenWarning(now, cfg.TokenExpiresAt) {
		log.WarnContext(ctx, "startup.token_warning", slog.String("warning", w))
	}

	info, err := h.Info(ctx)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		log.ErrorContext(ctx, "startup.hub_check_failed", slog.String("error", err.Error()))
		return ""
	}
	span.SetAttributes(attribute.String("beszel.hub.version", info.Version))
	log.InfoContext(ctx, "startup.ready",
		slog.String("version", version), slog.String("hub_version", info.Version),
		slog.String("token_source", string(cfg.TokenSource)), slog.Time("token_expires_at", cfg.TokenExpiresAt))
	return info.Version
}

type nopWriteCloser struct{ io.Writer }

func (nopWriteCloser) Close() error { return nil }
