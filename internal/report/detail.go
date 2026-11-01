package report

import (
	"cmp"
	"fmt"
	"slices"
	"time"

	"github.com/agrmohit/beszelscope/internal/hub"
)

// Section is a part of system_detail's answer.
type Section string

// Sections of system_detail, in the order of Beszel's system page tabs.
const (
	SectionSummary    Section = "summary"
	SectionCore       Section = "core"
	SectionNetwork    Section = "network"
	SectionDisk       Section = "disk"
	SectionGPU        Section = "gpu"
	SectionContainers Section = "containers"
	SectionServices   Section = "services"
	SectionUpdates    Section = "updates"
)

// Sections lists every section system_detail accepts.
var Sections = []Section{SectionSummary, SectionCore, SectionNetwork, SectionDisk, SectionGPU, SectionContainers, SectionServices, SectionUpdates}

// DetailInput is everything system_detail reads for one system, already
// fetched. FleetInput carries the problem sources the summary needs.
type DetailInput struct {
	FleetInput
	System   hub.System
	Details  *hub.SystemDetails  // nil when the hub has none
	Latest   *hub.SystemStats    // newest stats sample; nil when there is none
	Packages *hub.PackageUpdates // live from the agent; nil when not read
	// StatsErr, ContainersErr, ServicesErr and PackagesErr say why a read
	// failed; the sections that need it report it.
	StatsErr      error
	ContainersErr error
	ServicesErr   error
	PackagesErr   error
}

// DetailOptions picks what system_detail returns.
type DetailOptions struct {
	Sections []Section // empty means summary only
	// The list knobs shape containers and services only.
	Filter       string // case-insensitive substring of a name or container image; "" keeps all
	ProblemsOnly bool
	Sort         SortKey // SortCPU when ""
	Limit        int     // rows per list beyond problem rows; 0 means DefaultLimit
}

// DetailResult is the system_detail output. Only requested sections are set.
type DetailResult struct {
	AsOf       time.Time       `json:"as_of"`
	System     SystemRef       `json:"system"`
	Warnings   []string        `json:"warnings,omitempty"`
	Hints      []string        `json:"hints,omitempty"` // which list knob to change when rows were cut or nothing matched
	Summary    *Summary        `json:"summary,omitempty"`
	Core       *Core           `json:"core,omitempty"`
	Network    *Network        `json:"network,omitempty"`
	Disk       *Disk           `json:"disk,omitempty"`
	GPU        *GPUs           `json:"gpu,omitempty"`
	Containers *Containers     `json:"containers,omitempty"`
	Services   *Services       `json:"services,omitempty"`
	Updates    *UpdatesSection `json:"updates,omitempty"`
}

// SystemRef names the system and how fresh its data is.
type SystemRef struct {
	Name         string     `json:"name"`
	Status       hub.Status `json:"status"`
	AgeS         *int64     `json:"age_s,omitempty"`
	LastSeenAgoS *int64     `json:"last_seen_ago_s,omitempty"`
}

// Summary is Beszel's info bar plus the fleet row's numbers and problems.
// Live numbers are present only while the system is up.
type Summary struct {
	OS            string         `json:"os,omitempty"`
	Kernel        string         `json:"kernel,omitempty"`
	Hostname      string         `json:"hostname,omitempty"`
	Arch          string         `json:"arch,omitempty"`
	CPUModel      string         `json:"cpu_model,omitempty"`
	Cores         int            `json:"cores,omitempty"`
	Threads       int            `json:"threads,omitempty"`
	MemTotalBytes uint64         `json:"mem_total_bytes,omitempty"`
	Agent         string         `json:"agent,omitempty"`
	UptimeS       *uint64        `json:"uptime_s,omitempty"`
	CPUPct        *float64       `json:"cpu_pct,omitempty"`
	MemPct        *float64       `json:"mem_pct,omitempty"`
	DiskPct       *float64       `json:"disk_pct,omitempty"`
	DiskWorst     string         `json:"disk_worst,omitempty"`
	TempHottestC  *float64       `json:"temp_hottest_c,omitempty"`
	LoadAvg       []float64      `json:"load_avg,omitempty"` // 1, 5 and 15 minutes
	LoadPerThread *float64       `json:"load_per_thread,omitempty"`
	NetBytesPerS  *uint64        `json:"net_bytes_per_s,omitempty"`
	GPUPct        *float64       `json:"gpu_pct,omitempty"`
	Battery       *Battery       `json:"battery,omitempty"`
	Services      *ServiceCounts `json:"services,omitempty"`
	Updates       *Updates       `json:"updates,omitempty"`
	Problems      []Problem      `json:"problems,omitempty"`
}

// ServiceCounts counts systemd units.
type ServiceCounts struct {
	Total  int `json:"total"`
	Failed int `json:"failed"`
}

// Battery is the host's main battery.
type Battery struct {
	Pct   int    `json:"pct"`
	State string `json:"state"` // unknown, empty, full, charging, discharging or idle
}

// Core is Beszel's Core tab: CPU, load, memory, swap and sensors.
type Core struct {
	Unavailable     string             `json:"unavailable,omitempty"`
	SampledAgoS     *int64             `json:"sampled_ago_s,omitempty"`
	CPUPct          *float64           `json:"cpu_pct,omitempty"`
	CPUBreakdownPct map[string]float64 `json:"cpu_breakdown_pct,omitempty"`
	CPUCoresPct     []uint16           `json:"cpu_cores_pct,omitempty"`
	LoadAvg         []float64          `json:"load_avg,omitempty"`
	Mem             *Memory            `json:"mem,omitempty"`
	Swap            *Swap              `json:"swap,omitempty"`
	TempsC          map[string]float64 `json:"temps_c,omitempty"`
	FansRPM         map[string]uint16  `json:"fans_rpm,omitempty"`
	Battery         *Battery           `json:"battery,omitempty"`
	Absent          []string           `json:"absent,omitempty"` // "part: why it is missing"
}

// Memory is RAM use in bytes.
type Memory struct {
	TotalBytes  uint64   `json:"total_bytes"`
	UsedBytes   uint64   `json:"used_bytes"`
	CacheBytes  uint64   `json:"cache_bytes"`
	ZFSArcBytes uint64   `json:"zfs_arc_bytes,omitempty"`
	Pct         *float64 `json:"pct,omitempty"`
}

// Swap is swap use in bytes.
type Swap struct {
	TotalBytes uint64 `json:"total_bytes"`
	UsedBytes  uint64 `json:"used_bytes"`
}

// Detail builds the system_detail result for in.System.
func Detail(in DetailInput, o DetailOptions) (DetailResult, error) {
	o.Sort = cmp.Or(o.Sort, SortCPU)
	if !slices.Contains(Sorts, o.Sort) {
		return DetailResult{}, fmt.Errorf("unknown sort %q; want one of %s", o.Sort, joinNames(Sorts))
	}
	if o.Limit < 0 || o.Limit > MaxLimit {
		return DetailResult{}, fmt.Errorf("limit %d out of range 1-%d", o.Limit, MaxLimit)
	}
	s := in.System
	row := buildRow(in.FleetInput, s)
	out := DetailResult{
		AsOf:     in.Now.UTC().Truncate(time.Second),
		System:   SystemRef{Name: s.Name, Status: s.Status, AgeS: row.AgeS, LastSeenAgoS: row.LastSeenAgoS},
		Warnings: TokenWarning(in.Now, in.TokenExpiresAt),
	}
	if w := versionSkew(s, in.HubVersion); w != "" {
		out.Warnings = append(out.Warnings, w)
	}

	sections := o.Sections
	if len(sections) == 0 {
		sections = []Section{SectionSummary}
	}
	for _, sec := range sections {
		switch sec {
		case SectionSummary:
			out.Summary = summary(in, row)
		case SectionCore:
			out.Core = core(in)
		case SectionNetwork:
			out.Network = network(in)
		case SectionDisk:
			out.Disk = disk(in)
		case SectionGPU:
			out.GPU = gpus(in)
		case SectionContainers:
			var hint string
			out.Containers, hint = containers(in, o)
			out.Hints = appendNonEmpty(out.Hints, hint)
		case SectionServices:
			var hint string
			out.Services, hint = services(in, o)
			out.Hints = appendNonEmpty(out.Hints, hint)
		case SectionUpdates:
			out.Updates = updates(in)
		default:
			return DetailResult{}, fmt.Errorf("unknown section %q; want one of %s", sec, joinNames(Sections))
		}
	}
	return out, nil
}

func summary(in DetailInput, row Row) *Summary {
	out := &Summary{Problems: row.Problems}
	if d := in.Details; d != nil {
		out.OS, out.Kernel, out.Hostname, out.Arch = osName(*d), d.Kernel, d.Hostname, d.Arch
		out.CPUModel, out.Cores, out.Threads, out.MemTotalBytes = d.CPU, d.Cores, d.Threads, d.Memory
	}
	if in.System.Status != hub.StatusUp {
		return out
	}
	i := in.System.Info
	out.Agent, out.UptimeS = row.Agent, row.UptimeS
	out.CPUPct, out.MemPct, out.DiskPct, out.DiskWorst = row.CPUPct, row.MemPct, row.DiskPct, row.DiskWorst
	out.TempHottestC, out.LoadPerThread, out.NetBytesPerS = row.TempHottestC, row.LoadPerThread, row.NetBytesPerS
	out.LoadAvg = loadAvg(i.LoadAvg)
	if i.GPUPct > 0 {
		out.GPUPct = round(i.GPUPct, 1)
	}
	out.Battery = battery(i.Battery)
	if len(i.Services) >= 2 {
		out.Services = &ServiceCounts{Total: int(i.Services[0]), Failed: int(i.Services[1])}
	}
	out.Updates = row.Updates
	return out
}

// core reads the newest stats sample. Every sensor is returned: names such
// as "PMU tdie3" do not say what they measure, so a name filter misleads.
func core(in DetailInput) *Core {
	if u := in.unavailable(in.System); u != "" {
		return &Core{Unavailable: u}
	}
	if u := statsUnavailable(in); u != "" {
		return &Core{Unavailable: u}
	}
	x := in.Latest.Stats
	c := &Core{
		SampledAgoS: new(ageS(in.Now, in.Latest.Created.Time)),
		CPUPct:      round(x.CPU, 1),
		CPUCoresPct: x.CPUCores,
		LoadAvg:     loadAvg(x.LoadAvg),
		Swap:        &Swap{TotalBytes: x.Swap[0], UsedBytes: x.Swap[1]},
		Battery:     battery(x.Battery),
	}
	if len(x.CPUBreakdown) >= len(cpuParts) {
		c.CPUBreakdownPct = map[string]float64{}
		for i, part := range cpuParts {
			if v := round(x.CPUBreakdown[i], 2); v != nil {
				c.CPUBreakdownPct[part] = *v
			}
		}
	}
	if x.Mem[0] > 0 {
		c.Mem = &Memory{
			TotalBytes: x.Mem[0], UsedBytes: x.Mem[1], CacheBytes: x.Mem[2], ZFSArcBytes: x.Mem[3],
			Pct: round(x.MemPct, 1),
		}
	} else {
		c.Absent = append(c.Absent, "memory: agent older than 0.21.1 reports no byte counts")
	}

	for name, v := range x.Temps {
		if r := round(v, 1); r != nil {
			if c.TempsC == nil {
				c.TempsC = map[string]float64{}
			}
			c.TempsC[name] = *r
		}
	}
	c.FansRPM = x.Fans
	for part, present := range map[string]bool{"temperatures": len(x.Temps) > 0, "fans": len(x.Fans) > 0, "battery": c.Battery != nil} {
		if !present {
			c.Absent = append(c.Absent, part+": not reported by this host")
		}
	}
	slices.Sort(c.Absent)
	return c
}

// cpuParts names Beszel's CPU breakdown, in its order.
var cpuParts = []string{"user", "system", "iowait", "steal", "idle", "irq", "softirq", "nice"}

// statsUnavailable explains why there is no stats sample, or is "".
func statsUnavailable(in DetailInput) string {
	switch {
	case in.StatsErr != nil:
		return "could not read stats: " + in.StatsErr.Error()
	case in.Latest == nil:
		return "the hub has no stats sample from the last 5 minutes"
	default:
		return ""
	}
}

// unavailable explains why live data is missing, or is "" for an up system.
func (in FleetInput) unavailable(s hub.System) string {
	if s.Status == hub.StatusUp {
		return ""
	}
	seen, none := in.lastSeen(s)
	if none {
		return fmt.Sprintf("system is %s, no data in the last 30 days", s.Status)
	}
	return fmt.Sprintf("system is %s, last seen %s ago", s.Status, short(in.Now.Sub(seen)))
}

func versionSkew(s hub.System, hubVersion string) string {
	v := s.Info.AgentVersion
	if s.Status != hub.StatusUp || hubVersion == "" || v == "" || v == hubVersion {
		return ""
	}
	return fmt.Sprintf("%s runs agent %s; hub is %s", s.Name, v, hubVersion)
}

func osName(d hub.SystemDetails) string {
	if d.OSName != "" {
		return d.OSName
	}
	switch d.OS {
	case hub.OSLinux:
		return "Linux"
	case hub.OSDarwin:
		return "macOS"
	case hub.OSWindows:
		return "Windows"
	case hub.OSFreeBSD:
		return "FreeBSD"
	default:
		return fmt.Sprintf("unknown (os %d)", d.OS)
	}
}

var batteryStates = []string{"unknown", "empty", "full", "charging", "discharging", "idle"}

// battery is nil when the host reports none.
func battery(b [2]uint8) *Battery {
	if b == [2]uint8{} {
		return nil
	}
	state := "unknown"
	if int(b[1]) < len(batteryStates) {
		state = batteryStates[b[1]]
	}
	return &Battery{Pct: int(b[0]), State: state}
}

func loadAvg(la [3]float64) []float64 {
	if la == [3]float64{} {
		return nil
	}
	out := make([]float64, 0, len(la))
	for _, v := range la {
		out = append(out, roundTo(v, 2))
	}
	return out
}

func deref(p *float64) float64 {
	if p == nil {
		return 0
	}
	return *p
}

func appendNonEmpty(s []string, v string) []string {
	if v == "" {
		return s
	}
	return append(s, v)
}
