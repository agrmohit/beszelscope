package report

import (
	"cmp"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/agrmohit/beszelscope/internal/hub"
)

// Window is one metric_history time range, read from the stats type
// Beszel's own charts use for it (site utils.ts).
type Window struct {
	Name     string
	Type     hub.StatType
	Span     time.Duration
	Bucket   time.Duration
	Expected int // buckets in a full window
}

// Windows lists the ranges metric_history accepts, shortest first.
var Windows = []Window{
	{"1h", hub.Stat1m, time.Hour, time.Minute, 60},
	{"12h", hub.Stat10m, 12 * time.Hour, 10 * time.Minute, 72},
	{"24h", hub.Stat20m, 24 * time.Hour, 20 * time.Minute, 72},
	{"7d", hub.Stat120m, 7 * 24 * time.Hour, 2 * time.Hour, 84},
	{"30d", hub.Stat480m, 30 * 24 * time.Hour, 8 * time.Hour, 90},
}

// DefaultWindow is used when the caller names none.
const DefaultWindow = "24h"

// History limits.
const (
	MaxHistorySystems = 5
	MaxMetrics        = 6
	MaxPoints         = 60
	pointBudget       = 360 // series x points
)

// MetricKind is what metric_history reads; container and monitor need a target.
type MetricKind string

// The metric kinds metric_history accepts.
const (
	MetricCPU       MetricKind = "cpu"
	MetricIOWait    MetricKind = "cpu_iowait"
	MetricMem       MetricKind = "mem"
	MetricSwap      MetricKind = "swap"
	MetricDisk      MetricKind = "disk"
	MetricDiskIO    MetricKind = "disk_io"
	MetricNet       MetricKind = "net"
	MetricLoad      MetricKind = "load"
	MetricTemp      MetricKind = "temp"
	MetricGPU       MetricKind = "gpu"
	MetricContainer MetricKind = "container"
	MetricMonitor   MetricKind = "monitor"
)

// metricKinds lists every kind, in the order errors name them.
var metricKinds = []MetricKind{
	MetricCPU, MetricIOWait, MetricMem, MetricSwap, MetricDisk, MetricDiskIO,
	MetricNet, MetricLoad, MetricTemp, MetricGPU, MetricContainer, MetricMonitor,
}

// Metric is one requested metric: a kind and an optional target.
type Metric struct {
	Raw    string
	Kind   MetricKind
	Target string
}

// Source is a hub collection metric_history reads.
type Source int

// Sources of history.
const (
	SourceStats Source = iota
	SourceContainers
	SourceMonitors
)

// ParseMetrics checks the requested metrics and returns them with the
// sources they need. It runs before any hub read.
func ParseMetrics(raw []string) ([]Metric, []Source, error) {
	if len(raw) == 0 || len(raw) > MaxMetrics {
		return nil, nil, fmt.Errorf("want 1-%d metrics, got %d", MaxMetrics, len(raw))
	}
	var out []Metric
	need := map[Source]bool{}
	for _, r := range raw {
		name, target, _ := strings.Cut(r, ":")
		kind := MetricKind(name)
		if !slices.Contains(metricKinds, kind) {
			return nil, nil, fmt.Errorf("unknown metric %q; kinds are %s", r, joinNames(metricKinds))
		}
		switch kind {
		case MetricContainer, MetricMonitor:
			if target == "" {
				return nil, nil, fmt.Errorf("metric %q needs a target, e.g. %s:<name>", r, kind)
			}
		case MetricDisk, MetricNet, MetricTemp, MetricGPU:
		case MetricCPU, MetricIOWait, MetricMem, MetricSwap, MetricDiskIO, MetricLoad:
			if target != "" {
				return nil, nil, fmt.Errorf("metric %q takes no target", r)
			}
		}
		if slices.ContainsFunc(out, func(m Metric) bool { return m.Raw == r }) {
			return nil, nil, fmt.Errorf("metric %q given twice", r)
		}
		out = append(out, Metric{Raw: r, Kind: kind, Target: target})
		switch kind {
		case MetricContainer:
			need[SourceContainers] = true
		case MetricMonitor:
			need[SourceMonitors] = true
		case MetricCPU, MetricIOWait, MetricMem, MetricSwap, MetricDisk, MetricDiskIO, MetricNet, MetricLoad, MetricTemp, MetricGPU:
			need[SourceStats] = true
		}
	}
	return out, slices.Sorted(maps.Keys(need)), nil
}

// FindWindow returns the window named name; "" means DefaultWindow.
func FindWindow(name string) (Window, error) {
	if name == "" {
		name = DefaultWindow
	}
	i := slices.IndexFunc(Windows, func(w Window) bool { return w.Name == name })
	if i < 0 {
		return Window{}, fmt.Errorf("unknown window %q", name)
	}
	return Windows[i], nil
}

// HistoryInput is everything metric_history reads, already fetched. Rows
// are keyed by system ID and sorted oldest first.
type HistoryInput struct {
	Now            time.Time
	TokenExpiresAt time.Time
	Systems        []hub.System // the requested systems, in request order
	Window         Window
	Stats          map[string][]hub.SystemStats
	Containers     map[string][]hub.ContainerStats
	Monitors       []hub.Monitor
	MonitorStats   map[string][]hub.MonitorStats
	// Failed reads, by system ID; MonitorsErr is the monitor list itself.
	StatsErrs        map[string]error
	ContainerErrs    map[string]error
	MonitorStatsErrs map[string]error
	MonitorsErr      error
}

// HistoryOptions picks the metrics and the point count.
type HistoryOptions struct {
	Metrics []Metric
	Points  int
}

// HistoryResult is the metric_history output.
type HistoryResult struct {
	AsOf     time.Time    `json:"as_of"`
	Window   WindowRef    `json:"window"`
	Warnings []string     `json:"warnings,omitempty"`
	Hints    []string     `json:"hints,omitempty"`
	Series   []SeriesItem `json:"series"`
}

// WindowRef describes the window in seconds.
type WindowRef struct {
	SpanS       int64 `json:"span_s"`
	ResolutionS int64 `json:"resolution_s"`
}

// SeriesItem summarises one metric on one system. Peak is a true
// per-minute maximum and is set only where Beszel stores one;
// MaxBucketAvg is the highest bucket average otherwise.
type SeriesItem struct {
	System          string       `json:"system"`
	Metric          string       `json:"metric"`
	Part            string       `json:"part,omitempty"` // sent, recv, read, write, cpu or mem
	Unit            string       `json:"unit"`
	Avg             *float64     `json:"avg,omitempty"`
	MinBucketAvg    *float64     `json:"min_bucket_avg,omitempty"`
	Peak            *float64     `json:"peak,omitempty"`
	MaxBucketAvg    *float64     `json:"max_bucket_avg,omitempty"`
	HighAgoS        *int64       `json:"high_ago_s,omitempty"` // age of the bucket holding the high point
	Last            *float64     `json:"last,omitempty"`
	LastAgoS        *int64       `json:"last_ago_s,omitempty"`
	LossPct         *float64     `json:"loss_pct,omitempty"` // monitors only
	Buckets         int          `json:"buckets"`
	ExpectedBuckets int          `json:"expected_buckets"`
	CoveragePct     float64      `json:"coverage_pct"`
	Note            string       `json:"note,omitempty"`
	Points          [][2]float64 `json:"points,omitempty"` // [age_s, value], oldest first
}

// bucket is one bucket's value. hi is the bucket's true maximum when the
// line has peaks.
type bucket struct {
	at    time.Time
	v, hi float64
}

// line is one series being built: a metric part and its samples.
type line struct {
	part, unit string
	peaks      bool
	samples    []bucket
	rows       int      // buckets with data; monitors count failed probes too
	expected   int      // overrides the window's expected buckets (a monitor probing less often than the bucket)
	avg        *float64 // overrides the mean of samples (monitor latency is weighted)
	total      int64    // monitor probes, for loss
	success    int64
}

// History builds the metric_history result.
func History(in HistoryInput, o HistoryOptions) (HistoryResult, error) {
	if o.Points < 0 || o.Points > MaxPoints {
		return HistoryResult{}, fmt.Errorf("points %d out of range 0-%d", o.Points, MaxPoints)
	}
	out := HistoryResult{
		AsOf:     in.Now.UTC().Truncate(time.Second),
		Window:   WindowRef{SpanS: int64(in.Window.Span / time.Second), ResolutionS: int64(in.Window.Bucket / time.Second)},
		Warnings: TokenWarning(in.Now, in.TokenExpiresAt),
		Series:   []SeriesItem{},
	}
	var samples [][]bucket
	for _, s := range in.Systems {
		for _, m := range o.Metrics {
			lines, note, err := readLines(in, s, m)
			if err != nil {
				return HistoryResult{}, err
			}
			for _, l := range lines {
				item := summarise(in, l)
				item.System, item.Metric = s.Name, m.Raw
				if note != "" {
					item.Note = note
				}
				out.Series = append(out.Series, item)
				samples = append(samples, l.samples)
			}
		}
	}
	points := o.Points
	if n := len(out.Series); points > 0 && n*points > pointBudget {
		points = pointBudget / n
		out.Hints = append(out.Hints, fmt.Sprintf("points lowered from %d to %d so %d series stay within %d values; ask for fewer series to get more points",
			o.Points, points, n, pointBudget))
	}
	if points > 0 {
		for i := range out.Series {
			out.Series[i].Points = downsample(in.Now, samples[i], points)
		}
	}
	return out, nil
}

// skeleton returns the empty lines a metric produces. Directions are split
// because Beszel keeps a true peak per direction but none for the sum.
func skeleton(m Metric) []line {
	switch m.Kind {
	case MetricCPU, MetricMem:
		return []line{{unit: "pct", peaks: true}}
	case MetricIOWait, MetricSwap, MetricDisk, MetricGPU:
		return []line{{unit: "pct"}}
	case MetricDiskIO:
		return []line{{part: "read", unit: "bytes_per_s", peaks: true}, {part: "write", unit: "bytes_per_s", peaks: true}}
	case MetricNet:
		peaks := m.Target == "" // per-interface rows store no maxima
		return []line{{part: "sent", unit: "bytes_per_s", peaks: peaks}, {part: "recv", unit: "bytes_per_s", peaks: peaks}}
	case MetricLoad:
		return []line{{unit: "load"}}
	case MetricTemp:
		return []line{{unit: "celsius"}}
	case MetricContainer:
		return []line{{part: "cpu", unit: "pct"}, {part: "mem", unit: "bytes"}}
	case MetricMonitor:
		return []line{{unit: "ms", peaks: true}}
	}
	panic("skeleton: unhandled metric kind " + string(m.Kind)) // ParseMetrics admits only known kinds
}

// readLines fills a metric's lines for one system. note explains an empty
// series; err is an unknown target, which lists the valid ones.
func readLines(in HistoryInput, s hub.System, m Metric) (lines []line, note string, err error) {
	lines = skeleton(m)
	failed := func(err error) ([]line, string, error) {
		return lines, "could not read history: " + err.Error(), nil
	}
	switch m.Kind {
	case MetricContainer:
		if err := in.ContainerErrs[s.ID]; err != nil {
			return failed(err)
		}
		return containerLines(in.Containers[s.ID], s, m, lines)
	case MetricMonitor:
		if err := cmp.Or(in.MonitorsErr, in.MonitorStatsErrs[s.ID]); err != nil {
			return failed(err)
		}
		return monitorLines(in, s, m, lines)
	case MetricCPU, MetricIOWait, MetricMem, MetricSwap, MetricDisk, MetricDiskIO, MetricNet, MetricLoad, MetricTemp, MetricGPU:
		if err := in.StatsErrs[s.ID]; err != nil {
			return failed(err)
		}
		return statsLines(in.Stats[s.ID], s, m, lines)
	}
	panic("readLines: unhandled metric kind " + string(m.Kind)) // ParseMetrics admits only known kinds
}

// targetCheck validates m.Target against the names seen in the window. With
// no rows there is nothing to check against, so the series is just empty.
func targetCheck(s hub.System, m Metric, what string, rows int, valid map[string]bool) (note string, err error) {
	switch {
	case m.Target == "" || valid[m.Target] || rows == 0:
		return "", nil
	case len(valid) == 0:
		return "no " + what + " reported in this window", nil
	default:
		return "", fmt.Errorf("%s has no %s %q in this window; valid: %s", s.Name, what, m.Target,
			strings.Join(slices.Sorted(maps.Keys(valid)), ", "))
	}
}

// targetNoun names what a metric's target is, for messages; "" for kinds
// that take none.
func targetNoun(k MetricKind) string {
	switch k {
	case MetricDisk:
		return "filesystem"
	case MetricNet:
		return "interface"
	case MetricTemp:
		return "temperature sensor"
	case MetricGPU:
		return "GPU"
	case MetricCPU, MetricIOWait, MetricMem, MetricSwap, MetricDiskIO, MetricLoad, MetricContainer, MetricMonitor:
	}
	return ""
}

func statsLines(rows []hub.SystemStats, s hub.System, m Metric, lines []line) ([]line, string, error) {
	what := targetNoun(m.Kind)
	valid := map[string]bool{}
	for _, r := range rows {
		x := r.Stats
		switch m.Kind {
		case MetricDisk:
			for k := range x.ExtraFs {
				valid[k] = true
			}
		case MetricNet:
			for k := range x.Interfaces {
				valid[k] = true
			}
		case MetricTemp:
			for k := range x.Temps {
				valid[k] = true
			}
		case MetricGPU:
			for k := range x.GPU {
				valid[k] = true
			}
		case MetricCPU, MetricIOWait, MetricMem, MetricSwap, MetricDiskIO, MetricLoad, MetricContainer, MetricMonitor: // no named targets
		}
	}
	if note, err := targetCheck(s, m, what, len(rows), valid); note != "" || err != nil {
		return lines, note, err
	}
	if (m.Kind == MetricTemp || m.Kind == MetricGPU) && len(rows) > 0 && len(valid) == 0 {
		return lines, "no " + what + "s reported in this window", nil
	}
	for _, r := range rows {
		for i := range lines {
			if v, hi, ok := statValue(r.Stats, m, i); ok {
				lines[i].samples = append(lines[i].samples, bucket{at: r.Created.Time, v: v, hi: hi})
			}
		}
	}
	for i := range lines {
		lines[i].rows = len(lines[i].samples)
	}
	if m.Kind == MetricSwap && len(rows) > 0 && lines[0].rows == 0 {
		return lines, "no swap on this server", nil
	}
	return lines, "", nil
}

// statValue reads part i of metric m from one stats row. hi is the row's
// true maximum: rollups store it, and a 1m row is its own maximum.
func statValue(x hub.Stats, m Metric, i int) (v, hi float64, ok bool) {
	switch m.Kind {
	case MetricCPU:
		return x.CPU, max(x.MaxCPU, x.CPU), true
	case MetricIOWait:
		if len(x.CPUBreakdown) > 2 {
			return x.CPUBreakdown[2], 0, true
		}
	case MetricMem:
		if x.Mem[0] == 0 {
			return x.MemPct, x.MemPct, true // agents before 0.21.1 send no bytes
		}
		return x.MemPct, max(float64(x.MaxMemUsed)/float64(x.Mem[0])*100, x.MemPct), true
	case MetricSwap:
		if x.Swap[0] > 0 {
			return float64(x.Swap[1]) / float64(x.Swap[0]) * 100, 0, true
		}
	case MetricDisk:
		if m.Target == "" {
			return x.DiskPct, 0, true
		}
		if fs, found := x.ExtraFs[m.Target]; found && fs.TotalGB > 0 {
			return fs.UsedGB / fs.TotalGB * 100, 0, true
		}
	case MetricDiskIO:
		return float64(x.DiskIO[i]), float64(max(x.MaxDiskIO[i], x.DiskIO[i])), true
	case MetricNet:
		if m.Target == "" {
			return float64(x.Bandwidth[i]), float64(max(x.MaxBandwidth[i], x.Bandwidth[i])), true
		}
		if ni, found := x.Interfaces[m.Target]; found {
			return float64(ni[i]), 0, true
		}
	case MetricLoad:
		return x.LoadAvg[0], 0, true
	case MetricTemp:
		if m.Target != "" {
			t, found := x.Temps[m.Target]
			return t, 0, found
		}
		for _, t := range x.Temps { // from the first sensor: all may be below 0
			if !ok || t > v {
				v = t
			}
			ok = true
		}
		return v, 0, ok
	case MetricGPU:
		if m.Target != "" {
			g, found := x.GPU[m.Target]
			return g.Usage, 0, found
		}
		for _, g := range x.GPU {
			if !ok || g.Usage > v {
				v = g.Usage
			}
			ok = true
		}
		return v, 0, ok
	case MetricContainer, MetricMonitor: // not in stats rows
	}
	return 0, 0, false
}

func containerLines(rows []hub.ContainerStats, s hub.System, m Metric, lines []line) ([]line, string, error) {
	valid := map[string]bool{}
	for _, r := range rows {
		for _, c := range r.Stats {
			valid[c.Name] = true
		}
	}
	if note, err := targetCheck(s, m, "container", len(rows), valid); note != "" || err != nil {
		return lines, note, err
	}
	for _, r := range rows {
		i := slices.IndexFunc(r.Stats, func(c hub.ContainerStat) bool { return c.Name == m.Target })
		if i < 0 {
			continue
		}
		c := r.Stats[i]
		lines[0].samples = append(lines[0].samples, bucket{at: r.Created.Time, v: c.CPU})
		lines[1].samples = append(lines[1].samples, bucket{at: r.Created.Time, v: c.MemMiB * mib})
	}
	lines[0].rows, lines[1].rows = len(lines[0].samples), len(lines[1].samples)
	if n := lines[0].rows; n > 0 && n < len(rows) {
		return lines, "the container was missing from some buckets; Beszel counts its missing minutes as 0, so buckets around a start or stop read low", nil
	}
	return lines, "", nil
}

func monitorLines(in HistoryInput, s hub.System, m Metric, lines []line) ([]line, string, error) {
	valid, ids := map[string]bool{}, map[string]bool{}
	l := &lines[0]
	for _, mon := range in.Monitors {
		if mon.System == s.ID {
			valid[mon.Target] = true
			if mon.Target == m.Target {
				ids[mon.ID] = true
				// Rows exist only for minutes with a probe.
				if every := time.Duration(mon.Interval) * time.Second; every > in.Window.Bucket {
					l.expected = max(1, int(in.Window.Span/every))
				}
			}
		}
	}
	if len(valid) == 0 {
		return lines, "no network monitors configured for this server", nil
	}
	if !valid[m.Target] {
		return nil, "", fmt.Errorf("%s has no network monitor %q; valid: %s", s.Name, m.Target, strings.Join(slices.Sorted(maps.Keys(valid)), ", "))
	}
	var resSum float64
	for _, r := range in.MonitorStats[s.ID] {
		if !ids[r.Monitor] || r.Total <= 0 {
			continue
		}
		l.rows++
		l.total += r.Total
		l.success += r.Success
		if r.Success > 0 {
			resSum += r.ResSum
			l.samples = append(l.samples, bucket{at: time.UnixMilli(r.Created), v: r.ResSum / float64(r.Success) / 1000, hi: r.ResMax / 1000})
		}
	}
	if l.success > 0 {
		l.avg = new(resSum / float64(l.success) / 1000)
	}
	if l.rows > 0 && l.success == 0 {
		return lines, "every probe in this window failed", nil
	}
	return lines, "", nil
}

func summarise(in HistoryInput, l line) SeriesItem {
	item := SeriesItem{Part: l.part, Unit: l.unit, Buckets: l.rows, ExpectedBuckets: cmp.Or(l.expected, in.Window.Expected)}
	item.CoveragePct = roundTo(min(100, float64(l.rows)/float64(item.ExpectedBuckets)*100), 0)
	if l.total > 0 {
		item.LossPct = round((1-float64(l.success)/float64(l.total))*100, 2)
	}
	if len(l.samples) == 0 {
		if l.rows == 0 {
			item.Note = "no samples in this window"
		}
		return item
	}
	lo, high, sum := l.samples[0], l.samples[0], 0.0
	for _, x := range l.samples {
		sum += x.v
		lo = minBy(lo, x, func(s bucket) float64 { return s.v })
		if l.peaks {
			high = maxBy(high, x, func(s bucket) float64 { return s.hi })
		} else {
			high = maxBy(high, x, func(s bucket) float64 { return s.v })
		}
	}
	avg := sum / float64(len(l.samples))
	if l.avg != nil {
		avg = *l.avg
	}
	item.Avg, item.MinBucketAvg = round(avg, 2), round(lo.v, 2)
	if l.peaks {
		item.Peak = round(high.hi, 2)
	} else {
		item.MaxBucketAvg = round(high.v, 2)
	}
	item.HighAgoS = new(ageS(in.Now, high.at))
	last := l.samples[len(l.samples)-1]
	item.Last, item.LastAgoS = round(last.v, 2), new(ageS(in.Now, last.at))
	return item
}

// minBy and maxBy keep the earlier bucket on ties.
func minBy(a, b bucket, f func(bucket) float64) bucket {
	if f(b) < f(a) {
		return b
	}
	return a
}

func maxBy(a, b bucket, f func(bucket) float64) bucket {
	if f(b) > f(a) {
		return b
	}
	return a
}

// downsample returns at most n points, averaging consecutive buckets.
func downsample(now time.Time, samples []bucket, n int) [][2]float64 {
	n = min(n, len(samples))
	out := make([][2]float64, 0, n)
	for i := range n {
		chunk := samples[i*len(samples)/n : (i+1)*len(samples)/n]
		sum := 0.0
		for _, x := range chunk {
			sum += x.v
		}
		out = append(out, [2]float64{float64(ageS(now, chunk[len(chunk)-1].at)), roundTo(sum/float64(len(chunk)), 2)})
	}
	return out
}
