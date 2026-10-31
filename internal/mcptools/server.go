// Package mcptools defines beszelscope's MCP tools and builds the server.
package mcptools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"runtime/debug"
	"sync/atomic"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"

	"github.com/agrmohit/beszelscope/internal/hub"
)

// Sent to the client once per session; Claude Code shows it to the model.
const instructions = "Read-only view of a Beszel monitoring hub. Use fleet_status for the whole fleet or when no server is named. " +
	"When the user names one server, call system_detail for it directly. " +
	"Each problem fleet_status lists names the system_detail section that explains it."

// Hub is the part of the hub client the tools read from.
type Hub interface {
	Systems(ctx context.Context) ([]hub.System, error)
	Services(ctx context.Context) ([]hub.Service, error)
	Containers(ctx context.Context) ([]hub.Container, error)
	SmartDevices(ctx context.Context) ([]hub.SmartDevice, error)
	ZfsPools(ctx context.Context) ([]hub.ZfsPool, error)
	Monitors(ctx context.Context) ([]hub.Monitor, error)
	SystemDetails(ctx context.Context) ([]hub.SystemDetails, error)
	SystemStats(ctx context.Context, systemID string, t hub.StatType, since time.Time) ([]hub.SystemStats, error)
	ContainerStats(ctx context.Context, systemID string, t hub.StatType, since time.Time) ([]hub.ContainerStats, error)
	PackageUpdates(ctx context.Context, systemID string) (hub.PackageUpdates, error)
}

// Deps are everything the tools need, built by cmd/beszelscope.
type Deps struct {
	Hub            Hub
	HubURL         *url.URL
	HubVersion     string    // from the startup check; "" if unknown
	TokenExpiresAt time.Time // zero if unknown
	Logger         *slog.Logger
	Tracer         trace.TracerProvider
	Now            func() time.Time
	Version        string // beszelscope's own version
}

type tools struct {
	d          Deps
	tracer     trace.Tracer
	renewAsked atomic.Bool // the token-renewal prompt is shown once per process
}

// NewServer returns the MCP server with every tool registered.
func NewServer(d Deps) *mcp.Server {
	s := mcp.NewServer(
		&mcp.Implementation{Name: "beszelscope", Version: d.Version},
		&mcp.ServerOptions{Instructions: instructions, Logger: d.Logger},
	)
	t := &tools{d: d, tracer: d.Tracer.Tracer("github.com/agrmohit/beszelscope/internal/mcptools")}
	addFleet(s, t)
	addDetail(s, t)
	return s
}

// goroutinePanic carries a panic from a fetch goroutine, with its own stack,
// to the handler goroutine where instrument recovers it.
type goroutinePanic struct {
	value any
	stack []byte
}

var readOnly = &mcp.ToolAnnotations{ReadOnlyHint: true, IdempotentHint: true, OpenWorldHint: new(false)}

// instrument wraps a handler with one root span, one completion log line
// and panic recovery, so every call is traced and none can crash the server.
func instrument[In, Out any](t *tools, name string, h mcp.ToolHandlerFor[In, Out]) mcp.ToolHandlerFor[In, Out] {
	return func(ctx context.Context, req *mcp.CallToolRequest, in In) (res *mcp.CallToolResult, out Out, err error) {
		ctx = parentFromMeta(ctx, req)
		args, _ := json.Marshal(in) // In is a plain struct; this cannot fail
		ctx, span := t.tracer.Start(ctx, "mcp.tools/call "+name,
			trace.WithSpanKind(trace.SpanKindServer),
			trace.WithAttributes(attribute.String("mcp.tool.name", name), attribute.String("mcp.tool.arguments", string(args))))
		start := t.d.Now()
		defer func() {
			if r := recover(); r != nil {
				value, stack := any(r), debug.Stack()
				if gp, ok := r.(*goroutinePanic); ok {
					value, stack = gp.value, gp.stack
				}
				t.d.Logger.ErrorContext(ctx, "tool.call.panic",
					slog.String("tool", name), slog.Any("panic", value), slog.String("stack", string(stack)))
				res, err = nil, fmt.Errorf("internal error in %s; see beszelscope logs", name)
			}
			outcome := "ok"
			if err != nil {
				outcome = "error"
				span.RecordError(err)
				span.SetStatus(codes.Error, err.Error())
			}
			span.End()
			t.d.Logger.InfoContext(ctx, "tool.call.completed",
				slog.String("tool", name), slog.String("outcome", outcome),
				slog.Int64("duration_ms", t.d.Now().Sub(start).Milliseconds()))
		}()
		return h(ctx, req, in)
	}
}

// parentFromMeta continues the client's trace when it sends W3C trace
// context in the request's _meta.
func parentFromMeta(ctx context.Context, req *mcp.CallToolRequest) context.Context {
	if req == nil || req.Params == nil {
		return ctx
	}
	carrier := propagation.MapCarrier{}
	for _, k := range []string{"traceparent", "tracestate"} {
		if v, ok := req.Params.Meta[k].(string); ok {
			carrier[k] = v
		}
	}
	return propagation.TraceContext{}.Extract(ctx, carrier)
}

// hubError turns a hub failure into a message that tells the model whether
// a retry can help.
func hubError(err error) error {
	switch {
	case errors.Is(err, hub.ErrUnauthorized):
		return fmt.Errorf("the hub rejected beszelscope's token (expired or revoked); a human must store a new one "+
			"with `beszelscope token set`, and retrying will not help: %w", err)
	case errors.Is(err, hub.ErrUnreachable):
		return fmt.Errorf("could not reach the Beszel hub; it may be down or the network may be offline, retry later: %w", err)
	default:
		return fmt.Errorf("hub request failed: %w", err)
	}
}
