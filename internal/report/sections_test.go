package report

import (
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/agrmohit/beszelscope/internal/hub"
)

func sample(f func(*hub.Stats)) *hub.SystemStats {
	var x hub.Stats
	if f != nil {
		f(&x)
	}
	return &hub.SystemStats{Created: ago(30 * time.Second), Stats: x}
}

func down() hub.System {
	s := upSystem()
	s.Status, s.Updated = "down", ago(19*time.Minute)
	return s
}

func TestDetailNetwork(t *testing.T) {
	up := upSystem()
	expires := func(d time.Duration) *hub.CertInfo {
		return &hub.CertInfo{Expires: now.Add(d).UnixMilli(), Issuer: "WE1"}
	}
	mon := func(target string, f func(*hub.Monitor)) hub.Monitor {
		m := hub.Monitor{
			System: up.ID, Target: target, Protocol: "http", Interval: 60, Res: 199_100, ResAvg1h: 149_600, ResMax1h: 300_000,
			Enabled: true, Updated: ago(40 * time.Second),
		}
		if f != nil {
			f(&m)
		}
		return m
	}
	traffic := sample(func(x *hub.Stats) {
		x.Bandwidth = [2]uint64{56835, 96118}
		x.Interfaces = map[string][4]uint64{"tailscale0": {172, 163, 7, 3}, "eth0": {56663, 95955, 47, 85}}
	})
	tests := []struct {
		name     string
		sys      hub.System
		latest   *hub.SystemStats
		statsErr error
		monitors []hub.Monitor
		want     string
	}{
		{
			name: "traffic, interfaces by name, no wifi or monitors", sys: up, latest: traffic,
			want: `{"sampled_ago_s":30,"sent_bytes_per_s":56835,"recv_bytes_per_s":96118,"interfaces":[{"name":"eth0","sent_bytes_per_s":56663,"recv_bytes_per_s":95955,"total_sent_bytes":47,"total_recv_bytes":85},{"name":"tailscale0","sent_bytes_per_s":172,"recv_bytes_per_s":163,"total_sent_bytes":7,"total_recv_bytes":3}],"absent":["wifi: no WiFi interface","monitors: none configured for this server"]}`,
		},
		{
			name: "wifi signal", sys: up, latest: sample(func(x *hub.Stats) { x.WiFi = map[string]int8{"en0": -52} }),
			want: `{"sampled_ago_s":30,"sent_bytes_per_s":0,"recv_bytes_per_s":0,"wifi_rssi_dbm":{"en0":-52},"absent":["monitors: none configured for this server"]}`,
		},
		{
			name: "healthy monitor: ms latency, cert days, check age", sys: up, latest: sample(nil),
			monitors: []hub.Monitor{mon("https://a.example.com/", func(m *hub.Monitor) { m.CertInfo = expires(60*24*time.Hour + time.Hour) })},
			want:     `{"sampled_ago_s":30,"sent_bytes_per_s":0,"recv_bytes_per_s":0,"monitors":[{"target":"https://a.example.com/","protocol":"http","enabled":true,"interval_s":60,"latency_ms":199.1,"latency_avg_1h_ms":149.6,"latency_max_1h_ms":300,"loss_1h_pct":0,"cert":{"days_left":60,"issuer":"WE1"},"checked_ago_s":40}],"absent":["wifi: no WiFi interface"]}`,
		},
		{
			name: "problems first: loss, expiring and expired certs; other systems ignored", sys: up, latest: sample(nil),
			monitors: []hub.Monitor{
				mon("a-fine", nil),
				mon("b-lossy", func(m *hub.Monitor) { m.Loss1h = 2.5 }),
				mon("c-expiring", func(m *hub.Monitor) { m.CertInfo = expires(13 * 24 * time.Hour) }),
				mon("d-expired", func(m *hub.Monitor) { m.CertInfo = expires(-2 * 24 * time.Hour) }),
				mon("e-other-system", func(m *hub.Monitor) { m.System = "otherotherother"; m.Loss1h = 50 }),
			},
			want: `{"sampled_ago_s":30,"sent_bytes_per_s":0,"recv_bytes_per_s":0,"monitors":[{"target":"b-lossy","protocol":"http","enabled":true,"interval_s":60,"latency_ms":199.1,"latency_avg_1h_ms":149.6,"latency_max_1h_ms":300,"loss_1h_pct":2.5,"checked_ago_s":40,"problems":["monitor_failing"]},{"target":"c-expiring","protocol":"http","enabled":true,"interval_s":60,"latency_ms":199.1,"latency_avg_1h_ms":149.6,"latency_max_1h_ms":300,"loss_1h_pct":0,"cert":{"days_left":13,"issuer":"WE1"},"checked_ago_s":40,"problems":["cert_expiring"]},{"target":"d-expired","protocol":"http","enabled":true,"interval_s":60,"latency_ms":199.1,"latency_avg_1h_ms":149.6,"latency_max_1h_ms":300,"loss_1h_pct":0,"cert":{"days_left":-2,"issuer":"WE1"},"checked_ago_s":40,"problems":["cert_expired"]},{"target":"a-fine","protocol":"http","enabled":true,"interval_s":60,"latency_ms":199.1,"latency_avg_1h_ms":149.6,"latency_max_1h_ms":300,"loss_1h_pct":0,"checked_ago_s":40}],"absent":["wifi: no WiFi interface"]}`,
		},
		{
			name: "disabled monitor: listed, never a problem; no data omitted", sys: up, latest: sample(nil),
			monitors: []hub.Monitor{mon("tcp-db", func(m *hub.Monitor) {
				m.Enabled, m.Loss1h, m.Protocol, m.Port, m.Res, m.ResAvg1h, m.ResMax1h, m.Updated = false, 100, "tcp", 5432, 0, 0, 0, hub.Time{}
				m.CertInfo = expires(-24 * time.Hour)
			})},
			want: `{"sampled_ago_s":30,"sent_bytes_per_s":0,"recv_bytes_per_s":0,"monitors":[{"target":"tcp-db","protocol":"tcp","port":5432,"enabled":false,"interval_s":60,"loss_1h_pct":100,"cert":{"days_left":-1,"issuer":"WE1"}}],"absent":["wifi: no WiFi interface"]}`,
		},
		{name: "down system", sys: down(), latest: traffic, monitors: []hub.Monitor{mon("x", nil)}, want: `{"unavailable":"system is down, last seen 19m ago"}`},
		{name: "stats read failed", sys: up, statsErr: errors.New("HTTP 503"), want: `{"unavailable":"could not read stats: HTTP 503"}`},
		{name: "no recent sample", sys: up, want: `{"unavailable":"the hub has no stats sample from the last 5 minutes"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := DetailInput{FleetInput: FleetInput{Now: now, Monitors: tt.monitors}, System: tt.sys, Latest: tt.latest, StatsErr: tt.statsErr}
			r, err := Detail(in, DetailOptions{Sections: []string{SectionNetwork}})
			if err != nil {
				t.Fatal(err)
			}
			if got := asJSON(t, r.Network); got != tt.want {
				t.Fatalf("got  %s\nwant %s", got, tt.want)
			}
		})
	}
}

func TestDetailDisk(t *testing.T) {
	up := upSystem()
	disks := sample(func(x *hub.Stats) {
		x.DiskGB, x.DiskUsedGB, x.DiskPct, x.DiskIO, x.DiskIOStats = 74.96, 28.84, 38.47, [2]uint64{0, 314572}, [6]float64{0, 0, 0.394, 0, 0, 0}
		x.ExtraFs = map[string]hub.FsStats{
			"/mnt/media": {TotalGB: 100, UsedGB: 91.26, ReadBytes: 5, WriteBytes: 6},
			"/mnt/b":     {TotalGB: 0, UsedGB: 0},
		}
	})
	pool := func(name string, f func(*hub.ZfsPool)) hub.ZfsPool {
		p := hub.ZfsPool{
			System: up.ID, Name: name, Health: "ONLINE", Size: 4000, Alloc: 1200, Free: 2800,
			Scrub: &hub.Scrub{State: "FINISHED", Errors: 0}, Vdevs: []hub.Vdev{{Name: "mirror-0", State: "ONLINE"}}, Updated: ago(time.Minute),
		}
		if f != nil {
			f(&p)
		}
		return p
	}
	smart := func(name string, f func(*hub.SmartDevice)) hub.SmartDevice {
		d := hub.SmartDevice{System: up.ID, Name: name, Model: "WDC", Type: "sat", State: "PASSED", Capacity: 4e12, Temp: 33.26, Hours: 1633, Cycles: 178, Updated: ago(2 * time.Hour)}
		if f != nil {
			f(&d)
		}
		return d
	}
	tests := []struct {
		name     string
		sys      hub.System
		latest   *hub.SystemStats
		statsErr error
		pools    []hub.ZfsPool
		smart    []hub.SmartDevice
		want     string
	}{
		{
			name: "root then extras by name; no size means no pct", sys: up, latest: disks,
			want: `{"sampled_ago_s":30,"filesystems":[{"name":"/","total_gib":74.96,"used_gib":28.84,"pct":38.5,"read_bytes_per_s":0,"write_bytes_per_s":314572,"io_util_pct":0.4},{"name":"/mnt/b","total_gib":0,"used_gib":0,"read_bytes_per_s":0,"write_bytes_per_s":0},{"name":"/mnt/media","total_gib":100,"used_gib":91.26,"pct":91.3,"read_bytes_per_s":5,"write_bytes_per_s":6}],"absent":["pools: no ZFS or btrfs pools","smart: no SMART data reported"]}`,
		},
		{
			name: "pools: degraded first, display name, only faulty vdevs, raw sizes", sys: up, latest: sample(nil),
			pools: []hub.ZfsPool{
				pool("a-tank", nil),
				pool("b:abc", func(p *hub.ZfsPool) {
					p.DisplayName, p.Health, p.Status, p.Raw = "backup", "DEGRADED", "One or more devices has been removed", true
					p.Vdevs = []hub.Vdev{{Name: "sda", State: "ONLINE"}, {Name: "sdb", State: "REMOVED"}, {Name: "sdc", State: "ONLINE", ChecksumErrs: 3}}
				}),
				pool("other", func(p *hub.ZfsPool) { p.System = "otherotherother" }),
			},
			want: `{"sampled_ago_s":30,"filesystems":[{"name":"/","total_gib":0,"used_gib":0,"pct":0,"read_bytes_per_s":0,"write_bytes_per_s":0}],"pools":[{"name":"backup","health":"DEGRADED","status":"One or more devices has been removed","size_bytes":4000,"alloc_bytes":1200,"free_bytes":2800,"raw_sizes":true,"scrub":{"state":"FINISHED","errors":0},"bad_vdevs":[{"name":"sdb","state":"REMOVED","read_errors":0,"write_errors":0,"checksum_errors":0},{"name":"sdc","state":"ONLINE","read_errors":0,"write_errors":0,"checksum_errors":3}],"checked_ago_s":60,"problems":["pool_degraded"]},{"name":"a-tank","health":"ONLINE","size_bytes":4000,"alloc_bytes":1200,"free_bytes":2800,"scrub":{"state":"FINISHED","errors":0},"checked_ago_s":60}],"absent":["smart: no SMART data reported"]}`,
		},
		{
			name: "smart: failing first, short macOS name, failed attributes, no temp omitted", sys: up, latest: sample(nil),
			smart: []hub.SmartDevice{
				smart("/dev/sda", nil),
				smart("IOService:/AppleARMPE/arm-io@10F00000/AppleANS3CGv2Controller/NS_01@1", func(d *hub.SmartDevice) {
					d.State, d.Temp, d.Model = "FAILED", 0, "APPLE SSD"
					d.Attributes = []hub.SmartAttr{{Name: "Reallocated_Sector_Ct", WhenFailed: "now", RawString: "120"}, {Name: "Power_On_Hours"}}
				}),
				smart("/dev/sdb", func(d *hub.SmartDevice) { d.State = "WARNING" }),
			},
			want: `{"sampled_ago_s":30,"filesystems":[{"name":"/","total_gib":0,"used_gib":0,"pct":0,"read_bytes_per_s":0,"write_bytes_per_s":0}],"smart":[{"name":"/dev/sdb","model":"WDC","type":"sat","state":"WARNING","capacity_bytes":4000000000000,"temp_c":33.3,"power_on_hours":1633,"power_cycles":178,"checked_ago_s":7200,"problems":["smart_warning"]},{"name":"NS_01@1","model":"APPLE SSD","type":"sat","state":"FAILED","capacity_bytes":4000000000000,"power_on_hours":1633,"power_cycles":178,"failing_attributes":[{"name":"Reallocated_Sector_Ct","when_failed":"now","raw":"120"}],"checked_ago_s":7200,"problems":["smart_failed"]},{"name":"/dev/sda","model":"WDC","type":"sat","state":"PASSED","capacity_bytes":4000000000000,"temp_c":33.3,"power_on_hours":1633,"power_cycles":178,"checked_ago_s":7200}],"absent":["pools: no ZFS or btrfs pools"]}`,
		},
		{
			name: "down: SMART and pools are last-known, filesystems unavailable", sys: down(), latest: disks,
			pools: []hub.ZfsPool{pool("tank", nil)}, smart: []hub.SmartDevice{smart("/dev/sda", nil)},
			want: `{"unavailable":"system is down, last seen 19m ago","pools":[{"name":"tank","health":"ONLINE","size_bytes":4000,"alloc_bytes":1200,"free_bytes":2800,"scrub":{"state":"FINISHED","errors":0},"checked_ago_s":60}],"smart":[{"name":"/dev/sda","model":"WDC","type":"sat","state":"PASSED","capacity_bytes":4000000000000,"temp_c":33.3,"power_on_hours":1633,"power_cycles":178,"checked_ago_s":7200}]}`,
		},
		{
			name: "stats failed: SMART still shown", sys: up, statsErr: errors.New("HTTP 503"), smart: []hub.SmartDevice{smart("/dev/sda", nil)},
			want: `{"unavailable":"could not read stats: HTTP 503","smart":[{"name":"/dev/sda","model":"WDC","type":"sat","state":"PASSED","capacity_bytes":4000000000000,"temp_c":33.3,"power_on_hours":1633,"power_cycles":178,"checked_ago_s":7200}],"absent":["pools: no ZFS or btrfs pools"]}`,
		},
		{name: "no sample and nothing stored", sys: up, want: `{"unavailable":"the hub has no stats sample from the last 5 minutes","absent":["pools: no ZFS or btrfs pools","smart: no SMART data reported"]}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := DetailInput{FleetInput: FleetInput{Now: now, Pools: tt.pools, Smart: tt.smart}, System: tt.sys, Latest: tt.latest, StatsErr: tt.statsErr}
			r, err := Detail(in, DetailOptions{Sections: []string{SectionDisk}})
			if err != nil {
				t.Fatal(err)
			}
			if got := asJSON(t, r.Disk); got != tt.want {
				t.Fatalf("got  %s\nwant %s", got, tt.want)
			}
		})
	}
}

func TestSmartName(t *testing.T) {
	tests := []struct{ in, want string }{
		{"/dev/sda", "/dev/sda"},
		{"/dev/nvme0", "/dev/nvme0"},
		{"IOService:/AppleARMPE/arm-io@10F00000/NS_01@1", "NS_01@1"},
		{"IOService:NS_02", "IOService:NS_02"},
		{"disk0", "disk0"},
		{"", ""},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			if got := smartName(tt.in); got != tt.want {
				t.Fatalf("smartName(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestDetailGPU(t *testing.T) {
	up := upSystem()
	tests := []struct {
		name     string
		sys      hub.System
		latest   *hub.SystemStats
		statsErr error
		want     string
	}{
		{
			name: "by id; MiB to bytes; unreported fields omitted", sys: up,
			latest: sample(func(x *hub.Stats) {
				x.GPU = map[string]hub.GPU{
					"1": {Name: "Intel UHD", Usage: 0},
					"0": {Name: "RTX 3060", Usage: 37.46, MemUsedMB: 2048.5, MemTotalMB: 12288, PowerW: 45.123},
				}
			}),
			want: `{"sampled_ago_s":30,"gpus":[{"id":"0","name":"RTX 3060","usage_pct":37.5,"mem_used_bytes":2148007936,"mem_total_bytes":12884901888,"power_w":45.12},{"id":"1","name":"Intel UHD","usage_pct":0}]}`,
		},
		{
			name: "Mac: power, no memory", sys: up,
			latest: sample(func(x *hub.Stats) { x.GPU = map[string]hub.GPU{"0": {Name: "Apple GPU", Usage: 15.91, PowerW: 1.41}} }),
			want:   `{"sampled_ago_s":30,"gpus":[{"id":"0","name":"Apple GPU","usage_pct":15.9,"power_w":1.41}]}`,
		},
		{name: "no GPU", sys: up, latest: sample(nil), want: `{"sampled_ago_s":30,"absent":["gpus: none reported by this host"]}`},
		{name: "down system", sys: down(), latest: sample(nil), want: `{"unavailable":"system is down, last seen 19m ago"}`},
		{name: "stats read failed", sys: up, statsErr: errors.New("HTTP 503"), want: `{"unavailable":"could not read stats: HTTP 503"}`},
		{name: "no recent sample", sys: up, want: `{"unavailable":"the hub has no stats sample from the last 5 minutes"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := DetailInput{FleetInput: FleetInput{Now: now}, System: tt.sys, Latest: tt.latest, StatsErr: tt.statsErr}
			r, err := Detail(in, DetailOptions{Sections: []string{SectionGPU}})
			if err != nil {
				t.Fatal(err)
			}
			if got := asJSON(t, r.GPU); got != tt.want {
				t.Fatalf("got  %s\nwant %s", got, tt.want)
			}
		})
	}
}

// listResult is a list section and its hint as one JSON string.
func listResult(t *testing.T, r DetailResult, section any) string {
	t.Helper()
	return asJSON(t, map[string]any{"section": section, "hints": r.Hints})
}

func TestDetailContainers(t *testing.T) {
	up := upSystem()
	ctr := func(name string, cpu, memMiB float64, f func(*hub.Container)) hub.Container {
		c := hub.Container{
			System: up.ID, Name: name, Image: name + ":1", Status: "Up 2 hours", Health: 2,
			CPU: cpu, MemoryMiB: memMiB, NetBytes: 1500.7, Updated: now.Add(-20 * time.Second).UnixMilli(),
		}
		if f != nil {
			f(&c)
		}
		return c
	}
	unhealthy := func(c *hub.Container) { c.Health = 3 }
	row := func(name string, cpu float64, memBytes int, problem bool) string {
		health, problems := "healthy", ""
		if problem {
			health, problems = "unhealthy", `,"problems":["container_unhealthy"]`
		}
		return `{"name":"` + name + `","image":"` + name + `:1","status":"Up 2 hours","health":"` + health + `","cpu_pct":` +
			strconv.FormatFloat(cpu, 'f', -1, 64) + `,"mem_bytes":` + strconv.Itoa(memBytes) + `,"net_bytes_per_s":1500,"age_s":20` + problems + `}`
	}
	three := []hub.Container{ctr("a", 0.5, 100, nil), ctr("b", 2, 10, nil), ctr("c", 1, 50, nil)}
	five := []hub.Container{
		ctr("h1", 3, 1, nil), ctr("h2", 2, 1, nil),
		ctr("u1", 0.1, 1, unhealthy), ctr("u2", 0.2, 1, unhealthy), ctr("u3", 0.3, 1, unhealthy),
	}
	tests := []struct {
		name       string
		sys        hub.System
		containers []hub.Container
		recent     []hub.ContainerStats
		readErr    error
		opts       DetailOptions
		want       string
	}{
		{
			name: "default sort is cpu, highest first; MiB to bytes", sys: up, containers: three,
			want: `{"hints":null,"section":{"counts":{"total":3,"shown":3,"truncated":false},"unhealthy":0,"rows":[` +
				row("b", 2, 10485760, false) + `,` + row("c", 1, 52428800, false) + `,` + row("a", 0.5, 104857600, false) + `]}}`,
		},
		{
			name: "sort mem", sys: up, containers: three, opts: DetailOptions{Sort: SortMem},
			want: `{"hints":null,"section":{"counts":{"total":3,"shown":3,"truncated":false},"unhealthy":0,"rows":[` +
				row("a", 0.5, 104857600, false) + `,` + row("c", 1, 52428800, false) + `,` + row("b", 2, 10485760, false) + `]}}`,
		},
		{
			name: "sort name", sys: up, containers: []hub.Container{three[2], three[1], three[0]}, opts: DetailOptions{Sort: SortName},
			want: `{"hints":null,"section":{"counts":{"total":3,"shown":3,"truncated":false},"unhealthy":0,"rows":[` +
				row("a", 0.5, 104857600, false) + `,` + row("b", 2, 10485760, false) + `,` + row("c", 1, 52428800, false) + `]}}`,
		},
		{
			name: "equal cpu ties by name", sys: up, containers: []hub.Container{ctr("z", 1, 1, nil), ctr("m", 1, 1, nil)},
			want: `{"hints":null,"section":{"counts":{"total":2,"shown":2,"truncated":false},"unhealthy":0,"rows":[` +
				row("m", 1, 1048576, false) + `,` + row("z", 1, 1048576, false) + `]}}`,
		},
		{
			name: "problem rows first, then cpu", sys: up, containers: five,
			want: `{"hints":null,"section":{"counts":{"total":5,"shown":5,"truncated":false},"unhealthy":3,"rows":[` +
				row("u3", 0.3, 1048576, true) + `,` + row("u2", 0.2, 1048576, true) + `,` + row("u1", 0.1, 1048576, true) + `,` +
				row("h1", 3, 1048576, false) + `,` + row("h2", 2, 1048576, false) + `]}}`,
		},
		{
			name: "limit below the problem count keeps every problem row", sys: up, containers: five, opts: DetailOptions{Limit: 2},
			want: `{"hints":["containers: 2 of 5 rows not shown; raise limit (max 200) or use filter"],"section":{"counts":{"total":5,"shown":3,"truncated":true},"unhealthy":3,"rows":[` +
				row("u3", 0.3, 1048576, true) + `,` + row("u2", 0.2, 1048576, true) + `,` + row("u1", 0.1, 1048576, true) + `]}}`,
		},
		{
			name: "limit one past the problem rows", sys: up, containers: five, opts: DetailOptions{Limit: 4},
			want: `{"hints":["containers: 1 of 5 rows not shown; raise limit (max 200) or use filter"],"section":{"counts":{"total":5,"shown":4,"truncated":true},"unhealthy":3,"rows":[` +
				row("u3", 0.3, 1048576, true) + `,` + row("u2", 0.2, 1048576, true) + `,` + row("u1", 0.1, 1048576, true) + `,` +
				row("h1", 3, 1048576, false) + `]}}`,
		},
		{
			name: "limit equal to the row count cuts nothing", sys: up, containers: three, opts: DetailOptions{Limit: 3},
			want: `{"hints":null,"section":{"counts":{"total":3,"shown":3,"truncated":false},"unhealthy":0,"rows":[` +
				row("b", 2, 10485760, false) + `,` + row("c", 1, 52428800, false) + `,` + row("a", 0.5, 104857600, false) + `]}}`,
		},
		{
			name: "problems only", sys: up, containers: five, opts: DetailOptions{ProblemsOnly: true, Sort: SortName},
			want: `{"hints":null,"section":{"counts":{"total":5,"shown":3,"truncated":false},"unhealthy":3,"rows":[` +
				row("u1", 0.1, 1048576, true) + `,` + row("u2", 0.2, 1048576, true) + `,` + row("u3", 0.3, 1048576, true) + `]}}`,
		},
		{
			name: "problems only with none", sys: up, containers: three, opts: DetailOptions{ProblemsOnly: true},
			want: `{"hints":null,"section":{"counts":{"total":3,"shown":0,"truncated":false},"unhealthy":0}}`,
		},
		{
			name: "filter on name ignores case", sys: up, containers: five, opts: DetailOptions{Filter: "H"},
			want: `{"hints":null,"section":{"counts":{"total":5,"shown":2,"truncated":false},"unhealthy":3,"rows":[` +
				row("h1", 3, 1048576, false) + `,` + row("h2", 2, 1048576, false) + `]}}`,
		},
		{
			name: "filter on image finds a container named otherwise", sys: up,
			containers: []hub.Container{ctr("db", 1, 1, func(c *hub.Container) { c.Image = "postgres:18" }), ctr("web", 1, 1, nil)},
			opts:       DetailOptions{Filter: "Postgres"},
			want:       `{"hints":null,"section":{"counts":{"total":2,"shown":1,"truncated":false},"unhealthy":0,"rows":[{"name":"db","image":"postgres:18","status":"Up 2 hours","health":"healthy","cpu_pct":1,"mem_bytes":1048576,"net_bytes_per_s":1500,"age_s":20}]}}`,
		},
		{
			name: "filter matches nothing: hint says only running containers exist", sys: up, containers: three, opts: DetailOptions{Filter: "redis"},
			want: `{"hints":["containers: no running container's name or image contains \"redis\"; Beszel lists only running containers"],"section":{"counts":{"total":3,"shown":0,"truncated":false},"unhealthy":0}}`,
		},
		{
			name: "filter matching only a stopped container: listed in gone, no hint", sys: up,
			containers: []hub.Container{three[0], ctr("redis", 0, 1, func(c *hub.Container) { c.Updated = now.Add(-5 * time.Minute).UnixMilli() })},
			opts:       DetailOptions{Filter: "redis"},
			want:       `{"hints":null,"section":{"counts":{"total":1,"shown":0,"truncated":false},"unhealthy":0,"gone":["redis"]}}`,
		},
		{
			name: "gone: stale rows and names from the last hour; other systems and running names ignored", sys: up,
			containers: []hub.Container{
				three[0],
				ctr("stale", 9, 1, func(c *hub.Container) { c.Updated = now.Add(-3*time.Minute - time.Second).UnixMilli(); c.Health = 3 }),
				ctr("stale", 9, 1, func(c *hub.Container) { c.Updated = now.Add(-8 * time.Minute).UnixMilli() }),
				ctr("edge", 1, 1, func(c *hub.Container) { c.Updated = now.Add(-3 * time.Minute).UnixMilli() }),
				ctr("elsewhere", 1, 1, func(c *hub.Container) { c.System = "otherotherother"; c.Updated = 0 }),
				ctr("backup", 1, 1, func(c *hub.Container) { c.System = "otherotherother" }), // live elsewhere, gone here
			},
			recent: []hub.ContainerStats{
				{System: up.ID, Stats: []hub.ContainerStat{{Name: "a"}, {Name: "cron"}, {Name: "backup"}}},
				{System: up.ID, Stats: []hub.ContainerStat{{Name: "cron"}}},
				{System: "otherotherother", Stats: []hub.ContainerStat{{Name: "ghost"}}},
			},
			want: `{"hints":null,"section":{"counts":{"total":2,"shown":2,"truncated":false},"unhealthy":0,"rows":[` +
				`{"name":"edge","image":"edge:1","status":"Up 2 hours","health":"healthy","cpu_pct":1,"mem_bytes":1048576,"net_bytes_per_s":1500,"age_s":180},` +
				row("a", 0.5, 104857600, false) + `],"gone":["backup","cron","stale"]}}`,
		},
		{
			name: "recreated: the old row of a live name is not gone", sys: up,
			containers: []hub.Container{
				ctr("a", 0.5, 100, func(c *hub.Container) { c.Updated = now.Add(-9 * time.Minute).UnixMilli() }),
				three[0],
			},
			want: `{"hints":null,"section":{"counts":{"total":1,"shown":1,"truncated":false},"unhealthy":0,"rows":[` + row("a", 0.5, 104857600, false) + `]}}`,
		},
		{
			name: "health names, unknown health value", sys: up,
			containers: []hub.Container{
				ctr("n", 3, 1, func(c *hub.Container) { c.Health = 0 }), ctr("s", 2, 1, func(c *hub.Container) { c.Health = 1 }),
				ctr("x", 1, 1, func(c *hub.Container) { c.Health = 7 }),
			},
			want: `{"hints":null,"section":{"counts":{"total":3,"shown":3,"truncated":false},"unhealthy":0,"rows":[` +
				`{"name":"n","image":"n:1","status":"Up 2 hours","health":"none","cpu_pct":3,"mem_bytes":1048576,"net_bytes_per_s":1500,"age_s":20},` +
				`{"name":"s","image":"s:1","status":"Up 2 hours","health":"starting","cpu_pct":2,"mem_bytes":1048576,"net_bytes_per_s":1500,"age_s":20},` +
				`{"name":"x","image":"x:1","status":"Up 2 hours","health":"unknown","cpu_pct":1,"mem_bytes":1048576,"net_bytes_per_s":1500,"age_s":20}]}}`,
		},
		{
			name: "none running", sys: up, containers: []hub.Container{ctr("elsewhere", 1, 1, func(c *hub.Container) { c.System = "otherotherother" })},
			want: `{"hints":null,"section":{"counts":{"total":0,"shown":0,"truncated":false},"unhealthy":0,"absent":["containers: none running; Beszel lists only running containers"]}}`,
		},
		{name: "down system", sys: down(), containers: three, want: `{"hints":null,"section":{"unavailable":"system is down, last seen 19m ago"}}`},
		{name: "read failed", sys: up, readErr: errors.New("HTTP 503"), want: `{"hints":null,"section":{"unavailable":"could not read containers: HTTP 503"}}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := DetailInput{
				FleetInput: FleetInput{Now: now, Containers: tt.containers, RecentContainers: tt.recent},
				System:     tt.sys, ContainersErr: tt.readErr,
			}
			tt.opts.Sections = []string{SectionContainers}
			r, err := Detail(in, tt.opts)
			if err != nil {
				t.Fatal(err)
			}
			if got := listResult(t, r, r.Containers); got != tt.want {
				t.Fatalf("got  %s\nwant %s", got, tt.want)
			}
		})
	}
}

func TestDetailServices(t *testing.T) {
	up := upSystem()
	svc := func(name string, state, sub int, cpu float64, mem uint64) hub.Service {
		return hub.Service{System: up.ID, Name: name, State: state, Sub: sub, CPU: cpu, Memory: mem, MemPeak: 2 * mem}
	}
	units := []hub.Service{
		svc("sshd", 0, 1, 0.05, 300), svc("nginx", 0, 1, 1.5, 100), svc("backup", 2, 3, 0, 0),
		svc("cron", 1, 0, 0, 200), svc("other", 2, 3, 9, 9),
	}
	units[4].System = "otherotherother"
	tests := []struct {
		name     string
		services []hub.Service
		sys      hub.System
		readErr  error
		opts     DetailOptions
		want     string
	}{
		{
			name: "failed first, then cpu; state and sub names", sys: up, services: units,
			want: `{"hints":null,"section":{"counts":{"total":4,"shown":4,"truncated":false},"failed":1,"rows":[` +
				`{"name":"backup","state":"failed","sub":"failed","cpu_pct":0,"mem_bytes":0,"mem_peak_bytes":0,"problems":["service_failed"]},` +
				`{"name":"nginx","state":"active","sub":"running","cpu_pct":1.5,"mem_bytes":100,"mem_peak_bytes":200},` +
				`{"name":"sshd","state":"active","sub":"running","cpu_pct":0.05,"mem_bytes":300,"mem_peak_bytes":600},` +
				`{"name":"cron","state":"inactive","sub":"dead","cpu_pct":0,"mem_bytes":200,"mem_peak_bytes":400}]}}`,
		},
		{
			name: "sort mem with a limit", sys: up, services: units, opts: DetailOptions{Sort: SortMem, Limit: 2},
			want: `{"hints":["services: 2 of 4 rows not shown; raise limit (max 200) or use filter"],"section":{"counts":{"total":4,"shown":2,"truncated":true},"failed":1,"rows":[` +
				`{"name":"backup","state":"failed","sub":"failed","cpu_pct":0,"mem_bytes":0,"mem_peak_bytes":0,"problems":["service_failed"]},` +
				`{"name":"sshd","state":"active","sub":"running","cpu_pct":0.05,"mem_bytes":300,"mem_peak_bytes":600}]}}`,
		},
		{
			name: "problems only", sys: up, services: units, opts: DetailOptions{ProblemsOnly: true},
			want: `{"hints":null,"section":{"counts":{"total":4,"shown":1,"truncated":false},"failed":1,"rows":[` +
				`{"name":"backup","state":"failed","sub":"failed","cpu_pct":0,"mem_bytes":0,"mem_peak_bytes":0,"problems":["service_failed"]}]}}`,
		},
		{
			name: "filter", sys: up, services: units, opts: DetailOptions{Filter: "SSH"},
			want: `{"hints":null,"section":{"counts":{"total":4,"shown":1,"truncated":false},"failed":1,"rows":[` +
				`{"name":"sshd","state":"active","sub":"running","cpu_pct":0.05,"mem_bytes":300,"mem_peak_bytes":600}]}}`,
		},
		{
			name: "filter matches nothing", sys: up, services: units, opts: DetailOptions{Filter: "docker"},
			want: `{"hints":["services: no unit name contains \"docker\""],"section":{"counts":{"total":4,"shown":0,"truncated":false},"failed":1}}`,
		},
		{
			name: "states and subs past the known ones", sys: up,
			services: []hub.Service{svc("a", 3, 4, 0, 0), svc("b", 5, 2, 0, 0), svc("c", 6, 5, 0, 0), svc("d", 4, 1, 0, 0)},
			opts:     DetailOptions{Sort: SortName},
			want: `{"hints":null,"section":{"counts":{"total":4,"shown":4,"truncated":false},"failed":0,"rows":[` +
				`{"name":"a","state":"activating","sub":"unknown","cpu_pct":0,"mem_bytes":0,"mem_peak_bytes":0},` +
				`{"name":"b","state":"reloading","sub":"exited","cpu_pct":0,"mem_bytes":0,"mem_peak_bytes":0},` +
				`{"name":"c","state":"unknown","sub":"unknown","cpu_pct":0,"mem_bytes":0,"mem_peak_bytes":0},` +
				`{"name":"d","state":"deactivating","sub":"running","cpu_pct":0,"mem_bytes":0,"mem_peak_bytes":0}]}}`,
		},
		{
			name: "none on this host", sys: up, services: units[4:],
			want: `{"hints":null,"section":{"absent":["services: none reported; Beszel reads systemd units on Linux only"]}}`,
		},
		{name: "down system", sys: down(), services: units, want: `{"hints":null,"section":{"unavailable":"system is down, last seen 19m ago"}}`},
		{name: "read failed", sys: up, readErr: errors.New("HTTP 503"), want: `{"hints":null,"section":{"unavailable":"could not read services: HTTP 503"}}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := DetailInput{FleetInput: FleetInput{Now: now, Services: tt.services}, System: tt.sys, ServicesErr: tt.readErr}
			tt.opts.Sections = []string{SectionServices}
			r, err := Detail(in, tt.opts)
			if err != nil {
				t.Fatal(err)
			}
			if got := listResult(t, r, r.Services); got != tt.want {
				t.Fatalf("got  %s\nwant %s", got, tt.want)
			}
		})
	}
}

// TestDetailHints: each list section adds its own hint, in section order.
func TestDetailHints(t *testing.T) {
	up := upSystem()
	in := DetailInput{
		FleetInput: FleetInput{Now: now, Services: []hub.Service{{System: up.ID, Name: "a"}, {System: up.ID, Name: "b"}}},
		System:     up,
	}
	for range 2 {
		in.Containers = append(in.Containers, hub.Container{System: up.ID, Name: "c", Updated: now.UnixMilli()})
	}
	r, err := Detail(in, DetailOptions{Sections: []string{SectionServices, SectionContainers}, Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	want := `["services: 1 of 2 rows not shown; raise limit (max 200) or use filter","containers: 1 of 2 rows not shown; raise limit (max 200) or use filter"]`
	if got := asJSON(t, r.Hints); got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
}

func TestDetailUpdates(t *testing.T) {
	up := upSystem()
	withPU := func(pu ...uint16) hub.System { s := up; s.Info.PackageUpdates = pu; return s }
	checked := now.Add(-40 * time.Minute).Unix()
	pkgs := []hub.Package{
		{Name: "vim", Current: "9.1", Available: "9.2"},
		{Name: "Zsh", Available: "5.3"},
		{Name: "openssl", Current: "3.5.1", Available: "3.5.2", Security: true},
		{Name: "curl", Current: "8.1", Available: "8.2", Security: true},
	}
	tests := []struct {
		name     string
		sys      hub.System
		details  *hub.SystemDetails
		packages *hub.PackageUpdates
		readErr  error
		want     string
	}{
		{
			name: "security first, then name ignoring case (vim before Zsh); versions; check age", sys: up,
			packages: &hub.PackageUpdates{Manager: "dnf", CheckedAt: checked, SecurityKnown: true, Packages: pkgs},
			want:     `{"total":4,"security":2,"manager":"dnf","checked_ago_s":2400,"packages":[{"name":"curl","current":"8.1","available":"8.2","security":true},{"name":"openssl","current":"3.5.1","available":"3.5.2","security":true},{"name":"vim","current":"9.1","available":"9.2"},{"name":"Zsh","available":"5.3"}]}`,
		},
		{
			name: "manager does not flag security: no security count", sys: up,
			packages: &hub.PackageUpdates{Manager: "pacman", CheckedAt: checked, Packages: pkgs[:2]},
			want:     `{"total":2,"manager":"pacman","checked_ago_s":2400,"packages":[{"name":"vim","current":"9.1","available":"9.2"},{"name":"Zsh","available":"5.3"}]}`,
		},
		{
			name: "checked, nothing pending", sys: up,
			packages: &hub.PackageUpdates{Manager: "apt", CheckedAt: checked, SecurityKnown: true, Packages: []hub.Package{}},
			want:     `{"total":0,"security":0,"manager":"apt","checked_ago_s":2400}`,
		},
		{
			name: "first check still running", sys: up, packages: &hub.PackageUpdates{Manager: "apt"},
			want: `{"manager":"apt","absent":["updates: the agent's first package check has not finished"]}`,
		},
		{
			name: "agent does not check packages", sys: up, packages: &hub.PackageUpdates{Packages: []hub.Package{}},
			want: `{"absent":["updates: this agent does not check packages; Beszel checks them only on agents running natively on Linux, not in a container"]}`,
		},
		{
			name: "macOS: names the OS", sys: up, details: &hub.SystemDetails{OS: 1, OSName: "macOS 27.0.1"}, packages: &hub.PackageUpdates{},
			want: `{"absent":["updates: Beszel checks packages only on Linux; this host runs macOS 27.0.1"]}`,
		},
		{
			name: "FreeBSD without a name", sys: up, details: &hub.SystemDetails{OS: 3}, packages: &hub.PackageUpdates{},
			want: `{"absent":["updates: Beszel checks packages only on Linux; this host runs FreeBSD"]}`,
		},
		{
			name: "Linux: container, turned off or no manager", sys: up, details: &hub.SystemDetails{OS: 0, OSName: "Fedora Linux 44"}, packages: &hub.PackageUpdates{},
			want: `{"absent":["updates: this Linux agent does not check packages; it runs in a container, has checks turned off, or found no supported package manager"]}`,
		},
		{
			name: "list not read", sys: up,
			want: `{"absent":["updates: this agent does not check packages; Beszel checks them only on agents running natively on Linux, not in a container"]}`,
		},
		{
			name: "live call failed: counts from the systems row", sys: withPU(12, 3), readErr: errors.New("HTTP 500"),
			want: `{"unavailable":"could not ask the agent for the package list: HTTP 500","total":12,"security":3}`,
		},
		{
			name: "live call failed, total only", sys: withPU(12), readErr: errors.New("HTTP 500"),
			want: `{"unavailable":"could not ask the agent for the package list: HTTP 500","total":12}`,
		},
		{
			name: "live call failed, no counts", sys: up, readErr: errors.New("HTTP 500"),
			want: `{"unavailable":"could not ask the agent for the package list: HTTP 500"}`,
		},
		{name: "down system", sys: down(), packages: &hub.PackageUpdates{Manager: "dnf", CheckedAt: checked}, want: `{"unavailable":"system is down, last seen 19m ago"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := DetailInput{FleetInput: FleetInput{Now: now}, System: tt.sys, Details: tt.details, Packages: tt.packages, PackagesErr: tt.readErr}
			r, err := Detail(in, DetailOptions{Sections: []string{SectionUpdates}})
			if err != nil {
				t.Fatal(err)
			}
			if got := asJSON(t, r.Updates); got != tt.want {
				t.Fatalf("got  %s\nwant %s", got, tt.want)
			}
		})
	}
}
