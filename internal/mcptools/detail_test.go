package mcptools

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/agrmohit/beszelscope/internal/hub"
)

// TestSystemDetailMatchesReport: over MCP, the structured result is exactly
// the report package's golden output for the same fixtures.
func TestSystemDetailMatchesReport(t *testing.T) {
	both := []any{"summary", "core", "network", "disk"}
	tests := []struct {
		name   string
		args   map[string]any
		golden string
	}{
		{name: "VPS", args: map[string]any{"system": "bravo", "sections": both}, golden: "detail_bravo.json"},
		{name: "by ID, any case of name ignored", args: map[string]any{"system": "CHARLIE", "sections": both}, golden: "detail_charlie.json"},
		{name: "down system", args: map[string]any{"system": "alpha", "sections": both}, golden: "detail_alpha.json"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			res := callTool(t, connect(t, fixtureHub(t), nil).cs, "system_detail", tt.args, nil)
			if res.IsError {
				t.Fatalf("tool error: %s", text(res))
			}
			want, err := os.ReadFile("../report/testdata/" + tt.golden)
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

func TestSystemDetailErrors(t *testing.T) {
	tests := []struct {
		name    string
		fail    map[string]error
		args    map[string]any
		wantErr string
	}{
		{name: "no arguments at all", args: nil, wantErr: "system"},
		{name: "no system", args: map[string]any{"sections": []any{"core"}}, wantErr: "system"},
		{name: "unknown system", args: map[string]any{"system": "mail"}, wantErr: `no system "mail"; known: alpha, bravo, charlie`},
		{name: "empty system", args: map[string]any{"system": ""}, wantErr: "empty system name"},
		{name: "unknown section", args: map[string]any{"system": "bravo", "sections": []any{"alerts"}}, wantErr: "sections"},
		{name: "duplicate section", args: map[string]any{"system": "bravo", "sections": []any{"core", "core"}}, wantErr: "sections"},
		{name: "unknown argument", args: map[string]any{"system": "bravo", "limit": 5}, wantErr: "limit"},
		// Sensor names do not say what they measure, so there is no filter.
		{name: "no filter argument", args: map[string]any{"system": "charlie", "filter": "cpu"}, wantErr: "filter"},
		{name: "wrong type", args: map[string]any{"system": []any{"bravo"}}, wantErr: "system"},
		{name: "token rejected", fail: map[string]error{"Systems": hub.ErrUnauthorized}, args: map[string]any{"system": "bravo"}, wantErr: "beszelscope token set"},
		{name: "hub unreachable", fail: map[string]error{"Systems": hub.ErrUnreachable}, args: map[string]any{"system": "bravo"}, wantErr: "retry later"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			h := fixtureHub(t)
			h.fail = tt.fail
			res := callTool(t, connect(t, h, nil).cs, "system_detail", tt.args, nil)
			if !res.IsError || !strings.Contains(text(res), tt.wantErr) {
				t.Errorf("IsError=%v text=%q; want an error containing %q", res.IsError, text(res), tt.wantErr)
			}
		})
	}
}

// TestSystemDetailReads: each section makes only the reads it needs, and a
// failed read leaves the rest of the answer intact.
func TestSystemDetailReads(t *testing.T) {
	older := func(h *fakeHub) {
		var old hub.SystemStats
		for _, r := range h.stats {
			if r.System == "f00000000000004" { // bravo
				old = r
			}
		}
		old.Created.Time = old.Created.Add(-2 * time.Minute)
		old.Stats.CPU = 99
		h.stats = append([]hub.SystemStats{old}, h.stats...)
	}
	tests := []struct {
		name   string
		fail   string
		setup  func(*fakeHub)
		args   map[string]any
		want   []string
		absent []string
		unread []string // hub methods that must not be called
		once   []string // hub methods that must be called exactly once
	}{
		{
			name: "newest stats sample wins, wherever it sits", setup: older, args: map[string]any{"system": "bravo", "sections": []any{"core"}},
			want: []string{`"cpu_pct":7`}, absent: []string{`"cpu_pct":99`},
		},
		{
			name: "details fail: summary without OS facts", fail: "SystemDetails", args: map[string]any{"system": "bravo"},
			want:   []string{"could not read system details, so the summary lacks OS and CPU facts", `"cpu_pct":7`},
			absent: []string{`"os":`},
		},
		{
			name: "services fail: warning, summary still there", fail: "Services", args: map[string]any{"system": "bravo"},
			want: []string{"could not read systemd services, so related problems may be missing", `"os":"Fedora`},
		},
		{
			name: "container history fails", fail: "ContainerStats", args: map[string]any{"system": "bravo"},
			want: []string{"could not read container history"},
		},
		{
			name: "down system: no container history read", fail: "ContainerStats", args: map[string]any{"system": "alpha"},
			want: []string{`"status_down"`}, absent: []string{"container history"},
		},
		{
			name: "stats fail: core says so, no warning", fail: "SystemStats", args: map[string]any{"system": "bravo", "sections": []any{"core"}},
			want: []string{`"unavailable":"could not read stats: HTTP 503"`}, absent: []string{"warnings"},
		},
		{
			name: "down system: no stats read", fail: "SystemStats", args: map[string]any{"system": "alpha", "sections": []any{"core"}},
			want: []string{`"unavailable":"system is down`}, absent: []string{"HTTP 503"}, unread: []string{"SystemStats"},
		},
		{
			name: "summary alone reads no stats", fail: "SystemStats", args: map[string]any{"system": "bravo"},
			want: []string{`"summary"`}, absent: []string{"HTTP 503", `"core"`}, unread: []string{"SystemStats"},
		},
		{
			name: "core alone reads no details", fail: "SystemDetails", args: map[string]any{"system": "bravo", "sections": []any{"core"}},
			want: []string{`"core"`}, absent: []string{"HTTP 503", `"summary"`},
			unread: []string{"SystemDetails", "Services", "Containers", "SmartDevices", "ZfsPools", "Monitors", "ContainerStats"},
		},
		{
			name: "network alone: monitors and stats only", args: map[string]any{"system": "bravo", "sections": []any{"network"}},
			want:   []string{`"network"`, `"interfaces"`, `"monitors"`},
			unread: []string{"SystemDetails", "Services", "Containers", "SmartDevices", "ZfsPools", "ContainerStats"},
			once:   []string{"Monitors", "SystemStats"},
		},
		{
			name: "disk alone: SMART, pools and stats only", args: map[string]any{"system": "bravo", "sections": []any{"disk"}},
			want:   []string{`"filesystems"`, `"pools"`},
			unread: []string{"SystemDetails", "Services", "Containers", "Monitors", "ContainerStats"},
			once:   []string{"SmartDevices", "ZfsPools", "SystemStats"},
		},
		{
			name: "down system disk: last SMART, no stats read", fail: "SystemStats", args: map[string]any{"system": "alpha", "sections": []any{"disk"}},
			want: []string{`"unavailable":"system is down`, `"smart":[{`, `"NS_01@1"`}, absent: []string{"HTTP 503"},
			unread: []string{"SystemStats"},
		},
		{
			name: "every section reads each source once", args: map[string]any{"system": "bravo", "sections": []any{"summary", "core", "network", "disk"}},
			want: []string{`"summary"`, `"core"`, `"network"`, `"disk"`},
			once: []string{"Systems", "SystemDetails", "Services", "Containers", "SmartDevices", "ZfsPools", "Monitors", "ContainerStats", "SystemStats"},
		},
		{
			name: "monitors fail: network keeps traffic and warns", fail: "Monitors", args: map[string]any{"system": "bravo", "sections": []any{"network"}},
			want: []string{"could not read network monitors, so its rows and related problems may be missing", `"interfaces"`},
		},
		{
			name: "core alone reads no problem sources", fail: "Services", args: map[string]any{"system": "bravo", "sections": []any{"core"}},
			want: []string{`"core"`}, absent: []string{"HTTP 503"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			h := fixtureHub(t)
			if tt.fail != "" {
				h.fail[tt.fail] = errors.New("HTTP 503")
			}
			if tt.setup != nil {
				tt.setup(h)
			}
			res := callTool(t, connect(t, h, nil).cs, "system_detail", tt.args, nil)
			out, _ := json.Marshal(res.StructuredContent)
			if res.IsError {
				t.Fatalf("tool error: %s", text(res))
			}
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
			for _, m := range tt.once {
				if n := h.count(m); n != 1 {
					t.Errorf("%s called %d times, want once", m, n)
				}
			}
		})
	}
}
