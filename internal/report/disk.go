package report

import (
	"maps"
	"slices"
	"strings"

	"github.com/agrmohit/beszelscope/internal/hub"
)

// Disk is Beszel's Disk tab: filesystems, I/O, storage pools and SMART.
// SMART and pools are stored results, so they are shown even when the
// system is down; filesystems and I/O are live.
type Disk struct {
	Unavailable string       `json:"unavailable,omitempty"` // why filesystems and I/O are missing
	SampledAgoS *int64       `json:"sampled_ago_s,omitempty"`
	Filesystems []Filesystem `json:"filesystems,omitempty"`
	Pools       []PoolRow    `json:"pools,omitempty"`
	Smart       []SmartRow   `json:"smart,omitempty"`
	Absent      []string     `json:"absent,omitempty"` // "part: why it is missing"
}

// Filesystem is the root disk or an extra filesystem. Beszel stores sizes as
// GiB with 2 decimals, so they stay in GiB rather than imply byte precision.
type Filesystem struct {
	Name           string   `json:"name"` // "/" for the root disk
	TotalGiB       *float64 `json:"total_gib,omitempty"`
	UsedGiB        *float64 `json:"used_gib,omitempty"`
	Pct            *float64 `json:"pct,omitempty"`
	ReadBytesPerS  uint64   `json:"read_bytes_per_s"`
	WriteBytesPerS uint64   `json:"write_bytes_per_s"`
	IOUtilPct      *float64 `json:"io_util_pct,omitempty"`
}

// PoolRow is a ZFS or btrfs pool.
type PoolRow struct {
	Name        string    `json:"name"`
	Health      string    `json:"health"`
	Status      string    `json:"status,omitempty"` // zpool status message
	SizeBytes   uint64    `json:"size_bytes"`
	AllocBytes  uint64    `json:"alloc_bytes"`
	FreeBytes   uint64    `json:"free_bytes"`
	RawSizes    bool      `json:"raw_sizes,omitempty"` // sizes are physical, not usable space
	Scrub       *Scrub    `json:"scrub,omitempty"`
	BadVdevs    []VdevRow `json:"bad_vdevs,omitempty"` // only devices with errors or not ONLINE
	CheckedAgoS *int64    `json:"checked_ago_s,omitempty"`
	Problems    []string  `json:"problems,omitempty"`
}

// Scrub is a pool's last scrub.
type Scrub struct {
	State  string `json:"state"`
	Errors uint64 `json:"errors"`
}

// VdevRow is a pool device with a fault.
type VdevRow struct {
	Name           string `json:"name"`
	State          string `json:"state"`
	ReadErrors     uint64 `json:"read_errors"`
	WriteErrors    uint64 `json:"write_errors"`
	ChecksumErrors uint64 `json:"checksum_errors"`
}

// SmartRow is one drive's SMART summary.
type SmartRow struct {
	Name              string        `json:"name"`
	Model             string        `json:"model,omitempty"`
	Type              string        `json:"type,omitempty"`
	State             string        `json:"state"` // PASSED, WARNING, FAILED or UNKNOWN
	CapacityBytes     uint64        `json:"capacity_bytes,omitempty"`
	TempC             *float64      `json:"temp_c,omitempty"`
	PowerOnHours      uint64        `json:"power_on_hours,omitempty"`
	PowerCycles       uint64        `json:"power_cycles,omitempty"`
	FailingAttributes []FailingAttr `json:"failing_attributes,omitempty"`
	CheckedAgoS       *int64        `json:"checked_ago_s,omitempty"`
	Problems          []string      `json:"problems,omitempty"`
}

// FailingAttr is a SMART attribute that has failed.
type FailingAttr struct {
	Name       string `json:"name"`
	WhenFailed string `json:"when_failed"`
	Raw        string `json:"raw,omitempty"`
}

func disk(in DetailInput) *Disk {
	d := &Disk{}
	switch {
	case in.unavailable(in.System) != "":
		d.Unavailable = in.unavailable(in.System)
	case statsUnavailable(in) != "":
		d.Unavailable = statsUnavailable(in)
	default:
		x := in.Latest.Stats
		d.SampledAgoS = new(ageS(in.Now, in.Latest.Created.Time))
		root := Filesystem{
			Name: "/", TotalGiB: round(x.DiskGB, 2), UsedGiB: round(x.DiskUsedGB, 2), Pct: round(x.DiskPct, 1),
			ReadBytesPerS: x.DiskIO[0], WriteBytesPerS: x.DiskIO[1], IOUtilPct: ioUtil(x.DiskIOStats),
		}
		d.Filesystems = append(d.Filesystems, root)
		for _, name := range slices.Sorted(maps.Keys(x.ExtraFs)) {
			fs := x.ExtraFs[name]
			row := Filesystem{
				Name: name, TotalGiB: round(fs.TotalGB, 2), UsedGiB: round(fs.UsedGB, 2),
				ReadBytesPerS: fs.ReadBytes, WriteBytesPerS: fs.WriteBytes, IOUtilPct: ioUtil(fs.IOStats),
			}
			if fs.TotalGB > 0 {
				row.Pct = round(fs.UsedGB/fs.TotalGB*100, 1)
			}
			d.Filesystems = append(d.Filesystems, row)
		}
	}

	for _, p := range in.Pools {
		if p.System == in.System.ID {
			d.Pools = append(d.Pools, poolRow(in, p))
		}
	}
	slices.SortStableFunc(d.Pools, func(a, b PoolRow) int { return problemsFirst(len(a.Problems), len(b.Problems), a.Name, b.Name) })
	for _, s := range in.Smart {
		if s.System == in.System.ID {
			d.Smart = append(d.Smart, smartRow(in, s))
		}
	}
	slices.SortStableFunc(d.Smart, func(a, b SmartRow) int { return problemsFirst(len(a.Problems), len(b.Problems), a.Name, b.Name) })

	if len(d.Pools) == 0 {
		d.Absent = append(d.Absent, "pools: no ZFS or btrfs pools")
	}
	if len(d.Smart) == 0 {
		d.Absent = append(d.Absent, "smart: no SMART data reported")
	}
	return d
}

// ioUtil is the I/O utilisation from Beszel's disk I/O stats, or nil when
// the agent sent none.
func ioUtil(stats [6]float64) *float64 {
	if stats == [6]float64{} {
		return nil
	}
	return round(stats[2], 1)
}

func poolRow(in DetailInput, p hub.ZfsPool) PoolRow {
	name := p.Name
	if p.DisplayName != "" {
		name = p.DisplayName
	}
	row := PoolRow{
		Name: name, Health: p.Health, Status: p.Status,
		SizeBytes: p.Size, AllocBytes: p.Alloc, FreeBytes: p.Free, RawSizes: p.Raw,
	}
	if p.Scrub != nil {
		row.Scrub = &Scrub{State: p.Scrub.State, Errors: p.Scrub.Errors}
	}
	for _, v := range p.Vdevs {
		if v.State != "ONLINE" || v.ReadErrs+v.WriteErrs+v.ChecksumErrs > 0 {
			row.BadVdevs = append(row.BadVdevs, VdevRow{
				Name: v.Name, State: v.State, ReadErrors: v.ReadErrs, WriteErrors: v.WriteErrs, ChecksumErrors: v.ChecksumErrs,
			})
		}
	}
	if !p.Updated.IsZero() {
		row.CheckedAgoS = new(ageS(in.Now, p.Updated.Time))
	}
	if code := poolProblem(p); code != "" {
		row.Problems = []string{code}
	}
	return row
}

func smartRow(in DetailInput, s hub.SmartDevice) SmartRow {
	row := SmartRow{
		Name: smartName(s.Name), Model: s.Model, Type: s.Type, State: s.State,
		CapacityBytes: s.Capacity, PowerOnHours: s.Hours, PowerCycles: s.Cycles,
	}
	if s.Temp > 0 {
		row.TempC = round(s.Temp, 1)
	}
	for _, a := range s.Attributes {
		if a.WhenFailed != "" {
			row.FailingAttributes = append(row.FailingAttributes, FailingAttr{Name: a.Name, WhenFailed: a.WhenFailed, Raw: a.RawString})
		}
	}
	if !s.Updated.IsZero() {
		row.CheckedAgoS = new(ageS(in.Now, s.Updated.Time))
	}
	if code := smartProblem(s); code != "" {
		row.Problems = []string{code}
	}
	return row
}

// smartName shortens a macOS IOService path (about 160 characters) to its
// last element; device names such as /dev/sda stay as they are.
func smartName(name string) string {
	if !strings.HasPrefix(name, "IOService:") {
		return name
	}
	return name[strings.LastIndex(name, "/")+1:]
}
