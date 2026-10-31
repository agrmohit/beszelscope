package report

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/agrmohit/beszelscope/internal/hub"
)

func TestParseMetrics(t *testing.T) {
	tests := []struct {
		name    string
		in      []string
		sources []Source
		wantErr string
	}{
		{name: "plain kinds read stats", in: []string{"cpu", "mem", "load"}, sources: []Source{SourceStats}},
		{name: "targets", in: []string{"disk:/mnt", "net:eth0", "temp:PMU tdie1", "gpu:0"}, sources: []Source{SourceStats}},
		{name: "every source", in: []string{"cpu", "container:db", "monitor:https://a/"}, sources: []Source{SourceStats, SourceContainers, SourceMonitors}},
		{name: "containers alone", in: []string{"container:db"}, sources: []Source{SourceContainers}},
		{name: "target with a colon", in: []string{"monitor:https://a.example.com:8443/"}, sources: []Source{SourceMonitors}},
		{name: "six is the cap", in: []string{"cpu", "mem", "swap", "load", "disk", "net"}, sources: []Source{SourceStats}},
		{name: "none", in: nil, wantErr: "want 1-6 metrics, got 0"},
		{name: "seven", in: []string{"cpu", "mem", "swap", "load", "disk", "net", "gpu"}, wantErr: "want 1-6 metrics, got 7"},
		{name: "unknown kind", in: []string{"cpu_temp"}, wantErr: `unknown metric "cpu_temp"; kinds are cpu, cpu_iowait, mem, swap, disk, disk_io, net, load, temp, gpu, container, monitor`},
		{name: "container needs a target", in: []string{"container"}, wantErr: `metric "container" needs a target, e.g. container:<name>`},
		{name: "monitor needs a target", in: []string{"monitor:"}, wantErr: `metric "monitor:" needs a target, e.g. monitor:<name>`},
		{name: "cpu takes no target", in: []string{"cpu:0"}, wantErr: `metric "cpu:0" takes no target`},
		{name: "duplicate", in: []string{"cpu", "cpu"}, wantErr: `metric "cpu" given twice`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, sources, err := ParseMetrics(tt.in)
			if tt.wantErr != "" {
				if err == nil || err.Error() != tt.wantErr {
					t.Fatalf("err = %v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil || asJSON(t, sources) != asJSON(t, tt.sources) {
				t.Fatalf("sources = %v, err = %v; want %v", sources, err, tt.sources)
			}
		})
	}
}

func TestFindWindow(t *testing.T) {
	tests := []struct {
		in, want string
		wantErr  bool
	}{
		{"", "24h", false},
		{"1h", "1h", false},
		{"12h", "12h", false},
		{"24h", "24h", false},
		{"7d", "7d", false},
		{"30d", "30d", false},
		{"1w", "", true},
		{"24H", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			w, err := FindWindow(tt.in)
			if (err != nil) != tt.wantErr || w.Name != tt.want {
				t.Fatalf("FindWindow(%q) = %q, %v", tt.in, w.Name, err)
			}
		})
	}
}

func mustWindow(name string) Window {
	w, err := FindWindow(name)
	if err != nil {
		panic(err)
	}
	return w
}

func metrics(t *testing.T, raw ...string) []Metric {
	t.Helper()
	m, _, err := ParseMetrics(raw)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

// series drops the shared system field and joins the series as JSON lines.
func series(t *testing.T, r HistoryResult) string {
	t.Helper()
	var out []string
	for _, s := range r.Series {
		s.System = ""
		out = append(out, strings.Replace(asJSON(t, s), `"system":"",`, "", 1))
	}
	return strings.Join(out, "\n")
}

// statRows are three 7d buckets: two rollups with maxima, then one without.
func statRows() []hub.SystemStats {
	rows := []hub.SystemStats{
		{Created: ago(5 * time.Hour), Stats: hub.Stats{
			CPU: 10, MaxCPU: 50, MemPct: 40, Mem: [4]uint64{100, 40}, MaxMemUsed: 70, Swap: [2]uint64{200, 50}, DiskPct: 30,
			DiskIO: [2]uint64{100, 200}, MaxDiskIO: [2]uint64{1000, 2000}, Bandwidth: [2]uint64{10, 20}, MaxBandwidth: [2]uint64{100, 200},
			Interfaces: map[string][4]uint64{"eth0": {1, 2}}, LoadAvg: [3]float64{1}, Temps: map[string]float64{"a": 40, "b": 60},
			ExtraFs: map[string]hub.FsStats{"/mnt": {TotalGB: 200, UsedGB: 50}}, CPUBreakdown: []float64{0, 0, 5, 0, 95},
			GPU: map[string]hub.GPU{"0": {Usage: 10}, "1": {Usage: 30}},
		}},
		{Created: ago(3 * time.Hour), Stats: hub.Stats{
			CPU: 30, MaxCPU: 90, MemPct: 60, Mem: [4]uint64{100, 60}, MaxMemUsed: 95, Swap: [2]uint64{200, 100}, DiskPct: 32,
			DiskIO: [2]uint64{300, 400}, MaxDiskIO: [2]uint64{3000, 1000}, Bandwidth: [2]uint64{30, 40}, MaxBandwidth: [2]uint64{500, 100},
			Interfaces: map[string][4]uint64{"eth0": {3, 4}}, LoadAvg: [3]float64{3}, Temps: map[string]float64{"a": 70, "b": 50},
			ExtraFs: map[string]hub.FsStats{"/mnt": {TotalGB: 200, UsedGB: 60}, "/empty": {}}, CPUBreakdown: []float64{0, 0, 15, 0, 85},
			GPU: map[string]hub.GPU{"0": {Usage: 50}, "1": {Usage: 20}},
		}},
		{Created: ago(time.Hour), Stats: hub.Stats{
			CPU: 20, MemPct: 50, Mem: [4]uint64{100, 50}, Swap: [2]uint64{200, 0}, DiskPct: 31,
			DiskIO: [2]uint64{200, 300}, Bandwidth: [2]uint64{20, 30}, LoadAvg: [3]float64{2}, Temps: map[string]float64{"a": 45},
			CPUBreakdown: []float64{1, 2},
		}},
	}
	for i := range rows {
		rows[i].Type = hub.Stat120m
	}
	return rows
}

func TestHistoryStats(t *testing.T) {
	up := upSystem()
	tests := []struct {
		name    string
		metrics []string
		rows    []hub.SystemStats
		readErr error
		want    string
		wantErr string
	}{
		{
			name: "true peaks from maxima, and a row without maxima is its own peak", metrics: []string{"cpu", "mem"}, rows: statRows(),
			want: `{"metric":"cpu","unit":"pct","avg":20,"min_bucket_avg":10,"peak":90,"high_ago_s":10800,"last":20,"last_ago_s":3600,"buckets":3,"expected_buckets":84,"coverage_pct":4}` + "\n" +
				`{"metric":"mem","unit":"pct","avg":50,"min_bucket_avg":40,"peak":95,"high_ago_s":10800,"last":50,"last_ago_s":3600,"buckets":3,"expected_buckets":84,"coverage_pct":4}`,
		},
		{
			name: "directions split, each with its own peak", metrics: []string{"disk_io", "net"}, rows: statRows(),
			want: `{"metric":"disk_io","part":"read","unit":"bytes_per_s","avg":200,"min_bucket_avg":100,"peak":3000,"high_ago_s":10800,"last":200,"last_ago_s":3600,"buckets":3,"expected_buckets":84,"coverage_pct":4}` + "\n" +
				`{"metric":"disk_io","part":"write","unit":"bytes_per_s","avg":300,"min_bucket_avg":200,"peak":2000,"high_ago_s":18000,"last":300,"last_ago_s":3600,"buckets":3,"expected_buckets":84,"coverage_pct":4}` + "\n" +
				`{"metric":"net","part":"sent","unit":"bytes_per_s","avg":20,"min_bucket_avg":10,"peak":500,"high_ago_s":10800,"last":20,"last_ago_s":3600,"buckets":3,"expected_buckets":84,"coverage_pct":4}` + "\n" +
				`{"metric":"net","part":"recv","unit":"bytes_per_s","avg":30,"min_bucket_avg":20,"peak":200,"high_ago_s":18000,"last":30,"last_ago_s":3600,"buckets":3,"expected_buckets":84,"coverage_pct":4}`,
		},
		{
			name: "averages only: interface, swap, load, disks", metrics: []string{"net:eth0", "swap", "load", "disk", "disk:/mnt"}, rows: statRows(),
			want: `{"metric":"net:eth0","part":"sent","unit":"bytes_per_s","avg":2,"min_bucket_avg":1,"max_bucket_avg":3,"high_ago_s":10800,"last":3,"last_ago_s":10800,"buckets":2,"expected_buckets":84,"coverage_pct":2}` + "\n" +
				`{"metric":"net:eth0","part":"recv","unit":"bytes_per_s","avg":3,"min_bucket_avg":2,"max_bucket_avg":4,"high_ago_s":10800,"last":4,"last_ago_s":10800,"buckets":2,"expected_buckets":84,"coverage_pct":2}` + "\n" +
				`{"metric":"swap","unit":"pct","avg":25,"min_bucket_avg":0,"max_bucket_avg":50,"high_ago_s":10800,"last":0,"last_ago_s":3600,"buckets":3,"expected_buckets":84,"coverage_pct":4}` + "\n" +
				`{"metric":"load","unit":"load","avg":2,"min_bucket_avg":1,"max_bucket_avg":3,"high_ago_s":10800,"last":2,"last_ago_s":3600,"buckets":3,"expected_buckets":84,"coverage_pct":4}` + "\n" +
				`{"metric":"disk","unit":"pct","avg":31,"min_bucket_avg":30,"max_bucket_avg":32,"high_ago_s":10800,"last":31,"last_ago_s":3600,"buckets":3,"expected_buckets":84,"coverage_pct":4}` + "\n" +
				`{"metric":"disk:/mnt","unit":"pct","avg":27.5,"min_bucket_avg":25,"max_bucket_avg":30,"high_ago_s":10800,"last":30,"last_ago_s":10800,"buckets":2,"expected_buckets":84,"coverage_pct":2}`,
		},
		{
			name: "hottest sensor and busiest GPU per bucket; named ones; short breakdown skipped", metrics: []string{"temp", "temp:a", "gpu", "gpu:1", "cpu_iowait"}, rows: statRows(),
			want: `{"metric":"temp","unit":"celsius","avg":58.33,"min_bucket_avg":45,"max_bucket_avg":70,"high_ago_s":10800,"last":45,"last_ago_s":3600,"buckets":3,"expected_buckets":84,"coverage_pct":4}` + "\n" +
				`{"metric":"temp:a","unit":"celsius","avg":51.67,"min_bucket_avg":40,"max_bucket_avg":70,"high_ago_s":10800,"last":45,"last_ago_s":3600,"buckets":3,"expected_buckets":84,"coverage_pct":4}` + "\n" +
				`{"metric":"gpu","unit":"pct","avg":40,"min_bucket_avg":30,"max_bucket_avg":50,"high_ago_s":10800,"last":50,"last_ago_s":10800,"buckets":2,"expected_buckets":84,"coverage_pct":2}` + "\n" +
				`{"metric":"gpu:1","unit":"pct","avg":25,"min_bucket_avg":20,"max_bucket_avg":30,"high_ago_s":18000,"last":20,"last_ago_s":10800,"buckets":2,"expected_buckets":84,"coverage_pct":2}` + "\n" +
				`{"metric":"cpu_iowait","unit":"pct","avg":10,"min_bucket_avg":5,"max_bucket_avg":15,"high_ago_s":10800,"last":15,"last_ago_s":10800,"buckets":2,"expected_buckets":84,"coverage_pct":2}`,
		},
		{
			name: "old agent: memory peak falls back to the percentage", metrics: []string{"mem"},
			rows: []hub.SystemStats{{Created: ago(time.Hour), Stats: hub.Stats{MemPct: 33, MaxMemUsed: 99}}},
			want: `{"metric":"mem","unit":"pct","avg":33,"min_bucket_avg":33,"peak":33,"high_ago_s":3600,"last":33,"last_ago_s":3600,"buckets":1,"expected_buckets":84,"coverage_pct":1}`,
		},
		{
			name: "ties keep the earlier bucket", metrics: []string{"load"},
			rows: []hub.SystemStats{{Created: ago(5 * time.Hour), Stats: hub.Stats{LoadAvg: [3]float64{4}}}, {Created: ago(time.Hour), Stats: hub.Stats{LoadAvg: [3]float64{4}}}},
			want: `{"metric":"load","unit":"load","avg":4,"min_bucket_avg":4,"max_bucket_avg":4,"high_ago_s":18000,"last":4,"last_ago_s":3600,"buckets":2,"expected_buckets":84,"coverage_pct":2}`,
		},
		{
			name: "no sensors, GPUs or swap reported", metrics: []string{"temp", "temp:cpu", "gpu", "swap"},
			rows: []hub.SystemStats{{Created: ago(time.Hour), Stats: hub.Stats{CPU: 1}}},
			want: `{"metric":"temp","unit":"celsius","buckets":0,"expected_buckets":84,"coverage_pct":0,"note":"no temperature sensors reported in this window"}` + "\n" +
				`{"metric":"temp:cpu","unit":"celsius","buckets":0,"expected_buckets":84,"coverage_pct":0,"note":"no temperature sensor reported in this window"}` + "\n" +
				`{"metric":"gpu","unit":"pct","buckets":0,"expected_buckets":84,"coverage_pct":0,"note":"no GPUs reported in this window"}` + "\n" +
				`{"metric":"swap","unit":"pct","buckets":0,"expected_buckets":84,"coverage_pct":0,"note":"no swap on this server"}`,
		},
		{
			name: "no rows: empty series, a target is not checked", metrics: []string{"cpu", "temp:cpu"},
			want: `{"metric":"cpu","unit":"pct","buckets":0,"expected_buckets":84,"coverage_pct":0,"note":"no samples in this window"}` + "\n" +
				`{"metric":"temp:cpu","unit":"celsius","buckets":0,"expected_buckets":84,"coverage_pct":0,"note":"no samples in this window"}`,
		},
		{
			name: "read failed: every line says so", metrics: []string{"net"}, readErr: errors.New("HTTP 503"),
			want: `{"metric":"net","part":"sent","unit":"bytes_per_s","buckets":0,"expected_buckets":84,"coverage_pct":0,"note":"could not read history: HTTP 503"}` + "\n" +
				`{"metric":"net","part":"recv","unit":"bytes_per_s","buckets":0,"expected_buckets":84,"coverage_pct":0,"note":"could not read history: HTTP 503"}`,
		},
		{
			name: "a zero-size filesystem has no usage", metrics: []string{"disk:/empty"}, rows: statRows(),
			want: `{"metric":"disk:/empty","unit":"pct","buckets":0,"expected_buckets":84,"coverage_pct":0,"note":"no samples in this window"}`,
		},
		{name: "unknown filesystem lists the valid ones", metrics: []string{"disk:/nope"}, rows: statRows(), wantErr: `web has no filesystem "/nope" in this window; valid: /empty, /mnt`},
		{name: "unknown sensor", metrics: []string{"temp:cpu"}, rows: statRows(), wantErr: `web has no temperature sensor "cpu" in this window; valid: a, b`},
		{name: "unknown GPU", metrics: []string{"gpu:9"}, rows: statRows(), wantErr: `web has no GPU "9" in this window; valid: 0, 1`},
		{name: "unknown interface", metrics: []string{"net:wlan0"}, rows: statRows(), wantErr: `web has no interface "wlan0" in this window; valid: eth0`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := HistoryInput{
				Now: now, Systems: []hub.System{up}, Window: mustWindow("7d"),
				Stats: map[string][]hub.SystemStats{up.ID: tt.rows}, StatsErrs: map[string]error{up.ID: tt.readErr},
			}
			r, err := History(in, HistoryOptions{Metrics: metrics(t, tt.metrics...)})
			if tt.wantErr != "" {
				if err == nil || err.Error() != tt.wantErr {
					t.Fatalf("err = %v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got := series(t, r); got != tt.want {
				t.Fatalf("got\n%s\nwant\n%s", got, tt.want)
			}
		})
	}
}

func TestHistoryContainers(t *testing.T) {
	up := upSystem()
	rows := []hub.ContainerStats{
		{Created: ago(5 * time.Hour), Stats: []hub.ContainerStat{{Name: "web", CPU: 1, MemMiB: 10}, {Name: "db", CPU: 4, MemMiB: 100}}},
		{Created: ago(3 * time.Hour), Stats: []hub.ContainerStat{{Name: "web", CPU: 3, MemMiB: 30}}},
		{Created: ago(time.Hour), Stats: []hub.ContainerStat{{Name: "web", CPU: 2, MemMiB: 20}, {Name: "db", CPU: 2, MemMiB: 50}}},
	}
	tests := []struct {
		name    string
		metric  string
		rows    []hub.ContainerStats
		readErr error
		want    string
		wantErr string
	}{
		{
			name: "CPU and memory in bytes", metric: "container:web", rows: rows,
			want: `{"metric":"container:web","part":"cpu","unit":"pct","avg":2,"min_bucket_avg":1,"max_bucket_avg":3,"high_ago_s":10800,"last":2,"last_ago_s":3600,"buckets":3,"expected_buckets":84,"coverage_pct":4}` + "\n" +
				`{"metric":"container:web","part":"mem","unit":"bytes","avg":20971520,"min_bucket_avg":10485760,"max_bucket_avg":31457280,"high_ago_s":10800,"last":20971520,"last_ago_s":3600,"buckets":3,"expected_buckets":84,"coverage_pct":4}`,
		},
		{
			name: "missing from a bucket: note on both lines", metric: "container:db", rows: rows,
			want: `{"metric":"container:db","part":"cpu","unit":"pct","avg":3,"min_bucket_avg":2,"max_bucket_avg":4,"high_ago_s":18000,"last":2,"last_ago_s":3600,"buckets":2,"expected_buckets":84,"coverage_pct":2,"note":"the container was missing from some buckets; Beszel counts its missing minutes as 0, so buckets around a start or stop read low"}` + "\n" +
				`{"metric":"container:db","part":"mem","unit":"bytes","avg":78643200,"min_bucket_avg":52428800,"max_bucket_avg":104857600,"high_ago_s":18000,"last":52428800,"last_ago_s":3600,"buckets":2,"expected_buckets":84,"coverage_pct":2,"note":"the container was missing from some buckets; Beszel counts its missing minutes as 0, so buckets around a start or stop read low"}`,
		},
		{name: "unknown container lists the valid ones", metric: "container:redis", rows: rows, wantErr: `web has no container "redis" in this window; valid: db, web`},
		{
			name: "rows without containers", metric: "container:redis", rows: []hub.ContainerStats{{Created: ago(time.Hour)}},
			want: `{"metric":"container:redis","part":"cpu","unit":"pct","buckets":0,"expected_buckets":84,"coverage_pct":0,"note":"no container reported in this window"}` + "\n" +
				`{"metric":"container:redis","part":"mem","unit":"bytes","buckets":0,"expected_buckets":84,"coverage_pct":0,"note":"no container reported in this window"}`,
		},
		{
			name: "read failed", metric: "container:web", readErr: errors.New("HTTP 503"),
			want: `{"metric":"container:web","part":"cpu","unit":"pct","buckets":0,"expected_buckets":84,"coverage_pct":0,"note":"could not read history: HTTP 503"}` + "\n" +
				`{"metric":"container:web","part":"mem","unit":"bytes","buckets":0,"expected_buckets":84,"coverage_pct":0,"note":"could not read history: HTTP 503"}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := HistoryInput{
				Now: now, Systems: []hub.System{up}, Window: mustWindow("7d"),
				Containers: map[string][]hub.ContainerStats{up.ID: tt.rows}, ContainerErrs: map[string]error{up.ID: tt.readErr},
			}
			r, err := History(in, HistoryOptions{Metrics: metrics(t, tt.metric)})
			if tt.wantErr != "" {
				if err == nil || err.Error() != tt.wantErr {
					t.Fatalf("err = %v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got := series(t, r); got != tt.want {
				t.Fatalf("got\n%s\nwant\n%s", got, tt.want)
			}
		})
	}
}

func TestHistoryMonitors(t *testing.T) {
	up := upSystem()
	monitors := []hub.Monitor{
		{ID: "m1", System: up.ID, Target: "https://a/", Interval: 60},
		{ID: "m2", System: "otherotherother", Target: "https://a/", Interval: 60},
		{ID: "m3", System: up.ID, Target: "db:5432", Interval: 600},
	}
	at := func(d time.Duration) int64 { return now.Add(-d).UnixMilli() }
	rows := []hub.MonitorStats{
		{Monitor: "m1", Created: at(5 * time.Hour), Total: 10, Success: 10, ResSum: 1_000_000, ResMax: 200_000},
		{Monitor: "m1", Created: at(3 * time.Hour), Total: 10, Success: 5, ResSum: 1_000_000, ResMax: 400_000},
		{Monitor: "m1", Created: at(time.Hour), Total: 10},
		{Monitor: "m1", Created: at(30 * time.Minute)}, // no probes: ignored
		{Monitor: "m2", Created: at(time.Hour), Total: 10, Success: 10, ResSum: 9e9, ResMax: 9e9},
	}
	tests := []struct {
		name       string
		metric     string
		window     string
		monitors   []hub.Monitor
		rows       []hub.MonitorStats
		monitorErr error
		statsErr   error
		want       string
		wantErr    string
	}{
		{
			name: "latency weighted by successes, true peak, loss over all probes", metric: "monitor:https://a/", window: "7d", monitors: monitors, rows: rows,
			want: `{"metric":"monitor:https://a/","unit":"ms","avg":133.33,"min_bucket_avg":100,"peak":400,"high_ago_s":10800,"last":200,"last_ago_s":10800,"loss_pct":50,"buckets":3,"expected_buckets":84,"coverage_pct":4}`,
		},
		{
			name: "every probe failed", metric: "monitor:https://a/", window: "7d", monitors: monitors, rows: rows[2:3],
			want: `{"metric":"monitor:https://a/","unit":"ms","loss_pct":100,"buckets":1,"expected_buckets":84,"coverage_pct":1,"note":"every probe in this window failed"}`,
		},
		{
			name: "probing less often than the bucket lowers the expected count", metric: "monitor:db:5432", window: "1h", monitors: monitors,
			rows: []hub.MonitorStats{{Monitor: "m3", Created: at(10 * time.Minute), Total: 1, Success: 1, ResSum: 5000, ResMax: 5000}},
			want: `{"metric":"monitor:db:5432","unit":"ms","avg":5,"min_bucket_avg":5,"peak":5,"high_ago_s":600,"last":5,"last_ago_s":600,"loss_pct":0,"buckets":1,"expected_buckets":6,"coverage_pct":17}`,
		},
		{
			name: "probing as often as the bucket keeps the window's count", metric: "monitor:https://a/", window: "1h", monitors: monitors,
			want: `{"metric":"monitor:https://a/","unit":"ms","buckets":0,"expected_buckets":60,"coverage_pct":0,"note":"no samples in this window"}`,
		},
		{
			name: "more rows than expected: coverage stops at 100", metric: "monitor:db:5432", window: "1h", monitors: monitors,
			rows: func() []hub.MonitorStats {
				var out []hub.MonitorStats
				for i := range 7 {
					out = append(out, hub.MonitorStats{Monitor: "m3", Created: at(time.Duration(7-i) * 8 * time.Minute), Total: 1, Success: 1, ResSum: 5000, ResMax: 5000})
				}
				return out
			}(),
			want: `{"metric":"monitor:db:5432","unit":"ms","avg":5,"min_bucket_avg":5,"peak":5,"high_ago_s":3360,"last":5,"last_ago_s":480,"loss_pct":0,"buckets":7,"expected_buckets":6,"coverage_pct":100}`,
		},
		{name: "unknown target lists this server's", metric: "monitor:https://b/", window: "7d", monitors: monitors, wantErr: `web has no network monitor "https://b/"; valid: db:5432, https://a/`},
		{
			name: "no monitors on this server", metric: "monitor:https://a/", window: "7d", monitors: monitors[1:2],
			want: `{"metric":"monitor:https://a/","unit":"ms","buckets":0,"expected_buckets":84,"coverage_pct":0,"note":"no network monitors configured for this server"}`,
		},
		{
			name: "monitor list failed", metric: "monitor:https://a/", window: "7d", monitorErr: errors.New("HTTP 503"),
			want: `{"metric":"monitor:https://a/","unit":"ms","buckets":0,"expected_buckets":84,"coverage_pct":0,"note":"could not read history: HTTP 503"}`,
		},
		{
			name: "monitor stats failed", metric: "monitor:https://a/", window: "7d", monitors: monitors, statsErr: errors.New("HTTP 500"),
			want: `{"metric":"monitor:https://a/","unit":"ms","buckets":0,"expected_buckets":84,"coverage_pct":0,"note":"could not read history: HTTP 500"}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := HistoryInput{
				Now: now, Systems: []hub.System{up}, Window: mustWindow(tt.window), Monitors: tt.monitors, MonitorsErr: tt.monitorErr,
				MonitorStats: map[string][]hub.MonitorStats{up.ID: tt.rows}, MonitorStatsErrs: map[string]error{up.ID: tt.statsErr},
			}
			r, err := History(in, HistoryOptions{Metrics: metrics(t, tt.metric)})
			if tt.wantErr != "" {
				if err == nil || err.Error() != tt.wantErr {
					t.Fatalf("err = %v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got := series(t, r); got != tt.want {
				t.Fatalf("got\n%s\nwant\n%s", got, tt.want)
			}
		})
	}
}

func TestHistoryPoints(t *testing.T) {
	up := upSystem()
	tests := []struct {
		name    string
		metrics []string
		points  int
		want    string // hints, then the first series' points
		wantErr string
	}{
		{name: "none by default", metrics: []string{"cpu"}, want: `null null`},
		{name: "more points than buckets: every bucket", metrics: []string{"cpu"}, points: 5, want: `null [[18000,10],[10800,30],[3600,20]]`},
		{name: "equal: every bucket", metrics: []string{"cpu"}, points: 3, want: `null [[18000,10],[10800,30],[3600,20]]`},
		{name: "fewer: consecutive buckets averaged, aged by their last", metrics: []string{"cpu"}, points: 2, want: `null [[18000,10],[3600,25]]`},
		{name: "one", metrics: []string{"cpu"}, points: 1, want: `null [[3600,20]]`},
		{name: "six series of 60 fit the budget exactly", metrics: []string{"cpu", "mem", "load", "swap", "disk", "temp"}, points: 60, want: `null [[18000,10],[10800,30],[3600,20]]`},
		{
			name: "seven series of 60 shrink to 51", metrics: []string{"cpu", "mem", "load", "swap", "disk", "net"}, points: 60,
			want: `["points lowered from 60 to 51 so 7 series stay within 360 values; ask for fewer series to get more points"] [[18000,10],[10800,30],[3600,20]]`,
		},
		{name: "above 60", metrics: []string{"cpu"}, points: 61, wantErr: "points 61 out of range 0-60"},
		{name: "negative", metrics: []string{"cpu"}, points: -1, wantErr: "points -1 out of range 0-60"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := HistoryInput{Now: now, Systems: []hub.System{up}, Window: mustWindow("7d"), Stats: map[string][]hub.SystemStats{up.ID: statRows()}}
			r, err := History(in, HistoryOptions{Metrics: metrics(t, tt.metrics...), Points: tt.points})
			if tt.wantErr != "" {
				if err == nil || err.Error() != tt.wantErr {
					t.Fatalf("err = %v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got := asJSON(t, r.Hints) + " " + asJSON(t, r.Series[0].Points); got != tt.want {
				t.Fatalf("got  %s\nwant %s", got, tt.want)
			}
		})
	}
}

func TestHistoryEnvelope(t *testing.T) {
	up := upSystem()
	other := upSystem()
	other.ID, other.Name = "otherotherother", "db"
	in := HistoryInput{
		Now: now, Systems: []hub.System{other, up}, Window: mustWindow("1h"), TokenExpiresAt: now.Add(24 * time.Hour),
		Stats: map[string][]hub.SystemStats{up.ID: {{Created: ago(time.Minute), Stats: hub.Stats{CPU: 9}}}},
	}
	r, err := History(in, HistoryOptions{Metrics: metrics(t, "cpu")})
	if err != nil {
		t.Fatal(err)
	}
	r.AsOf = time.Time{}
	want := `{"as_of":"0001-01-01T00:00:00Z","window":{"span_s":3600,"resolution_s":60},"warnings":["hub token expires in 24h; renew it (see README)"],"series":[` +
		`{"system":"db","metric":"cpu","unit":"pct","buckets":0,"expected_buckets":60,"coverage_pct":0,"note":"no samples in this window"},` +
		`{"system":"web","metric":"cpu","unit":"pct","avg":9,"min_bucket_avg":9,"peak":9,"high_ago_s":60,"last":9,"last_ago_s":60,"buckets":1,"expected_buckets":60,"coverage_pct":2}]}`
	if got := asJSON(t, r); got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
}

// TestHistoryGolden runs metric_history over the captured bravo history.
func TestHistoryGolden(t *testing.T) {
	systems := load[hub.System](t, "hub/systems.json")
	monitors := load[hub.Monitor](t, "hub/network_monitors.json")
	var bravo hub.System
	for _, s := range systems {
		if s.Name == "bravo" {
			bravo = s
		}
	}
	calls := []struct {
		window  string
		metrics []string
		points  int
	}{
		{"1h", []string{"cpu", "mem", "monitor:https://example.com/"}, 0},
		{"12h", []string{"net:eth0", "cpu_iowait"}, 0},
		{"24h", []string{"container:gitea", "swap"}, 6},
		{"7d", []string{"cpu", "net", "disk_io", "load"}, 0},
		{"30d", []string{"cpu", "mem"}, 10},
	}
	out := map[string]HistoryResult{}
	for _, c := range calls {
		w := mustWindow(c.window)
		since := captured.Add(-w.Span)
		in := HistoryInput{
			Now: captured, Systems: []hub.System{bravo}, Window: w, Monitors: monitors,
			Stats: map[string][]hub.SystemStats{}, Containers: map[string][]hub.ContainerStats{}, MonitorStats: map[string][]hub.MonitorStats{},
		}
		for _, r := range load[hub.SystemStats](t, "hub/system_stats_bravo_"+string(w.Type)+".json") {
			if !r.Created.Before(since) {
				in.Stats[bravo.ID] = append(in.Stats[bravo.ID], r)
			}
		}
		if w.Type == hub.Stat1m || w.Type == hub.Stat20m {
			for _, r := range load[hub.ContainerStats](t, "hub/container_stats_bravo_"+string(w.Type)+".json") {
				if !r.Created.Before(since) {
					in.Containers[bravo.ID] = append(in.Containers[bravo.ID], r)
				}
			}
		}
		for _, r := range load[hub.MonitorStats](t, "hub/network_monitor_stats_bravo_"+string(w.Type)+".json") {
			if r.Created >= since.UnixMilli() {
				in.MonitorStats[bravo.ID] = append(in.MonitorStats[bravo.ID], r)
			}
		}
		r, err := History(in, HistoryOptions{Metrics: metrics(t, c.metrics...), Points: c.points})
		if err != nil {
			t.Fatal(err)
		}
		out[c.window] = r
	}
	golden(t, out, "history_bravo.json")
}
