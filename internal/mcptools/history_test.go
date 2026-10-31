package mcptools

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestMetricHistoryErrors(t *testing.T) {
	tests := []struct {
		name    string
		fail    map[string]error
		args    map[string]any
		wantErr string
		unread  []string // checked before any hub read
	}{
		{name: "no arguments", args: nil, wantErr: "systems"},
		{name: "no metrics", args: map[string]any{"systems": []any{"bravo"}}, wantErr: "metrics"},
		{name: "empty systems", args: map[string]any{"systems": []any{}, "metrics": []any{"cpu"}}, wantErr: "systems"},
		{name: "six systems", args: map[string]any{"systems": []any{"a", "b", "c", "d", "e", "f"}, "metrics": []any{"cpu"}}, wantErr: "systems"},
		{name: "seven metrics", args: map[string]any{"systems": []any{"bravo"}, "metrics": []any{"cpu", "mem", "swap", "load", "disk", "net", "gpu"}}, wantErr: "metrics"},
		{name: "duplicate metric", args: map[string]any{"systems": []any{"bravo"}, "metrics": []any{"cpu", "cpu"}}, wantErr: "metrics"},
		{name: "unknown window", args: map[string]any{"systems": []any{"bravo"}, "metrics": []any{"cpu"}, "window": "1w"}, wantErr: "window"},
		{name: "points above 60", args: map[string]any{"systems": []any{"bravo"}, "metrics": []any{"cpu"}, "points": 61}, wantErr: "points"},
		{name: "negative points", args: map[string]any{"systems": []any{"bravo"}, "metrics": []any{"cpu"}, "points": -1}, wantErr: "points"},
		{
			name: "unknown kind fails before any read", args: map[string]any{"systems": []any{"bravo"}, "metrics": []any{"cpu_temp"}},
			wantErr: `unknown metric "cpu_temp"`, unread: []string{"Systems", "SystemStats"},
		},
		{name: "unknown system", args: map[string]any{"systems": []any{"mail"}, "metrics": []any{"cpu"}}, wantErr: `no system "mail"`},
		{
			name: "unknown container lists the valid ones", args: map[string]any{"systems": []any{"bravo"}, "metrics": []any{"container:nope"}, "window": "1h"},
			wantErr: `bravo has no container "nope" in this window; valid: actual, app-1`,
		},
		{
			name: "unknown monitor lists the valid ones", args: map[string]any{"systems": []any{"bravo"}, "metrics": []any{"monitor:https://nope/"}},
			wantErr: `bravo has no network monitor "https://nope/"; valid: https://example.com/`,
		},
		{name: "hub read fails", fail: map[string]error{"Systems": errors.New("HTTP 503")}, args: map[string]any{"systems": []any{"bravo"}, "metrics": []any{"cpu"}}, wantErr: "HTTP 503"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			h := fixtureHub(t)
			for k, v := range tt.fail {
				h.fail[k] = v
			}
			res := callTool(t, connect(t, h, nil).cs, "metric_history", tt.args, nil)
			if !res.IsError || !strings.Contains(text(res), tt.wantErr) {
				t.Errorf("IsError=%v text=%q; want an error containing %q", res.IsError, text(res), tt.wantErr)
			}
			for _, m := range tt.unread {
				if h.called(m) {
					t.Errorf("%s was called", m)
				}
			}
		})
	}
}

// TestMetricHistoryReads: one read per system and source, of the window's
// stats type, and a failed read empties only the series that need it.
func TestMetricHistoryReads(t *testing.T) {
	tests := []struct {
		name   string
		fail   string
		args   map[string]any
		want   []string
		absent []string
		unread []string
		count  map[string]int
	}{
		{
			name: "stats metrics share one read; window picks the type", args: map[string]any{"systems": []any{"bravo"}, "metrics": []any{"cpu", "mem", "net", "load"}, "window": "7d"},
			want:   []string{`"expected_buckets":84`, `"buckets":84`, `"peak":98.09`, `"span_s":604800`},
			unread: []string{"ContainerStats", "MonitorStats", "Monitors"},
			count:  map[string]int{"SystemStats": 1, "Systems": 1},
		},
		{
			name: "default window is 24h", args: map[string]any{"systems": []any{"bravo"}, "metrics": []any{"cpu"}},
			want: []string{`"span_s":86400`, `"resolution_s":1200`, `"buckets":72`},
		},
		{
			name: "container metric reads container stats only", args: map[string]any{"systems": []any{"bravo"}, "metrics": []any{"container:gitea"}, "window": "24h"},
			want: []string{`"part":"cpu"`, `"part":"mem"`}, unread: []string{"SystemStats", "MonitorStats", "Monitors"},
			count: map[string]int{"ContainerStats": 1},
		},
		{
			name: "monitor metric reads the monitor list and monitor stats", args: map[string]any{"systems": []any{"bravo"}, "metrics": []any{"monitor:https://example.com/"}, "window": "1h"},
			want: []string{`"unit":"ms"`, `"loss_pct":0`, `"expected_buckets":6`}, unread: []string{"SystemStats", "ContainerStats"},
			count: map[string]int{"Monitors": 1, "MonitorStats": 1},
		},
		{
			name: "one read per system; a name and its ID count once", args: map[string]any{"systems": []any{"bravo", "charlie", "f00000000000004"}, "metrics": []any{"cpu", "load"}},
			want:  []string{`"system":"bravo"`, `"system":"charlie"`, `"note":"no samples in this window"`},
			count: map[string]int{"SystemStats": 2},
		},
		{
			name: "down server: history is still read", args: map[string]any{"systems": []any{"alpha"}, "metrics": []any{"cpu"}},
			want: []string{`"system":"alpha"`}, count: map[string]int{"SystemStats": 1},
		},
		{
			name: "stats read fails: those series say so, the rest stand", fail: "SystemStats", args: map[string]any{"systems": []any{"bravo"}, "metrics": []any{"cpu", "container:gitea"}},
			want:   []string{`"note":"could not read history: HTTP 503"`, `"part":"mem","system":"bravo","unit":"bytes"`},
			absent: []string{"warnings"},
		},
		{
			name: "monitor list fails: monitor series say so", fail: "Monitors", args: map[string]any{"systems": []any{"bravo"}, "metrics": []any{"monitor:https://example.com/"}},
			want: []string{`"note":"could not read history: HTTP 503"`},
		},
		{
			name: "points shrink to the budget with a hint", args: map[string]any{"systems": []any{"bravo", "charlie"}, "metrics": []any{"cpu", "net", "disk_io"}, "points": 60},
			want: []string{"points lowered from 60 to 36 so 10 series stay within 360 values"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			h := fixtureHub(t)
			if tt.fail != "" {
				h.fail[tt.fail] = errors.New("HTTP 503")
			}
			res := callTool(t, connect(t, h, nil).cs, "metric_history", tt.args, nil)
			if res.IsError {
				t.Fatalf("tool error: %s", text(res))
			}
			out, _ := json.Marshal(res.StructuredContent)
			for _, w := range tt.want {
				if !strings.Contains(string(out), w) {
					t.Errorf("missing %s in %s", w, out)
				}
			}
			for _, a := range tt.absent {
				if strings.Contains(string(out), a) {
					t.Errorf("unexpected %s in %s", a, out)
				}
			}
			for _, m := range tt.unread {
				if h.called(m) {
					t.Errorf("%s was called", m)
				}
			}
			for m, n := range tt.count {
				if got := h.count(m); got != n {
					t.Errorf("%s called %d times, want %d", m, got, n)
				}
			}
		})
	}
}
