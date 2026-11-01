package hub

import (
	"context"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// JSON tags follow Beszel v0.21.1 (internal/entities/system/system.go and
// the collection schema). Unknown fields are ignored.

// StatType is a Beszel stats resolution. Each record averages this window;
// rows are kept for 1h, 12h, 24h, 7d and 30d respectively.
type StatType string

// Stats resolutions.
const (
	Stat1m   StatType = "1m"
	Stat10m  StatType = "10m"
	Stat20m  StatType = "20m"
	Stat120m StatType = "120m"
	Stat480m StatType = "480m"
)

func (t StatType) valid() bool {
	switch t {
	case Stat1m, Stat10m, Stat20m, Stat120m, Stat480m:
		return true
	default:
		return false
	}
}

// Time is a PocketBase date such as "2026-10-30 22:18:50.483Z".
type Time struct{ time.Time }

const pbLayout = "2006-01-02 15:04:05.999Z"

// UnmarshalJSON parses a PocketBase date; "" is the zero time.
func (t *Time) UnmarshalJSON(b []byte) error {
	s, err := strconv.Unquote(string(b))
	if err != nil {
		return fmt.Errorf("date %s: %w", b, err)
	}
	if s == "" {
		t.Time = time.Time{}
		return nil
	}
	v, err := time.Parse(pbLayout, s)
	if err != nil {
		return fmt.Errorf("date %q: %w", s, err)
	}
	t.Time = v
	return nil
}

// Info is the hub's /api/beszel/info response.
type Info struct {
	Version string `json:"v"`
}

// Status is a system's state as Beszel reports it. Other values can arrive
// on the wire; switches name all four and handle the rest in a default.
type Status string

// The statuses Beszel sets.
const (
	StatusUp      Status = "up"
	StatusDown    Status = "down"
	StatusPaused  Status = "paused"
	StatusPending Status = "pending"
)

// OS is Beszel's operating system code.
type OS int

// The OS codes Beszel defines.
const (
	OSLinux   OS = 0
	OSDarwin  OS = 1
	OSWindows OS = 2
	OSFreeBSD OS = 3
)

// SmartState is a drive's overall SMART result.
type SmartState string

// The SMART results the agent reports.
const (
	SmartPassed  SmartState = "PASSED"
	SmartWarning SmartState = "WARNING"
	SmartFailed  SmartState = "FAILED"
	SmartUnknown SmartState = "UNKNOWN"
)

// System is a monitored host with its latest summary.
type System struct {
	ID      string     `json:"id"`
	Name    string     `json:"name"`
	Host    string     `json:"host"`
	Port    string     `json:"port"`
	Status  Status     `json:"status"`
	Info    SystemInfo `json:"info"`
	Updated Time       `json:"updated"`
}

// SystemInfo is the summary from the latest poll.
type SystemInfo struct {
	Uptime         uint64             `json:"u"` // seconds
	CPU            float64            `json:"cpu"`
	MemPct         float64            `json:"mp"`
	DiskPct        float64            `json:"dp"`
	BandwidthBytes uint64             `json:"bb"` // bytes/s, sent + received
	AgentVersion   string             `json:"v"`
	LoadAvg        [3]float64         `json:"la"`
	Threads        int                `json:"t"`
	GPUPct         float64            `json:"g"`
	DashTemp       float64            `json:"dt"`  // hottest sensor, C
	ExtraFsPct     map[string]float64 `json:"efs"` // used %
	Services       []uint16           `json:"sv"`  // [total, failed]
	Battery        [2]uint8           `json:"bat"` // [percent, state]
	PackageUpdates []uint16           `json:"pu"`  // [total, security]; security may be absent
	RootDiskName   string             `json:"rdn"`
}

// SystemDetails holds the static facts about a host.
type SystemDetails struct {
	System   string `json:"system"`
	Hostname string `json:"hostname"`
	OS       OS     `json:"os"`
	OSName   string `json:"os_name"`
	Kernel   string `json:"kernel"`
	CPU      string `json:"cpu"`
	Arch     string `json:"arch"`
	Cores    int    `json:"cores"`
	Threads  int    `json:"threads"`
	Sockets  int    `json:"sockets"`
	Memory   uint64 `json:"memory"` // bytes
	Podman   bool   `json:"podman"`
}

// Stats is one system_stats sample. GB fields are deprecated by Beszel but
// are the only memory figures in rows from agents before 0.21.1.
type Stats struct {
	CPU          float64              `json:"cpu"`
	MaxCPU       float64              `json:"cpum"` // rollups only
	CPUBreakdown []float64            `json:"cpub"` // user, system, iowait, steal, idle, irq, softirq, nice
	CPUCores     []uint16             `json:"cpus"` // busy % per core
	MemPct       float64              `json:"mp"`
	Mem          [4]uint64            `json:"mem"`  // total, used, buff/cache, ZFS ARC bytes
	MaxMemUsed   uint64               `json:"memm"` // rollups only
	Swap         [2]uint64            `json:"swap"` // total, used bytes
	MemGB        float64              `json:"m"`
	MemUsedGB    float64              `json:"mu"`
	SwapGB       float64              `json:"s"`
	SwapUsedGB   float64              `json:"su"`
	DiskGB       float64              `json:"d"`
	DiskUsedGB   float64              `json:"du"`
	DiskPct      float64              `json:"dp"`
	DiskIO       [2]uint64            `json:"dio"`  // read, write bytes/s
	MaxDiskIO    [2]uint64            `json:"diom"` // rollups only
	DiskIOStats  [6]float64           `json:"dios"`
	Bandwidth    [2]uint64            `json:"b"`  // sent, received bytes/s
	MaxBandwidth [2]uint64            `json:"bm"` // rollups only
	Interfaces   map[string][4]uint64 `json:"ni"` // up, down bytes/s; total up, down bytes
	LoadAvg      [3]float64           `json:"la"`
	Temps        map[string]float64   `json:"t"`
	Fans         map[string]uint16    `json:"f"` // RPM
	ExtraFs      map[string]FsStats   `json:"efs"`
	GPU          map[string]GPU       `json:"g"`
	Battery      [2]uint8             `json:"bat"`
	Pools        map[string]Pool      `json:"z"`
	WiFi         map[string]int8      `json:"wf"` // RSSI dBm
}

// FsStats is an extra filesystem in Stats.
type FsStats struct {
	TotalGB    float64    `json:"d"`
	UsedGB     float64    `json:"du"`
	ReadBytes  uint64     `json:"rb"` // per second
	WriteBytes uint64     `json:"wb"` // per second
	MaxRead    uint64     `json:"rbm"`
	MaxWrite   uint64     `json:"wbm"`
	IOStats    [6]float64 `json:"dios"`
}

// GPU is one GPU in Stats.
type GPU struct {
	Name       string  `json:"n"`
	Usage      float64 `json:"u"`  // %
	MemUsedMB  float64 `json:"mu"` // as reported by the agent
	MemTotalMB float64 `json:"mt"`
	PowerW     float64 `json:"p"`
}

// Pool is a ZFS or btrfs pool in Stats.
type Pool struct {
	Name    string  `json:"n"`
	TotalGB float64 `json:"d"`
	UsedGB  float64 `json:"du"`
	Health  string  `json:"h"`
}

// SystemStats is one system_stats record.
type SystemStats struct {
	System  string   `json:"system"`
	Type    StatType `json:"type"`
	Created Time     `json:"created"`
	Stats   Stats    `json:"stats"`
}

// ContainerStat is one container in a container_stats record.
type ContainerStat struct {
	Name      string    `json:"n"`
	CPU       float64   `json:"c"` // % of the whole host
	MemMiB    float64   `json:"m"`
	Bandwidth [2]uint64 `json:"b"`  // sent, received bytes/s
	SentMB    float64   `json:"ns"` // agents before 0.18.3
	RecvMB    float64   `json:"nr"`
}

// ContainerStats is one container_stats record.
type ContainerStats struct {
	System  string          `json:"system"`
	Type    StatType        `json:"type"`
	Created Time            `json:"created"`
	Stats   []ContainerStat `json:"stats"`
}

// MonitorStats is one network_monitor_stats record.
type MonitorStats struct {
	Monitor string   `json:"monitor"`
	System  string   `json:"system"`
	Type    StatType `json:"type"`
	Created int64    `json:"created"` // Unix ms, unlike other collections
	Total   int64    `json:"total_count"`
	Success int64    `json:"success_count"`
	ResSum  float64  `json:"res_sum"` // microseconds, successful probes only
	ResMin  float64  `json:"res_min"`
	ResMax  float64  `json:"res_max"`
}

// Container is a running container seen in the last 10 minutes.
type Container struct {
	ID        string  `json:"id"`
	System    string  `json:"system"`
	Name      string  `json:"name"`
	Image     string  `json:"image"`
	Ports     string  `json:"ports"`
	Status    string  `json:"status"` // Docker's text, e.g. "Up 3 days (healthy)"
	Health    int     `json:"health"` // 0 none, 1 starting, 2 healthy, 3 unhealthy
	CPU       float64 `json:"cpu"`    // % of the whole host
	MemoryMiB float64 `json:"memory"`
	NetBytes  float64 `json:"net"`     // bytes/s, sent + received
	Updated   int64   `json:"updated"` // Unix ms
	Updatable bool    `json:"updatable"`
}

// Service is a systemd unit.
type Service struct {
	System  string  `json:"system"`
	Name    string  `json:"name"`
	State   int     `json:"state"` // 0 active, 1 inactive, 2 failed, 3 activating, 4 deactivating, 5 reloading
	Sub     int     `json:"sub"`   // 0 dead, 1 running, 2 exited, 3 failed, 4 unknown
	CPU     float64 `json:"cpu"`
	CPUPeak float64 `json:"cpuPeak"`
	Memory  uint64  `json:"memory"` // bytes
	MemPeak uint64  `json:"memPeak"`
	Updated int64   `json:"updated"` // Unix ms
}

// SmartDevice is a disk's SMART summary.
type SmartDevice struct {
	System     string      `json:"system"`
	Name       string      `json:"name"`
	Model      string      `json:"model"`
	Type       string      `json:"type"`
	State      SmartState  `json:"state"`
	Capacity   uint64      `json:"capacity"` // bytes
	Temp       float64     `json:"temp"`
	Hours      uint64      `json:"hours"`
	Cycles     uint64      `json:"cycles"`
	Attributes []SmartAttr `json:"attributes"`
	Updated    Time        `json:"updated"`
}

// SmartAttr is one SMART attribute.
type SmartAttr struct {
	ID         int    `json:"id"`
	Name       string `json:"n"`
	Value      int    `json:"v"`
	Worst      int    `json:"w"`
	Threshold  int    `json:"t"`
	Raw        uint64 `json:"rv"`
	RawString  string `json:"rs"`
	WhenFailed string `json:"wf"` // non-empty when the attribute failed
}

// ZfsPool is a storage pool; btrfs pool names start with "b:".
type ZfsPool struct {
	System      string `json:"system"`
	Name        string `json:"name"`
	DisplayName string `json:"display_name"`
	Health      string `json:"health"` // ONLINE, DEGRADED, FAULTED, ...
	Status      string `json:"status"` // zpool status message
	Size        uint64 `json:"size"`   // bytes
	Alloc       uint64 `json:"alloc"`
	Free        uint64 `json:"free"`
	Scrub       *Scrub `json:"scrub"`
	Vdevs       []Vdev `json:"vdevs"`
	Raw         bool   `json:"raw"` // sizes are physical, not usable
	Updated     Time   `json:"updated"`
}

// Scrub is a pool's last scrub.
type Scrub struct {
	State    string `json:"state"` // NONE, SCANNING, FINISHED or CANCELED
	Progress string `json:"progress"`
	Errors   uint64 `json:"errors"`
}

// Vdev is one device of a pool.
type Vdev struct {
	Name         string `json:"name"`
	State        string `json:"state"`
	ReadErrs     uint64 `json:"readErrs"`
	WriteErrs    uint64 `json:"writeErrs"`
	ChecksumErrs uint64 `json:"checksumErrs"`
}

// Monitor is a network monitor with its last hour of results.
type Monitor struct {
	ID       string    `json:"id"`
	System   string    `json:"system"`
	Target   string    `json:"target"`
	Protocol string    `json:"protocol"` // icmp, tcp, http or dns
	Port     int       `json:"port"`
	Interval int       `json:"interval"` // seconds
	Res      float64   `json:"res"`      // microseconds
	ResAvg1h float64   `json:"resAvg1h"`
	ResMin1h float64   `json:"resMin1h"`
	ResMax1h float64   `json:"resMax1h"`
	Loss1h   float64   `json:"loss1h"` // %
	Enabled  bool      `json:"enabled"`
	CertInfo *CertInfo `json:"certInfo"`
	Updated  Time      `json:"updated"`
}

// CertInfo is an HTTPS target's certificate.
type CertInfo struct {
	Expires int64  `json:"expires"` // Unix ms
	Issuer  string `json:"issuer"`
}

// PackageUpdates is a live answer from the agent.
type PackageUpdates struct {
	Manager       string    `json:"manager"`
	CheckedAt     int64     `json:"checkedAt"` // Unix seconds
	SecurityKnown bool      `json:"securityKnown"`
	Packages      []Package `json:"packages"`
}

// Package is one pending update.
type Package struct {
	Name      string `json:"name"`
	Current   string `json:"current"`
	Available string `json:"available"`
	Security  bool   `json:"security"`
}

// Info returns the hub version. It answers 401 for a bad token, unlike lists.
func (c *Client) Info(ctx context.Context) (Info, error) {
	var i Info
	err := c.get(ctx, "api/beszel/info", nil, c.o.Timeout, &i)
	return i, err
}

// Systems returns every system shared with the token's user, by name.
func (c *Client) Systems(ctx context.Context) ([]System, error) {
	s, err := list[System](ctx, c, "systems", "id,name,host,port,status,info,updated", "", "name")
	if err != nil || len(s) > 0 {
		return s, err
	}
	// An expired token lists as a guest: HTTP 200 with no items.
	if _, err := c.Info(ctx); err != nil {
		return nil, err
	}
	return s, nil
}

// SystemDetails returns the static facts of every system.
func (c *Client) SystemDetails(ctx context.Context) ([]SystemDetails, error) {
	return list[SystemDetails](ctx, c, "system_details",
		"system,hostname,os,os_name,kernel,cpu,arch,cores,threads,sockets,memory,podman", "", "")
}

// Containers returns every running container.
func (c *Client) Containers(ctx context.Context) ([]Container, error) {
	return list[Container](ctx, c, "containers",
		"id,system,name,image,ports,status,health,cpu,memory,net,updated,updatable", "", "system,name")
}

// Services returns every systemd unit.
func (c *Client) Services(ctx context.Context) ([]Service, error) {
	return list[Service](ctx, c, "systemd_services",
		"system,name,state,sub,cpu,cpuPeak,memory,memPeak,updated", "", "system,name")
}

// SmartDevices returns every disk's SMART summary.
func (c *Client) SmartDevices(ctx context.Context) ([]SmartDevice, error) {
	return list[SmartDevice](ctx, c, "smart_devices",
		"system,name,model,type,state,capacity,temp,hours,cycles,attributes,updated", "", "system,name")
}

// ZfsPools returns every storage pool.
func (c *Client) ZfsPools(ctx context.Context) ([]ZfsPool, error) {
	return list[ZfsPool](ctx, c, "zfs_pools",
		"system,name,display_name,health,status,size,alloc,free,scrub,vdevs,raw,updated", "", "system,name")
}

// Monitors returns every network monitor.
func (c *Client) Monitors(ctx context.Context) ([]Monitor, error) {
	return list[Monitor](ctx, c, "network_monitors",
		"id,system,target,protocol,port,interval,res,resAvg1h,resMin1h,resMax1h,loss1h,enabled,certInfo,updated",
		"", "system,target")
}

// SystemStats returns a system's samples of type t created at or after since, oldest first.
func (c *Client) SystemStats(ctx context.Context, systemID string, t StatType, since time.Time) ([]SystemStats, error) {
	f, err := statsFilter(systemID, t, "'"+since.UTC().Format("2006-01-02 15:04:05.000Z")+"'")
	if err != nil {
		return nil, err
	}
	return list[SystemStats](ctx, c, "system_stats", "system,type,created,stats", f, "created")
}

// LastStatsAt returns when the system's newest stats row of any type was
// created, or the zero time when it has none. Beszel bumps systems.updated
// when it rewrites the record, so that time is not when data last arrived.
func (c *Client) LastStatsAt(ctx context.Context, systemID string) (time.Time, error) {
	if !recordID.MatchString(systemID) {
		return time.Time{}, fmt.Errorf("invalid system id %q", systemID)
	}
	q := url.Values{
		"fields": {"created"}, "filter": {"system='" + systemID + "'"}, "sort": {"-created"},
		"perPage": {"1"}, "page": {"1"}, "skipTotal": {"true"},
	}
	var p page[struct {
		Created Time `json:"created"`
	}]
	if err := c.get(ctx, "api/collections/system_stats/records", q, c.o.Timeout, &p); err != nil {
		return time.Time{}, err
	}
	if len(p.Items) == 0 {
		return time.Time{}, nil
	}
	return p.Items[0].Created.Time, nil
}

// ContainerStats returns a system's container samples of type t created at or after since, oldest first.
func (c *Client) ContainerStats(ctx context.Context, systemID string, t StatType, since time.Time) ([]ContainerStats, error) {
	f, err := statsFilter(systemID, t, "'"+since.UTC().Format("2006-01-02 15:04:05.000Z")+"'")
	if err != nil {
		return nil, err
	}
	return list[ContainerStats](ctx, c, "container_stats", "system,type,created,stats", f, "created")
}

// MonitorStats returns a system's monitor samples of type t created at or after since, oldest first.
func (c *Client) MonitorStats(ctx context.Context, systemID string, t StatType, since time.Time) ([]MonitorStats, error) {
	// network_monitor_stats.created is Unix ms, not a date.
	f, err := statsFilter(systemID, t, strconv.FormatInt(since.UnixMilli(), 10))
	if err != nil {
		return nil, err
	}
	return list[MonitorStats](ctx, c, "network_monitor_stats",
		"monitor,system,type,created,total_count,success_count,res_sum,res_min,res_max", f, "created")
}

// PackageUpdates asks the system's agent for pending updates. It waits on
// the agent, so it uses the live timeout.
func (c *Client) PackageUpdates(ctx context.Context, systemID string) (PackageUpdates, error) {
	var p PackageUpdates
	if !recordID.MatchString(systemID) {
		return p, fmt.Errorf("invalid system id %q", systemID)
	}
	q := url.Values{"system": {systemID}}
	err := c.get(ctx, "api/beszel/package-updates", q, c.o.LiveTimeout, &p)
	return p, err
}

// recordID matches PocketBase record ids, so ids are safe inside a filter.
var recordID = regexp.MustCompile(`^[a-z0-9]{1,15}$`)

// statsFilter builds a filter from checked values only; no free text ever
// reaches it. since is an already-quoted date or a bare number.
func statsFilter(systemID string, t StatType, since string) (string, error) {
	if !recordID.MatchString(systemID) {
		return "", fmt.Errorf("invalid system id %q", systemID)
	}
	if !t.valid() {
		return "", fmt.Errorf("invalid stats type %q", t)
	}
	return strings.Join([]string{"system='" + systemID + "'", "type='" + string(t) + "'", "created>=" + since}, " && "), nil
}
