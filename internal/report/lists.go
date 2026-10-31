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

// List knob values for containers and services.
const (
	SortCPU      = "cpu"
	SortMem      = "mem"
	SortName     = "name"
	DefaultLimit = 25
	MaxLimit     = 200
)

// Sorts lists every sort system_detail accepts.
var Sorts = []string{SortCPU, SortMem, SortName}

// GPUs is Beszel's GPU tab.
type GPUs struct {
	Unavailable string   `json:"unavailable,omitempty"`
	SampledAgoS *int64   `json:"sampled_ago_s,omitempty"`
	GPUs        []GPURow `json:"gpus,omitempty"`
	Absent      []string `json:"absent,omitempty"` // "part: why it is missing"
}

// GPURow is one GPU. Fields the agent does not report are omitted.
type GPURow struct {
	ID            string   `json:"id"`
	Name          string   `json:"name"`
	UsagePct      *float64 `json:"usage_pct,omitempty"`
	MemUsedBytes  *uint64  `json:"mem_used_bytes,omitempty"`
	MemTotalBytes *uint64  `json:"mem_total_bytes,omitempty"`
	PowerW        *float64 `json:"power_w,omitempty"`
}

// ListCounts says how much of a list is shown. Total counts every row on
// the server, before filter and problems_only.
type ListCounts struct {
	Total     int  `json:"total"`
	Shown     int  `json:"shown"`
	Truncated bool `json:"truncated"` // limit cut rows
}

// Containers is Beszel's Containers tab: running containers only.
type Containers struct {
	Unavailable string         `json:"unavailable,omitempty"`
	Counts      *ListCounts    `json:"counts,omitempty"`
	Unhealthy   *int           `json:"unhealthy,omitempty"`
	Rows        []ContainerRow `json:"rows,omitempty"`
	Gone        []string       `json:"gone,omitempty"` // stopped within the last hour
	Absent      []string       `json:"absent,omitempty"`
}

// ContainerRow is one running container.
type ContainerRow struct {
	Name         string   `json:"name"`
	Image        string   `json:"image"`
	Status       string   `json:"status"` // Docker's text, e.g. "Up 3 days"
	Health       string   `json:"health"` // none, starting, healthy, unhealthy or unknown
	CPUPct       *float64 `json:"cpu_pct,omitempty"`
	MemBytes     uint64   `json:"mem_bytes"`
	NetBytesPerS uint64   `json:"net_bytes_per_s"`
	AgeS         int64    `json:"age_s"`
	Problems     []string `json:"problems,omitempty"`
}

// Services is Beszel's Services tab: systemd units.
type Services struct {
	Unavailable string       `json:"unavailable,omitempty"`
	Counts      *ListCounts  `json:"counts,omitempty"`
	Failed      *int         `json:"failed,omitempty"`
	Rows        []ServiceRow `json:"rows,omitempty"`
	Absent      []string     `json:"absent,omitempty"`
}

// ServiceRow is one systemd unit.
type ServiceRow struct {
	Name         string   `json:"name"`
	State        string   `json:"state"` // active, inactive, failed, activating, deactivating or reloading
	Sub          string   `json:"sub"`   // dead, running, exited, failed or unknown
	CPUPct       *float64 `json:"cpu_pct,omitempty"`
	MemBytes     uint64   `json:"mem_bytes"`
	MemPeakBytes uint64   `json:"mem_peak_bytes"`
	Problems     []string `json:"problems,omitempty"`
}

const mib = 1 << 20

var (
	healthNames = []string{"none", "starting", "healthy", "unhealthy"}
	stateNames  = []string{"active", "inactive", "failed", "activating", "deactivating", "reloading"}
	subNames    = []string{"dead", "running", "exited", "failed", "unknown"}
)

// enumName returns names[i], or "unknown" when the hub sends a newer value.
func enumName(names []string, i int) string {
	if i < 0 || i >= len(names) {
		return "unknown"
	}
	return names[i]
}

func gpus(in DetailInput) *GPUs {
	if u := in.unavailable(in.System); u != "" {
		return &GPUs{Unavailable: u}
	}
	if u := statsUnavailable(in); u != "" {
		return &GPUs{Unavailable: u}
	}
	g := &GPUs{SampledAgoS: new(ageS(in.Now, in.Latest.Created.Time))}
	all := in.Latest.Stats.GPU
	for _, id := range slices.Sorted(maps.Keys(all)) {
		x := all[id]
		row := GPURow{ID: id, Name: x.Name, UsagePct: round(x.Usage, 1)}
		if x.MemTotalMB > 0 {
			row.MemUsedBytes, row.MemTotalBytes = new(uint64(x.MemUsedMB*mib)), new(uint64(x.MemTotalMB*mib))
		}
		if x.PowerW > 0 {
			row.PowerW = round(x.PowerW, 2)
		}
		g.GPUs = append(g.GPUs, row)
	}
	if len(g.GPUs) == 0 {
		g.Absent = []string{"gpus: none reported by this host"}
	}
	return g
}

func containers(in DetailInput, o DetailOptions) (*Containers, string) {
	if u := in.unavailable(in.System); u != "" {
		return &Containers{Unavailable: u}, ""
	}
	if in.ContainersErr != nil {
		return &Containers{Unavailable: "could not read containers: " + in.ContainersErr.Error()}, ""
	}
	var all []ContainerRow
	unhealthy := 0
	for _, c := range in.Containers {
		if c.System != in.System.ID || containerStale(in.Now, c) {
			continue
		}
		row := ContainerRow{
			Name: c.Name, Image: c.Image, Status: c.Status, Health: enumName(healthNames, c.Health),
			CPUPct: round(c.CPU, 2), MemBytes: uint64(c.MemoryMiB * mib), NetBytesPerS: uint64(c.NetBytes),
			AgeS: ageS(in.Now, time.UnixMilli(c.Updated)),
		}
		if c.Health == 3 {
			row.Problems = []string{CodeContainerUnhealthy}
			unhealthy++
		}
		all = append(all, row)
	}
	out := &Containers{Unhealthy: &unhealthy}
	for _, g := range goneContainers(in.FleetInput, in.System.ID) {
		if matches(g, o.Filter) {
			out.Gone = append(out.Gone, g)
		}
	}
	slices.Sort(out.Gone)
	var hint string
	out.Rows, out.Counts, hint = pick(all, o, "containers", func(r ContainerRow) listKey {
		return listKey{r.Name, r.Image, deref(r.CPUPct), float64(r.MemBytes), len(r.Problems) > 0}
	})
	switch {
	case len(all) == 0 && len(out.Gone) == 0:
		out.Absent = []string{"containers: none running; Beszel lists only running containers"}
	case o.Filter != "" && len(out.Gone) == 0 && !slices.ContainsFunc(all, func(r ContainerRow) bool { return matches(r.Name, o.Filter) || matches(r.Image, o.Filter) }):
		hint = fmt.Sprintf("containers: no running container's name or image contains %q; Beszel lists only running containers", o.Filter)
	}
	return out, hint
}

func services(in DetailInput, o DetailOptions) (*Services, string) {
	if u := in.unavailable(in.System); u != "" {
		return &Services{Unavailable: u}, ""
	}
	if in.ServicesErr != nil {
		return &Services{Unavailable: "could not read services: " + in.ServicesErr.Error()}, ""
	}
	var all []ServiceRow
	failed := 0
	for _, s := range in.Services {
		if s.System != in.System.ID {
			continue
		}
		row := ServiceRow{
			Name: s.Name, State: enumName(stateNames, s.State), Sub: enumName(subNames, s.Sub),
			CPUPct: round(s.CPU, 2), MemBytes: s.Memory, MemPeakBytes: s.MemPeak,
		}
		if s.State == 2 {
			row.Problems = []string{CodeServiceFailed}
			failed++
		}
		all = append(all, row)
	}
	if len(all) == 0 {
		return &Services{Absent: []string{"services: none reported; Beszel reads systemd units on Linux only"}}, ""
	}
	out := &Services{Failed: &failed}
	var hint string
	out.Rows, out.Counts, hint = pick(all, o, "services", func(r ServiceRow) listKey {
		return listKey{r.Name, "", deref(r.CPUPct), float64(r.MemBytes), len(r.Problems) > 0}
	})
	if o.Filter != "" && !slices.ContainsFunc(all, func(r ServiceRow) bool { return matches(r.Name, o.Filter) }) {
		hint = fmt.Sprintf("services: no unit name contains %q", o.Filter)
	}
	return out, hint
}

// listKey is what the list knobs look at in a row.
type listKey struct {
	name     string
	image    string // a container's image, which filter also matches
	cpu, mem float64
	problem  bool
}

/*
pick applies the list knobs to one section's rows.

Filter (on name, or a container's image) and problems_only drop rows first. The rest are ordered problem rows
first, then by sort (cpu and mem descending, name ascending, ties by name).
The limit then cuts only rows without problems, so a problem is never hidden
by the limit; when problem rows alone exceed it, all of them are kept.
The hint names the knob to change when the limit cut rows.
*/
func pick[T any](rows []T, o DetailOptions, what string, key func(T) listKey) ([]T, *ListCounts, string) {
	counts := &ListCounts{Total: len(rows)}
	var kept []T
	problems := 0
	for _, r := range rows {
		k := key(r)
		if !matches(k.name, o.Filter) && !matches(k.image, o.Filter) || o.ProblemsOnly && !k.problem {
			continue
		}
		kept = append(kept, r)
		if k.problem {
			problems++
		}
	}
	slices.SortStableFunc(kept, func(a, b T) int {
		ka, kb := key(a), key(b)
		if ka.problem != kb.problem {
			if ka.problem {
				return -1
			}
			return 1
		}
		var c int
		switch o.Sort {
		case SortMem:
			c = cmp.Compare(kb.mem, ka.mem)
		case SortName:
		default:
			c = cmp.Compare(kb.cpu, ka.cpu)
		}
		return cmp.Or(c, strings.Compare(ka.name, kb.name))
	})
	limit := o.Limit
	if limit <= 0 {
		limit = DefaultLimit
	}
	var hint string
	if keep := max(limit, problems); len(kept) > keep {
		hint = fmt.Sprintf("%s: %d of %d rows not shown; raise limit (max %d) or use filter", what, len(kept)-keep, len(kept), MaxLimit)
		kept = kept[:keep]
		counts.Truncated = true
	}
	counts.Shown = len(kept)
	return kept, counts, hint
}

// matches reports whether name contains filter, ignoring case. An empty
// filter matches everything.
func matches(name, filter string) bool {
	return strings.Contains(strings.ToLower(name), strings.ToLower(filter))
}

// containerStale reports a container row the agent stopped reporting: the
// container has stopped, and Beszel deletes the row 10 minutes later.
func containerStale(now time.Time, c hub.Container) bool {
	return now.Sub(time.UnixMilli(c.Updated)) > containerStaleAfter
}

// goneContainers lists the containers of system that stopped within the
// last hour: stale rows, and names in the 10m history with no row now. A
// recreated container has a new ID, so its stale old row sits beside a live
// one for 10 minutes; a name with a live row is never gone.
func goneContainers(in FleetInput, system string) []string {
	live, seen := map[string]bool{}, map[string]bool{}
	for _, c := range in.Containers {
		if c.System == system && !containerStale(in.Now, c) {
			live[c.Name] = true
		}
	}
	var gone []string
	add := func(name string) {
		if !live[name] && !seen[name] {
			seen[name] = true
			gone = append(gone, name)
		}
	}
	for _, c := range in.Containers {
		if c.System == system && containerStale(in.Now, c) {
			add(c.Name)
		}
	}
	for _, cs := range in.RecentContainers {
		if cs.System == system {
			for _, st := range cs.Stats {
				add(st.Name)
			}
		}
	}
	return gone
}
