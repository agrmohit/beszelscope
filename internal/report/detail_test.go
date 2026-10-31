package report

import (
	"encoding/json"
	"errors"
	"math"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/agrmohit/beszelscope/internal/hub"
)

func TestDetailGolden(t *testing.T) {
	fleet := FleetInput{
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
	fleet.LastSeen = fixtureLastSeen(t, fleet.Systems)
	details := load[hub.SystemDetails](t, "hub/system_details.json")
	input := func(name string) DetailInput {
		in := DetailInput{FleetInput: fleet}
		for _, s := range fleet.Systems {
			if s.Name == name {
				in.System = s
			}
		}
		for i := range details {
			if details[i].System == in.System.ID {
				in.Details = &details[i]
			}
		}
		if rows := load[hub.SystemStats](t, "hub/system_stats_latest_"+name+".json"); len(rows) > 0 {
			in.Latest = &rows[0]
		}
		return in
	}
	both := []string{SectionSummary, SectionCore, SectionNetwork, SectionDisk, SectionGPU, SectionContainers, SectionServices, SectionUpdates}
	tests := []struct {
		name   string
		system string
		opts   DetailOptions
		file   string
	}{
		{name: "VPS", system: "bravo", opts: DetailOptions{Sections: both}, file: "detail_bravo.json"},
		{name: "Mac with sensors and battery", system: "charlie", opts: DetailOptions{Sections: both}, file: "detail_charlie.json"},
		{name: "down system", system: "alpha", opts: DetailOptions{Sections: both}, file: "detail_alpha.json"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r, err := Detail(input(tt.system), tt.opts)
			if err != nil {
				t.Fatal(err)
			}
			golden(t, r, tt.file)
		})
	}
}

// golden compares v as indented JSON with testdata/name; -update rewrites it.
func golden(t *testing.T, v any, name string) {
	t.Helper()
	got, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	got = append(got, '\n')
	root, err := os.OpenRoot("testdata")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	if *update {
		if err := root.WriteFile(name, got, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	want, err := root.ReadFile(name)
	if err != nil {
		t.Fatalf("%v (run go test -update to create it)", err)
	}
	if string(got) != string(want) {
		t.Errorf("output differs from %s; rerun with -update and review the diff\ngot:\n%s", name, got)
	}
}

func asJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestDetailEnvelope(t *testing.T) {
	up := upSystem()
	down := up
	down.Status, down.Updated = "down", ago(19*time.Minute)
	sample := &hub.SystemStats{Created: ago(20 * time.Second), Stats: hub.Stats{CPU: 5}}
	tests := []struct {
		name     string
		sys      hub.System
		hub      string
		expires  time.Time
		sections []string
		sort     string
		limit    int
		want     string // the result as JSON, as_of dropped
		wantErr  string
	}{
		{
			name: "no sections means summary only", sys: up,
			want: `{"system":{"name":"web","status":"up","age_s":30},"summary":{"agent":"0.21.1","uptime_s":3600,"cpu_pct":12.3,"mem_pct":70,"disk_pct":50,"temp_hottest_c":48.3,"load_avg":[2,1,1],"load_per_thread":0.25,"net_bytes_per_s":2048}}`,
		},
		{
			name: "core only", sys: up, sections: []string{SectionCore},
			want: `{"system":{"name":"web","status":"up","age_s":30},"core":{"sampled_ago_s":20,"cpu_pct":5,"swap":{"total_bytes":0,"used_bytes":0},"absent":["battery: not reported by this host","fans: not reported by this host","memory: agent older than 0.21.1 reports no byte counts","temperatures: not reported by this host"]}}`,
		},
		{
			name: "down: last seen, not age", sys: down, sections: []string{SectionCore},
			want: `{"system":{"name":"web","status":"down","last_seen_ago_s":1140},"core":{"unavailable":"system is down, last seen 19m ago"}}`,
		},
		{
			name: "token expiring and agent skew warn", sys: up, hub: "0.22.0", expires: now.Add(5 * 24 * time.Hour), sections: []string{SectionCore},
			want: `{"system":{"name":"web","status":"up","age_s":30},"warnings":["hub token expires in 5d; renew it (see README)","web runs agent 0.21.1; hub is 0.22.0"],"core":{"sampled_ago_s":20,"cpu_pct":5,"swap":{"total_bytes":0,"used_bytes":0},"absent":["battery: not reported by this host","fans: not reported by this host","memory: agent older than 0.21.1 reports no byte counts","temperatures: not reported by this host"]}}`,
		},
		{
			name: "matching hub version does not warn", sys: up, hub: "0.21.1", sections: []string{SectionCore},
			want: `{"system":{"name":"web","status":"up","age_s":30},"core":{"sampled_ago_s":20,"cpu_pct":5,"swap":{"total_bytes":0,"used_bytes":0},"absent":["battery: not reported by this host","fans: not reported by this host","memory: agent older than 0.21.1 reports no byte counts","temperatures: not reported by this host"]}}`,
		},
		{name: "unknown section", sys: up, sections: []string{"alerts"}, wantErr: `unknown section "alerts"; want one of summary, core, network, disk, gpu, containers, services, updates`},
		{name: "unknown sort", sys: up, sort: "size", wantErr: `unknown sort "size"; want one of cpu, mem, name`},
		{name: "negative limit", sys: up, limit: -1, wantErr: "limit -1 out of range 1-200"},
		{name: "limit past the cap", sys: up, limit: 201, wantErr: "limit 201 out of range 1-200"},
		{
			name: "limit at the cap is fine", sys: up, limit: 200, sections: []string{SectionCore},
			want: `{"system":{"name":"web","status":"up","age_s":30},"core":{"sampled_ago_s":20,"cpu_pct":5,"swap":{"total_bytes":0,"used_bytes":0},"absent":["battery: not reported by this host","fans: not reported by this host","memory: agent older than 0.21.1 reports no byte counts","temperatures: not reported by this host"]}}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := DetailInput{FleetInput: FleetInput{Now: now, HubVersion: tt.hub, TokenExpiresAt: tt.expires}, System: tt.sys, Latest: sample}
			r, err := Detail(in, DetailOptions{Sections: tt.sections, Sort: tt.sort, Limit: tt.limit})
			if tt.wantErr != "" {
				if err == nil || err.Error() != tt.wantErr {
					t.Fatalf("err = %v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			r.AsOf = time.Time{}
			got := strings.Replace(asJSON(t, r), `"as_of":"0001-01-01T00:00:00Z",`, "", 1)
			if got != tt.want {
				t.Fatalf("got  %s\nwant %s", got, tt.want)
			}
		})
	}
}

func TestDetailSummary(t *testing.T) {
	up := upSystem()
	mac := hub.SystemDetails{
		System: up.ID, Hostname: "mac", OS: 1, Kernel: "25.0.0", CPU: "Apple M4", Arch: "arm64",
		Cores: 10, Threads: 10, Memory: 17179869184,
	}
	with := func(f func(*hub.System)) hub.System {
		s := up
		f(&s)
		return s
	}
	tests := []struct {
		name     string
		sys      hub.System
		details  *hub.SystemDetails
		services []hub.Service
		want     string
	}{
		{
			name: "static facts; OS falls back to the OS kind", sys: up, details: &mac,
			want: `{"os":"macOS","kernel":"25.0.0","hostname":"mac","arch":"arm64","cpu_model":"Apple M4","cores":10,"threads":10,"mem_total_bytes":17179869184,"agent":"0.21.1","uptime_s":3600,"cpu_pct":12.3,"mem_pct":70,"disk_pct":50,"temp_hottest_c":48.3,"load_avg":[2,1,1],"load_per_thread":0.25,"net_bytes_per_s":2048}`,
		},
		{
			name: "named OS wins", sys: up,
			details: &hub.SystemDetails{System: up.ID, OS: 0, OSName: "Fedora Linux 44", Kernel: "7.2"},
			want:    `{"os":"Fedora Linux 44","kernel":"7.2","agent":"0.21.1","uptime_s":3600,"cpu_pct":12.3,"mem_pct":70,"disk_pct":50,"temp_hottest_c":48.3,"load_avg":[2,1,1],"load_per_thread":0.25,"net_bytes_per_s":2048}`,
		},
		{
			name: "gpu, battery, services and updates",
			sys: with(func(s *hub.System) {
				s.Info.GPUPct, s.Info.Battery, s.Info.Services, s.Info.PackageUpdates = 15.94, [2]uint8{82, 4}, []uint16{59, 1}, []uint16{3, 0}
			}),
			want: `{"agent":"0.21.1","uptime_s":3600,"cpu_pct":12.3,"mem_pct":70,"disk_pct":50,"temp_hottest_c":48.3,"load_avg":[2,1,1],"load_per_thread":0.25,"net_bytes_per_s":2048,"gpu_pct":15.9,"battery":{"pct":82,"state":"discharging"},"services":{"total":59,"failed":1},"updates":{"total":3,"security":0}}`,
		},
		{
			name: "unknown battery state", sys: with(func(s *hub.System) { s.Info.Battery = [2]uint8{50, 9} }),
			want: `{"agent":"0.21.1","uptime_s":3600,"cpu_pct":12.3,"mem_pct":70,"disk_pct":50,"temp_hottest_c":48.3,"load_avg":[2,1,1],"load_per_thread":0.25,"net_bytes_per_s":2048,"battery":{"pct":50,"state":"unknown"}}`,
		},
		{
			name: "battery state just past the known ones", sys: with(func(s *hub.System) { s.Info.Battery = [2]uint8{50, 6} }),
			want: `{"agent":"0.21.1","uptime_s":3600,"cpu_pct":12.3,"mem_pct":70,"disk_pct":50,"temp_hottest_c":48.3,"load_avg":[2,1,1],"load_per_thread":0.25,"net_bytes_per_s":2048,"battery":{"pct":50,"state":"unknown"}}`,
		},
		{
			name: "this system's problems only", sys: up,
			services: []hub.Service{{System: up.ID, Name: "backup", State: 2}, {System: "otherotherother", Name: "cron", State: 2}},
			want:     `{"agent":"0.21.1","uptime_s":3600,"cpu_pct":12.3,"mem_pct":70,"disk_pct":50,"temp_hottest_c":48.3,"load_avg":[2,1,1],"load_per_thread":0.25,"net_bytes_per_s":2048,"problems":[{"code":"service_failed","detail":"backup","section":"services","count":1}]}`,
		},
		{
			name: "down: static facts and the status problem, no live numbers",
			sys:  with(func(s *hub.System) { s.Status, s.Updated = "down", ago(7*time.Minute) }), details: &mac,
			want: `{"os":"macOS","kernel":"25.0.0","hostname":"mac","arch":"arm64","cpu_model":"Apple M4","cores":10,"threads":10,"mem_total_bytes":17179869184,"problems":[{"code":"status_down","detail":"down for 7m","section":"summary"}]}`,
		},
		{
			name: "zero load average omitted", sys: with(func(s *hub.System) { s.Info.LoadAvg = [3]float64{} }),
			want: `{"agent":"0.21.1","uptime_s":3600,"cpu_pct":12.3,"mem_pct":70,"disk_pct":50,"temp_hottest_c":48.3,"load_per_thread":0,"net_bytes_per_s":2048}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := DetailInput{FleetInput: FleetInput{Now: now, Services: tt.services}, System: tt.sys, Details: tt.details}
			r, err := Detail(in, DetailOptions{})
			if err != nil {
				t.Fatal(err)
			}
			if got := asJSON(t, r.Summary); got != tt.want {
				t.Fatalf("got  %s\nwant %s", got, tt.want)
			}
		})
	}
}

func TestDetailCore(t *testing.T) {
	up := upSystem()
	full := hub.Stats{
		CPU: 21.44, CPUBreakdown: []float64{13.31, 8.1, 0, 0, 78.6, 0, 0, 0}, CPUCores: []uint16{50, 8},
		LoadAvg: [3]float64{3.411, 3.56, 3.59}, MemPct: 78.19, Mem: [4]uint64{100, 60, 30, 0}, Swap: [2]uint64{8, 2},
		Temps: map[string]float64{"CPU": 61.26, "NAND": 33}, Fans: map[string]uint16{"fan0": 1200}, Battery: [2]uint8{100, 2},
	}
	sample := func(f func(*hub.Stats)) *hub.SystemStats {
		x := full
		if f != nil {
			f(&x)
		}
		return &hub.SystemStats{Created: ago(30 * time.Second), Stats: x}
	}
	const fullJSON = `{"sampled_ago_s":30,"cpu_pct":21.4,"cpu_breakdown_pct":{"idle":78.6,"iowait":0,"irq":0,"nice":0,"softirq":0,"steal":0,"system":8.1,"user":13.31},"cpu_cores_pct":[50,8],"load_avg":[3.41,3.56,3.59],"mem":{"total_bytes":100,"used_bytes":60,"cache_bytes":30,"pct":78.2},"swap":{"total_bytes":8,"used_bytes":2},"temps_c":{"CPU":61.3,"NAND":33},"fans_rpm":{"fan0":1200},"battery":{"pct":100,"state":"full"}}`
	tests := []struct {
		name     string
		sys      hub.System
		latest   *hub.SystemStats
		statsErr error
		want     string
	}{
		{name: "every part", sys: up, latest: sample(nil), want: fullJSON},
		{
			name: "NaN temperature dropped", sys: up, latest: sample(func(x *hub.Stats) { x.Temps = map[string]float64{"bad": math.NaN(), "ok": 40} }),
			want: strings.Replace(fullJSON, `"temps_c":{"CPU":61.3,"NAND":33}`, `"temps_c":{"ok":40}`, 1),
		},
		{
			name: "no sensors or battery: named as not collected", sys: up,
			latest: sample(func(x *hub.Stats) { x.Temps, x.Fans, x.Battery = nil, nil, [2]uint8{} }),
			want:   `{"sampled_ago_s":30,"cpu_pct":21.4,"cpu_breakdown_pct":{"idle":78.6,"iowait":0,"irq":0,"nice":0,"softirq":0,"steal":0,"system":8.1,"user":13.31},"cpu_cores_pct":[50,8],"load_avg":[3.41,3.56,3.59],"mem":{"total_bytes":100,"used_bytes":60,"cache_bytes":30,"pct":78.2},"swap":{"total_bytes":8,"used_bytes":2},"absent":["battery: not reported by this host","fans: not reported by this host","temperatures: not reported by this host"]}`,
		},
		{
			name: "old agent: no byte memory, short breakdown", sys: up,
			latest: sample(func(x *hub.Stats) { x.Mem, x.CPUBreakdown = [4]uint64{}, []float64{1, 2} }),
			want:   `{"sampled_ago_s":30,"cpu_pct":21.4,"cpu_cores_pct":[50,8],"load_avg":[3.41,3.56,3.59],"swap":{"total_bytes":8,"used_bytes":2},"temps_c":{"CPU":61.3,"NAND":33},"fans_rpm":{"fan0":1200},"battery":{"pct":100,"state":"full"},"absent":["memory: agent older than 0.21.1 reports no byte counts"]}`,
		},
		{
			name: "ZFS ARC shown when present", sys: up, latest: sample(func(x *hub.Stats) { x.Mem[3] = 7 }),
			want: strings.Replace(fullJSON, `"cache_bytes":30,`, `"cache_bytes":30,"zfs_arc_bytes":7,`, 1),
		},
		{
			name: "paused system", sys: func() hub.System { s := up; s.Status, s.Updated = "paused", ago(2*time.Hour); return s }(),
			latest: sample(nil), want: `{"unavailable":"system is paused, last seen 2h ago"}`,
		},
		{name: "stats read failed", sys: up, statsErr: errors.New("hub unreachable"), want: `{"unavailable":"could not read stats: hub unreachable"}`},
		{name: "no recent sample", sys: up, want: `{"unavailable":"the hub has no stats sample from the last 5 minutes"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := DetailInput{FleetInput: FleetInput{Now: now}, System: tt.sys, Latest: tt.latest, StatsErr: tt.statsErr}
			r, err := Detail(in, DetailOptions{Sections: []string{SectionCore}})
			if err != nil {
				t.Fatal(err)
			}
			if got := asJSON(t, r.Core); got != tt.want {
				t.Fatalf("got  %s\nwant %s", got, tt.want)
			}
		})
	}
}
