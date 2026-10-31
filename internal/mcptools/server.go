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
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
	"go.opentelemetry.io/otel/trace"

	"github.com/agrmohit/beszelscope/internal/hub"
	"github.com/agrmohit/beszelscope/internal/logctx"
)

// Sent to the client once per session; Claude Code shows it to the model.
const instructions = "Read-only view of a Beszel monitoring hub. Use fleet_status for the whole fleet or when no server is named. " +
	"When the user names one server, call system_detail for it directly. " +
	"Each problem fleet_status lists names the system_detail section that explains it. " +
	"For trends over time (the last hour up to 30 days), use metric_history."

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
	LastStatsAt(ctx context.Context, systemID string) (time.Time, error)
	MonitorStats(ctx context.Context, systemID string, t hub.StatType, since time.Time) ([]hub.MonitorStats, error)
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
	addHistory(s, t)
	return s
}

// goroutinePanic carries a panic from a fetch goroutine, with its own stack,
// to the handler goroutine where instrument recovers it.
type goroutinePanic struct {
	value any
	stack []byte
}

var readOnly = &mcp.ToolAnnotations{ReadOnlyHint: true, IdempotentHint: true, OpenWorldHint: new(false)}

// MCP and GenAI attribute keys from OTel's GenAI semantic conventions for MCP
// (development status). The Go semconv package no longer carries them;
// error.type and network.transport still come from it.
const (
	mcpMethodNameKey      = attribute.Key("mcp.method.name")
	genAIToolNameKey      = attribute.Key("gen_ai.tool.name")
	genAIOperationNameKey = attribute.Key("gen_ai.operation.name")
	genAIToolCallArgsKey  = attribute.Key("gen_ai.tool.call.arguments")
	mcpProtocolVersionKey = attribute.Key("mcp.protocol.version")
	mcpSessionIDKey       = attribute.Key("mcp.session.id")
)

// instrument wraps a handler with one root span, one completion log line
// and panic recovery, so every call is traced and none can crash the server.
// The span follows OTel's MCP server conventions; the tool name is bound to
// every log line of the call.
func instrument[In, Out any](t *tools, name string, h mcp.ToolHandlerFor[In, Out]) mcp.ToolHandlerFor[In, Out] {
	return func(ctx context.Context, req *mcp.CallToolRequest, in In) (res *mcp.CallToolResult, out Out, err error) {
		ctx = logctx.With(parentFromMeta(ctx, req), slog.String(string(genAIToolNameKey), name))
		args, _ := json.Marshal(in) // In is a plain struct; this cannot fail
		ctx, span := t.tracer.Start(ctx, "tools/call "+name,
			trace.WithSpanKind(trace.SpanKindServer),
			trace.WithAttributes(
				mcpMethodNameKey.String("tools/call"),
				genAIToolNameKey.String(name),
				genAIOperationNameKey.String("execute_tool"),
				semconv.NetworkTransportPipe,              // stdio
				genAIToolCallArgsKey.String(string(args)), // opt-in in semconv; kept by choice
			))
		// The SDK does not expose the JSON-RPC request id, so jsonrpc.request.id
		// is left out. Over stdio there is no session id.
		if req != nil {
			if v := req.ProtocolVersion(); v != "" {
				span.SetAttributes(mcpProtocolVersionKey.String(v))
			}
			if req.Session != nil && req.Session.ID() != "" {
				span.SetAttributes(mcpSessionIDKey.String(req.Session.ID()))
			}
		}
		start := t.d.Now()
		defer func() {
			if r := recover(); r != nil {
				value, stack := any(r), debug.Stack()
				if gp, ok := r.(*goroutinePanic); ok {
					value, stack = gp.value, gp.stack
				}
				t.d.Logger.ErrorContext(ctx, "tool.call.panic", slog.Any("panic", value), slog.String("stack", string(stack)))
				res, err = nil, fmt.Errorf("internal error in %s; see beszelscope logs", name)
			}
			outcome := "ok"
			switch {
			case err != nil:
				outcome = "error"
				span.SetAttributes(semconv.ErrorTypeKey.String("tool_error"))
				span.RecordError(err)
				span.SetStatus(codes.Error, err.Error())
			case res != nil && res.IsError:
				outcome = "error"
				span.SetAttributes(semconv.ErrorTypeKey.String("tool_error"))
				span.SetStatus(codes.Error, "tool returned an error result")
			}
			span.End()
			t.d.Logger.InfoContext(ctx, "tool.call.completed",
				slog.Group("tool", slog.String("outcome", outcome), slog.Bool("cancelled", ctx.Err() != nil)),
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
