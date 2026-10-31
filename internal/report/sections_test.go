package report

import (
	"errors"
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
