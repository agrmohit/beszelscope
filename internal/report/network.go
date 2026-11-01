package report

import (
	"cmp"
	"maps"
	"slices"
	"strings"
	"time"
)

// Network is Beszel's Network tab: traffic, WiFi and network monitors.
type Network struct {
	Unavailable   string          `json:"unavailable,omitempty"`
	SampledAgoS   *int64          `json:"sampled_ago_s,omitempty"`
	SentBytesPerS *uint64         `json:"sent_bytes_per_s,omitempty"`
	RecvBytesPerS *uint64         `json:"recv_bytes_per_s,omitempty"`
	Interfaces    []Interface     `json:"interfaces,omitempty"`
	WiFiRSSIdBm   map[string]int8 `json:"wifi_rssi_dbm,omitempty"`
	Monitors      []MonitorRow    `json:"monitors,omitempty"`
	Absent        []string        `json:"absent,omitempty"` // "part: why it is missing"
}

// Interface is one network interface's traffic.
type Interface struct {
	Name           string `json:"name"`
	SentBytesPerS  uint64 `json:"sent_bytes_per_s"`
	RecvBytesPerS  uint64 `json:"recv_bytes_per_s"`
	TotalSentBytes uint64 `json:"total_sent_bytes"` // since the interface came up
	TotalRecvBytes uint64 `json:"total_recv_bytes"`
}

// MonitorRow is one network monitor: a probe the agent runs to a target.
type MonitorRow struct {
	Target         string   `json:"target"`
	Protocol       string   `json:"protocol"` // icmp, tcp, http or dns
	Port           int      `json:"port,omitempty"`
	Enabled        bool     `json:"enabled"`
	IntervalS      int      `json:"interval_s,omitempty"`
	LatencyMs      *float64 `json:"latency_ms,omitempty"`
	LatencyAvg1hMs *float64 `json:"latency_avg_1h_ms,omitempty"`
	LatencyMax1hMs *float64 `json:"latency_max_1h_ms,omitempty"`
	Loss1hPct      *float64 `json:"loss_1h_pct,omitempty"`
	Cert           *Cert    `json:"cert,omitempty"` // absent when unknown, not when fine
	CheckedAgoS    *int64   `json:"checked_ago_s,omitempty"`
	Problems       []Code   `json:"problems,omitempty"`
}

// Cert is an HTTPS target's certificate.
type Cert struct {
	DaysLeft int    `json:"days_left"` // negative once expired
	Issuer   string `json:"issuer,omitempty"`
}

func network(in DetailInput) *Network {
	if u := in.liveUnavailable(); u != "" {
		return &Network{Unavailable: u}
	}
	x := in.Latest.Val.Stats
	n := &Network{
		SampledAgoS:   new(ageS(in.Now, in.Latest.Val.Created.Time)),
		SentBytesPerS: new(x.Bandwidth[0]),
		RecvBytesPerS: new(x.Bandwidth[1]),
		WiFiRSSIdBm:   x.WiFi,
	}
	for _, name := range slices.Sorted(maps.Keys(x.Interfaces)) {
		v := x.Interfaces[name]
		n.Interfaces = append(n.Interfaces, Interface{
			Name: name, SentBytesPerS: v[0], RecvBytesPerS: v[1], TotalSentBytes: v[2], TotalRecvBytes: v[3],
		})
	}
	for _, m := range in.Monitors.Val {
		if m.System != in.System.ID {
			continue
		}
		row := MonitorRow{
			Target: m.Target, Protocol: m.Protocol, Port: m.Port, Enabled: m.Enabled, IntervalS: m.Interval,
			LatencyMs: msFromUs(m.Res), LatencyAvg1hMs: msFromUs(m.ResAvg1h), LatencyMax1hMs: msFromUs(m.ResMax1h),
			Loss1hPct: round(m.Loss1h, 1),
		}
		if !m.Updated.IsZero() {
			row.CheckedAgoS = new(ageS(in.Now, m.Updated.Time))
		}
		if m.CertInfo != nil && m.CertInfo.Expires > 0 {
			left := time.UnixMilli(m.CertInfo.Expires).Sub(in.Now)
			row.Cert = &Cert{DaysLeft: int(left.Hours() / 24), Issuer: m.CertInfo.Issuer}
		}
		if monitorFailing(m) {
			row.Problems = append(row.Problems, CodeMonitorFailing)
		}
		if code, _ := certProblem(m, in.Now); code != "" {
			row.Problems = append(row.Problems, code)
		}
		n.Monitors = append(n.Monitors, row)
	}
	slices.SortStableFunc(n.Monitors, func(a, b MonitorRow) int {
		return problemsFirst(len(a.Problems), len(b.Problems), a.Target, b.Target)
	})
	if len(n.WiFiRSSIdBm) == 0 {
		n.Absent = append(n.Absent, "wifi: no WiFi interface")
	}
	if len(n.Monitors) == 0 {
		n.Absent = append(n.Absent, "monitors: none configured for this server")
	}
	return n
}

// msFromUs turns Beszel's microseconds into milliseconds; 0 means no data.
func msFromUs(us float64) *float64 {
	if us <= 0 {
		return nil
	}
	return round(us/1000, 1)
}

// problemsFirst orders rows with problems before rows without, then by name.
func problemsFirst(aProblems, bProblems int, aName, bName string) int {
	return cmp.Or(trueFirst(aProblems > 0, bProblems > 0), strings.Compare(aName, bName))
}

// trueFirst orders true before false.
func trueFirst(a, b bool) int {
	switch {
	case a == b:
		return 0
	case a:
		return -1
	default:
		return 1
	}
}
