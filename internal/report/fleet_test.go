package report

import (
	"encoding/json"
	"flag"
	"math"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/agrmohit/beszelscope/internal/hub"
)

var update = flag.Bool("update", false, "rewrite golden files")

var now = time.Date(2026, 10, 30, 22, 0, 0, 0, time.UTC)

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

func ago(d time.Duration) hub.Time { return hub.Time{Time: now.Add(-d)} }

func upSystem() hub.System {
	return hub.System{
		ID: "aaaaaaaaaaaaaa1", Name: "web", Status: "up", Updated: ago(30 * time.Second),
		Info: hub.SystemInfo{
			CPU: 12.345, MemPct: 70.04, DiskPct: 50, BandwidthBytes: 2048, AgentVersion: "0.21.1",
			LoadAvg: [3]float64{2, 1, 1}, Threads: 8, DashTemp: 48.26, Uptime: 3600,
		},
	}
}

func TestResolve(t *testing.T) {
	systems := []hub.System{
		{ID: "aaaaaaaaaaaaaa1", Name: "web-01"},
		{ID: "aaaaaaaaaaaaaa2", Name: "web-02"},
		{ID: "aaaaaaaaaaaaaa3", Name: "NAS"},
		{ID: "aaaaaaaaaaaaaa4", Name: "db"},
		{ID: "aaaaaaaaaaaaaa5", Name: "db-replica"},
	}
	tests := []struct {
		name    string
		ref     string
		want    string // system ID, or "" for an error
		wantErr string
	}{
		{name: "exact id", ref: "aaaaaaaaaaaaaa3", want: "aaaaaaaaaaaaaa3"},
		{name: "exact name", ref: "web-01", want: "aaaaaaaaaaaaaa1"},
		{name: "name ignores case", ref: "nas", want: "aaaaaaaaaaaaaa3"},
		{name: "unique prefix", ref: "N", want: "aaaaaaaaaaaaaa3"},
		{name: "exact name beats prefix", ref: "db", want: "aaaaaaaaaaaaaa4"},
		{name: "ambiguous prefix lists ids", ref: "web", wantErr: "web-01 (aaaaaaaaaaaaaa1), web-02 (aaaaaaaaaaaaaa2)"},
		{name: "unknown lists names", ref: "mail", wantErr: "known: NAS, db, db-replica, web-01, web-02"},
		{name: "empty", ref: "", wantErr: "empty system name"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := Resolve(systems, tt.ref)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Errorf("err = %v, want it to contain %q", err, tt.wantErr)
				}
				return
			}
			if err != nil || got.ID != tt.want {
				t.Errorf("got %q (err %v), want %q", got.ID, err, tt.want)
			}
		})
	}
}

// problems flattens a result to "system: code(count): detail" lines.
func problems(r FleetResult) []string {
	var out []string
	for _, row := range r.Systems {
		for _, p := range row.Problems {
			out = append(out, row.Name+": "+p.Code+"("+itoa(p.Count)+")@"+p.Section+": "+p.Detail)
		}
	}
	return out
}

func itoa(n int) string { b, _ := json.Marshal(n); return string(b) }

func TestFleetProblems(t *testing.T) {
	sys := upSystem()
	tests := []struct {
		name string
		in   FleetInput
		want []string
	}{
		{name: "healthy", in: FleetInput{Systems: []hub.System{sys}}, want: nil},
		{
			name: "down for 7m", in: FleetInput{Systems: []hub.System{{Name: "web", Status: "down", Updated: ago(7 * time.Minute)}}},
			want: []string{"web: status_down(0)@summary: down for 7m"},
		},
		{
			name: "undefined status", in: FleetInput{Systems: []hub.System{{Name: "web", Status: "rebooting", Updated: ago(time.Hour)}}},
			want: []string{`web: status_down(0)@summary: unknown status "rebooting"`},
		},
		{
			name: "paused", in: FleetInput{Systems: []hub.System{{Name: "web", Status: "paused", Updated: ago(time.Hour)}}},
			want: []string{"web: status_paused(0)@summary: monitoring paused"},
		},
		{
			name: "pending", in: FleetInput{Systems: []hub.System{{Name: "web", Status: "pending", Updated: ago(time.Hour)}}},
			want: []string{"web: status_pending(0)@summary: agent has not connected"},
		},
		{
			name: "stale just over 180s", in: FleetInput{Systems: []hub.System{func() hub.System { s := sys; s.Updated = ago(181 * time.Second); return s }()}},
			want: []string{"web: stale(0)@summary: last poll 3m ago"},
		},
		{
			name: "not stale at exactly 180s", in: FleetInput{Systems: []hub.System{func() hub.System { s := sys; s.Updated = ago(180 * time.Second); return s }()}},
			want: nil,
		},
		{
			name: "failed units grouped and sorted",
			in: FleetInput{Systems: []hub.System{sys}, Services: []hub.Service{
				{System: sys.ID, Name: "zeta.service", State: 2},
				{System: sys.ID, Name: "alpha.service", State: 2},
				{System: sys.ID, Name: "ok.service", State: 0},
				{System: "other", Name: "x.service", State: 2},
			}},
			want: []string{"web: service_failed(2)@services: alpha.service, zeta.service"},
		},
		{
			name: "more than 5 names are capped",
			in: FleetInput{Systems: []hub.System{sys}, Services: []hub.Service{
				{System: sys.ID, Name: "a", State: 2},
				{System: sys.ID, Name: "b", State: 2},
				{System: sys.ID, Name: "c", State: 2},
				{System: sys.ID, Name: "d", State: 2},
				{System: sys.ID, Name: "e", State: 2},
				{System: sys.ID, Name: "f", State: 2},
				{System: sys.ID, Name: "g", State: 2},
			}},
			want: []string{"web: service_failed(7)@services: a, b, c, d, e +2 more"},
		},
		{
			name: "unhealthy container",
			in: FleetInput{Systems: []hub.System{sys}, Containers: []hub.Container{
				{System: sys.ID, Name: "app", Health: 3, Updated: now.Add(-time.Minute).UnixMilli()},
				{System: sys.ID, Name: "ok", Health: 2, Updated: now.Add(-time.Minute).UnixMilli()},
			}},
			want: []string{"web: container_unhealthy(1)@containers: app"},
		},
		{
			name: "stopped container: stale row",
			in: FleetInput{Systems: []hub.System{sys}, Containers: []hub.Container{
				{System: sys.ID, Name: "app", Health: 3, Updated: now.Add(-4 * time.Minute).UnixMilli()},
				{System: sys.ID, Name: "fresh", Updated: now.Add(-3 * time.Minute).UnixMilli()},
			}},
			want: []string{"web: container_gone(1)@containers: app"},
		},
		{
			// Recreating a container gives it a new ID, so Beszel keeps the
			// old row for 10 minutes beside the live one.
			name: "recreated container: stale old row beside a live one is not gone",
			in: FleetInput{Systems: []hub.System{sys}, Containers: []hub.Container{
				{System: sys.ID, Name: "app", Updated: now.Add(-5 * time.Minute).UnixMilli()},
				{System: sys.ID, Name: "app", Updated: now.Add(-10 * time.Second).UnixMilli()},
			}},
			want: nil,
		},
		{
			name: "container ran in the last hour, gone now",
			in: FleetInput{
				Systems:    []hub.System{sys},
				Containers: []hub.Container{{System: sys.ID, Name: "still", Updated: now.UnixMilli()}},
				RecentContainers: []hub.ContainerStats{
					{System: sys.ID, Stats: []hub.ContainerStat{{Name: "still"}, {Name: "crashed"}}},
					{System: sys.ID, Stats: []hub.ContainerStat{{Name: "crashed"}}},
					{System: "other", Stats: []hub.ContainerStat{{Name: "elsewhere"}}},
				},
			},
			want: []string{"web: container_gone(1)@containers: crashed"},
		},
		{
			name: "smart failed and warning; model preferred",
			in: FleetInput{Systems: []hub.System{sys}, Smart: []hub.SmartDevice{
				{System: sys.ID, Name: "/dev/sda", Model: "WDC A", State: "FAILED"},
				{System: sys.ID, Name: "/dev/sdb", State: "WARNING"},
				{System: sys.ID, Name: "/dev/sdc", State: "UNKNOWN"},
				{System: sys.ID, Name: "/dev/sdd", State: "PASSED"},
			}},
			want: []string{"web: smart_failed(1)@disk: WDC A", "web: smart_warning(1)@disk: /dev/sdb"},
		},
		{
			name: "degraded pool",
			in: FleetInput{Systems: []hub.System{sys}, Pools: []hub.ZfsPool{
				{System: sys.ID, Name: "tank", Health: "ONLINE"},
				{System: sys.ID, Name: "backup", Health: "DEGRADED"},
				{System: sys.ID, Name: "new", Health: ""},
			}},
			want: []string{"web: pool_degraded(1)@disk: backup DEGRADED"},
		},
		{
			name: "monitor loss and certs; disabled ignored",
			in: FleetInput{Systems: []hub.System{sys}, Monitors: []hub.Monitor{
				{System: sys.ID, Target: "a.com", Enabled: true, Loss1h: 12.4},
				{System: sys.ID, Target: "b.com", Enabled: true, CertInfo: &hub.CertInfo{Expires: now.Add(-3 * 24 * time.Hour).UnixMilli()}},
				{System: sys.ID, Target: "c.com", Enabled: true, CertInfo: &hub.CertInfo{Expires: now.Add(9 * 24 * time.Hour).UnixMilli()}},
				{System: sys.ID, Target: "d.com", Enabled: true, CertInfo: &hub.CertInfo{Expires: now.Add(14 * 24 * time.Hour).UnixMilli()}},
				{System: sys.ID, Target: "e.com", Enabled: false, Loss1h: 100},
				{System: sys.ID, Target: "f.com", Enabled: true, CertInfo: &hub.CertInfo{}},
			}},
			want: []string{
				"web: monitor_failing(1)@network: a.com 12% loss",
				"web: cert_expired(1)@network: b.com 3d ago",
				"web: cert_expiring(1)@network: c.com in 9d",
			},
		},
		{
			name: "security updates",
			in: FleetInput{Systems: []hub.System{func() hub.System {
				s := sys
				s.Info.PackageUpdates = []uint16{12, 3}
				return s
			}()}},
			want: []string{"web: security_updates(3)@updates: 3 security updates pending"},
		},
		{
			name: "known zero security updates is no problem",
			in: FleetInput{Systems: []hub.System{func() hub.System {
				s := sys
				s.Info.PackageUpdates = []uint16{12, 0}
				return s
			}()}},
			want: nil,
		},
		{
			name: "exactly 5 names, no overflow note",
			in: FleetInput{Systems: []hub.System{sys}, Services: []hub.Service{
				{System: sys.ID, Name: "a", State: 2},
				{System: sys.ID, Name: "b", State: 2},
				{System: sys.ID, Name: "c", State: 2},
				{System: sys.ID, Name: "d", State: 2},
				{System: sys.ID, Name: "e", State: 2},
			}},
			want: []string{"web: service_failed(5)@services: a, b, c, d, e"},
		},
		{
			name: "updates without security is no problem",
			in: FleetInput{Systems: []hub.System{func() hub.System {
				s := sys
				s.Info.PackageUpdates = []uint16{12}
				return s
			}()}},
			want: nil,
		},
		{
			name: "down system hides its stale structural data",
			in: FleetInput{
				Systems:  []hub.System{{ID: sys.ID, Name: "web", Status: "down", Updated: ago(time.Hour)}},
				Services: []hub.Service{{System: sys.ID, Name: "x", State: 2}},
			},
			want: []string{"web: status_down(0)@summary: down for 1h"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			tt.in.Now = now
			r, err := Fleet(tt.in, FleetOptions{})
			if err != nil {
				t.Fatal(err)
			}
			if got := problems(r); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("got  %q\nwant %q", got, tt.want)
			}
		})
	}
}

func TestFleetRow(t *testing.T) {
	sys := upSystem()
	tests := []struct {
		name string
		sys  hub.System
		want string // the row as JSON
	}{
		{
			name: "up: rounded metrics",
			sys:  sys,
			want: `{"name":"web","status":"up","age_s":30,"cpu_pct":12.3,"mem_pct":70,"disk_pct":50,"temp_hottest_c":48.3,"load_per_thread":0.25,"net_bytes_per_s":2048,"uptime_s":3600,"agent":"0.21.1"}`,
		},
		{
			name: "extra fs fuller than root is named",
			sys: func() hub.System {
				s := sys
				s.Info.ExtraFsPct = map[string]float64{"/mnt/media": 91.26, "/mnt/b": 10}
				return s
			}(),
			want: `{"name":"web","status":"up","age_s":30,"cpu_pct":12.3,"mem_pct":70,"disk_pct":91.3,"disk_worst":"/mnt/media","temp_hottest_c":48.3,"load_per_thread":0.25,"net_bytes_per_s":2048,"uptime_s":3600,"agent":"0.21.1"}`,
		},
		{
			name: "no sensors, no threads, zero cpu kept",
			sys: func() hub.System {
				s := sys
				s.Info.DashTemp, s.Info.Threads, s.Info.CPU = 0, 0, 0
				return s
			}(),
			want: `{"name":"web","status":"up","age_s":30,"cpu_pct":0,"mem_pct":70,"disk_pct":50,"net_bytes_per_s":2048,"uptime_s":3600,"agent":"0.21.1"}`,
		},
		{
			name: "updates with unknown security",
			sys: func() hub.System {
				s := sys
				s.Info.PackageUpdates = []uint16{4}
				return s
			}(),
			want: `{"name":"web","status":"up","age_s":30,"cpu_pct":12.3,"mem_pct":70,"disk_pct":50,"temp_hottest_c":48.3,"load_per_thread":0.25,"net_bytes_per_s":2048,"uptime_s":3600,"agent":"0.21.1","updates":{"total":4,"security":null}}`,
		},
		{
			name: "NaN cpu is omitted, not an encode error",
			sys: func() hub.System {
				s := sys
				s.Info.CPU = math.NaN()
				return s
			}(),
			want: `{"name":"web","status":"up","age_s":30,"mem_pct":70,"disk_pct":50,"temp_hottest_c":48.3,"load_per_thread":0.25,"net_bytes_per_s":2048,"uptime_s":3600,"agent":"0.21.1"}`,
		},
		{
			name: "down: no live metrics",
			sys:  hub.System{Name: "web", Status: "down", Updated: ago(2 * time.Minute), Info: sys.Info},
			want: `{"name":"web","status":"down","last_seen_ago_s":120,"problems":[{"code":"status_down","detail":"down for 2m","section":"summary"}]}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			r, err := Fleet(FleetInput{Now: now, Systems: []hub.System{tt.sys}}, FleetOptions{})
			if err != nil {
				t.Fatal(err)
			}
			got, err := json.Marshal(r.Systems[0])
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != tt.want {
				t.Errorf("got  %s\nwant %s", got, tt.want)
			}
		})
	}
}

func TestFleetWarnings(t *testing.T) {
	sys := upSystem()
	old := sys
	old.Info.AgentVersion = "0.20.0"
	tests := []struct {
		name    string
		expires time.Time
		hub     string
		systems []hub.System
		want    []string
	}{
		{name: "far expiry", expires: now.Add(3650 * 24 * time.Hour), hub: "0.21.1", systems: []hub.System{sys}},
		{name: "unknown expiry", hub: "0.21.1", systems: []hub.System{sys}},
		{
			name: "expires in 21d", expires: now.Add(21 * 24 * time.Hour), systems: []hub.System{sys},
			want: []string{"hub token expires in 21d; renew it (see README)"},
		},
		{name: "exactly 30d is fine", expires: now.Add(30 * 24 * time.Hour), systems: []hub.System{sys}},
		{
			name: "expired", expires: now.Add(-49 * time.Hour), systems: []hub.System{sys},
			want: []string{"hub token expired 2d 1h ago; a human must renew it (see README)"},
		},
		{
			name: "agent version skew", hub: "0.21.1", systems: []hub.System{sys, old},
			want: []string{"web runs agent 0.20.0; hub is 0.21.1"},
		},
		{name: "unknown hub version: no skew claim", systems: []hub.System{old}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			r, err := Fleet(FleetInput{Now: now, HubVersion: tt.hub, TokenExpiresAt: tt.expires, Systems: tt.systems}, FleetOptions{})
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(r.Warnings, tt.want) {
				t.Errorf("got %q, want %q", r.Warnings, tt.want)
			}
		})
	}
}

func TestFleetSelection(t *testing.T) {
	healthy := upSystem()
	broken := hub.System{ID: "aaaaaaaaaaaaaa2", Name: "db", Status: "down", Updated: ago(time.Minute)}
	paused := hub.System{ID: "aaaaaaaaaaaaaa3", Name: "old", Status: "paused", Updated: ago(time.Hour)}
	all := []hub.System{healthy, broken, paused}
	tests := []struct {
		name       string
		systems    []hub.System // all when nil
		opts       FleetOptions
		wantRows   []string
		wantCounts Counts
		wantErr    bool
	}{
		{
			name: "all", wantRows: []string{"web", "db", "old"},
			wantCounts: Counts{Total: 3, Up: 1, Down: 1, Paused: 1, WithProblems: 2},
		},
		{
			name: "problems only keeps full counts", opts: FleetOptions{ProblemsOnly: true}, wantRows: []string{"db", "old"},
			wantCounts: Counts{Total: 3, Up: 1, Down: 1, Paused: 1, WithProblems: 2},
		},
		{
			name: "named systems", opts: FleetOptions{Systems: []string{"WEB", "db"}}, wantRows: []string{"web", "db"},
			wantCounts: Counts{Total: 2, Up: 1, Down: 1, WithProblems: 1},
		},
		{
			name: "duplicate names counted once", opts: FleetOptions{Systems: []string{"web", "aaaaaaaaaaaaaa1"}},
			wantRows: []string{"web"}, wantCounts: Counts{Total: 1, Up: 1},
		},
		{
			name: "problems only, none", opts: FleetOptions{Systems: []string{"web"}, ProblemsOnly: true},
			wantRows: []string{}, wantCounts: Counts{Total: 1, Up: 1},
		},
		{name: "unknown name", opts: FleetOptions{Systems: []string{"mail"}}, wantErr: true},
		{
			name: "pending and an undefined status are counted",
			systems: []hub.System{
				healthy,
				{ID: "aaaaaaaaaaaaaa4", Name: "new", Status: hub.StatusPending, Updated: ago(time.Hour)},
				{ID: "aaaaaaaaaaaaaa5", Name: "odd", Status: "rebooting", Updated: ago(time.Hour)},
			},
			wantRows: []string{"web", "new", "odd"}, wantCounts: Counts{Total: 3, Up: 1, Pending: 1, Unknown: 1, WithProblems: 2},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			systems := all
			if tt.systems != nil {
				systems = tt.systems
			}
			r, err := Fleet(FleetInput{Now: now, Systems: systems}, tt.opts)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, want error %v", err, tt.wantErr)
			}
			if tt.wantErr {
				return
			}
			rows := []string{}
			for _, row := range r.Systems {
				rows = append(rows, row.Name)
			}
			if !reflect.DeepEqual(rows, tt.wantRows) || r.Counts != tt.wantCounts {
				t.Errorf("rows %q counts %+v; want %q %+v", rows, r.Counts, tt.wantRows, tt.wantCounts)
			}
		})
	}
}

// load decodes a hub fixture's items.
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

// fixtureLastSeen is the newest captured stats row of each system that is
// not up, as the hub's LastStatsAt returns it; zero when there is none.
func fixtureLastSeen(t *testing.T, systems []hub.System) map[string]time.Time {
	t.Helper()
	out := map[string]time.Time{}
	for _, s := range systems {
		if s.Status == "up" {
			continue
		}
		out[s.ID] = time.Time{}
		for _, r := range load[hub.SystemStats](t, "hub/system_stats_latest_"+s.Name+".json") {
			if r.Created.After(out[s.ID]) {
				out[s.ID] = r.Created.Time
			}
		}
	}
	return out
}

// TestFleetGolden runs the real (sanitised) fixtures through Fleet and
// compares the exact JSON the model would get. Run with -update to rewrite.
func TestFleetGolden(t *testing.T) {
	in := FleetInput{
		Now:            captured,
		HubVersion:     "0.21.1",
		TokenExpiresAt: captured.Add(10 * 365 * 24 * time.Hour),
		Systems:        load[hub.System](t, "hub/systems.json"),
		Services:       load[hub.Service](t, "hub/systemd_services.json"),
		Containers:     load[hub.Container](t, "hub/containers.json"),
		Smart:          load[hub.SmartDevice](t, "hub/smart_devices.json"),
		Pools:          load[hub.ZfsPool](t, "handmade/zfs_pools.json"),
		Monitors:       load[hub.Monitor](t, "hub/network_monitors.json"),
	}
	in.LastSeen = fixtureLastSeen(t, in.Systems)
	tests := []struct {
		name string
		opts FleetOptions
		file string
	}{
		{name: "all systems", file: "testdata/fleet_all.json"},
		{name: "problems only", opts: FleetOptions{ProblemsOnly: true}, file: "testdata/fleet_problems.json"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r, err := Fleet(in, tt.opts)
			if err != nil {
				t.Fatal(err)
			}
			got, err := json.MarshalIndent(r, "", "  ")
			if err != nil {
				t.Fatal(err)
			}
			got = append(got, '\n')
			if *update {
				if err := os.WriteFile(tt.file, got, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			want, err := os.ReadFile(tt.file)
			if err != nil {
				t.Fatalf("%v (run go test -update to create it)", err)
			}
			if string(got) != string(want) {
				t.Errorf("output differs from %s; rerun with -update and review the diff\ngot:\n%s", tt.file, got)
			}
		})
	}
}

// TestLastSeen: a system that is not up is dated by its newest stats row,
// because Beszel bumps systems.updated when it rewrites the record.
func TestLastSeen(t *testing.T) {
	sys := upSystem()
	sys.Status, sys.Updated = "down", ago(2*time.Hour) // record rewritten 2h ago
	paused := sys
	paused.Status = "paused"
	tests := []struct {
		name     string
		sys      hub.System
		lastSeen map[string]time.Time
		want     string // fleet row, then the detail core section
	}{
		{
			name: "newest stats row wins over updated", sys: sys, lastSeen: map[string]time.Time{sys.ID: now.Add(-58*time.Hour - 20*time.Minute)},
			want: `{"name":"web","status":"down","last_seen_ago_s":210000,"problems":[{"code":"status_down","detail":"down for 2d 10h","section":"summary"}]} {"unavailable":"system is down, last seen 2d 10h ago"}`,
		},
		{
			name: "no stats row in 30 days", sys: sys, lastSeen: map[string]time.Time{sys.ID: {}},
			want: `{"name":"web","status":"down","problems":[{"code":"status_down","detail":"down, no data in the last 30 days","section":"summary"}]} {"unavailable":"system is down, no data in the last 30 days"}`,
		},
		{
			name: "unknown: updated stands in", sys: sys,
			want: `{"name":"web","status":"down","last_seen_ago_s":7200,"problems":[{"code":"status_down","detail":"down for 2h","section":"summary"}]} {"unavailable":"system is down, last seen 2h ago"}`,
		},
		{
			name: "paused uses the stats row too", sys: paused, lastSeen: map[string]time.Time{sys.ID: now.Add(-25 * time.Hour)},
			want: `{"name":"web","status":"paused","last_seen_ago_s":90000,"problems":[{"code":"status_paused","detail":"monitoring paused","section":"summary"}]} {"unavailable":"system is paused, last seen 1d 1h ago"}`,
		},
		{
			name: "another system's entry is ignored", sys: sys, lastSeen: map[string]time.Time{"otherotherother": {}},
			want: `{"name":"web","status":"down","last_seen_ago_s":7200,"problems":[{"code":"status_down","detail":"down for 2h","section":"summary"}]} {"unavailable":"system is down, last seen 2h ago"}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := FleetInput{Now: now, Systems: []hub.System{tt.sys}, LastSeen: tt.lastSeen}
			f, err := Fleet(in, FleetOptions{})
			if err != nil {
				t.Fatal(err)
			}
			d, err := Detail(DetailInput{FleetInput: in, System: tt.sys}, DetailOptions{Sections: []Section{SectionCore}})
			if err != nil {
				t.Fatal(err)
			}
			if got := asJSON(t, f.Systems[0]) + " " + asJSON(t, d.Core); got != tt.want {
				t.Fatalf("got  %s\nwant %s", got, tt.want)
			}
		})
	}
}

func TestShort(t *testing.T) {
	tests := []struct {
		d    time.Duration
		want string
	}{
		{0, "0s"},
		{59 * time.Second, "59s"},
		{time.Minute, "1m"},
		{59*time.Minute + 59*time.Second, "59m"},
		{time.Hour, "1h"},
		{time.Hour + 55*time.Minute, "1h 55m"},
		{23*time.Hour + 59*time.Minute, "23h 59m"},
		{24 * time.Hour, "1d"},
		{25*time.Hour + 30*time.Minute, "1d 1h"},
		{58*time.Hour + 20*time.Minute, "2d 10h"},
		{30 * 24 * time.Hour, "30d"},
	}
	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			if got := short(tt.d); got != tt.want {
				t.Fatalf("short(%v) = %q, want %q", tt.d, got, tt.want)
			}
		})
	}
}
