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

// Code names a kind of problem. Rows list them in the order of these
// constants: buildRow and structural add them in it.
type Code string

// The problem codes.
const (
	CodeStatusDown         Code = "status_down"
	CodeStatusPaused       Code = "status_paused"
	CodeStatusPending      Code = "status_pending"
	CodeStale              Code = "stale"
	CodeServiceFailed      Code = "service_failed"
	CodeContainerUnhealthy Code = "container_unhealthy"
	CodeContainerGone      Code = "container_gone"
	CodeSmartFailed        Code = "smart_failed"
	CodeSmartWarning       Code = "smart_warning"
	CodePoolDegraded       Code = "pool_degraded"
	CodeMonitorFailing     Code = "monitor_failing"
	CodeCertExpired        Code = "cert_expired"
	CodeCertExpiring       Code = "cert_expiring"
	CodeSecurityUpdates    Code = "security_updates"
)

// section names the system_detail section that explains c.
func (c Code) section() Section {
	switch c {
	case CodeStatusDown, CodeStatusPaused, CodeStatusPending, CodeStale:
		return SectionSummary
	case CodeServiceFailed:
		return SectionServices
	case CodeContainerUnhealthy, CodeContainerGone:
		return SectionContainers
	case CodeSmartFailed, CodeSmartWarning, CodePoolDegraded:
		return SectionDisk
	case CodeMonitorFailing, CodeCertExpired, CodeCertExpiring:
		return SectionNetwork
	case CodeSecurityUpdates:
		return SectionUpdates
	}
	panic("section: unhandled problem code " + string(c)) // codes are the constants above
}

// Read is one hub read: what it returned, or why it failed. The zero Read
// is a read not made, and reads as empty.
type Read[T any] struct {
	Val T
	Err error
}

// FleetInput is everything fleet_status reads, already fetched.
type FleetInput struct {
	Now            time.Time
	HubVersion     string
	TokenExpiresAt time.Time
	Systems        []hub.System // the systems to report
	Services       Read[[]hub.Service]
	Containers     Read[[]hub.Container]
	Smart          Read[[]hub.SmartDevice]
	Pools          Read[[]hub.ZfsPool]
	Monitors       Read[[]hub.Monitor]
	// RecentContainers is each up system's 10m container rows from the
	// last hour, by system ID.
	RecentContainers map[string]Read[[]hub.ContainerStats]
	// LastSeen is when each system that is not up last sent data, by ID:
	// its newest stats row. Zero means none in Beszel's 30-day retention; a
	// missing entry or failed read means unknown, and systems.updated stands in.
	LastSeen map[string]Read[time.Time]
}

// FleetOptions narrows the result.
type FleetOptions struct {
	ProblemsOnly bool // drop rows without problems; counts still cover all
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
	Unknown      int `json:"unknown,omitempty"` // a status Beszel does not define
	WithProblems int `json:"with_problems"`
}

// Row is one system. Live metrics are present only while the system is up.
type Row struct {
	Name          string     `json:"name"`
	Status        hub.Status `json:"status"`
	AgeS          *int64     `json:"age_s,omitempty"`
	LastSeenAgoS  *int64     `json:"last_seen_ago_s,omitempty"`
	CPUPct        *float64   `json:"cpu_pct,omitempty"`
	MemPct        *float64   `json:"mem_pct,omitempty"`
	DiskPct       *float64   `json:"disk_pct,omitempty"`
	DiskWorst     string     `json:"disk_worst,omitempty"`
	TempHottestC  *float64   `json:"temp_hottest_c,omitempty"`
	LoadPerThread *float64   `json:"load_per_thread,omitempty"`
	NetBytesPerS  *uint64    `json:"net_bytes_per_s,omitempty"`
	UptimeS       *uint64    `json:"uptime_s,omitempty"`
	Agent         string     `json:"agent,omitempty"`
	Updates       *Updates   `json:"updates,omitempty"`
	Problems      []Problem  `json:"problems,omitempty"`
}

// Updates counts pending package updates; Security is null when unknown.
type Updates struct {
	Total    int  `json:"total"`
	Security *int `json:"security"`
}

// Problem is one kind of fault on a system.
type Problem struct {
	Code    Code    `json:"code"`
	Detail  string  `json:"detail"`
	Section Section `json:"section"`
	Count   int     `json:"count,omitempty"`
}

// Fleet builds the fleet_status result.
func Fleet(in FleetInput, o FleetOptions) FleetResult {
	out := FleetResult{AsOf: in.Now.UTC().Truncate(time.Second), Warnings: TokenWarning(in.Now, in.TokenExpiresAt)}
	for _, s := range in.Systems {
		row := buildRow(in, s)
		out.Counts.Total++
		switch s.Status {
		case hub.StatusUp:
			out.Counts.Up++
		case hub.StatusDown:
			out.Counts.Down++
		case hub.StatusPaused:
			out.Counts.Paused++
		case hub.StatusPending:
			out.Counts.Pending++
		default:
			out.Counts.Unknown++
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
	var failed []string
	failed = readFailed(failed, "systemd services", problemsMissing, in.Services.Err)
	failed = readFailed(failed, "containers", problemsMissing, in.Containers.Err)
	failed = readFailed(failed, "SMART devices", problemsMissing, in.Smart.Err)
	failed = readFailed(failed, "storage pools", problemsMissing, in.Pools.Err)
	failed = readFailed(failed, "network monitors", problemsMissing, in.Monitors.Err)
	for _, s := range in.Systems {
		failed = readFailed(failed, "container history of "+s.Name, problemsMissing, in.RecentContainers[s.ID].Err)
		failed = lastSeenFailed(failed, in, s)
	}
	slices.Sort(failed)
	out.Warnings = append(out.Warnings, failed...)
	return out
}

const problemsMissing = "related problems may be missing"

// readFailed appends the warning for a failed read: what was read, and what
// the result therefore lacks.
func readFailed(warnings []string, what, lacks string, err error) []string {
	if err == nil {
		return warnings
	}
	return append(warnings, fmt.Sprintf("could not read %s, so %s: %v", what, lacks, err))
}

func lastSeenFailed(warnings []string, in FleetInput, s hub.System) []string {
	return readFailed(warnings, "last data of "+s.Name, "its last-seen time is when its status last changed", in.LastSeen[s.ID].Err)
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
	if s.Status != hub.StatusUp {
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
	row.Updates = packageCounts(i)

	if d := time.Duration(age) * time.Second; d > staleAfter {
		row.Problems = append(row.Problems, problem(CodeStale, "last poll "+short(d)+" ago", 0))
	}
	row.Problems = append(row.Problems, structural(in, s)...)
	return row
}

// packageCounts reads the pending update counts on the systems row, or nil.
func packageCounts(i hub.SystemInfo) *Updates {
	if len(i.PackageUpdates) == 0 {
		return nil
	}
	u := &Updates{Total: int(i.PackageUpdates[0])}
	if len(i.PackageUpdates) > 1 {
		u.Security = new(int(i.PackageUpdates[1]))
	}
	return u
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
	r, ok := in.LastSeen[s.ID]
	if !ok || r.Err != nil {
		return s.Updated.Time, false
	}
	return r.Val, r.Val.IsZero()
}

func statusProblem(s hub.System, now, seen time.Time, none bool) []Problem {
	switch s.Status {
	case hub.StatusUp:
		return nil
	case hub.StatusDown:
		if none {
			return []Problem{problem(CodeStatusDown, "down, no data in the last 30 days", 0)}
		}
		return []Problem{problem(CodeStatusDown, "down for "+short(now.Sub(seen)), 0)}
	case hub.StatusPaused:
		return []Problem{problem(CodeStatusPaused, "monitoring paused", 0)}
	case hub.StatusPending:
		return []Problem{problem(CodeStatusPending, "agent has not connected", 0)}
	default:
		return []Problem{problem(CodeStatusDown, "unknown status "+strconv.Quote(string(s.Status)), 0)}
	}
}

// structural finds the faults Beszel records without alert rules.
func structural(in FleetInput, s hub.System) []Problem {
	items := map[Code][]string{} // what each problem names
	add := func(code Code, item string) { items[code] = append(items[code], item) }

	for _, svc := range in.Services.Val {
		if svc.System == s.ID && svc.State == hub.ServiceFailed {
			add(CodeServiceFailed, svc.Name)
		}
	}
	for _, c := range in.Containers.Val {
		if c.System == s.ID && c.Health == hub.HealthUnhealthy && !containerStale(in.Now, c) {
			add(CodeContainerUnhealthy, c.Name)
		}
	}
	items[CodeContainerGone] = goneContainers(in, s.ID)
	for _, d := range in.Smart.Val {
		if code := smartProblem(d); d.System == s.ID && code != "" {
			add(code, deviceName(d))
		}
	}
	for _, pl := range in.Pools.Val {
		if code := poolProblem(pl); pl.System == s.ID && code != "" {
			add(code, pl.Name+" "+pl.Health)
		}
	}
	for _, m := range in.Monitors.Val {
		if m.System != s.ID {
			continue
		}
		if monitorFailing(m) {
			add(CodeMonitorFailing, fmt.Sprintf("%s %.0f%% loss", m.Target, m.Loss1h))
		}
		if code, left := certProblem(m, in.Now); code != "" {
			when := "in " + short(left)
			if left <= 0 {
				when = short(-left) + " ago"
			}
			add(code, m.Target+" "+when)
		}
	}

	var p []Problem
	for _, code := range []Code{
		CodeServiceFailed, CodeContainerUnhealthy, CodeContainerGone, CodeSmartFailed, CodeSmartWarning,
		CodePoolDegraded, CodeMonitorFailing, CodeCertExpired, CodeCertExpiring,
	} {
		if n := len(items[code]); n > 0 {
			p = append(p, problem(code, list(items[code]), n))
		}
	}
	if pu := s.Info.PackageUpdates; len(pu) > 1 && pu[1] > 0 {
		p = append(p, problem(CodeSecurityUpdates, strconv.Itoa(int(pu[1]))+" security updates pending", int(pu[1])))
	}
	return p
}

// smartProblem is the problem code for a drive's SMART state, or "".
func smartProblem(d hub.SmartDevice) Code {
	switch d.State {
	case hub.SmartFailed:
		return CodeSmartFailed
	case hub.SmartWarning:
		return CodeSmartWarning
	case hub.SmartPassed, hub.SmartUnknown:
		return ""
	default: // a state the agent does not define is not a problem
		return ""
	}
}

// poolProblem is the problem code for a pool's health, or "". An unknown
// health is not a problem.
func poolProblem(p hub.ZfsPool) Code {
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
func certProblem(m hub.Monitor, now time.Time) (Code, time.Duration) {
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

func problem(code Code, detail string, count int) Problem {
	return Problem{Code: code, Detail: detail, Section: code.section(), Count: count}
}

// list joins sorted items, naming at most maxNames.
func list(items []string) string {
	items = slices.Sorted(slices.Values(items))
	if len(items) <= maxNames {
		return strings.Join(items, ", ")
	}
	return strings.Join(items[:maxNames], ", ") + fmt.Sprintf(" +%d more", len(items)-maxNames)
}
