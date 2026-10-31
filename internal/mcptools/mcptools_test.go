package mcptools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"

	"github.com/agrmohit/beszelscope/internal/hub"
)

// captured is when the hub fixtures were taken. Tests read it from meta.json
// so ages stay real after a recapture; a stale copy clamped them all to 0.
var captured = fixtureTime()

func fixtureTime() time.Time {
	b, err := os.ReadFile("../hub/testdata/hub/meta.json")
	if err != nil {
		panic(err)
	}
	var meta struct {
		CapturedAt time.Time `json:"captured_at"`
	}
	if err := json.Unmarshal(b, &meta); err != nil {
		panic(err)
	}
	return meta.CapturedAt
}

// fakeHub serves fixed records; fail and panicOn make one method misbehave.
type fakeHub struct {
	systems    []hub.System
	services   []hub.Service
	containers []hub.Container
	smart      []hub.SmartDevice
	pools      []hub.ZfsPool
	monitors   []hub.Monitor
	details    []hub.SystemDetails
	stats      []hub.SystemStats
	packages   map[string]hub.PackageUpdates // by system ID; the captured fixture is empty
	fail       map[string]error
	panicOn    string

	mu    sync.Mutex
	calls []string // methods called, in any order
}

func (f *fakeHub) called(method string) bool { return f.count(method) > 0 }

func (f *fakeHub) count(method string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		if c == method {
			n++
		}
	}
	return n
}

func (f *fakeHub) check(method string) error {
	f.mu.Lock()
	f.calls = append(f.calls, method)
	f.mu.Unlock()
	if f.panicOn == method {
		panic("boom in " + method)
	}
	return f.fail[method]
}

// reply returns v, or nothing with the error, as the real client does.
func reply[T any](f *fakeHub, method string, v []T) ([]T, error) {
	if err := f.check(method); err != nil {
		return nil, err
	}
	return v, nil
}

func (f *fakeHub) PackageUpdates(_ context.Context, systemID string) (hub.PackageUpdates, error) {
	if err := f.check("PackageUpdates"); err != nil {
		return hub.PackageUpdates{}, err
	}
	return f.packages[systemID], nil
}

func (f *fakeHub) Systems(context.Context) ([]hub.System, error) {
	return reply(f, "Systems", f.systems)
}

func (f *fakeHub) Services(context.Context) ([]hub.Service, error) {
	return reply(f, "Services", f.services)
}

func (f *fakeHub) Containers(context.Context) ([]hub.Container, error) {
	return reply(f, "Containers", f.containers)
}

func (f *fakeHub) SmartDevices(context.Context) ([]hub.SmartDevice, error) {
	return reply(f, "SmartDevices", f.smart)
}

func (f *fakeHub) ZfsPools(context.Context) ([]hub.ZfsPool, error) {
	return reply(f, "ZfsPools", f.pools)
}

func (f *fakeHub) Monitors(context.Context) ([]hub.Monitor, error) {
	return reply(f, "Monitors", f.monitors)
}

func (f *fakeHub) SystemDetails(context.Context) ([]hub.SystemDetails, error) {
	return reply(f, "SystemDetails", f.details)
}

// SystemStats returns the rows of systemID created at or after since.
func (f *fakeHub) SystemStats(_ context.Context, systemID string, _ hub.StatType, since time.Time) ([]hub.SystemStats, error) {
	var out []hub.SystemStats
	for _, r := range f.stats {
		if r.System == systemID && !r.Created.Before(since) {
			out = append(out, r)
		}
	}
	return reply(f, "SystemStats", out)
}

func (f *fakeHub) ContainerStats(context.Context, string, hub.StatType, time.Time) ([]hub.ContainerStats, error) {
	return reply[hub.ContainerStats](f, "ContainerStats", nil)
}

func load[T any](t *testing.T, name string) []T {
	t.Helper()
	root, err := os.OpenRoot("../hub/testdata")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	b, err := root.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	var p struct {
		Items []T `json:"items"`
	}
	if err := json.Unmarshal(b, &p); err != nil {
		t.Fatal(err)
	}
	return p.Items
}

func fixtureHub(t *testing.T) *fakeHub {
	t.Helper()
	return &fakeHub{
		systems:    load[hub.System](t, "hub/systems.json"),
		services:   load[hub.Service](t, "hub/systemd_services.json"),
		containers: load[hub.Container](t, "hub/containers.json"),
		smart:      load[hub.SmartDevice](t, "hub/smart_devices.json"),
		pools:      load[hub.ZfsPool](t, "handmade/zfs_pools.json"),
		monitors:   load[hub.Monitor](t, "hub/network_monitors.json"),
		details:    load[hub.SystemDetails](t, "hub/system_details.json"),
		stats: slices.Concat(
			load[hub.SystemStats](t, "hub/system_stats_latest_alpha.json"),
			load[hub.SystemStats](t, "hub/system_stats_latest_bravo.json"),
			load[hub.SystemStats](t, "hub/system_stats_latest_charlie.json"),
		),
		fail: map[string]error{},
	}
}

// lockedBuffer collects log lines written from several goroutines.
type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	n, err := l.b.Write(p)
	if err != nil {
		return n, fmt.Errorf("log buffer: %w", err)
	}
	return n, nil
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

type env struct {
	cs    *mcp.ClientSession
	spans *tracetest.SpanRecorder
	logs  *lockedBuffer
}

func connect(t *testing.T, h Hub, opts *mcp.ClientOptions) env {
	t.Helper()
	rec := tracetest.NewSpanRecorder()
	logs := &lockedBuffer{}
	hubURL, err := url.Parse("https://beszel.example.com")
	if err != nil {
		t.Fatal(err)
	}
	srv := NewServer(Deps{
		Hub: h, HubURL: hubURL, HubVersion: "0.21.1", TokenExpiresAt: captured.Add(10 * 365 * 24 * time.Hour),
		Logger: slog.New(slog.NewJSONHandler(logs, nil)),
		Tracer: sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec)),
		Now:    func() time.Time { return captured }, Version: "test",
	})
	st, ct := mcp.NewInMemoryTransports()
	ss, err := srv.Connect(t.Context(), st, nil)
	if err != nil {
		t.Fatal(err)
	}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "v0"}, opts).Connect(t.Context(), ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Close(); _ = ss.Close() })
	return env{cs: cs, spans: rec, logs: logs}
}

func call(t *testing.T, cs *mcp.ClientSession, args map[string]any, meta mcp.Meta) *mcp.CallToolResult {
	t.Helper()
	return callTool(t, cs, "fleet_status", args, meta)
}

func callTool(t *testing.T, cs *mcp.ClientSession, name string, args map[string]any, meta mcp.Meta) *mcp.CallToolResult {
	t.Helper()
	res, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: name, Arguments: args, Meta: meta})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	return res
}

func text(res *mcp.CallToolResult) string {
	var sb strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			sb.WriteString(tc.Text)
		}
	}
	return sb.String()
}

// TestFleetStatusMatchesReport: over MCP, the structured result is exactly
// the report package's golden output for the same fixtures.
func TestFleetStatusMatchesReport(t *testing.T) {
	tests := []struct {
		name   string
		args   map[string]any
		golden string
	}{
		{name: "all", args: map[string]any{}, golden: "../report/testdata/fleet_all.json"},
		// Regression: a schema default plus omitted arguments panicked inside the SDK.
		{name: "no arguments at all", args: nil, golden: "../report/testdata/fleet_all.json"},
		{name: "problems only", args: map[string]any{"problems_only": true}, golden: "../report/testdata/fleet_problems.json"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			res := call(t, connect(t, fixtureHub(t), nil).cs, tt.args, nil)
			if res.IsError {
				t.Fatalf("tool error: %s", text(res))
			}
			want, err := os.ReadFile(tt.golden)
			if err != nil {
				t.Fatal(err)
			}
			var got, exp any
			b, _ := json.Marshal(res.StructuredContent)
			if err := json.Unmarshal(b, &got); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(want, &exp); err != nil {
				t.Fatal(err)
			}
			if fmt.Sprint(got) != fmt.Sprint(exp) {
				t.Errorf("structured content differs from %s\ngot %s", tt.golden, b)
			}
		})
	}
}

func TestFleetStatusErrors(t *testing.T) {
	tests := []struct {
		name    string
		fail    map[string]error
		args    map[string]any
		wantErr string
	}{
		{name: "token rejected", fail: map[string]error{"Systems": hub.ErrUnauthorized}, wantErr: "beszelscope token set"},
		{name: "hub unreachable", fail: map[string]error{"Systems": hub.ErrUnreachable}, wantErr: "retry later"},
		{name: "other hub error", fail: map[string]error{"Systems": errors.New("HTTP 500")}, wantErr: "hub request failed"},
		{name: "unknown system", args: map[string]any{"systems": []any{"mail"}}, wantErr: `no system "mail"; known: alpha, bravo, charlie`},
		{name: "too many systems", args: map[string]any{"systems": manyNames(21)}, wantErr: "systems"},
		{name: "duplicate systems", args: map[string]any{"systems": []any{"alpha", "alpha"}}, wantErr: "systems"},
		{name: "unknown argument", args: map[string]any{"verbose": true}, wantErr: "verbose"},
		{name: "wrong type", args: map[string]any{"problems_only": "yes"}, wantErr: "problems_only"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			h := fixtureHub(t)
			h.fail = tt.fail
			res := call(t, connect(t, h, nil).cs, tt.args, nil)
			if !res.IsError || !strings.Contains(text(res), tt.wantErr) {
				t.Errorf("IsError=%v text=%q; want an error containing %q", res.IsError, text(res), tt.wantErr)
			}
		})
	}
}

func manyNames(n int) []any {
	out := make([]any, n)
	for i := range out {
		out[i] = fmt.Sprintf("s%d", i)
	}
	return out
}

func TestFleetStatusPartialFailure(t *testing.T) {
	tests := []struct {
		method  string
		warning string
		absent  string // must not appear
	}{
		{method: "Services", warning: "could not read systemd services"},
		{method: "Containers", warning: "could not read containers"},
		{method: "SmartDevices", warning: "could not read SMART devices"},
		{method: "ZfsPools", warning: "could not read storage pools"},
		{method: "Monitors", warning: "could not read network monitors"},
		// alpha is down, so its container history is never fetched.
		{method: "ContainerStats", warning: "could not read container history of bravo", absent: "container history of alpha"},
	}
	for _, tt := range tests {
		t.Run(tt.method, func(t *testing.T) {
			t.Parallel()
			h := fixtureHub(t)
			h.fail[tt.method] = errors.New("HTTP 503")
			res := call(t, connect(t, h, nil).cs, map[string]any{}, nil)
			out, _ := json.Marshal(res.StructuredContent)
			if res.IsError || !strings.Contains(string(out), tt.warning) || !strings.Contains(string(out), `"name":"alpha"`) ||
				(tt.absent != "" && strings.Contains(string(out), tt.absent)) {
				t.Errorf("IsError=%v out=%s; want rows plus warning %q and no %q", res.IsError, out, tt.warning, tt.absent)
			}
		})
	}
}

// ambiguousHub has two systems that the prefix "web" matches.
func ambiguousHub() *fakeHub {
	return &fakeHub{fail: map[string]error{}, systems: []hub.System{
		{ID: "aaaaaaaaaaaaaa1", Name: "web-01", Status: "up", Updated: hub.Time{Time: captured}},
		{ID: "aaaaaaaaaaaaaa2", Name: "web-02", Status: "up", Updated: hub.Time{Time: captured}},
	}}
}

func TestAmbiguousNameElicitation(t *testing.T) {
	formOnly := &mcp.ClientCapabilities{Elicitation: &mcp.ElicitationCapabilities{Form: &mcp.FormElicitationCapabilities{}}}
	tests := []struct {
		name     string
		opts     func(asked *atomic.Int32) *mcp.ClientOptions
		wantRows string // names of returned rows, or "" for a tool error
		wantAsks int32
	}{
		{
			name: "user picks web-02", wantRows: "web-02", wantAsks: 1,
			opts: func(asked *atomic.Int32) *mcp.ClientOptions {
				return &mcp.ClientOptions{Capabilities: formOnly, ElicitationHandler: func(context.Context, *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
					asked.Add(1)
					return &mcp.ElicitResult{Action: "accept", Content: map[string]any{"system": "web-02 (aaaaaaaaaaaaaa2)"}}, nil
				}}
			},
		},
		{
			name: "user declines", wantAsks: 1,
			opts: func(asked *atomic.Int32) *mcp.ClientOptions {
				return &mcp.ClientOptions{Capabilities: formOnly, ElicitationHandler: func(context.Context, *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
					asked.Add(1)
					return &mcp.ElicitResult{Action: "decline"}, nil
				}}
			},
		},
		{
			name: "client cannot elicit", wantAsks: 0,
			opts: func(*atomic.Int32) *mcp.ClientOptions { return nil },
		},
		{
			name: "url-only client is not asked a form", wantAsks: 0,
			opts: func(asked *atomic.Int32) *mcp.ClientOptions {
				return &mcp.ClientOptions{
					Capabilities: &mcp.ClientCapabilities{Elicitation: &mcp.ElicitationCapabilities{URL: &mcp.URLElicitationCapabilities{}}},
					ElicitationHandler: func(context.Context, *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
						asked.Add(1)
						return &mcp.ElicitResult{Action: "cancel"}, nil
					},
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var asked atomic.Int32
			res := call(t, connect(t, ambiguousHub(), tt.opts(&asked)).cs, map[string]any{"systems": []any{"web"}}, nil)
			got := ""
			if !res.IsError {
				var out struct {
					Systems []struct {
						Name string `json:"name"`
					} `json:"systems"`
				}
				b, _ := json.Marshal(res.StructuredContent)
				_ = json.Unmarshal(b, &out)
				var names []string
				for _, s := range out.Systems {
					names = append(names, s.Name)
				}
				got = strings.Join(names, ",")
			} else if !strings.Contains(text(res), "web-01 (aaaaaaaaaaaaaa1), web-02 (aaaaaaaaaaaaaa2)") {
				t.Errorf("error %q does not list the candidates", text(res))
			}
			if got != tt.wantRows || asked.Load() != tt.wantAsks {
				t.Errorf("rows %q after %d asks; want %q after %d", got, asked.Load(), tt.wantRows, tt.wantAsks)
			}
		})
	}
}

func TestTokenRenewalOffer(t *testing.T) {
	both := &mcp.ClientCapabilities{Elicitation: &mcp.ElicitationCapabilities{
		Form: &mcp.FormElicitationCapabilities{}, URL: &mcp.URLElicitationCapabilities{},
	}}
	tests := []struct {
		name     string
		caps     *mcp.ClientCapabilities
		hubErr   error
		wantAsks int32
	}{
		{name: "url-capable client asked once across calls", caps: both, hubErr: hub.ErrUnauthorized, wantAsks: 1},
		{name: "form-only client never asked", caps: &mcp.ClientCapabilities{Elicitation: &mcp.ElicitationCapabilities{Form: &mcp.FormElicitationCapabilities{}}}, hubErr: hub.ErrUnauthorized, wantAsks: 0},
		{name: "outage is not a token problem", caps: both, hubErr: hub.ErrUnreachable, wantAsks: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var asked atomic.Int32
			var gotURL atomic.Value
			opts := &mcp.ClientOptions{Capabilities: tt.caps, ElicitationHandler: func(_ context.Context, r *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
				asked.Add(1)
				gotURL.Store(r.Params.URL)
				return &mcp.ElicitResult{Action: "accept"}, nil
			}}
			h := fixtureHub(t)
			h.fail["Systems"] = tt.hubErr
			e := connect(t, h, opts)
			for range 2 {
				if res := call(t, e.cs, map[string]any{}, nil); !res.IsError {
					t.Fatal("want a tool error")
				}
			}
			if asked.Load() != tt.wantAsks {
				t.Errorf("asked %d times, want %d", asked.Load(), tt.wantAsks)
			}
			if tt.wantAsks > 0 && gotURL.Load() != "https://beszel.example.com/_/#/collections?collection=users" {
				t.Errorf("url = %v", gotURL.Load())
			}
		})
	}
}

func TestCallSpan(t *testing.T) {
	const parentTrace = "4bf92f3577b34da6a3ce929d0e0e4736"
	tests := []struct {
		name      string
		fail      map[string]error
		meta      mcp.Meta
		wantError bool
		wantTrace string // "" means a new trace
	}{
		{name: "ok call", fail: map[string]error{}},
		{name: "failed call marks the span", fail: map[string]error{"Systems": hub.ErrUnreachable}, wantError: true},
		{
			name: "client trace is continued", fail: map[string]error{},
			meta:      mcp.Meta{"traceparent": "00-" + parentTrace + "-00f067aa0ba902b7-01"},
			wantTrace: parentTrace,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			h := fixtureHub(t)
			h.fail = tt.fail
			e := connect(t, h, nil)
			call(t, e.cs, map[string]any{"problems_only": true}, tt.meta)
			spans := e.spans.Ended()
			if len(spans) != 1 {
				t.Fatalf("got %d spans, want exactly 1", len(spans))
			}
			s := spans[0]
			attrs := map[string]string{}
			for _, a := range s.Attributes() {
				attrs[string(a.Key)] = a.Value.String()
			}
			isErr := s.Status().Code.String() == "Error"
			if s.Name() != "mcp.tools/call fleet_status" || s.SpanKind() != trace.SpanKindServer ||
				attrs["mcp.tool.arguments"] != `{"problems_only":true}` || isErr != tt.wantError {
				t.Errorf("span %q kind %v attrs %v error %v", s.Name(), s.SpanKind(), attrs, isErr)
			}
			if tt.wantTrace != "" && s.SpanContext().TraceID().String() != tt.wantTrace {
				t.Errorf("trace %s, want the client's %s", s.SpanContext().TraceID(), tt.wantTrace)
			}
			if tt.wantTrace == "" && s.Parent().IsValid() {
				t.Errorf("span has a parent %v; want a root span", s.Parent())
			}
		})
	}
}

func TestCompletionLogAndPanics(t *testing.T) {
	tests := []struct {
		name      string
		panicOn   string
		fail      map[string]error
		wantLog   []string
		wantError string
	}{
		{name: "ok", fail: map[string]error{}, wantLog: []string{`"msg":"tool.call.completed"`, `"outcome":"ok"`, `"duration_ms":0`}},
		{name: "error", fail: map[string]error{"Systems": hub.ErrUnreachable}, wantLog: []string{`"outcome":"error"`}, wantError: "retry later"},
		{
			name: "panic is recovered", panicOn: "Systems", fail: map[string]error{},
			wantLog:   []string{`"msg":"tool.call.panic"`, `"panic":"boom in Systems"`, `"outcome":"error"`},
			wantError: "internal error in fleet_status",
		},
		{
			name: "panic in a parallel fetch is recovered", panicOn: "Monitors", fail: map[string]error{},
			wantLog:   []string{`"msg":"tool.call.panic"`},
			wantError: "internal error in fleet_status",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			h := fixtureHub(t)
			h.fail, h.panicOn = tt.fail, tt.panicOn
			e := connect(t, h, nil)
			res := call(t, e.cs, map[string]any{}, nil)
			if (tt.wantError != "") != res.IsError || !strings.Contains(text(res), tt.wantError) {
				t.Errorf("IsError=%v text=%q, want error %q", res.IsError, text(res), tt.wantError)
			}
			for _, w := range tt.wantLog {
				if !strings.Contains(e.logs.String(), w) {
					t.Errorf("log lacks %s:\n%s", w, e.logs)
				}
			}
			// The server must still answer once the fault is gone.
			h.panicOn, h.fail = "", map[string]error{}
			if res := call(t, e.cs, map[string]any{}, nil); res.IsError {
				t.Errorf("second call failed: %s", text(res))
			}
		})
	}
}

func TestListing(t *testing.T) {
	e := connect(t, fixtureHub(t), nil)
	tool := func(name string) func(t *testing.T) string {
		return func(t *testing.T) string {
			res, err := e.cs.ListTools(t.Context(), nil)
			if err != nil {
				t.Fatal(err)
			}
			for _, tl := range res.Tools {
				if tl.Name == name {
					b, _ := json.Marshal(tl)
					return string(b)
				}
			}
			t.Fatalf("no tool %s", name)
			return ""
		}
	}
	tests := []struct {
		name string
		got  func(t *testing.T) string
		want []string
	}{
		{name: "server instructions", want: []string{"Use fleet_status for the whole fleet", "call system_detail for it directly"}, got: func(*testing.T) string {
			return e.cs.InitializeResult().Instructions
		}},
		{name: "fleet_status definition", got: tool("fleet_status"), want: []string{
			`"readOnlyHint":true`, `"openWorldHint":false`, `"maxItems":20`,
			`"uniqueItems":true`, `"additionalProperties":false`, "for one named server, call system_detail directly",
			`"items":{"type":"string"},"maxItems":20,"type":"array"`, "unique prefix works",
		}},
		{name: "system_detail definition", got: tool("system_detail"), want: []string{
			`"readOnlyHint":true`, `"openWorldHint":false`, `"required":["system"]`, `"enum":["summary","core","network","disk","gpu","containers","services","updates"]`,
			`"maxItems":8,"type":"array"`, `"enum":["cpu","mem","name"]`, `"maximum":200,"minimum":1`, `"uniqueItems":true`, `"additionalProperties":false`, "one server",
			"without sections you get summary", "every temperature and fan sensor", "PMU tdie sensors are the CPU dies", "unique prefix works",
			"listed in gone", "shape the containers and services lists only", "25 when omitted",
			"security updates first", "natively on Linux",
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.got(t)
			for _, w := range tt.want {
				if !strings.Contains(got, w) {
					t.Errorf("missing %s in %s", w, got)
				}
			}
			// metric_history does not exist yet; naming it made models stall.
			if strings.Contains(got, "metric_history") {
				t.Errorf("mentions metric_history: %s", got)
			}
		})
	}
}
