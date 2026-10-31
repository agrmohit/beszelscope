package report

import (
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/agrmohit/beszelscope/internal/hub"
)

const (
	staleAfter          = 180 * time.Second // three missed 60 s polls
	containerStaleAfter = 3 * time.Minute
	certWarnBefore      = 14 * 24 * time.Hour
	tokenWarnBefore     = 30 * 24 * time.Hour
	maxNames            = 5 // names listed per grouped problem
)

// Problem codes, in the order problems are reported.
const (
	CodeStatusDown         = "status_down"
	CodeStatusPaused       = "status_paused"
	CodeStatusPending      = "status_pending"
	CodeStale              = "stale"
	CodeServiceFailed      = "service_failed"
	CodeContainerUnhealthy = "container_unhealthy"
	CodeContainerGone      = "container_gone"
	CodeSmartFailed        = "smart_failed"
	CodeSmartWarning       = "smart_warning"
	CodePoolDegraded       = "pool_degraded"
	CodeMonitorFailing     = "monitor_failing"
	CodeCertExpired        = "cert_expired"
	CodeCertExpiring       = "cert_expiring"
	CodeSecurityUpdates    = "security_updates"
)

var codeOrder = []string{
	CodeStatusDown, CodeStatusPaused, CodeStatusPending, CodeStale, CodeServiceFailed,
	CodeContainerUnhealthy, CodeContainerGone, CodeSmartFailed, CodeSmartWarning, CodePoolDegraded,
	CodeMonitorFailing, CodeCertExpired, CodeCertExpiring, CodeSecurityUpdates,
}

// codeSection names the system_detail section that explains each code.
var codeSection = map[string]string{
	CodeStatusDown: "summary", CodeStatusPaused: "summary", CodeStatusPending: "summary", CodeStale: "summary",
	CodeServiceFailed: "services", CodeContainerUnhealthy: "containers", CodeContainerGone: "containers",
	CodeSmartFailed: "disk", CodeSmartWarning: "disk", CodePoolDegraded: "disk",
	CodeMonitorFailing: "network", CodeCertExpired: "network", CodeCertExpiring: "network",
	CodeSecurityUpdates: "updates",
}

// FleetInput is everything fleet_status reads, already fetched.
type FleetInput struct {
	Now              time.Time
	HubVersion       string
	TokenExpiresAt   time.Time
	Systems          []hub.System
	Services         []hub.Service
	Containers       []hub.Container
	RecentContainers []hub.ContainerStats // 10m rows from the last hour
	Smart            []hub.SmartDevice
	Pools            []hub.ZfsPool
	Monitors         []hub.Monitor
	// LastSeen is when each system that is not up last sent data: its
	// newest stats row. Zero means none in Beszel's 30-day retention; a
	// missing entry means unknown, and systems.updated stands in.
	LastSeen map[string]time.Time
}

// FleetOptions narrows the result.
type FleetOptions struct {
	Systems      []string // names or IDs; empty means all
	ProblemsOnly bool     // drop rows without problems; counts still cover all
}

// FleetResult is the fleet_status output.
type FleetResult struct {
	AsOf     time.Time `json:"as_of"`
	Counts   Counts    `json:"counts"`
	Warnings []string  `json:"warnings,omitempty"`
	Systems  []Row     `json:"systems"`
}

// Counts tallies systems by status.
type Counts struct {
	Total        int `json:"total"`
	Up           int `json:"up"`
	Down         int `json:"down"`
	Paused       int `json:"paused"`
	Pending      int `json:"pending"`
	WithProblems int `json:"with_problems"`
}

// Row is one system. Live metrics are present only while the system is up.
type Row struct {
	Name          string    `json:"name"`
	Status        string    `json:"status"`
	AgeS          *int64    `json:"age_s,omitempty"`
	LastSeenAgoS  *int64    `json:"last_seen_ago_s,omitempty"`
	CPUPct        *float64  `json:"cpu_pct,omitempty"`
	MemPct        *float64  `json:"mem_pct,omitempty"`
	DiskPct       *float64  `json:"disk_pct,omitempty"`
	DiskWorst     string    `json:"disk_worst,omitempty"`
	TempHottestC  *float64  `json:"temp_hottest_c,omitempty"`
	LoadPerThread *float64  `json:"load_per_thread,omitempty"`
	NetBytesPerS  *uint64   `json:"net_bytes_per_s,omitempty"`
	UptimeS       *uint64   `json:"uptime_s,omitempty"`
	Agent         string    `json:"agent,omitempty"`
	Updates       *Updates  `json:"updates,omitempty"`
	Problems      []Problem `json:"problems,omitempty"`
}

// Updates counts pending package updates; Security is null when unknown.
type Updates struct {
	Total    int  `json:"total"`
	Security *int `json:"security"`
}

// Problem is one kind of fault on a system.
type Problem struct {
	Code    string `json:"code"`
	Detail  string `json:"detail"`
	Section string `json:"section"`
	Count   int    `json:"count,omitempty"`
}

// Fleet builds the fleet_status result.
func Fleet(in FleetInput, o FleetOptions) (FleetResult, error) {
	systems, err := selectSystems(in.Systems, o.Systems)
	if err != nil {
		return FleetResult{}, err
	}
	out := FleetResult{AsOf: in.Now.UTC().Truncate(time.Second), Warnings: TokenWarning(in.Now, in.TokenExpiresAt)}
	for _, s := range systems {
		row := buildRow(in, s)
		out.Counts.Total++
		switch s.Status {
		case "up":
			out.Counts.Up++
		case "down":
			out.Counts.Down++
		case "paused":
			out.Counts.Paused++
		case "pending":
			out.Counts.Pending++
		}
		if len(row.Problems) > 0 {
			out.Counts.WithProblems++
		}
		out.Warnings = appendNonEmpty(out.Warnings, versionSkew(s, in.HubVersion))
		if o.ProblemsOnly && len(row.Problems) == 0 {
			continue
		}
		out.Systems = append(out.Systems, row)
	}
	if out.Systems == nil {
		out.Systems = []Row{}
	}
	return out, nil
}

func selectSystems(all []hub.System, refs []string) ([]hub.System, error) {
	if len(refs) == 0 {
		return all, nil
	}
	var out []hub.System
	seen := map[string]bool{}
	for _, ref := range refs {
		s, err := Resolve(all, ref)
		if err != nil {
			return nil, err
		}
		if !seen[s.ID] {
			seen[s.ID] = true
			out = append(out, s)
		}
	}
	return out, nil
}

// TokenWarning returns the warning for a token that has expired or expires
// within 30 days, or nil. A zero expires means unknown.
func TokenWarning(now, expires time.Time) []string {
	if expires.IsZero() {
		return nil
	}
	left := expires.Sub(now)
	switch {
	case left <= 0:
		return []string{"hub token expired " + short(-left) + " ago; a human must renew it (see README)"}
	case left < tokenWarnBefore:
		return []string{"hub token expires in " + short(left) + "; renew it (see README)"}
	default:
		return nil
	}
}

func buildRow(in FleetInput, s hub.System) Row {
	row := Row{Name: s.Name, Status: s.Status}
	if s.Status != "up" {
		seen, none := in.lastSeen(s)
		if !none {
			row.LastSeenAgoS = new(ageS(in.Now, seen))
		}
		row.Problems = statusProblem(s, in.Now, seen, none)
		return row
	}
	age := ageS(in.Now, s.Updated.Time)

	i := s.Info
	row.AgeS = &age
	row.CPUPct = round(i.CPU, 1)
	row.MemPct = round(i.MemPct, 1)
	disk, worst := worstDisk(i)
	row.DiskPct, row.DiskWorst = round(disk, 1), worst
	if i.DashTemp > 0 {
		row.TempHottestC = round(i.DashTemp, 1)
	}
	if i.Threads > 0 {
		row.LoadPerThread = round(i.LoadAvg[0]/float64(i.Threads), 2)
	}
	row.NetBytesPerS = new(i.BandwidthBytes)
	row.UptimeS = new(i.Uptime)
	row.Agent = i.AgentVersion
	if len(i.PackageUpdates) > 0 {
		row.Updates = &Updates{Total: int(i.PackageUpdates[0])}
		if len(i.PackageUpdates) > 1 {
			row.Updates.Security = new(int(i.PackageUpdates[1]))
		}
	}

	var p []Problem
	if d := time.Duration(age) * time.Second; d > staleAfter {
		p = append(p, problem(CodeStale, "last poll "+short(d)+" ago", 0))
	}
	p = append(p, structural(in, s)...)
	row.Problems = sortProblems(p)
	return row
}

// worstDisk is the fullest of the root disk and the extra filesystems.
func worstDisk(i hub.SystemInfo) (pct float64, name string) {
	pct = i.DiskPct
	for _, fs := range slices.Sorted(maps.Keys(i.ExtraFsPct)) {
		if v := i.ExtraFsPct[fs]; v > pct {
			pct, name = v, fs
		}
	}
	return pct, name
}

// lastSeen is when s last sent data; none reports no data in 30 days.
// systems.updated is the fallback: Beszel bumps it on record rewrites, so it
// can make a long-silent system look recently down.
func (in FleetInput) lastSeen(s hub.System) (seen time.Time, none bool) {
	t, ok := in.LastSeen[s.ID]
	if !ok {
		return s.Updated.Time, false
	}
	return t, t.IsZero()
}

func statusProblem(s hub.System, now, seen time.Time, none bool) []Problem {
	switch s.Status {
	case "down":
		if none {
			return []Problem{problem(CodeStatusDown, "down, no data in the last 30 days", 0)}
		}
		return []Problem{problem(CodeStatusDown, "down for "+short(now.Sub(seen)), 0)}
	case "paused":
		return []Problem{problem(CodeStatusPaused, "monitoring paused", 0)}
	case "pending":
		return []Problem{problem(CodeStatusPending, "agent has not connected", 0)}
	default:
		return []Problem{problem(CodeStatusDown, "unknown status "+strconv.Quote(s.Status), 0)}
	}
}

// structural finds the faults Beszel records without alert rules.
func structural(in FleetInput, s hub.System) []Problem {
	var failed, unhealthy, gone, smartFailed, smartWarn, pools, monitors, expired, expiring []string

	for _, svc := range in.Services {
		if svc.System == s.ID && svc.State == 2 {
			failed = append(failed, svc.Name)
		}
	}

	for _, c := range in.Containers {
		if c.System == s.ID && c.Health == 3 && !containerStale(in.Now, c) {
			unhealthy = append(unhealthy, c.Name)
		}
	}
	gone = goneContainers(in, s.ID)

	for _, d := range in.Smart {
		if d.System != s.ID {
			continue
		}
		switch smartProblem(d) {
		case CodeSmartFailed:
			smartFailed = append(smartFailed, deviceName(d))
		case CodeSmartWarning:
			smartWarn = append(smartWarn, deviceName(d))
		}
	}

	for _, pl := range in.Pools {
		if pl.System == s.ID && poolProblem(pl) != "" {
			pools = append(pools, pl.Name+" "+pl.Health)
		}
	}

	for _, m := range in.Monitors {
		if m.System != s.ID {
			continue
		}
		if monitorFailing(m) {
			monitors = append(monitors, fmt.Sprintf("%s %.0f%% loss", m.Target, m.Loss1h))
		}
		switch code, left := certProblem(m, in.Now); code {
		case CodeCertExpired:
			expired = append(expired, m.Target+" "+short(-left)+" ago")
		case CodeCertExpiring:
			expiring = append(expiring, m.Target+" in "+short(left))
		}
	}

	var p []Problem
	add := func(code string, items []string) {
		if len(items) > 0 {
			p = append(p, problem(code, list(items), len(items)))
		}
	}
	add(CodeServiceFailed, failed)
	add(CodeContainerUnhealthy, unhealthy)
	add(CodeContainerGone, gone)
	add(CodeSmartFailed, smartFailed)
	add(CodeSmartWarning, smartWarn)
	add(CodePoolDegraded, pools)
	add(CodeMonitorFailing, monitors)
	add(CodeCertExpired, expired)
	add(CodeCertExpiring, expiring)
	if pu := s.Info.PackageUpdates; len(pu) > 1 && pu[1] > 0 {
		p = append(p, problem(CodeSecurityUpdates, strconv.Itoa(int(pu[1]))+" security updates pending", int(pu[1])))
	}
	return p
}

// smartProblem is the problem code for a drive's SMART state, or "".
func smartProblem(d hub.SmartDevice) string {
	switch d.State {
	case "FAILED":
		return CodeSmartFailed
	case "WARNING":
		return CodeSmartWarning
	default:
		return ""
	}
}

// poolProblem is the problem code for a pool's health, or "". An unknown
// health is not a problem.
func poolProblem(p hub.ZfsPool) string {
	if p.Health != "" && p.Health != "ONLINE" {
		return CodePoolDegraded
	}
	return ""
}

// monitorFailing reports any packet loss in the last hour on an enabled monitor.
func monitorFailing(m hub.Monitor) bool {
	return m.Enabled && m.Loss1h > 0
}

// certProblem is the certificate problem of an enabled monitor, or "", with
// the time left (negative once expired). Missing cert info is unknown, not fine.
func certProblem(m hub.Monitor, now time.Time) (string, time.Duration) {
	if !m.Enabled || m.CertInfo == nil || m.CertInfo.Expires <= 0 {
		return "", 0
	}
	left := time.UnixMilli(m.CertInfo.Expires).Sub(now)
	switch {
	case left <= 0:
		return CodeCertExpired, left
	case left < certWarnBefore:
		return CodeCertExpiring, left
	default:
		return "", left
	}
}

// deviceName prefers the model; macOS device names are long IOService paths.
func deviceName(d hub.SmartDevice) string {
	if d.Model != "" {
		return d.Model
	}
	return d.Name
}

func problem(code, detail string, count int) Problem {
	return Problem{Code: code, Detail: detail, Section: codeSection[code], Count: count}
}

// list joins sorted items, naming at most maxNames.
func list(items []string) string {
	items = slices.Sorted(slices.Values(items))
	if len(items) <= maxNames {
		return strings.Join(items, ", ")
	}
	return strings.Join(items[:maxNames], ", ") + fmt.Sprintf(" +%d more", len(items)-maxNames)
}

func sortProblems(p []Problem) []Problem {
	slices.SortStableFunc(p, func(a, b Problem) int {
		return slices.Index(codeOrder, a.Code) - slices.Index(codeOrder, b.Code)
	})
	return p
}
